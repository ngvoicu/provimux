#!/usr/bin/env bash
# Provimux sync (PMX-14), run by .github/workflows/provimux-sync.yml and by hand
# (.github/provimux/SYNC.md).
#
# Rebases the checked-out Provimux main (HEAD) onto a CLIProxyAPI release tag and
# checks the result is no worse than that release. It pushes nothing: the caller
# pushes when the status is "pass".
#
#   1. the tag: $PMX_SYNC_TAG, or the newest release of $PMX_UPSTREAM_REPO;
#      skip (status=skip) when v<base>-pmx.* already exists on $PMX_REMOTE or the tag
#      is not newer than HEAD's base;
#   2. a real run ($PMX_DRY_RUN=false) without $SYNC_TOKEN stops here (status=error);
#   3. fetch the tag; refuse workflow files the tag adds under .github/workflows
#      (they would be enabled, and could fire on the tags the sync pushes);
#   4. git rebase <tag> (never --onto: commits upstream took as they were drop out);
#   5. go build ./...;
#   6. go test on every package whose Go files the stack and layer change, on the
#      rebased tree and on the pristine tag; a test failing on Provimux that does not
#      fail on the tag (and fails again on a rerun) is a new failure;
#   7. .github/provimux/guard.sh on the rebased HEAD, as on a push to main;
#
# Works on a detached HEAD, so no branch moves. Leaves HEAD at the rebased commit on
# pass (the caller pushes it as main, with the tags v<base> and v<base>-pmx.1); on
# fail, HEAD is back at the old main. The only local ref it adds is upstream's tag.
# Runs with bash 3.2 (macOS) and needs git, go, jq (and gh without PMX_SYNC_TAG).
#
# Env:
#   PMX_SYNC_TAG       upstream tag (vX.Y.Z); empty = upstream's latest release
#   PMX_DRY_RUN        true (default) or false
#   SYNC_TOKEN         only checked for presence here; the caller pushes with it
#   PMX_REMOTE         remote of this repo, for the existing -pmx tags (default origin)
#   PMX_UPSTREAM       upstream git URL (default https://github.com/router-for-me/CLIProxyAPI)
#   PMX_UPSTREAM_REPO  owner/name for the latest release (default router-for-me/CLIProxyAPI)
#   PMX_SYNC_OUT       directory for summary.md, issue.md, logs (default: a new temp dir)
# Outputs (to $GITHUB_OUTPUT when set, else stdout): status (pass|fail|skip|error),
# step, tag, base, old, new, out.
# Exit: 0 pass or skip, 1 fail, 2 error.
set -euo pipefail

# Run from a copy: the rebase rewrites the working tree, this file included.
if [[ -z "${PMX_SYNC_COPY:-}" ]]; then
  copy="$(mktemp "${TMPDIR:-/tmp}/provimux-sync.XXXXXX")"
  cp "$0" "$copy"
  PMX_SYNC_COPY="$copy" exec bash "$copy" "$@"
fi
trap 'rm -f "${PMX_SYNC_COPY:?}"' EXIT

dry_run="${PMX_DRY_RUN:-true}"
remote="${PMX_REMOTE:-origin}"
upstream="${PMX_UPSTREAM:-https://github.com/router-for-me/CLIProxyAPI}"
upstream_repo="${PMX_UPSTREAM_REPO:-router-for-me/CLIProxyAPI}"
out="${PMX_SYNC_OUT:-$(mktemp -d "${TMPDIR:-/tmp}/provimux-sync-out.XXXXXX")}"
mkdir -p "$out"
summary="$out/summary.md"
issue="$out/issue.md"
: >"$summary"
rm -f "$issue"
export PMX_UPSTREAM="$upstream" # guard.sh reads it

tag='' base='' old='' new='' step=''
log() { echo "==> $*" >&2; }
emit() {
  local f="${GITHUB_OUTPUT:-/dev/stdout}"
  {
    echo "status=$1"
    echo "step=$step"
    echo "tag=$tag"
    echo "base=$base"
    echo "old=$old"
    echo "new=$new"
    echo "out=$out"
  } >>"$f"
}
md() { printf '%s\n' "$@" >>"$summary"; }
skip() {
  log "skip: $*"
  md "Nothing to do: $*"
  emit skip
  exit 0
}
error() {
  echo "::error::$*" >&2
  md "**Error** ($step): $*"
  emit error
  exit 2
}
# fail STEP-TEXT DETAILS-FILE: the result is worse than upstream (or does not apply).
fail() {
  local what=$1 details=${2:-}
  echo "::error::Sync to $tag failed at step '$step': $what" >&2
  {
    echo "The sync of Provimux \`main\` ($(git rev-parse --short "$old")) onto CLIProxyAPI \`$tag\` failed at step **$step**: $what"
    echo
    [[ -n "$details" && -s "$details" ]] && { cat "$details"; echo; }
    echo "Nothing was published. Fix it by hand following \`.github/provimux/SYNC.md\`; the next scheduled run retries and closes this issue once \`v$base-pmx.1\` is published."
  } >"$issue"
  { echo "### Failed at step: $step"; echo; cat "$issue"; } >>"$summary"
  emit fail
  exit 1
}

newest_base() { # newest vX.Y.Z tag reachable from $1
  git tag --merged "$1" --list 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1
}

# --- 1. Which release, and is there anything to do ---------------------------------
step=tag
[[ -z "$(git status --porcelain --untracked-files=no)" ]] || error "The working tree has uncommitted changes."
old="$(git rev-parse HEAD)"
git checkout -q --detach
tag="${PMX_SYNC_TAG:-}"
if [[ -z "$tag" ]]; then
  tag="$(gh api "repos/$upstream_repo/releases/latest" --jq .tag_name)" || error "Cannot read the latest release of $upstream_repo."
fi
[[ "$tag" =~ ^v([0-9]+\.[0-9]+\.[0-9]+)$ ]] || error "Upstream tag '$tag' is not vX.Y.Z."
base="${BASH_REMATCH[1]}"
old_base="$(newest_base HEAD)"
[[ -n "$old_base" ]] || error "No vX.Y.Z tag is reachable from main ($(git rev-parse --short HEAD)); fetch the tags."
log "upstream $tag, main $(git rev-parse --short HEAD) on $old_base, dry run: $dry_run"
md "### Provimux sync: CLIProxyAPI $tag" "" "- main: \`$(git rev-parse --short HEAD)\` on \`$old_base\`" "- dry run: $dry_run" ""

existing="$(git ls-remote --tags "$remote" "refs/tags/v$base-pmx.*" | awk '{print $2}' | sed 's|refs/tags/||; s/\^{}$//' | sort -u | paste -sd ' ' -)"
[[ -z "$existing" ]] || skip "$tag already has a Provimux release ($existing)."
if [[ "$(printf '%s\n%s\n' "$old_base" "$tag" | sort -V | tail -n 1)" != "$tag" || "$old_base" == "$tag" ]]; then
  skip "$tag is not newer than main's base $old_base."
fi

# --- 2. A real run needs the token to push ----------------------------------------
step=token
if [[ "$dry_run" != "true" && -z "${SYNC_TOKEN:-}" ]]; then
  error "Secret SYNC_TOKEN is not set: a real sync cannot push main and the tags (GITHUB_TOKEN can neither bypass the main ruleset nor start the release workflows). Add it as described in .github/provimux/SYNC.md, or run with dry_run."
fi

# --- 3. Fetch the tag; no new upstream workflow files -------------------------------
step=fetch
git fetch -q --no-tags "$upstream" "+refs/tags/$tag:refs/tags/$tag.sync-check" ||
  error "Cannot fetch $tag from $upstream."
fetched="$(git rev-parse "refs/tags/$tag.sync-check^{commit}")"
git tag -d "$tag.sync-check" >/dev/null
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  [[ "$(git rev-parse "refs/tags/$tag^{commit}")" == "$fetched" ]] ||
    error "Local tag $tag is $(git rev-parse --short "refs/tags/$tag^{commit}"), upstream's is ${fetched:0:8}."
else
  git fetch -q --no-tags "$upstream" "refs/tags/$tag:refs/tags/$tag"
fi

step=workflows
added="$(git diff --name-only --diff-filter=A "$old_base" "$tag" -- .github/workflows/)"
if [[ -n "$added" ]]; then
  details="$out/workflows.md"
  { echo "Workflow files $tag adds:"; echo; sed 's/^/- `/; s/$/`/' <<<"$added"; } >"$details"
  fail "upstream added workflow files. Pushed to main they would be active (upstream workflows are disabled one by one), and a tag push could run them. Push main by hand, disable them with \`gh workflow disable\`, record them in .github/provimux/upstream-workflows.md, then push the tags (SYNC.md)." "$details"
fi

# --- 4. Rebase --------------------------------------------------------------------
step=rebase
log "git rebase $tag"
if ! git rebase "$tag" >"$out/rebase.log" 2>&1; then
  details="$out/rebase.md"
  stopped="$(git rev-parse -q --verify REBASE_HEAD || true)"
  {
    if [[ -n "$stopped" ]]; then
      echo "Stopped at \`$(git rev-parse --short "$stopped")\` $(git log -1 --format='%s' "$stopped")"
      echo
      echo "Conflicting files:"
      echo
      git diff --name-only --diff-filter=U | sed 's/^/- `/; s/$/`/'
      echo
      echo "If upstream merged this patch in another form, drop that PR's commits from the stack and rebase again (SYNC.md, \"Upstream took a patch in another form\")."
    else
      echo '```'
      tail -n 30 "$out/rebase.log"
      echo '```'
    fi
  } >"$details"
  git rebase --abort >/dev/null 2>&1 || true
  git checkout -q --detach "$old"
  fail "the stack does not rebase cleanly onto $tag." "$details"
fi
new="$(git rev-parse HEAD)"

# Commits the rebase dropped (upstream already has them, or they became empty):
# matched by author, author date and subject, which a rebase keeps.
git log --format='%an%x09%at%x09%s' "$tag..$new" >"$out/new.keys"
git log --reverse --format='%h%x09%an%x09%at%x09%s' "$old_base..$old" |
  while IFS=$'\t' read -r c an at s; do
    grep -qxF "$an"$'\t'"$at"$'\t'"$s" "$out/new.keys" || echo "- \`$c\` $s"
  done >"$out/dropped.md"
md "- rebased: \`$(git rev-parse --short "$old")\` -> \`$(git rev-parse --short "$new")\` on \`$tag\`, $(git rev-list --count "$tag..$new") commits (was $(git rev-list --count "$old_base..$old"))"
if [[ -s "$out/dropped.md" ]]; then md "- commits the rebase dropped:" "$(sed 's/^/  /' "$out/dropped.md")"; else md "- commits the rebase dropped: none"; fi

# --- 5. Build -----------------------------------------------------------------------
step=build
log "go build ./..."
if ! go build ./... >"$out/build.log" 2>&1; then
  details="$out/build.md"
  { echo '```'; tail -n 40 "$out/build.log"; echo '```'; } >"$details"
  git checkout -q --detach "$old"
  fail "go build ./... fails on the rebased tree." "$details"
fi
md "- build: ok"

# --- 6. Tests: Provimux vs the pristine tag ---------------------------------------------
step="test"
module="$(go list -m)"
pkgs=()
while read -r d; do
  [[ -d "$d" ]] && compgen -G "$d/*.go" >/dev/null && pkgs+=("./$d")
done < <(git diff --name-only "$tag" "$new" -- '*.go' | sed 's|/[^/]*$||; s|^[^/]*\.go$|.|' | sort -u)
((${#pkgs[@]})) || pkgs=(./cmd/server)
pristine="$(mktemp -d "${TMPDIR:-/tmp}/provimux-pristine.XXXXXX")"
cleanup_pristine() { git worktree remove --force "$pristine" >/dev/null 2>&1 || true; rm -rf "${pristine:?}"; }
git worktree add -q --detach "$pristine" "$tag"
pristine_pkgs=()
for p in "${pkgs[@]}"; do compgen -G "$pristine/${p#./}/*.go" >/dev/null && pristine_pkgs+=("$p"); done

# failures JSON-LOG: "package test" per failing test, "package [package]" for a
# package that fails without a failing test (build error, panic, TestMain).
failures() {
  jq -r 'select(.Action=="fail" or .Action=="build-fail")
         | if .Test then "\(.Package) \(.Test)"
           else "\(.Package // .ImportPath) [package]" end' "$1" 2>/dev/null |
    sort -u | awk '{ if ($2=="[package]") pk[$1]=1; else { t[$1]=1; print } }
                   END { for (p in pk) if (!(p in t)) print p " [package]" }' | sort -u
}
gotest() { (cd "$1" && shift && go test -json -count=1 -timeout 15m "$@" 2>&1 | grep '^{' || true); }

log "go test (Provimux): ${pkgs[*]}"
gotest . "${pkgs[@]}" >"$out/test-provimux.json"
log "go test (pristine $tag): ${pristine_pkgs[*]:-none}"
if ((${#pristine_pkgs[@]})); then gotest "$pristine" "${pristine_pkgs[@]}" >"$out/test-pristine.json"; else : >"$out/test-pristine.json"; fi
cleanup_pristine
failures "$out/test-provimux.json" >"$out/fail-provimux.txt"
failures "$out/test-pristine.json" >"$out/fail-pristine.txt"
comm -23 "$out/fail-provimux.txt" "$out/fail-pristine.txt" >"$out/fail-new.txt"

# Rerun each new failure once, so a flaky test does not block a release.
: >"$out/fail-confirmed.txt"
: >"$out/fail-flaky.txt"
while read -r pkg test; do
  [[ -z "$pkg" ]] && continue
  rel="./${pkg#"$module"/}"
  [[ "$pkg" == "$module" ]] && rel=.
  run="^${test%%/*}\$"
  [[ "$test" == "[package]" ]] && run='.'
  if [[ -n "$(gotest . -run "$run" "$rel" | failures /dev/stdin)" ]]; then
    echo "$pkg $test" >>"$out/fail-confirmed.txt"
  else
    echo "$pkg $test" >>"$out/fail-flaky.txt"
  fi
done <"$out/fail-new.txt"

passed_count() { jq -r 'select(.Action=="pass" and .Test) | "\(.Package) \(.Test)"' "$1" | sort -u | wc -l | tr -d ' '; }
{
  echo "- tests: ${#pkgs[@]} packages (${#pristine_pkgs[@]} also on $tag); Provimux $(passed_count "$out/test-provimux.json") passed, $(wc -l <"$out/fail-provimux.txt" | tr -d ' ') failed; $tag $(passed_count "$out/test-pristine.json") passed, $(wc -l <"$out/fail-pristine.txt" | tr -d ' ') failed"
  if [[ -s "$out/fail-pristine.txt" ]]; then echo "  - failing on $tag too (not counted):"; sed 's/^/    - `/; s/$/`/' "$out/fail-pristine.txt"; fi
  if [[ -s "$out/fail-flaky.txt" ]]; then echo "  - failed once on Provimux, passed on a rerun (flaky, not counted):"; sed 's/^/    - `/; s/$/`/' "$out/fail-flaky.txt"; fi
  if [[ -s "$out/fail-confirmed.txt" ]]; then echo "  - **failing on Provimux only:**"; sed 's/^/    - `/; s/$/`/' "$out/fail-confirmed.txt"; fi
} >>"$summary"
if [[ -s "$out/fail-confirmed.txt" ]]; then
  details="$out/test.md"
  {
    echo "Tests that fail on the rebased Provimux and pass on $tag (each failed again on a rerun):"
    echo
    sed 's/^/- `/; s/$/`/' "$out/fail-confirmed.txt"
    echo
    echo "Packages tested: ${pkgs[*]}"
  } >"$details"
  git checkout -q --detach "$old"
  fail "$(wc -l <"$out/fail-confirmed.txt" | tr -d ' ') test(s) fail on Provimux that pass on $tag." "$details"
fi

# --- 7. Guard -------------------------------------------------------------------------
step=guard
log "guard.sh"
if ! bash .github/provimux/guard.sh >"$out/guard.log" 2>&1; then
  details="$out/guard.md"
  { echo '```'; tail -n 40 "$out/guard.log"; echo '```'; } >"$details"
  git checkout -q --detach "$old"
  fail "the Provimux guard rejects the rebased main." "$details"
fi
md "- guard: $(tail -n 1 "$out/guard.log")"

md "" "Would push (\`git push --atomic --force-with-lease=refs/heads/main:$old\`):" "" \
  "- \`main\`: \`$old\` -> \`$new\`" \
  "- tag \`$tag\` -> \`$(git rev-parse "$tag^{commit}")\` (upstream's)" \
  "- tag \`v$base-pmx.1\` -> \`$new\` (starts provimux-release and provimux-docker)"
step="done"
emit pass
log "pass: main $(git rev-parse --short "$old") -> $(git rev-parse --short "$new"), tags $tag v$base-pmx.1"
