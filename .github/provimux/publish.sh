#!/usr/bin/env bash
# Provimux publish (.github/provimux/SYNC.md): the owner's step after provimux-check said
# "ready to publish". Runs on the owner's Mac with the owner's own git and gh credentials;
# it never asks for or stores a token.
#
#   publish.sh <vX.Y.Z> [--dry-run] [--yes] [--run <id>] [--remote <name>]
#              [--accept-dropped <sha,...>] [--accept-changed <sha,...>] [--accept-workflows <path,...>]
#              [--main-only | --tags-only] [--timeout <minutes>]
#
# 1. Downloads the artifact provimux-check-<tag> of the newest successful provimux-check
#    run on main (or of --run) and verifies it: result.json says pass for this tag; the
#    bundle's tip is result.json's candidate; the upstream tag still points at the recorded
#    commit and is still a published, non-draft, non-prerelease release; the remote main
#    still is the recorded old main; v<base>-pmx.* does not exist yet.
# 2. Verifies the candidate again here, in a temporary repository (the check job ran
#    upstream's code, so its claims about structure are not trusted): the new base
#    descends from the old base, the candidate from the new base, no merge commits;
#    git range-diff of <old base>..<old main> against <new base>..<candidate>; the new
#    workflow paths; guard.sh (old main's copy) with the expected base. Dropped or
#    changed patches and new workflow files are refused unless named exactly with the
#    --accept-* flags (commits: the old main's commit, 7 or more hex digits).
# 3. Prints what it will push and asks (--yes skips the question). One atomic push:
#    main with --force-with-lease=refs/heads/main:<recorded old main> (a literal SHA),
#    tag v<base> at upstream's commit, tag v<base>-pmx.1 at the candidate.
# 4. Waits until the GitHub release v<base>-pmx.1 is published with every asset (the
#    release workflow does the rest), then closes the "sync: v<base>" issue.
#
#   --dry-run    everything except the push (shows git push --dry-run), no wait, no close
#   --main-only  push only main (new upstream workflow files: SYNC.md); no wait, no close
#   --tags-only  the remote main must already be the candidate; push the two tags, wait, close
#
# Run it from a clone of ngvoicu/provimux (any branch, nothing is checked out or changed:
# the work happens in a temporary repository that borrows this clone's objects).
# Needs bash 3.2+, git, gh (logged in as the owner), jq.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=common.sh
. "$here/common.sh"

usage() { sed -n '2,33p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
die() { echo "publish: $*" >&2; exit 1; }
log() { echo "==> $*" >&2; }

tag='' dry_run=0 yes=0 run_id='' remote='' accept_dropped='' accept_changed='' accept_workflows=''
mode=all timeout_min=120
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) dry_run=1 ;;
    --yes) yes=1 ;;
    --run) run_id="${2:?}"; shift ;;
    --remote) remote="${2:?}"; shift ;;
    --accept-dropped) accept_dropped="${2:?}"; shift ;;
    --accept-changed) accept_changed="${2:?}"; shift ;;
    --accept-workflows) accept_workflows="${2:?}"; shift ;;
    --main-only) mode=main ;;
    --tags-only) mode=tags ;;
    --timeout) timeout_min="${2:?}"; shift ;;
    -h | --help) usage ;;
    v*) [[ -z "$tag" ]] || usage; tag="$1" ;;
    *) usage ;;
  esac
  shift
done
[[ "$tag" =~ $PMX_STABLE_RE ]] || usage
base="${tag#v}"
pmx_tag="v$base-pmx.1"

# --- where to push --------------------------------------------------------------------
git rev-parse --git-dir >/dev/null 2>&1 || die "run it inside a clone of $PMX_REPO"
if [[ -z "$remote" ]]; then
  remote="$(git remote -v | awk -v r="$PMX_REPO" '$3 == "(push)" && ($2 ~ ("github.com[:/]" r "(\\.git)?$")) {print $1; exit}')"
  [[ -n "$remote" ]] || die "no remote of this clone points at $PMX_REPO; pass --remote"
fi
push_url="$(git remote get-url --push "$remote")"
log "remote $remote ($push_url)"

work="$(mktemp -d "${TMPDIR:-/tmp}/provimux-publish.XXXXXX")"
trap 'rm -rf "${work:?}"' EXIT

# --- 1. the check's artifact ------------------------------------------------------------
if [[ -z "$run_id" ]]; then
  # Newest unexpired artifact of that name whose run is a successful provimux-check on main.
  for id in $(gh api "repos/$PMX_REPO/actions/artifacts?name=provimux-check-$tag&per_page=100" \
    --jq '.artifacts | map(select(.expired | not)) | sort_by(.created_at) | reverse | .[].workflow_run.id'); do
    if [[ "$(gh api "repos/$PMX_REPO/actions/runs/$id" --jq '"\(.path) \(.head_branch) \(.status) \(.conclusion)"')" == \
      ".github/workflows/provimux-check.yml main completed success" ]]; then
      run_id="$id"
      break
    fi
  done
  [[ -n "$run_id" ]] || die "no successful provimux-check run on main has an artifact provimux-check-$tag; run the check first (Actions → provimux-check)"
fi
log "check run https://github.com/$PMX_REPO/actions/runs/$run_id"
gh run download "$run_id" -R "$PMX_REPO" -n "provimux-check-$tag" -D "$work/artifact" >/dev/null ||
  die "cannot download the artifact provimux-check-$tag of run $run_id"
res="$work/artifact/result.json"
bundle="$work/artifact/candidate.bundle"
[[ -s "$res" ]] || die "the artifact has no result.json"
r() { jq -r "$1 // empty" "$res"; }
[[ "$(r .status)" == pass ]] || die "the check's result is '$(r .status)', not pass: $(jq -r '.reasons | join("; ")' "$res")"
[[ -s "$bundle" ]] || die "the artifact has no candidate.bundle"
[[ "$(r .upstream.tag)" == "$tag" ]] || die "the artifact is for '$(r .upstream.tag)', not $tag"
[[ "$(r .tests.ok)" == true && "$(r .build.ok)" == true && "$(r .guard.ok)" == true ]] ||
  die "result.json says pass but tests/build/guard are not all ok"
upstream_commit="$(r .upstream.commit)"
old_main="$(r .old.main)"
old_base_tag="$(r .old.base_tag)"
old_base_commit="$(r .old.base_commit)"
candidate="$(r .candidate)"
for v in upstream_commit old_main old_base_commit candidate; do
  [[ "${!v}" =~ $PMX_SHA_RE ]] || die "result.json: $v is not a commit SHA"
done
[[ "$old_base_tag" =~ $PMX_STABLE_RE ]] || die "result.json: old base tag '$old_base_tag'"

# --- the world still matches what was checked ---------------------------------------------
s="$(upstream_release_state "$tag")"
[[ "$s" == stable ]] || die "$tag is now '$s' in $PMX_UPSTREAM_REPO, not a stable release"
up_tags="$(git ls-remote --tags "$PMX_UPSTREAM_URL")"
[[ "$(ls_remote_tag "$up_tags" "$tag")" == "$upstream_commit" ]] ||
  die "upstream's $tag is now $(ls_remote_tag "$up_tags" "$tag"), the check used $upstream_commit"
[[ "$(ls_remote_tag "$up_tags" "$old_base_tag")" == "$old_base_commit" ]] ||
  die "upstream's $old_base_tag is not $old_base_commit"
remote_refs="$(git ls-remote "$push_url" refs/heads/main "refs/tags/$tag" "refs/tags/$tag^{}" "refs/tags/v$base-pmx.*")"
remote_main="$(awk '$2 == "refs/heads/main" {print $1}' <<<"$remote_refs")"
if [[ "$mode" == tags ]]; then
  [[ "$remote_main" == "$candidate" ]] || die "--tags-only: the remote main is $remote_main, not the candidate $candidate"
else
  [[ "$remote_main" == "$old_main" ]] ||
    die "the remote main is $remote_main, but the check rebased $old_main; run the check again"
fi
existing_pmx="$(awk '{print $2}' <<<"$remote_refs" | grep -E "^refs/tags/v$base-pmx\." | sed 's|^refs/tags/||; s/\^{}$//' | sort -u | paste -sd ' ' - || true)"
[[ -z "$existing_pmx" ]] || die "$existing_pmx already exists on $remote; a -pmx.2 on the same base is cut by hand (SYNC.md)"
remote_base_tag="$(ls_remote_tag "$remote_refs" "$tag")"
[[ -z "$remote_base_tag" || "$remote_base_tag" == "$upstream_commit" ]] ||
  die "$remote already has $tag at $remote_base_tag, upstream's is $upstream_commit"

# --- 2. verify the candidate here -----------------------------------------------------------
repo="$work/repo.git"
git init -q --bare "$repo"
objects="$(cd "$(git rev-parse --git-common-dir)" && pwd)/objects"
echo "$objects" >"$repo/objects/info/alternates"
g() { git --git-dir="$repo" "$@"; }
g fetch -q --no-tags "$push_url" "+refs/heads/main:refs/pmx/remote-main"
g fetch -q --no-tags "$PMX_UPSTREAM_URL" "+refs/tags/$tag:refs/pmx/upstream" "+refs/tags/$old_base_tag:refs/pmx/old-base"
[[ "$(g rev-parse "refs/pmx/upstream^{commit}")" == "$upstream_commit" ]] || die "fetched $tag is not $upstream_commit"
[[ "$(g rev-parse "refs/pmx/old-base^{commit}")" == "$old_base_commit" ]] || die "fetched $old_base_tag is not $old_base_commit"
g cat-file -e "$old_main^{commit}" 2>/dev/null || g fetch -q --no-tags "$push_url" "$old_main" ||
  die "cannot get the old main $old_main"
g bundle verify -q "$bundle" >/dev/null 2>&1 || die "the bundle does not verify against $tag"
heads="$(g bundle list-heads "$bundle")"
[[ "$(wc -l <<<"$heads" | tr -d ' ')" == 1 ]] || die "the bundle has more than one head"
[[ "$(awk '{print $1}' <<<"$heads")" == "$candidate" ]] || die "the bundle's tip is not result.json's candidate $candidate"
g fetch -q "$bundle" "+$(awk '{print $2}' <<<"$heads"):refs/pmx/candidate"
[[ "$(g rev-parse refs/pmx/candidate)" == "$candidate" ]] || die "the fetched candidate is not $candidate"

g merge-base --is-ancestor "$old_base_commit" "$old_main" || die "$old_base_tag is not an ancestor of the old main"
g merge-base --is-ancestor "$old_base_commit" "$upstream_commit" || die "$tag does not descend from the old base $old_base_tag"
g merge-base --is-ancestor "$upstream_commit" "$candidate" || die "the candidate is not built on $tag"
[[ -z "$(g rev-list --merges "$upstream_commit..$candidate")" ]] || die "the candidate has merge commits above $tag"

g range-diff --no-color --no-notes "$old_base_commit..$old_main" "$upstream_commit..$candidate" >"$work/range-diff.txt"
: >"$work/dropped"; : >"$work/changed"; : >"$work/added"
while IFS= read -r line; do
  p="$(sed -E 's/^ *([0-9]+|-+): +([0-9a-f]+|-+) ([=!<>]) +([0-9]+|-+): +([0-9a-f]+|-+) (.*)$/\3	\2	\5	\6/' <<<"$line")"
  IFS=$'\t' read -r op a b subj <<<"$p"
  case "$op" in
    '<') printf '%s\t%s\n' "$(g rev-parse "$a^{commit}")" "$subj" >>"$work/dropped" ;;
    '!') printf '%s\t%s\n' "$(g rev-parse "$a^{commit}")" "$subj" >>"$work/changed" ;;
    '>') printf '%s\t%s\n' "$(g rev-parse "$b^{commit}")" "$subj" >>"$work/added" ;;
  esac
done < <(grep -E '^ *([0-9]+|-+): +([0-9a-f]+|-+) [=!<>] +([0-9]+|-+): +([0-9a-f]+|-+) ' "$work/range-diff.txt" || true)
[[ ! -s "$work/added" ]] && [[ -n "$(cat "$work/range-diff.txt")" ]] || die "the range-diff is empty or has commits not in the old main: see $work/range-diff.txt"
g ls-tree -r --name-only "$old_base_commit" -- .github/workflows/ | sort >"$work/wf-old"
g ls-tree -r --name-only "$upstream_commit" -- .github/workflows/ | sort >"$work/wf-tag"
comm -13 "$work/wf-old" "$work/wf-tag" >"$work/wf-new"

# The check's lists must agree with what was computed here.
same() { diff <(cut -f1 "$1" | sort) <(jq -r "$2" "$res" | sort) >/dev/null; }
same "$work/dropped" '.decisions.dropped[].commit' || die "result.json's dropped commits differ from this range-diff"
same "$work/changed" '.decisions.changed[].commit' || die "result.json's changed commits differ from this range-diff"
same "$work/wf-new" '.decisions.new_workflows[]' || die "result.json's new workflow paths differ from this comparison"

# Guard: the old main's copy (the owner's), on the candidate, with the exact base.
g show "$old_main:.github/provimux/guard.sh" >"$work/guard.sh"
g worktree add -q --detach "$work/candidate" "$candidate"
if ! (cd "$work/candidate" && PMX_UPSTREAM="$PMX_UPSTREAM_URL" PMX_EXPECT_BASE="$tag" PMX_EXPECT_BASE_COMMIT="$upstream_commit" \
  bash "$work/guard.sh") >"$work/guard.log" 2>&1; then
  cat "$work/guard.log" >&2
  die "the guard rejects the candidate"
fi
guard_ok="$(tail -n 1 "$work/guard.log")"

# --- the owner's decisions ------------------------------------------------------------------
refused=0
# accepted LIST-FILE ACCEPT-CSV NAME KIND: every entry named, and every name an entry.
accepted() {
  local file=$1 csv=$2 flag=$3 kind=$4 item name hit
  local names
  names="$(tr ',' '\n' <<<"$csv" | sed '/^$/d')"
  while IFS= read -r name; do
    [[ -n "$name" ]] || continue
    hit=0
    while IFS=$'\t' read -r item _; do
      [[ -n "$item" ]] || continue
      if [[ "$kind" == commit ]]; then
        [[ ${#name} -ge 7 && "$item" == "$name"* ]] && hit=1
      else
        [[ "$item" == "$name" ]] && hit=1
      fi
    done <"$file"
    [[ $hit -eq 1 ]] || { echo "publish: $flag names '$name', which is not in the list below" >&2; refused=1; }
  done <<<"$names"
  while IFS=$'\t' read -r item rest; do
    [[ -n "$item" ]] || continue
    hit=0
    while IFS= read -r name; do
      [[ -z "$name" ]] && continue
      if [[ "$kind" == commit ]]; then
        [[ ${#name} -ge 7 && "$item" == "$name"* ]] && hit=1
      else
        [[ "$item" == "$name" ]] && hit=1
      fi
    done <<<"$names"
    [[ $hit -eq 1 ]] || { echo "publish: not accepted ($flag): $item ${rest:-}" >&2; refused=1; }
  done <"$file"
}
accepted "$work/dropped" "$accept_dropped" --accept-dropped commit
accepted "$work/changed" "$accept_changed" --accept-changed commit
accepted "$work/wf-new" "$accept_workflows" --accept-workflows path
if [[ -s "$work/wf-new" && "$mode" == all ]]; then
  echo "publish: $tag adds workflow files; publish in two steps (--main-only, disable them, --tags-only), see SYNC.md" >&2
  refused=1
fi

# --- 3. the plan ---------------------------------------------------------------------------------
n_same="$(grep -cE '^ *[0-9]+: +[0-9a-f]+ = ' "$work/range-diff.txt" || true)"
cat <<EOF

Provimux publish: CLIProxyAPI $tag ($upstream_commit)
  check run       https://github.com/$PMX_REPO/actions/runs/$run_id
  old main        $old_main (on $old_base_tag)
  candidate       $candidate ($(g rev-list --count "$upstream_commit..$candidate") commits on $tag)
  range-diff      $n_same unchanged, $(wc -l <"$work/changed" | tr -d ' ') changed, $(wc -l <"$work/dropped" | tr -d ' ') dropped
  new workflows   $(wc -l <"$work/wf-new" | tr -d ' ')
  guard (here)    $guard_ok
  tests (check)   $(jq -r '.tests | "candidate \(.candidate.packages) packages, \(.candidate.passed) passed, \(.candidate.failed) failed; \(.new_failures | length) new failures; \(.flaky | length) flaky; \(.failing_on_tag_too | length) failing on the tag too"' "$res")
EOF
for f in dropped changed; do
  if [[ -s "$work/$f" ]]; then echo "  $f:"; sed 's/^/    /' "$work/$f"; fi
done
[[ -s "$work/wf-new" ]] && { echo "  new workflow files:"; sed 's/^/    /' "$work/wf-new"; }
if [[ -s "$work/changed" ]]; then
  echo
  echo "range-diff of the changed patches:"
  sed -n '/^ *[0-9]*: *[0-9a-f]* ! /,/^ *[0-9-]*: *[0-9a-f-]* [=<>] /p' "$work/range-diff.txt" | sed 's/^/  /'
fi
[[ $refused -eq 0 ]] || die "refused: accept each item above by name (SYNC.md), or fix the stack"

# The upstream tag is pushed only when the remote does not have it yet (same commit checked above).
clean=()
[[ "$mode" != tags ]] && clean+=("$candidate:refs/heads/main")
[[ "$mode" != main && -z "$remote_base_tag" ]] && clean+=("$upstream_commit:refs/tags/$tag")
[[ "$mode" != main ]] && clean+=("$candidate:refs/tags/$pmx_tag")
pushargs=(--atomic --porcelain)
[[ "$mode" != tags ]] && pushargs+=("--force-with-lease=refs/heads/main:$old_main")

echo
echo "Push to $push_url:"
echo "  git push ${pushargs[*]} $push_url ${clean[*]}"
for x in "${clean[@]}"; do echo "    ${x%%:*} -> ${x#*:}"; done
[[ "$mode" == main ]] && echo "  (main only: then disable the new workflows and run --tags-only, SYNC.md)"
echo

if [[ $dry_run -eq 1 ]]; then
  log "dry run: git push --dry-run"
  g push --dry-run "${pushargs[@]}" "$push_url" "${clean[@]}" || die "git push --dry-run failed"
  echo
  echo "Dry run: nothing was pushed."
  exit 0
fi

if [[ $yes -eq 0 ]]; then
  printf 'Push this? Type "publish %s": ' "$tag" >/dev/tty
  read -r answer </dev/tty
  [[ "$answer" == "publish $tag" ]] || die "not confirmed; nothing pushed"
fi
g push "${pushargs[@]}" "$push_url" "${clean[@]}" || die "the push failed; nothing was changed on $remote (atomic)"
echo "Pushed."
if [[ "$mode" == main ]]; then
  echo "Main is $candidate. Now disable the new workflows, then: $0 $tag --tags-only (SYNC.md)."
  exit 0
fi

# --- 4. wait for the release, then close the issue ----------------------------------------------
log "waiting for the release $pmx_tag (up to $timeout_min minutes)"
deadline=$(( $(date +%s) + timeout_min * 60 ))
while :; do
  s="$(release_state "$pmx_tag")"
  run_state="$(release_run_state "$pmx_tag")"
  log "release: $s; release run: $run_state"
  [[ "$s" == complete ]] && break
  case "$run_state" in
    failure:*) die "the provimux-release run for $pmx_tag ended ${run_state#failure:}; the issue stays open (SYNC.md: \"Check a release\")" ;;
    success) [[ "$s" == complete ]] || die "the release run succeeded but the release is $s; the issue stays open" ;;
  esac
  [[ $(date +%s) -lt $deadline ]] || die "timed out waiting for $pmx_tag; the issue stays open. Check it later (SYNC.md)."
  sleep 30
done
url="https://github.com/$PMX_REPO/releases/tag/$pmx_tag"
echo "Published: $url"
for n in $(sync_issues "$base"); do
  gh issue close "$n" -R "$PMX_REPO" --comment "Published $url (main ${old_main:0:12} -> ${candidate:0:12}); the release has all its assets." >/dev/null
  echo "Closed issue #$n"
done
