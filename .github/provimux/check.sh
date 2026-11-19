#!/usr/bin/env bash
# Provimux sync check (.github/provimux/SYNC.md), run by .github/workflows/provimux-check.yml.
# It publishes nothing and needs no token beyond read access: the owner publishes with
# publish.sh after reading the result.
#
#   check.sh select   which upstream release, and whether there is anything to do
#                     (calls the GitHub API with gh, read-only; runs no repository code)
#   check.sh run      rebase, range-diff, workflow paths, guard, build, go test on both
#                     trees (runs upstream's and the candidate's code; needs no token)
#   check.sh          both
#
# select: the tag is $PMX_TAG, or upstream's newest stable release (published, not a
# draft, not a prerelease); an explicit tag must be one too. The base of main is the
# newest vX.Y.Z tag reachable from main, which must be upstream's tag at the same commit
# (no fallback to an older one). Then:
#   - v<base>-pmx.* exists here: "published" when every such release is complete,
#     "in-progress" while its release run runs, "incomplete" otherwise;
#   - main is already on the tag (and no -pmx tag): "unreleased";
#   - the tag does not descend from main's base: "fail";
#   - otherwise: "check", and run goes on.
# run: rebase main onto the tag's commit (committer date = author date, so the same
# inputs give the same candidate), bundle <tag commit>..candidate, git range-diff of
# <old base>..<old main> against <tag>..<candidate> (dropped and changed patches need the
# owner's explicit consent in publish.sh), the paths under .github/workflows/ new in
# the tag (same), guard.sh with the expected base, go build ./..., and go test ./... on
# the candidate and on the pristine tag (testsum.jq, testcmp.jq).
#
# The status ends in $PMX_CHECK_OUT/result.json with candidate.bundle and logs/:
#   pass | fail | error | published | in-progress | incomplete | unreleased
# Exit status: 0 unless the script itself breaks (the status is the result).
#
# Env: PMX_TAG (optional), PMX_MAIN_REF (default HEAD: the main to rebase),
# PMX_CHECK_OUT (default: a new temp dir), PMX_RUN_URL (recorded in result.json),
# PMX_UPSTREAM_REPO / PMX_UPSTREAM_URL / PMX_REPO / PMX_REPO_URL (common.sh).
# Runs with bash 3.2 (macOS); needs git, go, jq, gh. Run from the repository; the
# rebase happens in a temporary worktree, so the checkout and its branch do not move.
# The only refs it writes are refs/pmx-check/*, deleted on exit. It never creates a tag.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=common.sh
. "$here/common.sh"

mode="${1:-all}"
out="${PMX_CHECK_OUT:-$(mktemp -d "${TMPDIR:-/tmp}/provimux-check.XXXXXX")}"
mkdir -p "$out/logs"
state="$out/state.env"
log() { echo "==> $*" >&2; }

# --- state shared by the two steps ------------------------------------------------
status='' step='' tag='' base='' upstream_commit='' old_main='' old_base_tag='' old_base_commit=''
candidate='' commits='' build_ok='' guard_ok='' tests_ok=''
reasons="$out/reasons.txt"
save_state() {
  {
    for v in status step tag base upstream_commit old_main old_base_tag old_base_commit; do
      printf '%s=%q\n' "$v" "${!v}"
    done
  } >"$state"
}
reason() { printf '%s\n' "$*" >>"$reasons"; log "$*"; }

# result.json from the variables and files so far; also on an unexpected exit.
# write_result [STATUS REASON]: with arguments, a provisional result (select, before run).
write_result() {
  local s="$status" rfile="$reasons"
  if [[ $# -eq 2 ]]; then
    s=$1 rfile="$out/reasons.provisional.txt"
    printf '%s\n' "$2" >"$rfile"
  elif [[ -z "$s" || "$s" == "running" || "$s" == "check" ]]; then
    s=error
    reason "check.sh stopped unexpectedly during step '$step'"
  fi
  touch "$reasons" "$out/dropped.tsv" "$out/changed.tsv" "$out/added.tsv" "$out/wf-new.txt" "$out/wf-removed.txt" \
    "$out/logs/range-diff.txt" "$out/logs/guard.log" "$out/logs/build.log" "$out/rebase.json" "$out/release.json"
  [[ -s "$out/tests.json" ]] || echo 'null' >"$out/tests.json"
  [[ -s "$out/rebase.json" ]] || echo 'null' >"$out/rebase.json"
  [[ -s "$out/release.json" ]] || echo 'null' >"$out/release.json"
  jq -n \
    --arg status "$s" --arg step "$step" --arg tag "$tag" --arg base "$base" \
    --arg upstream_repo "$PMX_UPSTREAM_REPO" --arg upstream_commit "$upstream_commit" \
    --arg old_main "$old_main" --arg old_base_tag "$old_base_tag" --arg old_base_commit "$old_base_commit" \
    --arg candidate "$candidate" --arg commits "$commits" \
    --arg build_ok "$build_ok" --arg guard_ok "$guard_ok" --arg tests_ok "$tests_ok" \
    --arg run_url "${PMX_RUN_URL:-}" \
    --rawfile reasons "$rfile" \
    --rawfile dropped "$out/dropped.tsv" --rawfile changed "$out/changed.tsv" --rawfile added "$out/added.tsv" \
    --rawfile wf_new "$out/wf-new.txt" --rawfile wf_removed "$out/wf-removed.txt" \
    --rawfile range_diff "$out/logs/range-diff.txt" --rawfile guard "$out/logs/guard.log" \
    --rawfile build "$out/logs/build.log" \
    --slurpfile tests "$out/tests.json" --slurpfile rebase "$out/rebase.json" --slurpfile release "$out/release.json" '
    def lines: split("\n") | map(select(length > 0));
    def commits: lines | map(split("\t") | {commit: .[0], subject: (.[1:] | join("\t"))});
    def ok($v): if $v == "" then null else ($v == "true") end;
    def tail($n): lines | .[-$n:] | join("\n");
    {
      schema: 1,
      status: $status,
      step: $step,
      reasons: ($reasons | lines),
      upstream: {repo: $upstream_repo, tag: $tag, commit: $upstream_commit},
      base: $base,
      old: {main: $old_main, base_tag: $old_base_tag, base_commit: $old_base_commit},
      candidate: $candidate,
      commits: (if $commits == "" then null else ($commits | tonumber) end),
      decisions: {dropped: ($dropped | commits), changed: ($changed | commits), new_workflows: ($wf_new | lines)},
      added: ($added | commits),
      removed_workflows: ($wf_removed | lines),
      rebase: $rebase[0],
      build: {ok: ok($build_ok), tail: ($build | tail(40))},
      guard: {ok: ok($guard_ok), output: ($guard | tail(40))},
      tests: $tests[0],
      release: $release[0],
      range_diff: (if ($range_diff | length) > 60000 then ($range_diff[0:60000] + "\n[truncated: see logs/range-diff.txt]") else $range_diff end),
      run_url: $run_url
    }' >"$out/result.json"
  log "result: $s ($out/result.json)"
}

cleanup_paths=()
cleanup() {
  local p
  for p in ${cleanup_paths[@]+"${cleanup_paths[@]}"}; do
    git worktree remove --force "$p" >/dev/null 2>&1 || true
    rm -rf "${p:?}"
  done
  git for-each-ref --format='%(refname)' refs/pmx-check/ | while read -r r; do git update-ref -d "$r"; done
  git worktree prune >/dev/null 2>&1 || true
}

# --- select ---------------------------------------------------------------------------
do_select() {
  local up_tags t c existing states s run_state
  : >"$reasons"
  status=running
  step=select
  old_main="$(git rev-parse "${PMX_MAIN_REF:-HEAD}^{commit}")"
  up_tags="$(git ls-remote --tags "$PMX_UPSTREAM_URL")" || { status=error; reason "cannot list the tags of $PMX_UPSTREAM_URL"; return; }

  # The base of main: the newest vX.Y.Z reachable from it, which must be upstream's tag.
  old_base_tag="$(git tag --merged "$old_main" --list 'v*' | grep -E "$PMX_STABLE_RE" | sort -V | tail -n 1 || true)"
  if [[ -z "$old_base_tag" ]]; then
    status=error; reason "no vX.Y.Z tag is reachable from main ${old_main:0:8}; fetch the tags"; return
  fi
  old_base_commit="$(git rev-parse "$old_base_tag^{commit}")"
  c="$(ls_remote_tag "$up_tags" "$old_base_tag")"
  if [[ "$c" != "$old_base_commit" ]]; then
    status=error
    reason "main's base $old_base_tag is ${old_base_commit:0:8} here but ${c:-absent} in $PMX_UPSTREAM_REPO"
    return
  fi

  # The release.
  tag="${PMX_TAG:-}"
  if [[ -z "$tag" ]]; then
    tag="$(newest_stable_release)" || true
    [[ -n "$tag" ]] || { status=error; reason "cannot read the releases of $PMX_UPSTREAM_REPO"; return; }
  fi
  if [[ ! "$tag" =~ $PMX_STABLE_RE ]]; then
    status=fail; reason "'$tag' is not a vX.Y.Z tag"; return
  fi
  base="${tag#v}"
  s="$(upstream_release_state "$tag")"
  if [[ "$s" != stable ]]; then
    status=fail
    case "$s" in
      missing) reason "$tag is not a published release of $PMX_UPSTREAM_REPO" ;;
      error:*) status=error; reason "cannot read release $tag of $PMX_UPSTREAM_REPO: ${s#error:}" ;;
      *) reason "$tag is a $s release of $PMX_UPSTREAM_REPO, not a stable one" ;;
    esac
    return
  fi
  upstream_commit="$(ls_remote_tag "$up_tags" "$tag")"
  [[ -n "$upstream_commit" ]] || { status=fail; reason "$tag has a release but no git tag in $PMX_UPSTREAM_URL"; return; }
  log "upstream $tag = ${upstream_commit:0:8}; main ${old_main:0:8} on $old_base_tag"

  # Already released here?
  existing="$(git ls-remote --tags "$PMX_REPO_URL" "refs/tags/v$base-pmx.*" | awk '{print $2}' |
    sed 's|^refs/tags/||; s/\^{}$//' | sort -u -V)"
  if [[ -n "$existing" ]]; then
    states=''
    status=published
    for t in $existing; do
      s="$(release_state "$t")"
      run_state=''
      [[ "$s" == complete ]] || run_state="$(release_run_state "$t")"
      states="$states{\"tag\":\"$t\",\"release\":\"$s\",\"run\":\"$run_state\"}"
      if [[ "$s" != complete ]]; then
        if [[ "$run_state" == running ]]; then
          [[ "$status" == incomplete ]] || status=in-progress
        else
          status=incomplete
          reason "tag $t exists but its release is $s (release run: $run_state)"
        fi
      fi
    done
    echo "$states" | jq -s '.' >"$out/release.json"
    log "v$base already has Provimux tags ($(echo $existing)): $status"
    return
  fi
  if [[ "$upstream_commit" == "$old_base_commit" ]]; then
    status=unreleased
    reason "main ${old_main:0:8} is already on $tag but no v$base-pmx.* tag exists"
    return
  fi
  if ! git merge-base --is-ancestor "$old_base_commit" "$upstream_commit" 2>/dev/null; then
    # The commit may not be here yet: fetch it, then ask again.
    git fetch -q --no-tags "$PMX_UPSTREAM_URL" "refs/tags/$tag" || { status=error; reason "cannot fetch $tag"; return; }
    if ! git merge-base --is-ancestor "$old_base_commit" "$upstream_commit"; then
      status=fail; reason "$tag (${upstream_commit:0:8}) does not descend from main's base $old_base_tag"; return
    fi
  fi
  status=check
}

# --- run ------------------------------------------------------------------------------
# gotest DIR OUTPREFIX ARGS...: go test -json, keeping the exit status in OUTPREFIX.exit.
# go test runs in its own process group under a wall-clock limit ($PMX_TEST_LIMIT seconds,
# default 2400): a hang (a stopped or starved test binary is not caught by -timeout) kills
# the group, the exit status is then 124 and the packages without a result fail the check.
# $PMX_TEST_P sets -p (default: go's own, the number of CPUs).
gotest() {
  local dir=$1 prefix=$2 e pid waited=0 limit="${PMX_TEST_LIMIT:-2400}"
  shift 2
  set +e
  set -m
  (cd "$dir" && exec go test -json -count=1 ${PMX_TEST_P:+-p "$PMX_TEST_P"} -timeout 15m "$@" >"$prefix.json" 2>"$prefix.stderr") &
  pid=$!
  set +m
  while kill -0 "$pid" 2>/dev/null; do
    if [[ $waited -ge $limit ]]; then
      reason "go test ($(basename "$prefix")) ran longer than ${limit}s and was killed"
      kill -TERM -- "-$pid" 2>/dev/null
      sleep 10
      kill -KILL -- "-$pid" 2>/dev/null
      break
    fi
    sleep 5
    waited=$((waited + 5))
  done
  wait "$pid"
  e=$?
  [[ $waited -ge $limit ]] && e=124
  set -e
  echo "$e" >"$prefix.exit"
  jq -R -c -f "$here/testevents.jq" "$prefix.json" | jq -s -f "$here/testsum.jq" >"$prefix.sum.json"
}

# sample_resources FILE: memory, load and the largest processes every 60s (Linux), until killed.
sample_resources() {
  command -v free >/dev/null 2>&1 || return 0
  while :; do
    { date -u +%FT%TZ; uptime; free -m; ps -eo pid,etime,stat,rss,args --sort=-rss | head -n 12 | cut -c1-200; echo; } >>"$1" 2>&1
    sleep 60
  done
}

# export_tree COMMIT DIR: the commit's files without .git (tests cannot reach the repository).
export_tree() {
  mkdir -p "$2"
  git archive --format=tar "$1" | tar -x -C "$2"
}

do_run() {
  local wt stopped p e op a b subj line tc tt sampler
  # shellcheck disable=SC1090
  . "$state"
  [[ "$status" == check ]] || { log "nothing to run (status $status)"; return; }
  status=running

  # The tag's commit, verified again.
  step=fetch
  git fetch -q --no-tags "$PMX_UPSTREAM_URL" "refs/tags/$tag" || { status=error; reason "cannot fetch $tag"; return; }
  [[ "$(git rev-parse "FETCH_HEAD^{commit}")" == "$upstream_commit" ]] ||
    { status=error; reason "$tag moved while checking: now $(git rev-parse --short "FETCH_HEAD^{commit}")"; return; }
  git update-ref refs/pmx-check/upstream "$upstream_commit"

  # 1. Rebase, in a temporary worktree.
  step=rebase
  wt="$(mktemp -d "${TMPDIR:-/tmp}/provimux-check-wt.XXXXXX")"
  cleanup_paths+=("$wt")
  git worktree add -q --detach "$wt" "$old_main"
  log "git rebase $tag"
  if ! (cd "$wt" && git -c user.name='github-actions[bot]' -c user.email='41898282+github-actions[bot]@users.noreply.github.com' \
    -c rebase.autoStash=false -c rebase.updateRefs=false \
    rebase --committer-date-is-author-date "$upstream_commit") >"$out/logs/rebase.log" 2>&1; then
    stopped="$(cd "$wt" && git rev-parse -q --verify REBASE_HEAD || true)"
    (
      cd "$wt"
      jq -n --arg stopped "$stopped" \
        --arg subject "$( [[ -n "$stopped" ]] && git log -1 --format=%s "$stopped" || true)" \
        --arg conflicts "$(git diff --name-only --diff-filter=U || true)" \
        --rawfile log "$out/logs/rebase.log" \
        '{ok: false, stopped_at: $stopped, subject: $subject,
          conflicts: ($conflicts | split("\n") | map(select(length > 0))),
          log: ($log | split("\n") | .[-30:] | join("\n"))}' >"$out/rebase.json"
      git rebase --abort >/dev/null 2>&1 || true
    )
    status=fail
    reason "main does not rebase cleanly onto $tag${stopped:+ (stopped at ${stopped:0:8})}"
    return
  fi
  candidate="$(cd "$wt" && git rev-parse HEAD)"
  commits="$(git rev-list --count "$upstream_commit..$candidate")"
  echo '{"ok": true}' >"$out/rebase.json"
  git update-ref refs/pmx-check/candidate "$candidate"
  git bundle create -q "$out/candidate.bundle" refs/pmx-check/candidate "^$upstream_commit"
  log "candidate ${candidate:0:8}: $commits commits on $tag"

  # 2. range-diff: which owner commits the rebase dropped or changed.
  step=range-diff
  git range-diff --no-color --no-notes "$old_base_commit..$old_main" "$upstream_commit..$candidate" >"$out/logs/range-diff.txt"
  # Header lines: "<n>:  <sha> <op> <n>:  <sha> <subject>", op one of = ! < >.
  : >"$out/dropped.tsv"; : >"$out/changed.tsv"; : >"$out/added.tsv"
  grep -E '^ *([0-9]+|-+): +([0-9a-f]+|-+) [=!<>] +([0-9]+|-+): +([0-9a-f]+|-+) ' "$out/logs/range-diff.txt" |
    while IFS= read -r line; do
      p="$(sed -E 's/^ *([0-9]+|-+): +([0-9a-f]+|-+) ([=!<>]) +([0-9]+|-+): +([0-9a-f]+|-+) (.*)$/\3	\2	\5	\6/' <<<"$line")"
      IFS=$'\t' read -r op a b subj <<<"$p"
      case "$op" in
        '<') printf '%s\t%s\n' "$(git rev-parse "$a^{commit}")" "$subj" >>"$out/dropped.tsv" ;;
        '!') printf '%s\t%s\n' "$(git rev-parse "$a^{commit}")" "$subj" >>"$out/changed.tsv" ;;
        '>') printf '%s\t%s\n' "$(git rev-parse "$b^{commit}")" "$subj" >>"$out/added.tsv" ;;
      esac
    done
  [[ -s "$out/added.tsv" ]] && reason "the candidate has commits that are not in the old main (range-diff '>')"
  [[ -z "$(git rev-list --merges "$upstream_commit..$candidate")" ]] || reason "the candidate has merge commits above $tag"

  # 3. Workflow files: every path new in the tag stops publication until the owner decides.
  step=workflows
  git ls-tree -r --name-only "$old_base_commit" -- .github/workflows/ | sort >"$out/wf-old.txt"
  git ls-tree -r --name-only "$upstream_commit" -- .github/workflows/ | sort >"$out/wf-tag.txt"
  comm -13 "$out/wf-old.txt" "$out/wf-tag.txt" >"$out/wf-new.txt"
  comm -23 "$out/wf-old.txt" "$out/wf-tag.txt" >"$out/wf-removed.txt"

  # 4. Guard: the base it reports must be exactly the tag's commit.
  step=guard
  if (cd "$wt" && PMX_UPSTREAM="$PMX_UPSTREAM_URL" PMX_EXPECT_BASE="$tag" PMX_EXPECT_BASE_COMMIT="$upstream_commit" \
    bash "$here/guard.sh") >"$out/logs/guard.log" 2>&1; then
    guard_ok=true
  else
    guard_ok=false; reason "the guard rejects the candidate"
  fi

  # 5. Build (in a copy without .git, like the tests).
  step=build
  tc="$(mktemp -d "${TMPDIR:-/tmp}/provimux-check-candidate.XXXXXX")"
  cleanup_paths+=("$tc")
  export_tree "$candidate" "$tc"
  log "go build ./..."
  if (cd "$tc" && go build ./...) >"$out/logs/build.log" 2>&1; then
    build_ok=true
  else
    build_ok=false; reason "go build ./... fails on the candidate"
  fi

  # 6. Tests: go test ./... on both trees, the candidate's failing packages rerun whole.
  step="test"
  tt="$(mktemp -d "${TMPDIR:-/tmp}/provimux-check-tag.XXXXXX")"
  cleanup_paths+=("$tt")
  export_tree "$upstream_commit" "$tt"
  (cd "$tc" && go list -e -f '{{.ImportPath}}' ./...) 2>"$out/logs/list-candidate.stderr" | jq -R -s 'split("\n") | map(select(length > 0))' >"$out/pkgs-candidate.json"
  (cd "$tt" && go list -e -f '{{.ImportPath}}' ./...) 2>"$out/logs/list-tag.stderr" | jq -R -s 'split("\n") | map(select(length > 0))' >"$out/pkgs-tag.json"
  [[ "$(jq length "$out/pkgs-candidate.json")" -gt 0 ]] || reason "go list ./... finds no package on the candidate"
  [[ "$(jq length "$out/pkgs-tag.json")" -gt 0 ]] || reason "go list ./... finds no package on $tag"
  sample_resources "$out/logs/resources.log" &
  sampler=$!
  log "go test ./... (candidate)"
  gotest "$tc" "$out/logs/test-candidate" ./...
  log "go test ./... ($tag)"
  gotest "$tt" "$out/logs/test-tag" ./...
  # Rerun every package with a failing test (not those that do not build), whole.
  echo '{"term":{},"build_failed":[],"failed":[],"passed":[]}' >"$out/logs/rerun.sum.json"
  jq -r '. as $s | [.failed[] | split(" ")[0]] | unique[] | select(. as $p | ($s.build_failed | index($p)) == null)' \
    "$out/logs/test-candidate.sum.json" >"$out/rerun-pkgs.txt"
  if [[ -s "$out/rerun-pkgs.txt" ]]; then
    log "rerun: $(paste -sd ' ' "$out/rerun-pkgs.txt")"
    # shellcheck disable=SC2046
    gotest "$tc" "$out/logs/test-rerun" $(cat "$out/rerun-pkgs.txt")
    cp "$out/logs/test-rerun.sum.json" "$out/logs/rerun.sum.json"
  fi
  kill "$sampler" 2>/dev/null || true
  jq -n --slurpfile cand "$out/logs/test-candidate.sum.json" --slurpfile pris "$out/logs/test-tag.sum.json" \
    --slurpfile rerun "$out/logs/rerun.sum.json" \
    --slurpfile cpkgs "$out/pkgs-candidate.json" --slurpfile ppkgs "$out/pkgs-tag.json" \
    --argjson cexit "$(cat "$out/logs/test-candidate.exit")" --argjson pexit "$(cat "$out/logs/test-tag.exit")" \
    -f "$here/testcmp.jq" >"$out/tests.json"
  if [[ "$(jq -r .ok "$out/tests.json")" == true ]]; then
    tests_ok=true
  else
    tests_ok=false
    jq -r '.reasons[]' "$out/tests.json" | while IFS= read -r e; do reason "$e"; done
  fi

  step="done"
  if [[ -s "$reasons" ]]; then status=fail; else status=pass; fi
}

case "$mode" in
  select)
    # When there is something to check, result.json says so until run replaces it.
    trap 'save_state; if [[ "$status" == check ]]; then step=run; write_result error "the run step did not finish (see the job log)"; else write_result; fi' EXIT
    do_select
    ;;
  run)
    trap 'cleanup; write_result' EXIT
    do_run
    ;;
  all)
    trap 'cleanup; write_result' EXIT
    do_select
    save_state
    [[ "$status" == check ]] && do_run
    ;;
  *) echo "usage: $0 [select|run]" >&2; exit 2 ;;
esac
echo "status=$status" >>"${GITHUB_OUTPUT:-/dev/null}"
echo "tag=$tag" >>"${GITHUB_OUTPUT:-/dev/null}"
echo "out=$out" >>"${GITHUB_OUTPUT:-/dev/null}"
