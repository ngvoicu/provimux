#!/usr/bin/env bash
# shellcheck disable=SC2030,SC2031 # PATH is set for the subshells that run the stubs only
# Fixture tests for the Provimux sync scripts (.github/provimux/SYNC.md, "Testing the
# scripts"). Offline: local git repositories stand in for upstream and for Provimux, and
# a stub gh answers the GitHub API from files. Runs no repository code but these scripts.
#
#   bash .github/provimux/selftest.sh        exit 0 when every case passes
#
# Cases:
#   testcmp.jq    an owner-only test failing twice while an unrelated test fails upstream;
#                 a failing test whose package rerun does not build; a rerun with no valid
#                 exit status; a test first seen failing on the rerun; a package-level
#                 failure on both trees; a package rerun that does not build after an
#                 upstream-waived failure; and the two waivers that must still hold (same test fails
#                 on the tag; explicit pass on the rerun)
#   guard.sh      base only upstream (no local tag); a reachable base tag withdrawn or
#                 moved upstream; the expected base
#   release-meta  the base tag only upstream; a withdrawn base
#   report.sh     a successful release run for the wrong SHA (not published), the right
#                 SHA (published), a failed check job (not published); a failure result
#                 (every detail shown)
#   guard.sh      on git objects only (a bare repository, HEAD = the candidate)
#   archive       git archive = the commit's files (names, blobs, modes), NUL-delimited:
#                 plain (with an executable, a symlink and a file name with a newline)
#                 passes; export-ignore (also of the newline file) and export-subst fail
#   verifier      the inline first step of provimux-publish.yml (run from the workflow
#                 file between its markers, with a stub gh and git): passes only for
#                 workflow_sha = the check's head_sha = the live main, the event's exact
#                 attempt completed/success on main (schedule or dispatch, this repo), one
#                 verdict job with exactly one artifact annotation; refuses an old
#                 revision or rerun (workflow_sha), main moved, another event, a failed,
#                 running or mismatched attempt, a push run, no or two verdict jobs, no or
#                 two annotations, an unreadable main
#   autopublish   decide (check_artifact for the given run and attempt): only the artifact
#                 the verdict job of that attempt bound by its one annotation counts, and
#                 it must be the verified id (a planted artifact of the same run is
#                 ignored; another run's, a second annotation, a name or digest mismatch,
#                 an expired artifact, a missing verdict job fail; another attempt's
#                 binding is not used).
#                 notify reads the remote first: no token (no hand publish offered; a check
#                 of the tag instead); refused (nothing on the remote; the missing token
#                 noted); main moved; main = candidate without tags; -pmx tag with an
#                 incomplete release (rerun command, never "publish again"); a lost push
#                 acknowledgement with a complete release (closes an open issue, opens
#                 none, promises no check); remote unreadable; no tag; a failed tap step
#                 (its own issue "homebrew tap: <tag> needs the owner", opened or
#                 commented on, never retitled or closed; the sync issue is closed)
#   publish.sh    --ci: refuses a push token in its environment, and bad mode flags
#   check.sh      state.json parsed, never sourced; verdict step outcomes and job
#                 conclusions; run_artifact by id, run, name, digest and bytes
#   releases_status / report.sh  the newest -pmx.<n> decides; the sweep of older bases;
#                 a transient error, a publish and the sweep never touch the tap issue
#   tap.sh        release (immutable, assets, digests), verify (only version, url,
#                 sha256 lines), prepare (only GitHub's latest, read strictly: a
#                 superseded tag on both paths leaves the tap alone; a higher tap version
#                 is refused; a same-version repair; local commit, lease as read, nothing
#                 pushed), check (ok only for the latest, byte-identical), artifact (bound
#                 by tap-check of a successful run)
#   provenance    the release workflow's inline check (run from the workflow): only the
#                 owner's newest success status for exactly this tag counts; a bot's,
#                 another tag's, a moved tag, an unreadable list (told apart) refuse
#   T-90          tap.sh still (latest again before a tap push; both writers);
#                 notify without the optional plan; a failed tap-check opens the tap
#                 issue (the report job, run from the workflow); publish.sh's tap runs
#                 even when closing the sync issue fails
#   latest        release-latest (run from the workflow): only its own release id,
#                 never backward, stops on an unreadable or tagless latest, fails a
#                 wrong readback; a readback still showing the old latest (or none) is
#                 read again (stub sleep: 2 4 8 16 32 s), another release fails at once
#   published     publish-release's readback (run from the workflow): not found (404),
#                 a draft or not immutable is read again; another read error fails at once
#   image readback  put_manifest of the image step (run from the workflow, a stub curl
#                 for the registry): an absent tag right after the PUT is read again
#                 (6 reads, then "never appeared"); another digest or an HTTP 500 fails
#                 at once
#   notify table  the base tag's commit once; a failed release run as "failed"
#   README commit (design/root-readme.md) check.sh prepare on one upstream tag per case:
#                 the READMEs edited, rewritten (a plain range-diff then unpairs the
#                 README commit), untouched: resolved, same candidate twice; a later
#                 layer conflict, a moved README_CN.md, an added README_KO.md (guard), an
#                 owner patch conflict, a README commit that changes another path, two
#                 README commits: fail. stack_range_diff and readme_normal on hand-built
#                 candidates: each of the five conditions alone, the lists of the
#                 segments, empty segments, whole names. guard.sh rule 5. publish.sh --ci
#                 --plan end to end: the README case, a bundle out of normal form, a
#                 result.json naming another README commit. git range-diff only in
#                 common.sh; guard.sh's set = PMX_README_PATHS
# Needs bash 3.2+, git, jq.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/provimux-selftest.XXXXXX")"
trap 'rm -rf "${tmp:?}"' EXIT
pass=0 failed=0
ok() { echo "ok    $*"; pass=$((pass + 1)); }
bad() { echo "FAIL  $*"; failed=$((failed + 1)); }
# expect NAME WANT GOT
expect() { if [[ "$3" == "$2" ]]; then ok "$1"; else bad "$1 (want '$2', got '$3')"; fi; }
# contains NAME FILE TEXT
contains() { if grep -qF -- "$3" "$2"; then ok "$1"; else bad "$1 ('$3' not in the output)"; fi; }
lacks() { if grep -qF -- "$3" "$2"; then bad "$1 ('$3' in the output)"; else ok "$1"; fi; }
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com \
  GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com

# --- testcmp.jq ----------------------------------------------------------------------------------
# ev ACTION PACKAGE [TEST]: one go test -json result event.
ev() { if [[ -n "${3:-}" ]]; then printf '{"Action":"%s","Package":"%s","Test":"%s"}\n' "$1" "$2" "$3"; else printf '{"Action":"%s","Package":"%s"}\n' "$1" "$2"; fi; }
sum() { jq -R -c -f "$here/testevents.jq" | jq -s -f "$here/testsum.jq"; }
# cmp CAND PRIS RERUN|- CEXIT PEXIT REXIT|null PKGS: testcmp.jq's verdict as "<ok> <new> <flaky> <upstream>".
cmp() {
  local d="$tmp/cmp" pk
  mkdir -p "$d"
  sum <<<"$1" >"$d/c.json"; sum <<<"$2" >"$d/p.json"
  if [[ "$3" == - ]]; then echo '{"term":{},"build_failed":[],"failed":[],"passed":[]}' >"$d/r.json"; else sum <<<"$3" >"$d/r.json"; fi
  pk="$(tr ' ' '\n' <<<"$7" | jq -R -s 'split("\n") | map(select(length > 0))')"
  echo "$pk" >"$d/pk.json"
  jq -n --slurpfile cand "$d/c.json" --slurpfile pris "$d/p.json" --slurpfile rerun "$d/r.json" \
    --slurpfile cpkgs "$d/pk.json" --slurpfile ppkgs "$d/pk.json" \
    --argjson cexit "$4" --argjson pexit "$5" --argjson rexit "$6" -f "$here/testcmp.jq" >"$d/out.json"
  jq -r '"\(.ok) new=\(.new_failures | join(",")) flaky=\(.flaky | join(",")) upstream=\(.failing_on_tag_too | join(","))"' "$d/out.json"
}

echo "# testcmp.jq"
# A: the owner's test fails twice; an unrelated upstream test fails on the tag.
cand="$(ev fail x/a TestOwner; ev pass x/a TestShared; ev fail x/a)"
pris="$(ev fail x/a TestUpstreamBroken; ev pass x/a TestShared; ev fail x/a)"
rerun="$(ev fail x/a TestOwner; ev pass x/a TestShared; ev fail x/a)"
expect "owner-only test failing twice, unrelated upstream failure: fails the check" \
  "false new=x/a TestOwner flaky= upstream=" "$(cmp "$cand" "$pris" "$rerun" 1 1 1 x/a)"
# B: the test fails; its package rerun does not build.
cand="$(ev fail x/b TestX; ev fail x/b)"
pris="$(ev pass x/b TestX; ev pass x/b)"
rerun='{"Action":"build-fail","ImportPath":"x/b [x/b.test]"}
{"Action":"fail","Package":"x/b","FailedBuild":"x/b [x/b.test]"}'
expect "failing test whose package rerun fails to build: fails the check" \
  "false new=x/b TestX flaky= upstream=" "$(cmp "$cand" "$pris" "$rerun" 1 0 1 x/b)"
# B2: the rerun passes but has no valid exit status (killed, or 0 with a failing package).
rerun="$(ev pass x/b TestX; ev pass x/b)"
expect "rerun pass with exit status 124: fails the check" \
  "false new=x/b TestX flaky= upstream=" "$(cmp "$cand" "$pris" "$rerun" 1 0 124 x/b)"
expect "rerun pass, no rerun exit status at all: fails the check" \
  "false new=x/b TestX flaky= upstream=" "$(cmp "$cand" "$pris" "$rerun" 1 0 null x/b)"
# B3: the rerun has a result for another test only (the failing one did not run).
rerun="$(ev pass x/b TestOther; ev pass x/b)"
expect "rerun without the failing test: fails the check" \
  "false new=x/b TestX flaky= upstream=" "$(cmp "$cand" "$pris" "$rerun" 1 0 0 x/b)"
# C: the same test fails on the tag: baseline waiver.
cand="$(ev fail x/c TestUp; ev fail x/c)"
pris="$(ev fail x/c TestUp; ev fail x/c)"
rerun="$(ev fail x/c TestUp; ev fail x/c)"
expect "same test fails on the tag: waived" "true new= flaky= upstream=x/c TestUp" "$(cmp "$cand" "$pris" "$rerun" 1 1 1 x/c)"
# D: explicit pass on a valid whole-package rerun: flaky waiver.
cand="$(ev fail x/d TestF; ev fail x/d)"
pris="$(ev pass x/d TestF; ev pass x/d)"
rerun="$(ev pass x/d TestF; ev pass x/d)"
expect "explicit pass on the package rerun: flaky" "true new= flaky=x/d TestF upstream=" "$(cmp "$cand" "$pris" "$rerun" 1 0 0 x/d)"
# E: a package that fails without a failing test, and passes on the rerun.
cand="$(ev fail x/e)"
pris="$(ev pass x/e)"
rerun="$(ev pass x/e)"
expect "[package] failure, package passes on the rerun: flaky" "true new= flaky=x/e [package] upstream=" "$(cmp "$cand" "$pris" "$rerun" 1 0 0 x/e)"
expect "[package] failure, no rerun: fails the check" "false new=x/e [package] flaky= upstream=" "$(cmp "$cand" "$pris" - 1 0 null x/e)"
# F: a transient panic in TestEarly stops the first run before TestOwnerFeature; on the
# rerun TestEarly passes and TestOwnerFeature fails (both pass on the tag).
cand="$(ev fail x/f TestEarly; ev fail x/f)"
pris="$(ev pass x/f TestEarly; ev pass x/f TestOwnerFeature; ev pass x/f)"
rerun="$(ev pass x/f TestEarly; ev fail x/f TestOwnerFeature; ev fail x/f)"
expect "test first seen failing on the rerun: fails the check" \
  "false new=x/f TestOwnerFeature flaky=x/f TestEarly upstream=" "$(cmp "$cand" "$pris" "$rerun" 1 0 1 x/f)"
# G: package-level failure (setup, no test name) twice on the candidate; upstream's setup
# fails too, for another reason. Never waived by the tag.
cand="$(ev fail x/g)"
pris="$(ev fail x/g)"
rerun="$(ev fail x/g)"
expect "package-level failure, also package-level on the tag: fails the check" \
  "false new=x/g [package] flaky= upstream=" "$(cmp "$cand" "$pris" "$rerun" 1 1 1 x/g)"
# H: the same named test fails on the tag, and the candidate's package rerun fails to
# build: the baseline waiver holds for the test, but the build failure fails the check.
cand="$(ev fail x/h TestUp; ev fail x/h)"
pris="$(ev fail x/h TestUp; ev fail x/h)"
rerun='{"Action":"build-fail","ImportPath":"x/h [x/h.test]"}
{"Action":"fail","Package":"x/h","FailedBuild":"x/h [x/h.test]"}'
expect "upstream-waived failure whose package rerun fails to build: fails the check" \
  "false new= flaky= upstream=x/h TestUp" "$(cmp "$cand" "$pris" "$rerun" 1 1 1 x/h)"

# --- fixture repositories ------------------------------------------------------------------------
# up: "upstream" with v1.0.0 -> v1.1.0, and an unrelated commit (for a moved tag).
up="$tmp/up"
git init -q -b main "$up"
(
  cd "$up"
  mkdir -p .github/workflows internal/auth
  printf 'module example.com/x\n\ngo 1.24\n' >go.mod
  printf 'env:\n  GO_VERSION: "1.24.1"\n' >.github/workflows/release.yaml
  echo a >internal/auth/a.go
  git add -A && git commit -q -m one && git tag v1.0.0
  echo b >b.txt && git add -A && git commit -q -m two && git tag -a -m v1.1.0 v1.1.0
  git checkout -q -b other v1.0.0 && echo c >c.txt && git add -A && git commit -q -m other && git checkout -q main
)
v110="$(git -C "$up" rev-parse 'v1.1.0^{commit}')"
other="$(git -C "$up" rev-parse other)"
# pmx: Provimux = v1.1.0 + a patch + a layer commit; local tags v1.0.0 and v1.1.0.
pmx="$tmp/pmx"
git clone -q "$up" "$pmx"
(
  cd "$pmx"
  git checkout -q --detach v1.1.0
  echo p >patch.txt && git add -A && git commit -q -m 'owner patch'
  echo l >layer.txt && git add -A && git commit -q -m 'layer' -m 'Provimux-Layer: branding'
  # The README commit, so the guard passes with require_readme=0 and =1 alike.
  printf '# Provimux\n' >README.md && git add README.md && git commit -q -m 'docs(provimux): root README' -m 'Provimux-Layer: branding
Provimux-Readme: root'
)
guard() { (cd "$pmx" && PMX_UPSTREAM="$up" "$@" bash "$here/guard.sh") >"$tmp/guard.out" 2>&1 && echo pass || echo fail; }

echo "# guard.sh"
expect "base tag here and upstream: passes" pass "$(guard env)"
sed 's/^require_readme=[01]$/require_readme=1/' "$here/guard.sh" >"$tmp/guard-fix-req1.sh"
expect "  ... also with require_readme=1 (the fixture has its README commit)" pass "$( (cd "$pmx" && PMX_UPSTREAM="$up" bash "$tmp/guard-fix-req1.sh") >"$tmp/guard.out" 2>&1 && echo pass || echo fail)"
contains "  ... on base v1.1.0" "$tmp/guard.out" "Base: v1.1.0"
git -C "$pmx" tag -d v1.1.0 >/dev/null
expect "base tag only upstream (no local tag): passes" pass "$(guard env)"
contains "  ... on base v1.1.0" "$tmp/guard.out" "Base: v1.1.0"
expect "expected base v1.1.0 at its commit: passes" pass "$(guard env PMX_EXPECT_BASE=v1.1.0 PMX_EXPECT_BASE_COMMIT="$v110")"
expect "expected base v1.0.0 (older than the base): fails" fail "$(guard env PMX_EXPECT_BASE=v1.0.0)"
# As publish.sh runs it: a bare repository whose HEAD is the candidate, nothing checked out.
bare="$tmp/guard-bare.git"
git init -q --bare "$bare"
(cd "$pmx/.git/objects" && pwd) >"$bare/objects/info/alternates"
git --git-dir="$bare" update-ref --no-deref HEAD "$(git -C "$pmx" rev-parse HEAD)"
if (cd "$tmp" && GIT_DIR="$bare" PMX_UPSTREAM="$up" PMX_EXPECT_BASE=v1.1.0 PMX_EXPECT_BASE_COMMIT="$v110" bash "$here/guard.sh") >"$tmp/guard.out" 2>&1; then
  ok "guard on git objects only (bare repository, HEAD = candidate): passes"
else bad "guard on git objects only (bare repository, HEAD = candidate): passes ($(tail -n 1 "$tmp/guard.out"))"; fi
git -C "$pmx" tag v1.1.0 "$v110"
git -C "$up" tag -d v1.1.0 >/dev/null
expect "reachable base tag withdrawn upstream: fails" fail "$(guard env)"
contains "  ... says withdrawn" "$tmp/guard.out" "withdrawn"
expect "withdrawn, with the expected base: fails" fail "$(guard env PMX_EXPECT_BASE=v1.1.0 PMX_EXPECT_BASE_COMMIT="$v110")"
git -C "$up" tag v1.1.0 "$other"
expect "reachable base tag moved upstream: fails" fail "$(guard env)"
contains "  ... says moved" "$tmp/guard.out" "moved"
git -C "$up" tag -d v1.1.0 >/dev/null && git -C "$up" tag -a -m v1.1.0 v1.1.0 "$v110"

echo "# archive_matches (tested tree = released tree)"
ar="$tmp/ar"
git init -q -b main "$ar"
(
  cd "$ar"
  mkdir -p pkg
  echo 'package pkg' >pkg/a.go; echo 'package pkg' >pkg/b.go
  printf '#!/bin/sh\n' >run.sh && chmod +x run.sh
  ln -s pkg/a.go link.go
  git add -A && git commit -q -m one
)
# archive_case COMMIT: pass | fail, the differences in $tmp/ar.rep
archive_case() {
  rm -rf "$tmp/ar.x" && mkdir -p "$tmp/ar.x"
  git -C "$ar" archive --format=tar "$1" | tar -x -C "$tmp/ar.x"
  # shellcheck source=common.sh
  (. "$here/common.sh" && archive_matches "$ar/.git" "$1" "$tmp/ar.x" "$tmp/ar.rep") && echo pass || echo fail
}
expect "plain tree with an executable and a symlink: archive matches" pass "$(archive_case "$(git -C "$ar" rev-parse HEAD)")"
nl=$'pkg/new\nline.go'
(cd "$ar" && echo 'package pkg' >"$nl" && printf 'x\n' >'pkg/new' && git add -A && git commit -q -m newline)
expect "a file name with a newline (and a file named like its first line): archive matches" pass "$(archive_case "$(git -C "$ar" rev-parse HEAD)")"
(cd "$ar" && printf 'pkg/new?line.go export-ignore\n' >.gitattributes && git add -A && git commit -q -m ignore-nl)
expect "export-ignore on the file with a newline: mismatch" fail "$(archive_case "$(git -C "$ar" rev-parse HEAD)")"
# shellcheck disable=SC2016 # a literal escaped name
contains "  ... names it on one line, escaped" "$tmp/ar.rep" 'pkg/new\nline.go'
(cd "$ar" && git rm -q -- "$nl" pkg/new .gitattributes && git commit -q -m back)
(cd "$ar" && echo 'pkg/b.go export-ignore' >.gitattributes && git add -A && git commit -q -m ignore)
expect "export-ignore on a Go file: mismatch" fail "$(archive_case "$(git -C "$ar" rev-parse HEAD)")"
contains "  ... names the file left out" "$tmp/ar.rep" "- 100644"
contains "  ... pkg/b.go" "$tmp/ar.rep" "pkg/b.go"
# shellcheck disable=SC2016 # a literal export-subst placeholder
(cd "$ar" && printf '// $Format:%%H$\npackage pkg\n' >pkg/b.go && echo 'pkg/b.go export-subst' >.gitattributes && git add -A && git commit -q -m subst)
expect "export-subst on a Go file: mismatch" fail "$(archive_case "$(git -C "$ar" rev-parse HEAD)")"
contains "  ... the changed blob shown" "$tmp/ar.rep" "+ 100644"

echo "# release-meta.sh"
git -C "$pmx" tag -d v1.1.0 >/dev/null
git -C "$pmx" tag v1.1.0-pmx.1 HEAD
meta() {
  : >"$tmp/meta.out"
  (cd "$pmx" && GITHUB_EVENT_NAME=push GITHUB_REF_NAME=v1.1.0-pmx.1 GITHUB_REF=refs/tags/v1.1.0-pmx.1 \
    GITHUB_OUTPUT="$tmp/meta.out" GITHUB_STEP_SUMMARY=/dev/null PMX_UPSTREAM="$up" \
    bash "$here/release-meta.sh") >"$tmp/meta.log" 2>&1 && echo pass || echo fail
}
expect "base tag only upstream: passes" pass "$(meta)"
expect "  ... base=1.1.0" "base=1.1.0" "$(grep '^base=' "$tmp/meta.out" || true)"
git -C "$pmx" tag v1.1.0 "$v110"
git -C "$up" tag -d v1.1.0 >/dev/null
expect "base tag withdrawn upstream: fails" fail "$(meta)"
git -C "$up" tag -a -m v1.1.0 v1.1.0 "$v110"

# --- report.sh -------------------------------------------------------------------------------------
echo "# report.sh"
stub="$tmp/stub"
mkdir -p "$stub/bin" "$stub/api"
cat >"$stub/bin/gh" <<'EOF'
#!/usr/bin/env bash
# Stub gh: "gh api PATH [--jq EXPR]" prints $STUB_API/<PATH with / ? & = as _>.json (through
# jq -r EXPR), else a 404. "gh run download ID -n NAME -D DIR" copies $STUB_API/artifact-NAME/.
if [ "$1 $2" = "run download" ]; then
  n='' d=''
  while [ $# -gt 0 ]; do case "$1" in -n) n=$2; shift ;; -D) d=$2; shift ;; esac; shift; done
  [ -d "$STUB_API/artifact-$n" ] || { echo "stub gh: no artifact $n" >&2; exit 1; }
  mkdir -p "$d" && cp -R "$STUB_API/artifact-$n/." "$d/"
  exit 0
fi
# "gh issue ..." is recorded in $STUB_API/../issue.log.
if [ "$1" = issue ]; then echo "$*" >>"$STUB_API/../issue.log"; exit 0; fi
[ "$1" = api ] || { echo "stub gh: $*" >&2; exit 1; }
shift
p='' q='' m=GET
while [ $# -gt 0 ]; do
  case "$1" in --jq) q=$2; shift ;; -X) m=$2; shift ;; -H | -f | -F) shift ;; -*) ;; *) [ -n "$p" ] || p=$1 ;; esac
  shift
done
f="$STUB_API/$(printf %s "$p" | tr '/?&=' '____').json"
# A write is recorded in $STUB_API/../calls.log; PATCH of a release makes it the latest one.
if [ "$m" != GET ]; then
  echo "$m $p" >>"$STUB_API/../calls.log"
  case "$p" in */releases/[0-9]*) cp "$f" "$(dirname "$f")/$(printf %s "${p%/releases/*}/releases/latest" | tr '/?&=' '____').json" ;; esac
  exit 0
fi
[ -f "$f" ] || { echo "gh: Not Found (HTTP 404)" >&2; exit 1; }
if [ "$(head -c 8 "$f")" = "HTTP 503" ]; then echo "gh: Service Unavailable (HTTP 503)" >&2; exit 1; fi
if [ -n "$q" ]; then jq -r "$q" "$f"; else cat "$f"; fi
EOF
printf '#!/bin/sh\nexit 22\n' >"$stub/bin/curl"
chmod +x "$stub/bin/gh" "$stub/bin/curl"
# Provimux's remote: tag v1.1.0-pmx.1 at the layer commit.
remote="$tmp/remote.git"
git init -q --bare "$remote"
git -C "$pmx" push -q "$remote" "HEAD:refs/heads/main" "refs/tags/v1.1.0-pmx.1"
pmx_sha="$(git -C "$pmx" rev-parse HEAD)"
other_sha=0123456789012345678901234567890123456789
# shellcheck source=common.sh
. "$here/common.sh"
expected_assets 1.1.0-pmx.1 | jq -R -s '{draft: false, prerelease: false, published_at: "2026-11-19T00:00:00Z",
  assets: (split("\n") | map(select(length > 0) | {name: ., state: "uploaded"}))}' \
  >"$stub/api/repos_test_provimux_releases_tags_v1.1.0-pmx.1.json"
runs() { # runs SHA: one successful provimux-release run of the tag push, for commit SHA
  jq -n --arg sha "$1" '{workflow_runs: [{id: 7, path: ".github/workflows/provimux-release.yml", event: "push",
    head_branch: "v1.1.0-pmx.1", head_sha: $sha, repository: {full_name: "test/provimux"},
    status: "completed", conclusion: "success", created_at: "2026-11-19T00:00:00Z"}]}' \
    >"$stub/api/repos_test_provimux_actions_runs_event_push_branch_v1.1.0-pmx.1_per_page_50.json"
}
report() { # report RESULT CHECK_JOB: the dry-run output in $tmp/report.out, the status printed
  PATH="$stub/bin:$PATH" STUB_API="$stub/api" PMX_REPO=test/provimux PMX_REPO_URL="$remote" \
    PMX_REPORT_DRY_RUN=1 RESULT="$1" CHECK_JOB="$2" RUN_URL=https://example.invalid/run \
    bash "$here/report.sh" >"$tmp/report.out" 2>&1 || { echo "exit $?"; return; }
  sed -n '1s/^status=\([^ ]*\) .*/\1/p' "$tmp/report.out"
}
echo '{"status": "published", "upstream": {"tag": "v1.1.0"}, "reasons": []}' >"$tmp/published.json"
runs 0123456789012345678901234567890123456789
expect "release run succeeded for another SHA: not published" incomplete "$(report "$tmp/published.json" success)"
contains "  ... says which commit" "$tmp/report.out" "release run for that commit none"
runs "$pmx_sha"
expect "release run succeeded for the tag's SHA, release complete: published" published "$(report "$tmp/published.json" success)"
expect "the same, but the check job failed: not published" error "$(report "$tmp/published.json" 'prepare success, test failure, verdict success')"
jq '.draft = true' "$stub/api/repos_test_provimux_releases_tags_v1.1.0-pmx.1.json" >"$tmp/d.json"
mv "$tmp/d.json" "$stub/api/repos_test_provimux_releases_tags_v1.1.0-pmx.1.json"
expect "release is a draft: not published" incomplete "$(report "$tmp/published.json" success)"

cat >"$tmp/fail.json" <<'EOF'
{"status": "fail", "step": "done", "upstream": {"tag": "v1.1.0"}, "reasons": ["1 test(s) fail on the candidate"],
 "rebase": {"ok": true},
 "build": {"ok": false, "tail": "build: undefined: Foo"},
 "guard": {"ok": false, "output": "::error::Commit abc has no trailer"},
 "tests": {"ok": false, "new_failures": ["x/a TestOwner"], "candidate_build_failures": ["x/z"],
           "missing_results": {"candidate": [], "tag": []}, "candidate": {"exit": 1}, "tag": {"exit": 1},
           "rerun": {"exit": 1}}}
EOF
expect "failure result: reported as failed" fail "$(report "$tmp/fail.json" success)"
contains "  ... tests shown" "$tmp/report.out" "x/a TestOwner"
contains "  ... build failures shown" "$tmp/report.out" "x/z"
contains "  ... build output shown" "$tmp/report.out" "undefined: Foo"
contains "  ... guard output shown" "$tmp/report.out" "has no trailer"
lacks "  ... no rebase section for a clean rebase" "$tmp/report.out" "The rebase stopped at"
jq '.rebase = {ok: false, stopped_at: "0123456789abcdef0123456789abcdef01234567", subject: "owner patch", conflicts: ["a.go"]}' \
  "$tmp/fail.json" >"$tmp/fail2.json"
expect "rebase failure: reported as failed" fail "$(report "$tmp/fail2.json" success)"
contains "  ... rebase stop shown" "$tmp/report.out" "The rebase stopped at"
lacks "  ... no warnings section without warnings" "$tmp/report.out" "Warnings from the candidate"
jq '.warnings = [{package: "x/a", test: "TestW", text: "2 Devin model(s): m-1, m-2"}]' "$tmp/fail.json" >"$tmp/warn.json"
expect "failure with a test warning: still failed" fail "$(report "$tmp/warn.json" success)"
contains "  ... the warning shown" "$tmp/report.out" "TestW: 2 Devin model(s): m-1, m-2"
jq '.status = "pass" | .candidate = "0123456789abcdef0123456789abcdef01234567" | .commits = 3
    | .decisions = {dropped: [], changed: [], new_workflows: []}
    | .tests = {ok: true, new_failures: [], flaky: [], failing_on_tag_too: [], candidate: {packages: 1, passed: 1, failed: 0},
               tag: {packages: 1, passed: 1, failed: 0}}' "$tmp/warn.json" >"$tmp/warn-pass.json"
expect "pass with a test warning: ready to publish" pass "$(report "$tmp/warn-pass.json" success)"
contains "  ... the warning shown" "$tmp/report.out" "Warnings from the candidate's tests"

# --- provimux-publish.yml's verifier, autopublish.sh and publish.sh --ci ----------------------------
# checkrun ID [JQ]: attempt 1 of a provimux-check run of test/provimux on main that passed (JQ edits it).
checkrun() {
  jq -n --arg sha "$pmx_sha" --argjson id "$1" '{id: $id, run_attempt: 1, path: ".github/workflows/provimux-check.yml", head_branch: "main",
    event: "schedule", status: "completed", conclusion: "success", head_sha: $sha,
    repository: {full_name: "test/provimux"}, head_repository: {full_name: "test/provimux"}}' |
    jq "${2:-.}" >"$stub/api/repos_test_provimux_actions_runs_$1_attempts_1.json"
}
# jobs RUN ATTEMPT NAME...: the jobs of that attempt (the job "verdict" gets id $VID, 900 by default).
jobs() {
  local run=$1 att=$2; shift 2
  printf '%s\n' "$@" | jq -R -s --argjson vid "${VID:-900}" 'split("\n") | map(select(length > 0)) | to_entries
    | {jobs: map({name: .value, id: (if .value == "verdict" then $vid else 800 + .key end)})}' \
    >"$stub/api/repos_test_provimux_actions_runs_${run}_attempts_${att}_jobs_per_page_100.json"
}
# bind MESSAGE...: the annotations titled provimux-check-artifact of the verdict job $VID
# (900 by default), plus an unrelated one.
bind() {
  { printf '%s\n' "$@" | jq -R -s 'split("\n") | map(select(length > 0) | {title: "provimux-check-artifact", message: .})'
    echo '[{"title": "other", "message": "1 provimux-check-v9.9.9"}]'; } | jq -s 'add' \
    >"$stub/api/repos_test_provimux_check-runs_${VID:-900}_annotations_per_page_100.json"
}
# artifact ID RUN NAME STATUS TAG [JQ]: an artifact of RUN with a result.json (JQ edits its metadata).
artifact() {
  local d="$tmp/art-$1"
  rm -rf "$d"; mkdir -p "$d"
  jq -n --arg s "$4" --arg t "$5" '{status: $s, upstream: {tag: $t}}' >"$d/result.json"
  (cd "$d" && zip -q a.zip result.json) && mv "$d/a.zip" "$stub/api/repos_test_provimux_actions_artifacts_$1_zip.json"
  jq -n --argjson id "$1" --argjson run "$2" --arg n "$3" '{id: $id, name: $n, expired: false, digest: "sha256:aa", workflow_run: {id: $run}}' |
    jq "${6:-.}" >"$stub/api/repos_test_provimux_actions_artifacts_$1.json"
}

echo "# provimux-publish.yml: the inline verifier"
wf="$here/../workflows/provimux-publish.yml"
sed -n '/^ *# >>> verify$/,/^ *# <<< verify$/p' "$wf" | sed 's/^          //' >"$tmp/verify.sh"
if [[ "$(grep -c '' "$tmp/verify.sh")" -gt 20 ]]; then ok "verifier lines found in the workflow"; else bad "verifier lines found in the workflow"; fi
# A stub git for ls-remote only: prints $STUB_MAIN as refs/heads/main (fails when it is "fail").
mkdir -p "$stub/gitbin"
cat >"$stub/gitbin/git" <<'EOF'
#!/usr/bin/env bash
[ "$1" = ls-remote ] || { echo "stub git: $*" >&2; exit 1; }
[ "$STUB_MAIN" != fail ] || { echo "fatal: unable to access" >&2; exit 128; }
printf '%s\trefs/heads/main\n' "$STUB_MAIN"
EOF
chmod +x "$stub/gitbin/git"
# verify ENV...: "<run> <attempt> <artifact> <sha>" (or "exit N"); the reason in $tmp/verify.why
verify() {
  : >"$tmp/verify.out"
  env PATH="$stub/gitbin:$stub/bin:$PATH" STUB_API="$stub/api" PMX_REPO=test/provimux GITHUB_OUTPUT="$tmp/verify.out" \
    STUB_MAIN="$pmx_sha" EVENT_NAME=workflow_run WORKFLOW_REF="test/provimux/.github/workflows/provimux-publish.yml@refs/heads/main" \
    WORKFLOW_SHA="$pmx_sha" EVENT_RUN=42 EVENT_ATTEMPT=1 EVENT_HEAD_SHA="$pmx_sha" "$@" \
    bash "$tmp/verify.sh" >"$tmp/verify.log" 2>&1 || { local rc=$?; sed -n 's/^why=//p' "$tmp/verify.out" >"$tmp/verify.why"; echo "exit $rc"; return; }
  echo "$(sed -n 's/^run=//p' "$tmp/verify.out") $(sed -n 's/^attempt=//p' "$tmp/verify.out") $(sed -n 's/^artifact=//p' "$tmp/verify.out") $(sed -n 's/^sha=//p' "$tmp/verify.out")"
}
checkrun 42; jobs 42 1 prepare 'test (candidate)' 'test (tag)' verdict report
bind '501 provimux-check-v1.2.0 sha256:aa'
expect "check attempt passed on the live main = workflow_sha: run 42 attempt 1 artifact 501" "42 1 501 $pmx_sha" "$(verify env)"
expect "an old workflow revision (or a rerun of an old run): refused" "exit 1" "$(verify WORKFLOW_SHA="$other_sha")"
contains "  ... says why" "$tmp/verify.why" "nothing is published from another main"
expect "main moved since the check (event head_sha is older): refused" "exit 1" "$(verify EVENT_HEAD_SHA="$other_sha")"
expect "main moved after both (live main differs): refused" "exit 1" "$(verify STUB_MAIN="$other_sha")"
expect "main unreadable: refused, with a reason" "exit 1" "$(verify STUB_MAIN=fail)"
contains "  ... says so" "$tmp/verify.why" "cannot read refs/heads/main"
expect "workflow from another branch: refused" "exit 1" "$(verify WORKFLOW_REF=test/provimux/.github/workflows/provimux-publish.yml@refs/heads/x)"
expect "started by workflow_dispatch: refused" "exit 1" "$(verify EVENT_NAME=workflow_dispatch)"
expect "no attempt in the event: refused" "exit 1" "$(verify EVENT_ATTEMPT=)"
expect "attempt 2 named by the event, unreadable: refused" "exit 1" "$(verify EVENT_ATTEMPT=2)"
checkrun 42 '.conclusion = "failure"'
expect "that attempt failed: refused" "exit 1" "$(verify env)"
checkrun 42 '.status = "in_progress" | .conclusion = null'
expect "that attempt still running: refused" "exit 1" "$(verify env)"
checkrun 42 '.run_attempt = 2'
expect "the API answers another attempt: refused" "exit 1" "$(verify env)"
checkrun 42 '.event = "push"'
expect "check run started by a push: refused" "exit 1" "$(verify env)"
checkrun 42 '.head_branch = "feature"'
expect "check run not on main: refused" "exit 1" "$(verify env)"
checkrun 42 '.path = ".github/workflows/other.yml"'
expect "another workflow's run: refused" "exit 1" "$(verify env)"
checkrun 42 '.head_repository.full_name = "fork/provimux"'
expect "a fork's run: refused" "exit 1" "$(verify env)"
checkrun 42
jobs 42 1 prepare report
expect "no verdict job in that attempt: refused" "exit 1" "$(verify env)"
jobs 42 1 prepare verdict verdict
expect "two verdict jobs: refused" "exit 1" "$(verify env)"
jobs 42 1 prepare 'test (candidate)' 'test (tag)' verdict report
bind
expect "no artifact annotation: refused" "exit 1" "$(verify env)"
bind '501 provimux-check-v1.2.0 sha256:aa' '502 provimux-check-v1.3.0 sha256:aa'
expect "two artifact annotations: refused" "exit 1" "$(verify env)"
bind '501 provimux-check-v1.2.0 sha256:aa'

echo "# autopublish.sh decide (check_artifact, the verified run, attempt and artifact)"
decide() { # decide ENV...: "<action> <tag>" (or "exit N")
  : >"$tmp/decide.out"
  env PATH="$stub/bin:$PATH" STUB_API="$stub/api" PMX_REPO=test/provimux PMX_REPO_URL="$remote" \
    GITHUB_OUTPUT="$tmp/decide.out" PMX_CHECK_RUN=42 PMX_CHECK_ATTEMPT=1 PMX_ARTIFACT=501 PMX_EXPECT_MAIN="$pmx_sha" "$@" \
    bash "$here/autopublish.sh" decide >"$tmp/decide.log" 2>&1 || { echo "exit $?"; return; }
  echo "$(sed -n 's/^action=//p' "$tmp/decide.out") $(sed -n 's/^tag=//p' "$tmp/decide.out")"
}
artifact 501 42 provimux-check-v1.2.0 pass v1.2.0
expect "bound verdict artifact 501 says pass: publish v1.2.0" "publish v1.2.0" "$(decide env)"
expect "the verified id is another artifact than the one bound now: error" "exit 1" "$(decide PMX_ARTIFACT=502)"
contains "  ... says not the verified one" "$tmp/decide.log" "not the verified 502"
# A test job (it runs the candidate's code) uploads a passing artifact for another tag;
# the verdict bound its own, which failed.
artifact 501 42 provimux-check-v1.2.0 fail v1.2.0; artifact 502 42 provimux-check-v1.3.0 pass v1.3.0
expect "planted passing artifact, bound verdict says fail: skip (the plant is ignored)" "skip v1.2.0" "$(decide env)"
expect "  ... and naming the plant's id cannot pick it: error" "exit 1" "$(decide PMX_ARTIFACT=502)"
artifact 501 42 provimux-check-v1.2.0 pass v1.2.0
expect "planted artifact, bound verdict says pass: publish the bound one" "publish v1.2.0" "$(decide env)"
bind '502 provimux-check-v1.3.0 sha256:aa'
artifact 502 43 provimux-check-v1.3.0 pass v1.3.0
expect "annotation names an artifact of another run: error" "exit 1" "$(decide PMX_ARTIFACT=502)"
artifact 502 42 provimux-check-v1.3.0 pass v1.3.0
bind '501 provimux-check-v1.2.0 sha256:aa' '502 provimux-check-v1.3.0 sha256:aa'
expect "two binding annotations: error" "exit 1" "$(decide env)"
bind
expect "no binding annotation: error" "exit 1" "$(decide env)"
bind '501 provimux-check-v1.3.0 sha256:aa'
expect "annotation's name is not the artifact's: error" "exit 1" "$(decide env)"
bind '501 provimux-check-v1.2.0 sha256:bb'
expect "digest differs: error" "exit 1" "$(decide env)"
bind '501 provimux-check-v1.2.0 sha256:aa'
artifact 501 42 provimux-check-v1.2.0 pass v1.2.0 '.expired = true'
expect "bound artifact expired: error" "exit 1" "$(decide env)"
artifact 501 42 provimux-check-v1.2.0 pass v1.3.0
expect "result.json for another tag than its artifact: error" "exit 1" "$(decide env)"
artifact 501 42 provimux-check-v1.2.0 pass v1.2.0
# A rerun (attempt 2) has its own verdict job (id 901) binding another artifact; the
# verified attempt 1 is used, never "the latest".
VID=901 jobs 42 2 prepare verdict report; VID=901 bind '502 provimux-check-v1.3.0 sha256:aa'
expect "attempt 2 binds 502, the verified attempt 1: publish 501's v1.2.0" "publish v1.2.0" "$(decide env)"
expect "  ... the verified attempt 2 with artifact 501: error" "exit 1" "$(decide PMX_CHECK_ATTEMPT=2)"
expect "  ... the verified attempt 2 with its artifact 502: publish v1.3.0" "publish v1.3.0" "$(decide PMX_CHECK_ATTEMPT=2 PMX_ARTIFACT=502)"
jobs 42 2 prepare report
expect "an attempt without a verdict job: error (attempt 1's is not used)" "exit 1" "$(decide PMX_CHECK_ATTEMPT=2)"
artifact 503 42 provimux-check-none published ''; bind '503 provimux-check-none'
expect "the check selected no release (no digest given): skip" "skip " "$(decide PMX_ARTIFACT=503)"
artifact 504 42 provimux-check-v1.1.0 pass v1.1.0; bind '504 provimux-check-v1.1.0 sha256:aa'
expect "v1.1.0-pmx.1 exists already (second run, re-run): skip" "skip v1.1.0" "$(decide PMX_ARTIFACT=504)"
contains "  ... says already exists" "$tmp/decide.log" "already exists"
expect "no check run id: error" "exit 1" "$(decide PMX_CHECK_RUN=)"
expect "no attempt: error" "exit 1" "$(decide PMX_CHECK_ATTEMPT=)"
expect "no artifact id: error" "exit 1" "$(decide PMX_ARTIFACT=)"

echo "# autopublish.sh notify (reads the remote first)"
# notify TAG OUTCOME LOG-TEXT [ENV...]: the dry-run issue text in $tmp/notify.out, the title printed.
notify() {
  local t=$1 o=$2 l=$3; shift 3
  printf '%s' "$l" >"$tmp/publish.log"
  env PATH="$stub/bin:$PATH" STUB_API="$stub/api" PMX_REPO=test/provimux PMX_REPO_URL="$remote" \
    PMX_NOTIFY_DRY_RUN=1 TAG="$t" OUTCOME="$o" LOG="$tmp/publish.log" EXPECT_MAIN="$pmx_sha" RUN_URL=https://example.invalid/run \
    "$@" bash "$here/autopublish.sh" notify >"$tmp/notify.out" 2>&1 || { echo "exit $?"; return; }
  sed -n 's/^title=//p' "$tmp/notify.out"
}
expect "no token, nothing on the remote: waits for the token" "sync: v1.2.0 — ready, publishing waits for PROVIMUX_TOKEN" "$(notify v1.2.0 no-token '' HAS_TOKEN=false)"
contains "  ... offers the hand publish (it updates the tap too)" "$tmp/notify.out" "publish.sh v1.2.0"
contains "  ... says publish.sh updates the tap" "$tmp/notify.out" "updates the Homebrew tap itself"
contains "  ... names the environment" "$tmp/notify.out" "environment \`provimux-token\`"
contains "  ... gives the check of this tag" "$tmp/notify.out" "gh workflow run provimux-check.yml -R test/provimux -f tag=v1.2.0"
contains "  ... shows main unchanged" "$tmp/notify.out" "(the checked main: unchanged)"
log1='==> remote origin
publish: not accepted (--accept-dropped): 0123456789abcdef0123456789abcdef01234567 owner patch
publish: refused: accept each item above by name (SYNC.md), or fix the stack
'
expect "dropped patch, nothing on the remote: refused, owner needed" "sync: v1.2.0 — publish refused, owner needed" "$(notify v1.2.0 failed "$log1" HAS_TOKEN=true)"
contains "  ... the reason is shown" "$tmp/notify.out" "not accepted (--accept-dropped): 0123456789ab"
lacks "  ... progress lines are not" "$tmp/notify.out" "remote origin"
contains "  ... says nothing was pushed" "$tmp/notify.out" "nothing was pushed"
contains "  ... names the manual path" "$tmp/notify.out" "publish.sh v1.2.0"
# shellcheck disable=SC2016 # a literal Markdown table row
contains "  ... and that no -pmx tag exists" "$tmp/notify.out" '| `v1.2.0-pmx.*` | absent |'
m1="$(sed -n 2p "$tmp/notify.out")"
notify v1.2.0 failed "publish: the range-diff is empty or has commits not in the old main: see /tmp/x/provimux-publish.AbC123/range-diff.txt" >/dev/null
m2="$(sed -n 2p "$tmp/notify.out")"
notify v1.2.0 failed "publish: the range-diff is empty or has commits not in the old main: see /var/y/provimux-publish.Zz9/range-diff.txt" >/dev/null
expect "same reason, another temp dir: same marker (one comment)" "$m2" "$(sed -n 2p "$tmp/notify.out")"
if [[ "$m1" != "$m2" ]]; then ok "  ... another reason: another marker"; else bad "  ... another reason: another marker ($m1)"; fi
expect "publish.sh broke without a reason, before the verifier knew main: refused" "sync: v1.2.0 — publish refused, owner needed" \
  "$(notify v1.2.0 failed 'boom' EXPECT_MAIN=)"
contains "  ... says so" "$tmp/notify.out" "ended without a reason"
contains "  ... and that it stopped before its push" "$tmp/notify.out" "stopped before its push step"
expect "main is another commit than the checked one: main moved, nothing published" "sync: v1.2.0 — main moved, nothing published" \
  "$(notify v1.2.0 failed 'publish: main is now x' EXPECT_MAIN="$other_sha")"
lacks "  ... no publish command" "$tmp/notify.out" "publish.sh v1.2.0"
lacks "  ... promises no next check" "$tmp/notify.out" "next check"
contains "  ... gives the check of this tag" "$tmp/notify.out" "gh workflow run provimux-check.yml -R test/provimux -f tag=v1.2.0"
printf 'CANDIDATE=%s\nUPSTREAM_COMMIT=%s\n' "$pmx_sha" "$v110" >"$tmp/plan.env"
expect "main is the candidate, no -pmx tag: main pushed without its tags" "sync: v1.2.0 — main pushed without its tags, owner needed" \
  "$(notify v1.2.0 failed 'publish: git push reported a failure' EXPECT_MAIN="$other_sha" PLAN="$tmp/plan.env")"
contains "  ... gives the tag push" "$tmp/notify.out" "$pmx_sha:refs/tags/v1.2.0-pmx.1"
# v1.1.0-pmx.1 exists on the remote; its release is a draft (above), its release run (id 7) succeeded.
expect "-pmx tag exists, release a draft: pushed, release not complete" "sync: v1.1.0 — pushed, release not complete, owner needed" \
  "$(notify v1.1.0 failed 'publish: the provimux-release run for v1.1.0-pmx.1 ended failure; the issue stays open')"
contains "  ... says how to rerun the release run" "$tmp/notify.out" "gh run rerun 7 --failed -R test/provimux"
lacks "  ... never says to publish again" "$tmp/notify.out" "publish.sh v1.1.0"
lacks "  ... nor that nothing was pushed" "$tmp/notify.out" "nothing was pushed"
# The table of refs: the base tag (annotated, as upstream's are) once, and a failed release run in words.
git -C "$pmx" push -q "$remote" refs/tags/v1.1.0
jq '.workflow_runs[0].conclusion = "failure"' "$stub/api/repos_test_provimux_actions_runs_event_push_branch_v1.1.0-pmx.1_per_page_50.json" >"$tmp/r.json"
mv "$tmp/r.json" "$stub/api/repos_test_provimux_actions_runs_event_push_branch_v1.1.0-pmx.1_per_page_50.json"
notify v1.1.0 failed 'publish: the provimux-release run for v1.1.0-pmx.1 ended failure; the issue stays open' >/dev/null
# shellcheck disable=SC2016 # a literal Markdown table row
contains "table: the base tag's commit, once" "$tmp/notify.out" '| `v1.1.0` | `'"$v110"'` |'
contains "  ... a failed release run reads 'failed'" "$tmp/notify.out" 'release: draft; release run for that commit: failed |'
lacks "  ... never 'failure:failure'" "$tmp/notify.out" 'failure:failure'
git -C "$remote" tag -d v1.1.0 >/dev/null
runs "$pmx_sha"
jq '.draft = false' "$stub/api/repos_test_provimux_releases_tags_v1.1.0-pmx.1.json" >"$tmp/d.json"
mv "$tmp/d.json" "$stub/api/repos_test_provimux_releases_tags_v1.1.0-pmx.1.json"
expect "push reported a failure but the remote has the complete release (lost acknowledgement): released" "sync: v1.1.0 — released" \
  "$(notify v1.1.0 failed 'publish: git push reported a failure' HAS_TOKEN=false)"
lacks "  ... not 'nothing was pushed'" "$tmp/notify.out" "nothing was pushed"
lacks "  ... promises no next check" "$tmp/notify.out" "next check"
printf 'PMX_TAG=v1.1.0-pmx.1\nCANDIDATE=%s\n' "$pmx_sha" >"$tmp/plan-tap.env"
expect "complete release, the tap update failed: the sync issue says released (it is about the release only)" "sync: v1.1.0 — released" \
  "$(notify v1.1.0 tap-failed 'publish: the Homebrew tap update was refused (see the log)' PLAN="$tmp/plan-tap.env")"
contains "  ... the tap failure has its own issue" "$tmp/notify.out" "tap_title=homebrew tap: v1.1.0-pmx.1 needs the owner"
contains "  ... which gives the owner's tap command" "$tmp/notify.out" "publish.sh --tap v1.1.0-pmx.1"
contains "  ... and the read-only check" "$tmp/notify.out" "tap.sh check v1.1.0-pmx.1"
contains "  ... and says no automation closes it" "$tmp/notify.out" "No automation closes or retitles this issue"
lacks "  ... no sync title carries the tap state" "$tmp/notify.out" "Homebrew tap not updated"
# The same for real (stub gh records issue commands): no open issue -> none is opened;
# an open "sync: v1.1.0 ..." issue -> it is closed.
notify_real() { # [ENV...]: the first 14 characters of the gh issue commands; all of them in $stub/issue.log
  : >"$stub/issue.log"
  env PATH="$stub/bin:$PATH" STUB_API="$stub/api" PMX_REPO=test/provimux PMX_REPO_URL="$remote" \
    TAG=v1.1.0 OUTCOME=failed LOG="$tmp/publish.log" EXPECT_MAIN="$pmx_sha" RUN_URL=https://example.invalid/run \
    "$@" bash "$here/autopublish.sh" notify >"$tmp/notify.out" 2>&1 || { echo "exit $?"; return; }
  tr '\n' ' ' <"$stub/issue.log" | cut -c1-14
}
echo '[]' >"$stub/api/repos_test_provimux_issues_state_open_per_page_100.json"
expect "released, no open issue: opens none" "" "$(notify_real)"
echo '[{"number": 5, "title": "sync: v1.1.0 — ready, publishing waits for PROVIMUX_TOKEN"}]' >"$stub/api/repos_test_provimux_issues_state_open_per_page_100.json"
expect "released, the open issue: closes it" "issue close 5 " "$(notify_real)"
# The tap failure (T-86): its own issue, opened or commented on; never retitled or closed.
echo '[{"number": 5, "title": "sync: v1.1.0 — ready, publishing waits for PROVIMUX_TOKEN"}]' >"$stub/api/repos_test_provimux_issues_state_open_per_page_100.json"
notify_real OUTCOME=tap-failed PLAN="$tmp/plan-tap.env" >/dev/null
contains "tap step failed, no tap issue yet: opens \"homebrew tap: <tag> needs the owner\"" "$stub/issue.log" "issue create -R test/provimux --title homebrew tap: v1.1.0-pmx.1 needs the owner"
contains "  ... and closes the sync issue (the release is complete)" "$stub/issue.log" "issue close 5 "
lacks "  ... never retitles the sync issue to a tap state" "$stub/issue.log" "issue edit"
echo '[{"number": 5, "title": "sync: v1.1.0 — ready, publishing waits for PROVIMUX_TOKEN"}, {"number": 9, "title": "homebrew tap: v1.1.0-pmx.1 needs the owner"}]' \
  >"$stub/api/repos_test_provimux_issues_state_open_per_page_100.json"
expect "tap step failed again, the tap issue open: one comment on it, nothing else of it" "issue comment 9 -R test/provimux" "$(notify_real OUTCOME=tap-failed PLAN="$tmp/plan-tap.env" >/dev/null; grep -F ' 9 ' "$stub/issue.log" | cut -c1-32)"
lacks "  ... not closed" "$stub/issue.log" "issue close 9"
lacks "  ... not retitled" "$stub/issue.log" "issue edit 9"
notify_real >/dev/null
contains "a later failure of the release, released: closes the sync issue" "$stub/issue.log" "issue close 5 "
lacks "  ... never the tap issue (the notifier case)" "$stub/issue.log" " 9 "
expect "remote unreadable: state unknown" "sync: v1.2.0 — publish failed, state unknown, owner needed" \
  "$(notify v1.2.0 failed 'publish: x' PMX_REPO_URL="$tmp/no-such-remote.git")"
contains "  ... says to look before acting" "$tmp/notify.out" "Do not publish or rerun anything before checking"
expect "no valid tag (the verifier failed early): provimux-publish: failed" "provimux-publish: failed" \
  "$(notify 'x; y' failed '' WHY='cannot read run 42 attempt 1')"
contains "  ... the verifier's reason is shown" "$tmp/notify.out" "cannot read run 42 attempt 1"

echo "# publish.sh --ci"
ci() { (cd "$pmx" && env -u PMX_PUSH_TOKEN -u PROVIMUX_TOKEN "$@") >"$tmp/ci.out" 2>&1 && echo "exit 0" || echo "exit $?"; }
P="$here/publish.sh"
full=(--ci --plan "$tmp/p1" --run 42 --attempt 1 --artifact 501 --expect-main "$pmx_sha")
expect "--ci with PROVIMUX_TOKEN in the environment: refused" "exit 1" "$(ci PROVIMUX_TOKEN=x bash "$P" v1.2.0 "${full[@]}")"
contains "  ... says publish.sh must never see it" "$tmp/ci.out" "must never see it"
expect "--ci with PMX_PUSH_TOKEN in the environment: refused" "exit 1" "$(ci PMX_PUSH_TOKEN=x bash "$P" v1.2.0 --ci --wait "$tmp/p1")"
expect "--ci without --plan or --wait: refused" "exit 1" "$(ci bash "$P" v1.2.0 --ci)"
expect "--ci with both --plan and --wait: refused" "exit 1" "$(ci bash "$P" v1.2.0 --ci --plan "$tmp/p1" --wait "$tmp/p1")"
expect "--ci --plan without --run/--expect-main: refused" "exit 1" "$(ci bash "$P" v1.2.0 --ci --plan "$tmp/p1")"
expect "--ci --plan without --attempt: refused" "exit 1" "$(ci bash "$P" v1.2.0 --ci --plan "$tmp/p1" --run 42 --artifact 501 --expect-main "$pmx_sha")"
expect "--ci --plan without --artifact: refused" "exit 1" "$(ci bash "$P" v1.2.0 --ci --plan "$tmp/p1" --run 42 --attempt 1 --expect-main "$pmx_sha")"
expect "--attempt without --run: refused" "exit 1" "$(ci bash "$P" v1.2.0 --attempt 1)"
expect "--artifact not digits: refused" "exit 1" "$(ci bash "$P" v1.2.0 --run 42 --artifact 5x)"
expect "--plan without --ci: refused" "exit 1" "$(ci bash "$P" v1.2.0 --plan "$tmp/p1")"
expect "--expect-main not a SHA: refused" "exit 1" "$(ci bash "$P" v1.2.0 --expect-main abc)"
mkdir -p "$tmp/p2" && printf 'TAG=v1.3.0\n' >"$tmp/p2/plan.env"
expect "--ci --wait on a plan for another tag: refused" "exit 1" "$(ci bash "$P" v1.2.0 --ci --wait "$tmp/p2")"

# --- check.sh: the state is data; the verdict's inputs are bound -----------------------------------
echo "# check.sh: state.json is parsed, never sourced"
# vstate DIR [JQ]: a valid state "test" in DIR/state.json (JQ edits it).
vstate() {
  mkdir -p "$1"
  jq -n --arg s "$pmx_sha" --arg u "$v110" '{status: "test", step: "test", tag: "v1.2.0", base: "1.2.0",
    upstream_commit: $u, old_main: $s, old_base_tag: "v1.1.0", old_base_commit: $u, candidate: $s,
    commits: "2", guard_ok: "true"}' | jq "${2:-.}" >"$1/state.json"
}
vdir="$tmp/vd"
# shellcheck disable=SC2016,SC2030,SC2031 # literal text for jq/eval; PATH only for that subshell
for bad in '.candidate = "$(touch '"$tmp"'/pwned)"' '.tag = "v1.2.0; touch '"$tmp"'/pwned"' '.status = "test\ntouch x"' '.commits = 2' '.guard_ok = "yes"'; do
  rm -rf "$vdir"; vstate "$vdir" "$bad"
  printf 'status=test\ntouch %s/pwned\n' "$tmp" >"$vdir/state.env"
  (cd "$pmx" && PMX_CHECK_OUT="$vdir" bash "$here/check.sh" tree candidate) >"$tmp/tree.log" 2>&1 && r=pass || r=fail
  expect "state with $bad: check.sh tree refuses" fail "$r"
  contains "  ... because the state is invalid" "$tmp/tree.log" "state is missing or invalid"
done
rm -rf "$vdir"; vstate "$vdir"
(cd "$pmx" && PMX_CHECK_OUT="$vdir" bash "$here/check.sh" tree candidate) >"$tmp/tree.log" 2>&1 || true
lacks "a valid state: check.sh tree reads it (goes past the state)" "$tmp/tree.log" "state is missing or invalid"
if [[ -e "$tmp/pwned" ]]; then bad "  ... and nothing in it ran"; else ok "  ... and nothing in it ran"; fi
rm -rf "$vdir"
# shellcheck disable=SC2016 # literal text for jq
vstate "$vdir" '.candidate = "$(touch '"$tmp"'/pwned)"'
PMX_CHECK_OUT="$vdir" bash "$here/check.sh" verdict >/dev/null 2>&1 || true
expect "verdict with an invalid state: error" error "$(jq -r .status "$vdir/result.json" 2>/dev/null)"
contains "  ... says the state is invalid" "$vdir/result.json" "no valid state"

echo "# check.sh verdict: step outcomes and job conclusions"
# vtest DIR: raw output of two passing trees (x/a), exit statuses 0.
vtest() {
  local w
  for w in candidate tag; do
    mkdir -p "$1/test/$w"
    { ev pass x/a TestA; ev pass x/a; } >"$1/test/$w/test.json"
    echo 0 >"$1/test/$w/test.exit"; echo x/a >"$1/test/$w/pkgs.txt"
  done
  echo 0 >"$1/test/candidate/build.exit"; : >"$1/test/candidate/build.log"
}
# vjobs CAND TAG: the conclusions of the jobs "test (candidate)" and "test (tag)" of run 77.
vjobs() {
  jq -n --arg c "$1" --arg t "$2" '{jobs: [{name: "prepare", conclusion: "success"}, {name: "test (candidate)", conclusion: $c}, {name: "test (tag)", conclusion: $t}]}' \
    >"$stub/api/repos_test_provimux_actions_runs_77_attempts_1_jobs_per_page_100.json"
}
# verdict ENV...: the status of result.json
verdict() {
  rm -rf "$vdir"; vstate "$vdir"; vtest "$vdir"
  [[ -z "${VPRE:-}" ]] || eval "$VPRE"
  env PATH="$stub/bin:$PATH" STUB_API="$stub/api" PMX_REPO=test/provimux PMX_CHECK_OUT="$vdir" PMX_RUN_ID=77 PMX_RUN_ATTEMPT=1 \
    PMX_OUTCOME_BUILD=success PMX_OUTCOME_CAND_TEST=success PMX_OUTCOME_RERUN=success PMX_OUTCOME_TAG_TEST=success "$@" \
    bash "$here/check.sh" verdict >"$tmp/verdict.log" 2>&1 || true
  jq -r .status "$vdir/result.json" 2>/dev/null || echo none
}
vjobs success success
expect "outcomes agree with the exit statuses, both jobs succeeded: pass" pass "$(verdict env)"
expect "test step failed but its job recorded exit 0: error" error "$(verdict PMX_OUTCOME_CAND_TEST=failure)"
contains "  ... says which step" "$vdir/result.json" "candidate-test step ended 'failure'"
# shellcheck disable=SC2016,SC2030,SC2031 # literal text for jq/eval; PATH only for that subshell
expect "build step succeeded but build.exit says 2: error" error "$(VPRE='echo 2 >"$vdir/test/candidate/build.exit"' verdict env)"
# shellcheck disable=SC2016,SC2030,SC2031 # literal text for jq/eval; PATH only for that subshell
expect "build failed and build.exit says 2: a failed check, not an error" fail "$(VPRE='echo 2 >"$vdir/test/candidate/build.exit"' verdict PMX_OUTCOME_BUILD=failure)"
expect "no outcome for the tag's test step: error" error "$(verdict PMX_OUTCOME_TAG_TEST=)"
expect "rerun step failed with no rerun recorded: error" error "$(verdict PMX_OUTCOME_RERUN=failure)"
vjobs success failure
expect "the job test (tag) did not conclude success (jobs API): error" error "$(verdict env)"
contains "  ... says so" "$vdir/result.json" "test (tag)"
vjobs success success
jq '.jobs += [{name: "test (candidate)", conclusion: "success"}]' "$stub/api/repos_test_provimux_actions_runs_77_attempts_1_jobs_per_page_100.json" >"$tmp/j.json"
mv "$tmp/j.json" "$stub/api/repos_test_provimux_actions_runs_77_attempts_1_jobs_per_page_100.json"
expect "two jobs named test (candidate): error" error "$(verdict env)"
vjobs success success

echo "# check.sh verdict: provimux-warning lines of the candidate's tests"
# vwarn: the candidate's test x/a/TestW skips with a warning (and one line that is not JSON).
# shellcheck disable=SC2016 # literal text for eval
vwarn='{ ev pass x/a TestA
  printf "%s\n" "{\"Action\":\"output\",\"Package\":\"x/a\",\"Test\":\"TestW\",\"Output\":\"    w_test.go:9: provimux-warning: 2 Devin model(s) carry the placeholder: m-1, m-2 <b>\\n\"}"
  echo "not json"; ev skip x/a TestW; ev pass x/a; } >"$vdir/test/candidate/test.json"'
# shellcheck disable=SC2030,SC2031 # PATH only for that subshell
expect "a test that warns and skips: still a pass" pass "$(VPRE="$vwarn" verdict env)"
expect "  ... result.json has the warning, without markup" "TestW: 2 Devin model(s) carry the placeholder: m-1, m-2 b" \
  "$(jq -r '.warnings[] | "\(.test): \(.text)"' "$vdir/result.json")"
expect "no warning: an empty list" "0 pass" "$(verdict env >/dev/null; jq -r '"\(.warnings | length) \(.status)"' "$vdir/result.json")"

echo "# run_artifact: by id, run, name and digest"
ra_zip="$tmp/ra"; rm -rf "$ra_zip"; mkdir -p "$ra_zip"; echo '{}' >"$ra_zip/state.json"
(cd "$ra_zip" && zip -q a.zip state.json)
ra_d="$(sha256_of "$ra_zip/a.zip")"
cp "$ra_zip/a.zip" "$stub/api/repos_test_provimux_actions_artifacts_601_zip.json"
ra_meta() { jq -n --arg d "sha256:$ra_d" '{id: 601, name: "pmx-prepare", expired: false, digest: $d, workflow_run: {id: 77}}' | jq "${1:-.}" >"$stub/api/repos_test_provimux_actions_artifacts_601.json"; }
# shellcheck disable=SC2016,SC2030,SC2031 # literal text for jq/eval; PATH only for that subshell
ra() { rm -rf "$tmp/ra.out"; (PATH="$stub/bin:$PATH" STUB_API="$stub/api" PMX_REPO=test/provimux; export PATH STUB_API PMX_REPO; run_artifact "$@" "$tmp/ra.out") >/dev/null 2>&1 && echo ok || echo refused; }
ra_meta
expect "the producing job's id and digest: downloaded" ok "$(ra 77 601 pmx-prepare "$ra_d")"
if [[ -s "$tmp/ra.out/state.json" ]]; then ok "  ... and unpacked"; else bad "  ... and unpacked"; fi
expect "an artifact of another run: refused" refused "$(ra 78 601 pmx-prepare "$ra_d")"
expect "another name: refused" refused "$(ra 77 601 pmx-test-tag "$ra_d")"
expect "another digest than the producing job's: refused" refused "$(ra 77 601 pmx-prepare "$(printf '%064d' 0)")"
expect "no id (the job uploaded nothing): refused" refused "$(ra 77 '' pmx-prepare "$ra_d")"
ra_meta '.expired = true'
expect "expired: refused" refused "$(ra 77 601 pmx-prepare "$ra_d")"
ra_meta; printf 'other' >"$stub/api/repos_test_provimux_actions_artifacts_601_zip.json"
expect "the zip downloaded is not that digest (replaced bytes): refused" refused "$(ra 77 601 pmx-prepare "$ra_d")"

# --- releases_status and the reporter: the newest -pmx.<n> decides; older bases ---------------------
echo "# releases_status: the newest v<base>-pmx.<n> decides"
git -C "$pmx" tag v1.1.0-pmx.2 "$pmx_sha" && git -C "$pmx" push -q "$remote" refs/tags/v1.1.0-pmx.2
relfile() { expected_assets "$1" | jq -R -s '{draft: false, prerelease: false, published_at: "2026-11-19T00:00:00Z",
  assets: (split("\n") | map(select(length > 0) | {name: ., state: "uploaded"}))}' | jq "${2:-.}" >"$stub/api/repos_test_provimux_releases_tags_v$1.json"; }
relrun() { jq -n --arg sha "$pmx_sha" --arg t "v$1" --arg c "$2" '{workflow_runs: [{id: 8, path: ".github/workflows/provimux-release.yml", event: "push",
    head_branch: $t, head_sha: $sha, repository: {full_name: "test/provimux"},
    status: (if $c == "running" then "in_progress" else "completed" end), conclusion: (if $c == "running" then null else $c end), created_at: "2026-11-19T00:00:00Z"}]}' \
    >"$stub/api/repos_test_provimux_actions_runs_event_push_branch_v$1_per_page_50.json"; }
# shellcheck disable=SC2016,SC2030,SC2031 # literal text for jq/eval; PATH only for that subshell
rstat() { : >"$tmp/rs.txt"; (PATH="$stub/bin:$PATH" STUB_API="$stub/api" PMX_REPO=test/provimux PMX_REPO_URL="$remote"; export PATH STUB_API PMX_REPO PMX_REPO_URL; releases_status 1.1.0 "$tmp/rs.json" "$tmp/rs.txt"); }
relfile 1.1.0-pmx.1 '.draft = true'; relrun 1.1.0-pmx.1 failure
relfile 1.1.0-pmx.2; relrun 1.1.0-pmx.2 success
expect "failed -pmx.1, complete -pmx.2: published" published "$(rstat)"
contains "  ... names -pmx.1 as replaced" "$tmp/rs.txt" "v1.1.0-pmx.2 replaces it"
relfile 1.1.0-pmx.1; relrun 1.1.0-pmx.1 success; relrun 1.1.0-pmx.2 failure
expect "complete -pmx.1, failed -pmx.2: incomplete" incomplete "$(rstat)"
relrun 1.1.0-pmx.2 running
expect "complete -pmx.1, -pmx.2 running: in-progress" in-progress "$(rstat)"

echo "# report.sh: open issues of older bases (sweep)"
echo '[{"number": 11, "title": "sync: v1.1.0 — release incomplete"}, {"number": 12, "title": "sync: v1.2.0 — ready to publish"}, {"number": 13, "title": "sync: v1.0.0 — ready to publish"}]' \
  >"$stub/api/repos_test_provimux_issues_state_open_per_page_100.json"
jq -n --arg s "$pmx_sha" '{status: "fail", step: "rebase", upstream: {tag: "v1.2.0"}, reasons: ["x"]}' >"$tmp/r12.json"
relrun 1.1.0-pmx.2 failure
report "$tmp/r12.json" success >/dev/null
contains "older base with a failed newest release: release incomplete (older base)" "$tmp/report.out" "sweep: #11 title=sync: v1.1.0 — release incomplete (older base), owner needed"
contains "  ... with the rerun command" "$tmp/report.out" "gh run rerun 8 --failed -R test/provimux"
contains "older base never published: says so" "$tmp/report.out" "sweep: #13 title=sync: v1.0.0 — not published; the check now checks v1.2.0"
lacks "  ... this run's own base is not swept" "$tmp/report.out" "sweep: #12"
relrun 1.1.0-pmx.2 success
report "$tmp/r12.json" success >/dev/null
contains "older base whose newest release is complete: closed" "$tmp/report.out" "sweep: close #11 (v1.1.0)"

# --- tap.sh: the Homebrew tap, prepared without a token ------------------------------------------
echo "# tap.sh: release, verify, prepare (only to the latest release), artifact"
tapd="$tmp/tapfx"; rm -rf "$tapd"; mkdir -p "$tapd/assets"
# The release v1.3.0-pmx.1: every expected asset, checksums.txt, digests in the API.
TV=1.3.0-pmx.1
: >"$tapd/checksums.txt"
for f in $(expected_assets "$TV"); do
  [[ "$f" == checksums.txt ]] && continue
  echo "archive $f" >"$tapd/assets/$f"
  printf '%s  %s\n' "$(sha256_of "$tapd/assets/$f")" "$f" >>"$tapd/checksums.txt"
done
cp "$tapd/checksums.txt" "$stub/api/repos_test_provimux_releases_assets_900001.json"
tap_release() { # [JQ]: the release JSON of v$TV (JQ edits it)
  { for f in $(expected_assets "$TV"); do
      if [[ "$f" == checksums.txt ]]; then jq -n --arg d "sha256:$(sha256_of "$tapd/checksums.txt")" '{id: 900001, name: "checksums.txt", state: "uploaded", digest: $d}'
      else jq -n --arg n "$f" --arg d "sha256:$(sha256_of "$tapd/assets/$f")" '{id: 900002, name: $n, state: "uploaded", digest: $d}'; fi
    done; } | jq -s '{id: 77001, tag_name: "v'"$TV"'", draft: false, prerelease: false, immutable: true, published_at: "2026-11-19T00:00:00Z", assets: .}' | jq "${1:-.}" \
    >"$stub/api/repos_test_provimux_releases_tags_v$TV.json"
}
# The tap: a formula at 1.2.0-pmx.1 with two archive URLs.
tapr="$tmp/tap.git"; rm -rf "$tapr" "$tmp/tapw"; git init -q --bare -b main "$tapr"; git init -q -b main "$tmp/tapw"
mkdir -p "$tmp/tapw/Formula"
cat >"$tmp/tapw/Formula/provimux.rb" <<'RB'
class Provimux < Formula
  desc "test"
  version "1.2.0-pmx.1"
  on_macos do
    url "https://github.com/ngvoicu/provimux/releases/download/v1.2.0-pmx.1/provimux_1.2.0-pmx.1_darwin_aarch64.tar.gz"
    sha256 "0000000000000000000000000000000000000000000000000000000000000000"
  end
  on_linux do
    url "https://github.com/ngvoicu/provimux/releases/download/v1.2.0-pmx.1/provimux_1.2.0-pmx.1_linux_amd64.tar.gz"
    sha256 "1111111111111111111111111111111111111111111111111111111111111111"
  end
end
RB
(cd "$tmp/tapw" && git add -A && git commit -q -m tap && git push -q "$tapr" HEAD:main)
tapsh() { (cd "$tmp" && env PATH="$stub/bin:$PATH" STUB_API="$stub/api" PMX_REPO=test/provimux PMX_TAP_URL="$tapr" GITHUB_OUTPUT= "$@") >"$tmp/tap.out" 2>"$tmp/tap.err" && echo ok || echo refused; }
tap_release
expect "release published, immutable, digests = checksums.txt: ok" ok "$(tapsh bash "$here/tap.sh" release "v$TV" "$tmp/tr")"
tap_release '.immutable = false'
expect "release not immutable: refused" refused "$(tapsh bash "$here/tap.sh" release "v$TV" "$tmp/tr")"
tap_release '.assets[1].digest = "sha256:'"$(printf '%064d' 0)"'"'
expect "an archive's digest is not checksums.txt's: refused" refused "$(tapsh bash "$here/tap.sh" release "v$TV" "$tmp/tr")"
tap_release 'del(.assets[2])'
expect "an asset missing: refused" refused "$(tapsh bash "$here/tap.sh" release "v$TV" "$tmp/tr")"
tap_release '.assets[0].digest = null'
expect "an asset without a digest: refused" refused "$(tapsh bash "$here/tap.sh" release "v$TV" "$tmp/tr")"
tap_release
# tlatest JSON|none: GitHub's "latest" release answer. v$TV is the newest release.
tlatest() { if [[ "$1" == none ]]; then rm -f "$stub/api/repos_test_provimux_releases_latest.json"; else printf '%s\n' "$1" >"$stub/api/repos_test_provimux_releases_latest.json"; fi; }
tlatest '{"tag_name": "v'"$TV"'"}'
rm -rf "$tmp/tp"
expect "prepare 1.2.0 -> $TV (formula from update-formula.sh): ok" ok "$(tapsh bash "$here/tap.sh" prepare "v$TV" "$tmp/tp")"
contains "  ... action push" "$tmp/tap.out" "action=push"
contains "  ... the lease is the tap's main as read" "$tmp/tap.out" "lease=refs/heads/main:$(git --git-dir="$tapr" rev-parse main)"
if git --git-dir="$tmp/tp/tap/.git" show HEAD:Formula/provimux.rb | grep -qF "version \"$TV\""; then ok "  ... committed the new version locally"; else bad "  ... committed the new version locally"; fi
expect "  ... and pushed nothing" "$(cd "$tmp/tapw" && git rev-parse HEAD)" "$(git --git-dir="$tapr" rev-parse main)"
# verify on its own
sed -n 's/^.*$/&/p' "$tmp/tp/tap/Formula/provimux.rb" >"$tmp/new.rb"
expect "verify: the prepared formula: ok" ok "$(tapsh bash "$here/tap.sh" verify "$TV" "$tmp/tp/release/checksums.txt" "$tmp/tapw/Formula/provimux.rb" "$tmp/new.rb")"
sed 's/desc "test"/desc "other"/' "$tmp/new.rb" >"$tmp/bad.rb"
expect "verify: another line changed: refused" refused "$(tapsh bash "$here/tap.sh" verify "$TV" "$tmp/tp/release/checksums.txt" "$tmp/tapw/Formula/provimux.rb" "$tmp/bad.rb")"
awk '/sha256/ && !d {sub(/"[0-9a-f]*"/, "\"'"$(printf '%064d' 7)"'\""); d = 1} {print}' "$tmp/new.rb" >"$tmp/bad.rb"
expect "verify: a sha256 not checksums.txt's: refused" refused "$(tapsh bash "$here/tap.sh" verify "$TV" "$tmp/tp/release/checksums.txt" "$tmp/tapw/Formula/provimux.rb" "$tmp/bad.rb")"
sed "s/version \"$TV\"/version \"9.9.9-pmx.1\"/" "$tmp/new.rb" >"$tmp/bad.rb"
expect "verify: another version: refused" refused "$(tapsh bash "$here/tap.sh" verify "$TV" "$tmp/tp/release/checksums.txt" "$tmp/tapw/Formula/provimux.rb" "$tmp/bad.rb")"
expect "prepare with a given formula that changes another line: refused" refused "$(tapsh bash "$here/tap.sh" prepare "v$TV" "$tmp/tp" "$tmp/bad.rb")"
# The tap moves only to GitHub's latest release, read strictly.
tlatest 'HTTP 503'
expect "latest unreadable (503): prepare stops" refused "$(tapsh bash "$here/tap.sh" prepare "v$TV" "$tmp/tp")"
tlatest none
expect "no latest (404): prepare stops" refused "$(tapsh bash "$here/tap.sh" prepare "v$TV" "$tmp/tp")"
tlatest '{"tag_name": "v1.3.0"}'
expect "latest is not a -pmx tag: prepare stops" refused "$(tapsh bash "$here/tap.sh" prepare "v$TV" "$tmp/tp")"
tlatest '{"tag_name": "v1.2.0-pmx.1"}'
expect "the tag is newer than latest (latest not moved yet): prepare stops" refused "$(tapsh bash "$here/tap.sh" prepare "v$TV" "$tmp/tp")"
tlatest '{"tag_name": "v'"$TV"'"}'
# A tap at a higher version than the newest release: reported, never "ok" or "none".
(cd "$tmp/tapw" && sed -i.bak 's/version "1.2.0-pmx.1"/version "1.9.0-pmx.1"/' Formula/provimux.rb && rm -f Formula/provimux.rb.bak && git commit -qam newer && git push -q "$tapr" HEAD:main)
expect "the tap is at a higher version than the newest release: prepare refuses" refused "$(tapsh bash "$here/tap.sh" prepare "v$TV" "$tmp/tp")"
contains "  ... says so" "$tmp/tap.err" "higher than the newest release $TV"
expect "  ... tap.sh check: wrong, not ok" refused "$(tapsh bash "$here/tap.sh" check "v$TV" "$tmp/tc")"
contains "  ... says so" "$tmp/tap.out" "wrong: the tap is at '1.9.0-pmx.1', not the newest release $TV"
# artifact: the formula the release run's tap-check bound to itself.
(cd "$tmp/tp" && mkdir -p af && cp tap/Formula/provimux.rb af/provimux.rb && cd af && zip -q f.zip provimux.rb)
fd="$(sha256_of "$tmp/tp/af/f.zip")"
cp "$tmp/tp/af/f.zip" "$stub/api/repos_test_provimux_actions_artifacts_700_zip.json"
jq -n --arg d "sha256:$fd" '{id: 700, name: "formula", expired: false, digest: $d, workflow_run: {id: 55}}' >"$stub/api/repos_test_provimux_actions_artifacts_700.json"
jq -n --arg sha "$pmx_sha" '{workflow_runs: [{id: 55, path: ".github/workflows/provimux-release.yml", event: "push", head_branch: "v1.3.0-pmx.1",
  head_sha: $sha, repository: {full_name: "test/provimux"}, status: "completed", conclusion: "success", created_at: "2026-11-19T00:00:00Z"}]}' \
  >"$stub/api/repos_test_provimux_actions_runs_event_push_branch_v1.3.0-pmx.1_per_page_50.json"
echo '{"id": 55, "status": "completed", "conclusion": "success", "run_attempt": 2}' >"$stub/api/repos_test_provimux_actions_runs_55.json"
echo '{"jobs": [{"name": "formula", "id": 61}, {"name": "tap-check", "id": 62}]}' >"$stub/api/repos_test_provimux_actions_runs_55_attempts_2_jobs_per_page_100.json"
tapann() { printf '%s\n' "$@" | jq -R -s 'split("\n") | map(select(length > 0) | {title: "provimux-formula-artifact", message: .})' >"$stub/api/repos_test_provimux_check-runs_62_annotations_per_page_100.json"; }
tapann "700 formula sha256:$fd"
expect "artifact: the formula tap-check bound, of the successful run's attempt: ok" ok "$(tapsh bash "$here/tap.sh" artifact "v$TV" "$pmx_sha" "$tmp/ta")"
if command cmp -s "$tmp/ta/provimux.rb" "$tmp/tp/af/provimux.rb"; then ok "  ... unpacked"; else bad "  ... unpacked"; fi
tapann "700 formula sha256:$(printf '%064d' 0)"
expect "artifact: another digest than the annotation's: refused" refused "$(tapsh bash "$here/tap.sh" artifact "v$TV" "$pmx_sha" "$tmp/ta")"
tapann "700 formula sha256:$fd" "701 formula sha256:$fd"
expect "artifact: two binding annotations: refused" refused "$(tapsh bash "$here/tap.sh" artifact "v$TV" "$pmx_sha" "$tmp/ta")"
tapann "700 formula sha256:$fd"
echo '{"id": 55, "status": "completed", "conclusion": "failure", "run_attempt": 2}' >"$stub/api/repos_test_provimux_actions_runs_55.json"
expect "artifact: the release run did not succeed: refused" refused "$(tapsh bash "$here/tap.sh" artifact "v$TV" "$pmx_sha" "$tmp/ta")"
echo '{"id": 55, "status": "completed", "conclusion": "success", "run_attempt": 2}' >"$stub/api/repos_test_provimux_actions_runs_55.json"
expect "artifact: a run for another commit: refused" refused "$(tapsh bash "$here/tap.sh" artifact "v$TV" "$other_sha" "$tmp/ta")"

echo "# tap.sh: the same version with other bytes is repaired, never \"none\"; tap.sh check"
# settap VERSION [SHA0]: the tap's formula at VERSION; the first sha256 SHA0 (default: the release's).
settap() {
  cp "$tmp/tapw/Formula/provimux.rb" "$tmp/tapw/Formula/old.rb" 2>/dev/null || true
  sed -E "s/(provimux_|v)1\.[0-9]+\.0-pmx\.1/\11.2.0-pmx.1/g; s/version \"[^\"]*\"/version \"1.2.0-pmx.1\"/" "$tmp/tapw/Formula/old.rb" >"$tmp/tapw/Formula/provimux.rb"
  rm -f "$tmp/tapw/Formula/old.rb"
  if [[ "$1" != 1.2.0-pmx.1 ]]; then
    bash "$here/update-formula.sh" "$1" "$tapd/checksums.txt" "$tmp/tapw/Formula/provimux.rb" >/dev/null 2>&1 || {
      sed -i.bak "s/version \"1.2.0-pmx.1\"/version \"$1\"/" "$tmp/tapw/Formula/provimux.rb"; rm -f "$tmp/tapw/Formula/provimux.rb.bak"; }
  fi
  if [[ -n "${2:-}" ]]; then
    awk -v z="$2" '/sha256/ && !d {sub(/"[0-9a-f]*"/, "\"" z "\""); d = 1} {print}' "$tmp/tapw/Formula/provimux.rb" >"$tmp/x.rb" && mv "$tmp/x.rb" "$tmp/tapw/Formula/provimux.rb"
  fi
  (cd "$tmp/tapw" && git add -A && git commit -q -m "tap $1" --allow-empty && git push -q -f "$tapr" HEAD:main)
}
wrong="$(printf '%064d' 5)"
settap "$TV" "$wrong"
rm -rf "$tmp/tp2"
expect "tap at $TV with a wrong sha256, formula from the release (owner's path): ok" ok "$(tapsh bash "$here/tap.sh" prepare "v$TV" "$tmp/tp2")"
contains "  ... action push (repair), not none" "$tmp/tap.out" "action=push"
contains "  ... says it repairs" "$tmp/tap.err" "repairing it"
if git --git-dir="$tmp/tp2/tap/.git" show HEAD:Formula/provimux.rb | grep -qF "$wrong"; then bad "  ... the commit has the release's sha256"; else ok "  ... the commit has the release's sha256"; fi
git --git-dir="$tmp/tp2/tap/.git" show HEAD:Formula/provimux.rb >"$tmp/verified.rb"
expect "the same with the verified formula given (the publish job's path): ok" ok "$(tapsh bash "$here/tap.sh" prepare "v$TV" "$tmp/tp2" "$tmp/verified.rb")"
contains "  ... action push (repair), not none" "$tmp/tap.out" "action=push"
expect "tap.sh check: the tap at $TV with a wrong sha256: wrong" refused "$(tapsh bash "$here/tap.sh" check "v$TV" "$tmp/tc")"
contains "  ... says so" "$tmp/tap.out" "wrong: the tap is at $TV but its urls or sha256 are not the release's"
settap "$TV"
expect "tap at $TV with the release's formula: prepare does nothing" ok "$(tapsh bash "$here/tap.sh" prepare "v$TV" "$tmp/tp2")"
contains "  ... action none" "$tmp/tap.out" "action=none"
expect "tap.sh check: the tap serves $TV: ok" ok "$(tapsh bash "$here/tap.sh" check "v$TV" "$tmp/tc")"
settap 1.2.0-pmx.1
expect "tap.sh check: the tap at an older version: wrong" refused "$(tapsh bash "$here/tap.sh" check "v$TV" "$tmp/tc")"
settap 1.9.0-pmx.1 "$wrong"
expect "tap.sh check: a higher version with a wrong sha256: wrong, never ok" refused "$(tapsh bash "$here/tap.sh" check "v$TV" "$tmp/tc")"
lacks "  ... not ok" "$tmp/tap.out" "ok:"

echo "# tap.sh: a superseded release (-pmx.1 after -pmx.2 became latest) never moves the tap"
settap "$TV" "$wrong"
tlatest '{"tag_name": "v1.3.0-pmx.2"}'
rm -rf "$tmp/tp3"
expect "the owner's path (publish.sh --tap v$TV): ok, nothing to do" ok "$(tapsh bash "$here/tap.sh" prepare "v$TV" "$tmp/tp3")"
contains "  ... action none" "$tmp/tap.out" "action=none"
lacks "  ... no push" "$tmp/tap.out" "action=push"
contains "  ... says superseded, tap left alone" "$tmp/tap.err" "v$TV is superseded by v1.3.0-pmx.2; tap left alone"
if [[ -e "$tmp/tp3/tap" ]]; then bad "  ... nothing cloned or committed"; else ok "  ... nothing cloned or committed"; fi
expect "the publish job's path (verified formula given): ok, nothing to do" ok "$(tapsh bash "$here/tap.sh" prepare "v$TV" "$tmp/tp3" "$tmp/verified.rb")"
contains "  ... action none" "$tmp/tap.out" "action=none"
lacks "  ... no push" "$tmp/tap.out" "action=push"
settap "$TV"
expect "tap.sh check of the superseded tag, even with the tap byte-identical to it: wrong" refused "$(tapsh bash "$here/tap.sh" check "v$TV" "$tmp/tc")"
contains "  ... names the newest" "$tmp/tap.out" "is not the newest release (v1.3.0-pmx.2)"
tlatest '{"tag_name": "v'"$TV"'"}'

echo "# report.sh and its sweep never read, retitle or close the tap's issue"
git -C "$pmx" tag v1.3.0-pmx.1 "$pmx_sha" && git -C "$pmx" push -q "$remote" refs/tags/v1.3.0-pmx.1
# report_real RESULT CHECK_JOB: report.sh writing issues (the stub gh logs them in $stub/issue.log).
report_real() {
  : >"$stub/issue.log"
  PATH="$stub/bin:$PATH" STUB_API="$stub/api" PMX_REPO=test/provimux PMX_REPO_URL="$remote" \
    RESULT="$1" CHECK_JOB="$2" RUN_URL=https://example.invalid/run bash "$here/report.sh" >"$tmp/report.out" 2>&1 || echo "exit $?"
}
echo '{"body": "<!-- provimux-sync status=pass candidate=none -->"}' >"$stub/api/repos_test_provimux_issues_22.json"
echo '[{"number": 21, "title": "homebrew tap: v1.3.0-pmx.1 needs the owner"}, {"number": 22, "title": "sync: v1.3.0 — ready to publish"}]' \
  >"$stub/api/repos_test_provimux_issues_state_open_per_page_100.json"
settap "$TV" "$wrong"
jq -n '{status: "error", step: "upstream", upstream: {tag: "v1.3.0"}, reasons: ["gh: Service Unavailable (HTTP 503)"]}' >"$tmp/r13e.json"
expect "a transient check error: report.sh runs" "" "$(report_real "$tmp/r13e.json" failure)"
contains "  ... retitles the sync issue" "$stub/issue.log" "issue edit 22"
lacks "  ... never the tap issue" "$stub/issue.log" " 21 "
echo '{"status": "published", "upstream": {"tag": "v1.3.0"}, "reasons": []}' >"$tmp/r13.json"
expect "then published, the tap still wrong: report.sh runs" "" "$(report_real "$tmp/r13.json" success)"
contains "  ... closes the sync issue (the release is complete)" "$stub/issue.log" "issue close 22"
lacks "  ... never the tap issue" "$stub/issue.log" " 21 "
# The sweep from a newer base, the tap at a higher version with a wrong sha256.
settap 1.9.0-pmx.1 "$wrong"
jq -n '{status: "fail", step: "rebase", upstream: {tag: "v1.4.0"}, reasons: ["x"]}' >"$tmp/r14.json"
report_real "$tmp/r14.json" success >/dev/null
contains "older base published: the sweep closes its sync issue" "$stub/issue.log" "issue close 22"
lacks "  ... never the tap issue, whatever version the tap is at" "$stub/issue.log" " 21 "
lacks "  ... and runs no tap check" "$tmp/report.out" "tap"
echo '[]' >"$stub/api/repos_test_provimux_issues_state_open_per_page_100.json"

# --- provimux-release.yml: provenance (the owner's commit status) and release-latest ----------------
rwf="$here/../workflows/provimux-release.yml"
# inline NAME: the lines of the release workflow between "# >>> NAME" and "# <<< NAME".
inline() { sed -n "/^ *# >>> $1\$/,/^ *# <<< $1\$/p" "$rwf" | sed 's/^          //' >"$tmp/$1.sh"; [[ "$(grep -c '' "$tmp/$1.sh")" -gt 10 ]]; }
echo "# provimux-release.yml: provenance (commit status by the owner)"
if inline provenance; then ok "provenance lines found in the workflow"; else bad "provenance lines found in the workflow"; fi
mkdir -p "$stub/gitbin2"
cat >"$stub/gitbin2/git" <<'EOF'
#!/usr/bin/env bash
[ "$1" = ls-remote ] || exit 1
printf '%s\trefs/tags/%s\n' "$STUB_TAGSHA" "$TAG"
EOF
chmod +x "$stub/gitbin2/git"
# statuses JSON...: the commit statuses of $pmx_sha.
statuses() { jq -s '.' <<<"$*" >"$stub/api/repos_test_provimux_commits_${pmx_sha}_statuses_per_page_100.json"; }
st() { jq -nc --arg c "$1" --arg s "$2" --arg l "$3" --arg t "$4" --argjson id "$5" '{id: $id, context: $c, state: $s, creator: {login: $l, type: $t}}'; }
prov() {
  env PATH="$stub/gitbin2:$stub/bin:$PATH" STUB_API="$stub/api" GH_REPO=test/provimux OWNER=ngvoicu TAG=v1.3.0-pmx.1 SHA="$pmx_sha" \
    STUB_TAGSHA="$pmx_sha" GITHUB_REF=refs/tags/v1.3.0-pmx.1 GITHUB_STEP_SUMMARY=/dev/null PMX_NO_WAIT=1 "$@" \
    bash "$tmp/provenance.sh" >"$tmp/prov.out" 2>&1 && echo pass || echo refused
}
statuses "$(st provimux-release/v1.3.0-pmx.1 success ngvoicu User 1)"
expect "the owner's success status for this tag: released" pass "$(prov env)"
statuses "$(st provimux-release/v1.3.0-pmx.1 success github-actions[bot] Bot 1)"
expect "the same status by github-actions[bot] (GITHUB_TOKEN of any workflow): refused" refused "$(prov env)"
statuses "$(st provimux-release/v1.3.0-pmx.1 success ngvoicu User 1)" "$(st provimux-release/v1.3.0-pmx.1 failure ngvoicu User 2)"
expect "the owner's newest status is failure: refused" refused "$(prov env)"
statuses "$(st provimux-release/v1.3.0-pmx.2 success ngvoicu User 1)"
expect "a status for another tag only: refused" refused "$(prov env)"
contains "  ... says there is none" "$tmp/prov.out" "has no commit status provimux-release/v1.3.0-pmx.1"
statuses "$(st provimux-release/v1.3.0-pmx.1 success ngvoicu User 1)"
expect "the tag on the remote is another commit: refused" refused "$(prov STUB_TAGSHA="$other_sha")"
echo 'HTTP 503' >"$stub/api/repos_test_provimux_commits_${pmx_sha}_statuses_per_page_100.json"
expect "the statuses cannot be read: refused, as a read error" refused "$(prov env)"
contains "  ... says it cannot read them (not 'none')" "$tmp/prov.out" "cannot read the commit statuses"

echo "# provimux-release.yml: release-latest promotes only its own release"
if inline latest; then ok "latest lines found in the workflow"; else bad "latest lines found in the workflow"; fi
rel() { jq -n --argjson id "$1" --arg t "$2" '{id: $id, tag_name: $t, draft: false, prerelease: false, immutable: true}' | jq "${3:-.}" >"$stub/api/repos_test_provimux_releases_$1.json"; }
latest() { if [[ "$1" == none ]]; then rm -f "$stub/api/repos_test_provimux_releases_latest.json"; else printf '%s\n' "$1" >"$stub/api/repos_test_provimux_releases_latest.json"; fi; }
lrun() {
  : >"$stub/calls.log"
  env PATH="$stub/bin:$PATH" STUB_API="$stub/api" GH_REPO=test/provimux "$@" bash "$tmp/latest.sh" >"$tmp/latest.out" 2>&1 && echo pass || echo refused
}
rel 301 v1.3.0-pmx.1; rel 299 v1.2.0-pmx.1
latest '{"tag_name": "v1.2.0-pmx.1"}'
expect "latest is older: this run's release 301 is promoted" pass "$(lrun TAG=v1.3.0-pmx.1 ID=301)"
contains "  ... by its own id" "$stub/calls.log" "PATCH repos/test/provimux/releases/301"
latest '{"tag_name": "v1.3.0-pmx.1"}'
expect "an older run (v1.2.0-pmx.1) while latest is newer: left as it is" pass "$(lrun TAG=v1.2.0-pmx.1 ID=299)"
if [[ -s "$stub/calls.log" ]]; then bad "  ... and nothing written"; else ok "  ... and nothing written"; fi
latest none
expect "no latest yet (404): promoted" pass "$(lrun TAG=v1.2.0-pmx.1 ID=299)"
latest 'HTTP 503'
expect "latest unreadable (503): stops" refused "$(lrun TAG=v1.3.0-pmx.1 ID=301)"
if [[ -s "$stub/calls.log" ]]; then bad "  ... and nothing written"; else ok "  ... and nothing written"; fi
latest '{}'
expect "latest answers 200 without a tag: stops" refused "$(lrun TAG=v1.3.0-pmx.1 ID=301)"
latest '{"tag_name": "v1.2.0-pmx.1"}'
expect "the id is another tag's release: refused (never another release)" refused "$(lrun TAG=v1.3.0-pmx.1 ID=299)"
rel 301 v1.3.0-pmx.1 '.immutable = false'
expect "this run's release is not immutable: refused" refused "$(lrun TAG=v1.3.0-pmx.1 ID=301)"
rel 301 v1.3.0-pmx.1
cp "$stub/api/repos_test_provimux_releases_299.json" "$tmp/r299"; rel 301 v1.3.0-pmx.1
# From here the PATCH changes nothing by itself: the read-back shows what the test puts there.
# shellcheck disable=SC2016 # literal text for sed
sed -i.bak 's|cp "\$f" "\$(dirname|true "$(dirname|' "$stub/bin/gh" && rm -f "$stub/bin/gh.bak"
# A stub sleep: records each wait in $STUB_SLEEPS and runs $STUB_ON_SLEEP (the write becoming
# visible), without waiting.
mkdir -p "$stub/sleepbin"
# shellcheck disable=SC2016 # expanded by the stub
printf '#!/bin/sh\necho "$1" >>"$STUB_SLEEPS"\n[ -z "$STUB_ON_SLEEP" ] || sh -c "$STUB_ON_SLEEP"\n' >"$stub/sleepbin/sleep"
chmod +x "$stub/sleepbin/sleep"
sleeps() { tr '\n' ' ' <"$tmp/sleeps" | sed 's/ $//'; }
lwait() { : >"$tmp/sleeps"; lrun PATH="$stub/sleepbin:$stub/bin:$PATH" STUB_SLEEPS="$tmp/sleeps" "$@"; }
lf="$stub/api/repos_test_provimux_releases_latest.json"
latest '{"tag_name": "v1.2.0-pmx.1"}'
expect "latest still reads as before the change: read again, then fails" refused "$(lwait TAG=v1.3.0-pmx.1 ID=301)"
expect "  ... 6 reads, 2 4 8 16 32 s apart" "2 4 8 16 32" "$(sleeps)"
contains "  ... says what it reads" "$tmp/latest.out" "reads back as 'v1.2.0-pmx.1'"
expect "latest shows the change after the first wait: promoted" pass \
  "$(lwait TAG=v1.3.0-pmx.1 ID=301 STUB_ON_SLEEP="echo '{\"tag_name\": \"v1.3.0-pmx.1\"}' >'$lf'")"
expect "  ... after one wait" "2" "$(sleeps)"
latest none
expect "no latest before: a 404 right after the change is read again; promoted" pass \
  "$(lwait TAG=v1.2.0-pmx.1 ID=299 STUB_ON_SLEEP="echo '{\"tag_name\": \"v1.2.0-pmx.1\"}' >'$lf'")"
expect "  ... after one wait" "2" "$(sleeps)"
latest '{"tag_name": "v1.2.0-pmx.1"}'
expect "latest then reads as a third release: fails at that read" refused \
  "$(lwait TAG=v1.3.0-pmx.1 ID=301 STUB_ON_SLEEP="echo '{\"tag_name\": \"v1.1.0-pmx.1\"}' >'$lf'")"
expect "  ... no further wait" "2" "$(sleeps)"
contains "  ... says what it reads" "$tmp/latest.out" "reads back as 'v1.1.0-pmx.1'"
latest '{"tag_name": "v1.2.0-pmx.1"}'

echo "# provimux-release.yml: publish-release reads the published release back"
if inline published; then ok "published lines found in the workflow"; else bad "published lines found in the workflow"; fi
pf="$stub/api/repos_test_provimux_releases_tags_v1.3.0-pmx.1.json"
pubrel() { jq -n --argjson d "$1" --argjson i "$2" '{id: 301, draft: $d, prerelease: false, immutable: $i, assets: []}'; }
prun() { # prun [ENV...]: the published lines with the stub gh and sleep; pub in $tmp/pub-rb.out
  : >"$tmp/sleeps"
  # shellcheck disable=SC2016 # expanded by the harness shell
  env PATH="$stub/sleepbin:$stub/bin:$PATH" STUB_API="$stub/api" STUB_SLEEPS="$tmp/sleeps" GH_REPO=test/provimux TAG=v1.3.0-pmx.1 "$@" \
    bash -c 'set -euo pipefail; fail() { echo "::error::$*"; exit 1; }; . "$1"; jq -c ".[0] | {draft, immutable}" <<<"$pub"' _ "$tmp/published.sh" \
    >"$tmp/pub-rb.out" 2>&1 && echo pass || echo failed
}
pubrel false true >"$pf"
expect "published and immutable at the first read: no wait" pass "$(prun)"
expect "  ... no wait" "" "$(sleeps)"
rm -f "$pf"
expect "not found by its tag (404) right after publishing, found after a wait: ok" pass "$(prun STUB_ON_SLEEP="jq -n '{id: 301, draft: false, prerelease: false, immutable: true, assets: []}' >'$pf'")"
expect "  ... after one wait" "2" "$(sleeps)"
pubrel true false >"$pf"
expect "still a draft right after publishing, published after a wait: ok" pass "$(prun STUB_ON_SLEEP="jq -n '{id: 301, draft: false, prerelease: false, immutable: true, assets: []}' >'$pf'")"
expect "  ... after one wait" "2" "$(sleeps)"
rm -f "$pf"
expect "never found by its tag: fails after 6 reads" failed "$(prun)"
expect "  ... 2 4 8 16 32 s apart" "2 4 8 16 32" "$(sleeps)"
contains "  ... says so" "$tmp/pub-rb.out" "not found by its tag after publishing it"
echo 'HTTP 503' >"$pf"
expect "the release cannot be read (503): fails at once" failed "$(prun)"
expect "  ... no wait" "" "$(sleeps)"
pubrel false false >"$pf"
expect "published but never immutable: read 6 times, the check after it refuses" pass "$(prun)"
contains "  ... hands on immutable=false" "$tmp/pub-rb.out" '"immutable":false'
expect "  ... 2 4 8 16 32 s apart" "2 4 8 16 32" "$(sleeps)"
rm -f "$pf"

echo "# provimux-release.yml: image-push reads a manifest back (put_manifest), no registry"
if inline readback; then ok "readback lines found in the workflow"; else bad "readback lines found in the workflow"; fi
# A stub curl for a registry: a PUT stores the body's digest under its ref; a HEAD answers
# 404 for the first $REG/lag reads after the PUT, then 200 with the stored digest ($REG/wrong
# instead when it exists); $REG/err makes every HEAD a 500. Requests are logged in $REG/log.
reg="$tmp/reg"; mkdir -p "$stub/regbin"
cat >"$stub/regbin/curl" <<'EOF'
#!/usr/bin/env bash
m=GET head='' hdr='' data='' url=''
while [ $# -gt 0 ]; do
  case "$1" in -I) head=1 ;; -X) m=$2; shift ;; -D) hdr=$2; shift ;; --data-binary) data=${2#@}; shift ;; -o | -H | -u | -w | -T) shift ;; http*) url=$1 ;; esac
  shift
done
ref=${url##*/manifests/}; f="$REG/m-$ref"
if [ "$m" = PUT ]; then
  echo "PUT $ref" >>"$REG/log"
  { sha256sum "$data" 2>/dev/null || shasum -a 256 "$data"; } | awk '{print "sha256:" $1}' >"$f"
  cp "$REG/lag" "$f.lag"; exit 0
fi
[ -n "$head" ] || exit 22
echo "HEAD $ref" >>"$REG/log"
if [ -f "$REG/err" ]; then printf 500; exit 0; fi
if [ ! -f "$f" ]; then printf 404; exit 0; fi
n=$(cat "$f.lag"); if [ "$n" -gt 0 ]; then echo $((n - 1)) >"$f.lag"; printf 404; exit 0; fi
printf 'HTTP/1.1 200 OK\r\nDocker-Content-Digest: %s\r\n\r\n' "$(cat "$REG/wrong" 2>/dev/null || cat "$f")" >"$hdr"
printf 200
EOF
chmod +x "$stub/regbin/curl"
# shellcheck disable=SC2016 # expanded by the harness shell
{ echo 'set -euo pipefail'; echo 'fail() { echo "::error::$*"; exit 1; }'
  echo 'name=test/provimux; api=http://registry.invalid/v2/test/provimux; accept=x'
  echo "auth=(-H 'Authorization: Bearer test')"
  command -v sha256sum >/dev/null 2>&1 || echo 'sha256sum() { shasum -a 256 "$@"; }'
  cat "$tmp/readback.sh"
  echo 'put_manifest "$1" "$2" application/vnd.oci.image.index.v1+json'; echo 'echo "ok: $1"'; } >"$tmp/rb-harness.sh"
echo '{"schemaVersion": 2}' >"$tmp/rb-index.json"
rb() { # rb LAG [wrong|err]: put_manifest 1.3.0-pmx.1 with LAG absent reads after the PUT
  rm -rf "$reg"; mkdir -p "$reg"; echo "$1" >"$reg/lag"; : >"$tmp/sleeps"
  case "${2:-}" in wrong) echo "sha256:$(printf '%064d' 0)" >"$reg/wrong" ;; err) : >"$reg/err" ;; esac
  env PATH="$stub/regbin:$stub/sleepbin:$PATH" REG="$reg" STUB_SLEEPS="$tmp/sleeps" \
    bash "$tmp/rb-harness.sh" 1.3.0-pmx.1 "$tmp/rb-index.json" >"$tmp/rb.out" 2>&1 && echo pass || echo failed
}
heads() { grep -c '^HEAD ' "$reg/log"; }
expect "the tag reads back at once: ok, no wait" pass "$(rb 0)"
expect "  ... one read, no wait" "1 " "$(heads) $(sleeps)"
expect "absent (404) twice right after the PUT, as ghcr.io did for 8.1.3-pmx.1: ok" pass "$(rb 2)"
expect "  ... 3 reads, waits 2 4" "3 2 4" "$(heads) $(sleeps)"
expect "never appears: fails after 6 reads" failed "$(rb 9)"
expect "  ... waits 2 4 8 16 32" "6 2 4 8 16 32" "$(heads) $(sleeps)"
contains "  ... says the tag never appeared" "$tmp/rb.out" "test/provimux:1.3.0-pmx.1 never appeared after the push"
expect "another digest after the PUT: fails at once" failed "$(rb 0 wrong)"
expect "  ... one read, no wait" "1 " "$(heads) $(sleeps)"
contains "  ... names both digests" "$tmp/rb.out" "is sha256:$(printf '%064d' 0) after the push, expected sha256:"
expect "another digest after an absent read: fails at that read" failed "$(rb 1 wrong)"
expect "  ... 2 reads, one wait" "2 2" "$(heads) $(sleeps)"
expect "the registry answers 500 to the read: fails at once" failed "$(rb 0 err)"
expect "  ... one read, no wait" "1 " "$(heads) $(sleeps)"
contains "  ... says HTTP 500" "$tmp/rb.out" "HTTP 500"


# --- T-90: the tap's last moment, the optional plan, tap-check's failure, the owner's order -----
echo "# tap.sh still: GitHub's latest again, right before a tap push"
tlatest '{"tag_name": "v'"$TV"'"}'
expect "latest is still the tag: ok" ok "$(tapsh bash "$here/tap.sh" still "v$TV")"
contains "  ... still=yes" "$tmp/tap.out" "still=yes"
tlatest '{"tag_name": "v1.3.0-pmx.2"}'
expect "a newer release became latest: ok (no push)" ok "$(tapsh bash "$here/tap.sh" still "v$TV")"
contains "  ... still=no" "$tmp/tap.out" "still=no"
contains "  ... superseded, tap left alone" "$tmp/tap.err" "v$TV is superseded by v1.3.0-pmx.2; tap left alone"
tlatest 'HTTP 503'
expect "latest unreadable: refused" refused "$(tapsh bash "$here/tap.sh" still "v$TV")"
tlatest '{"tag_name": "v1.2.0-pmx.1"}'
expect "latest went back to an older release: refused" refused "$(tapsh bash "$here/tap.sh" still "v$TV")"
pwf="$here/../workflows/provimux-publish.yml"
if grep -qF "steps.tap.outputs.action == 'push' && steps.tapstill.outputs.still == 'yes' }}" "$pwf"; then ok "the publish job's Tap push runs only after the token-free re-check said still=yes"; else bad "the publish job's Tap push runs only after the token-free re-check said still=yes"; fi
if grep -qF "steps.tapstill.outcome == 'failure'" "$pwf"; then ok "  ... a failed re-check is a tap failure (the tap issue)"; else bad "  ... a failed re-check is a tap failure (the tap issue)"; fi

echo "# publish.sh (owner): the re-check before the tap push; a failed sync close never skips the tap"
# harness: publish.sh's own hand_tap and close_issues (and its "finish" lines), with a
# fake tap.sh for prepare and a git that only records a push.
pubfn() { awk -v f="$1() {" '$0 == f {p = 1} p {print} p && $0 == "}" {exit}' "$here/publish.sh"; }
fake="$tmp/fakepub"; mkdir -p "$fake/bin"
realgit="$(command -v git)"
# shellcheck disable=SC2016 # expanded by the git wrapper
printf '#!/usr/bin/env bash\nfor a in "$@"; do [ "$a" = push ] && { echo "git $*" >>"$FAKE_LOG"; exit 0; }; done\nexec %q "$@"\n' "$realgit" >"$fake/bin/git"
cat >"$fake/tap.sh" <<'EOF'
#!/usr/bin/env bash
echo "tap.sh $*" >>"$FAKE_LOG"
case "$1" in
  prepare) printf '%b\n' "$FAKE_PREPARE" ;;
  still) exec bash "$REAL_HERE/tap.sh" still "$2" ;;
esac
EOF
cat >"$fake/bin/gh" <<'EOF'
#!/usr/bin/env bash
if [ "$1 $2" = "issue close" ]; then echo "gh: Service Unavailable (HTTP 503)" >&2; exit 1; fi
exec "$STUB_GH" "$@"
EOF
chmod +x "$fake/bin/git" "$fake/bin/gh"
# shellcheck disable=SC2016 # expanded by the harness shell
{ echo 'set -euo pipefail'; printf '. %q\n' "$here/common.sh"; echo 'here="$FAKE_HERE"'
  echo 'die() { echo "publish: $*" >&2; exit 1; }'; echo 'log() { :; }'; echo 'wait_release() { :; }'
  echo "PMX_PMX_RE='^v[0-9]+\\.[0-9]+\\.[0-9]+-pmx\\.[0-9]+\$'"
  pubfn hand_tap; pubfn close_issues; } >"$tmp/pub-harness.sh"
sed -n '/^# >>> finish$/,/^# <<< finish$/p' "$here/publish.sh" >"$tmp/pub-finish.sh"
if [[ "$(grep -c '' "$tmp/pub-finish.sh")" -gt 3 ]]; then ok "publish.sh's finish lines found"; else bad "publish.sh's finish lines found"; fi
pubrun() { # pubrun SCRIPT: runs the harness then SCRIPT; output in $tmp/pub.out, the fake log in $tmp/pub.log
  : >"$tmp/pub.log"
  env PATH="$fake/bin:$stub/bin:$PATH" STUB_GH="$stub/bin/gh" STUB_API="$stub/api" PMX_REPO=test/provimux FAKE_HERE="$fake" REAL_HERE="$here" \
    FAKE_LOG="$tmp/pub.log" FAKE_PREPARE="${FAKE_PREPARE:-action=none}" pmx_tag="v$TV" base=1.3.0 candidate="$pmx_sha" old_main="$pmx_sha" \
    bash -c ". \"$tmp/pub-harness.sh\"; $1" >"$tmp/pub.out" 2>&1 && echo "exit 0" || echo "exit $?"
}
prep="action=push\nrepo=$tmp/no-such.git\nlease=refs/heads/main:$pmx_sha\nref=$pmx_sha:refs/heads/main\nfrom=1.2.0-pmx.1"
tlatest '{"tag_name": "v1.3.0-pmx.2"}'
expect "publish.sh --tap: latest moved after prepare: exit 0" "exit 0" "$(FAKE_PREPARE="$prep" pubrun "hand_tap v$TV")"
lacks "  ... no push" "$tmp/pub.log" "git "
contains "  ... superseded, tap left alone" "$tmp/pub.out" "superseded by v1.3.0-pmx.2; tap left alone"
tlatest '{"tag_name": "v'"$TV"'"}'
expect "publish.sh --tap: latest unchanged: exit 0" "exit 0" "$(FAKE_PREPARE="$prep" pubrun "hand_tap v$TV")"
contains "  ... pushes with the lease on the tap's main" "$tmp/pub.log" "--force-with-lease=refs/heads/main:$pmx_sha"
echo '[{"number": 22, "title": "sync: v1.3.0 — ready to publish"}]' >"$stub/api/repos_test_provimux_issues_state_open_per_page_100.json"
expect "hand publish, closing the sync issue fails (503): still exits 1 at the end" "exit 1" "$(pubrun ". \"$tmp/pub-finish.sh\"")"
contains "  ... the tap was still updated" "$tmp/pub.log" "tap.sh prepare v$TV"
contains "  ... says the close failed" "$tmp/pub.out" "cannot close issue #22"
contains "  ... and that the sync issue stays open" "$tmp/pub.out" "the sync issue of v1.3.0 could not be closed"
echo '[]' >"$stub/api/repos_test_provimux_issues_state_open_per_page_100.json"

echo "# autopublish.sh notify: the tap issue without the optional plan"
expect "tap step failed, the plan artifact missing: released, and the tap issue" "sync: v1.1.0 — released" \
  "$(notify v1.1.0 tap-failed 'publish: the Homebrew tap update was refused (see the log)' PLAN="$tmp/no-such-plan.env")"
contains "  ... the tap issue, for <tag>-pmx.1" "$tmp/notify.out" "tap_title=homebrew tap: v1.1.0-pmx.1 needs the owner"

echo "# provimux-release.yml: a failed tap-check opens the tap issue too"
if inline report; then ok "report lines found in the workflow"; else bad "report lines found in the workflow"; fi
rrep() { # rrep FAILED-JOB [jobs-api-503]: the report step with that job failed (needs.tap-check.result
  # follows it); with a second argument the jobs API answers 503. The gh issue commands in $stub/issue.log
  : >"$stub/issue.log"
  local jobs="$stub/api/repos_test_provimux_actions_runs_66_attempts_1_jobs_per_page_100.json" tcr=success
  if [[ $# -gt 1 ]]; then
    echo "HTTP 503" >"$jobs"
  else
    jq -n --arg j "$1" '{jobs: [{name: "formula", conclusion: "success"}, {name: $j, conclusion: "failure"}]}' >"$jobs"
  fi
  [[ "$1" != tap-check ]] || tcr=failure
  env PATH="$stub/bin:$PATH" STUB_API="$stub/api" GH_REPO=test/provimux TAG="v$TV" SHA="$pmx_sha" RUN_URL=https://example.invalid/run \
    ATTEMPT=1 GITHUB_RUN_ID=66 TAP_CHECK_RESULT="$tcr" bash "$tmp/report.sh" >"$tmp/rrep.out" 2>&1 && echo "exit 0" || echo "exit $?"
}
expect "tap-check failed: the report step runs" "exit 0" "$(rrep tap-check)"
contains "  ... the sync issue" "$stub/issue.log" "issue create -R test/provimux --title sync: v1.3.0 — release v$TV failed, owner needed"
contains "  ... and the tap issue" "$stub/issue.log" "issue create -R test/provimux --title homebrew tap: v$TV needs the owner"
echo '[{"number": 31, "title": "homebrew tap: v'"$TV"' needs the owner"}]' >"$stub/api/repos_test_provimux_issues_state_open_per_page_100.json"
rrep tap-check >/dev/null
contains "tap-check failed, the tap issue open: commented on" "$stub/issue.log" "issue comment 31 "
lacks "  ... never retitled or closed" "$stub/issue.log" "issue edit 31"
echo '[]' >"$stub/api/repos_test_provimux_issues_state_open_per_page_100.json"
expect "another job failed: the report step runs" "exit 0" "$(rrep image-push)"
lacks "  ... no tap issue" "$stub/issue.log" "homebrew tap:"
expect "tap-check failed and the jobs API answers 503: the report step runs" "exit 0" "$(rrep tap-check 503)"
contains "  ... the sync issue, without the job list" "$stub/issue.log" "issue create -R test/provimux --title sync: v1.3.0 — release v$TV failed, owner needed"
contains "  ... and still the tap issue (from needs.tap-check.result)" "$stub/issue.log" "issue create -R test/provimux --title homebrew tap: v$TV needs the owner"
expect "another job failed and the jobs API answers 503: the report step runs" "exit 0" "$(rrep image-push 503)"
lacks "  ... no tap issue" "$stub/issue.log" "homebrew tap:"
# --- the README commit (design/root-readme.md) ------------------------------------------------------
# ru: an upstream with README.md, README_CN.md, README_JA.md (long enough that a rewrite
# unpairs the README commit in a plain range-diff), LICENSE, code.go, patch.txt at v1.0.0,
# and one tag per case, each a commit on v1.0.0.
# rp: the stack on v1.0.0: an owner patch, a layer commit (it also carries guard.sh, for
# publish.sh), the README commit, a layer commit that edits the pointer, a layer commit.
echo "# README commit: fixture"
ru="$tmp/ru"
rtext() { local i; for i in $(seq 1 40); do echo "$1 line $i, see https://example.invalid/?ref=$1$i"; done; }
git init -q -b main "$ru"
(
  cd "$ru"
  rtext en >README.md; rtext cn >README_CN.md; rtext ja >README_JA.md
  echo 'MIT' >LICENSE; echo 'package x' >code.go; seq 1 10 | sed 's/^/p0 /' >patch.txt
  git add -A && git commit -q -m v1 && git tag v1.0.0
  # tag NAME MESSAGE SHELL: a commit on v1.0.0 made by SHELL, tagged NAME.
  t() { git checkout -q --detach v1.0.0; eval "$3"; git add -A; git commit -q -m "$2"; git tag "$1"; }
  t v1.1.0 'edit the READMEs' 'sed -i.bak "5s/line/LINE/" README.md README_CN.md README_JA.md; rm -f ./*.bak'
  t v1.2.0 'rewrite the READMEs' 'rtext new >README.md; rtext newcn >README_CN.md; git rm -q README_JA.md'
  t v1.3.0 'code only' 'echo "// more" >>code.go'
  t v1.4.0 'READMEs and layer2.txt' 'sed -i.bak "5s/line/LINE/" README.md README_CN.md README_JA.md; rm -f ./*.bak; seq 1 10 | sed "s/^/up /" >layer2.txt'
  t v1.5.0 'move README_CN.md' 'mkdir docs; git mv README_CN.md docs/README_CN.md'
  t v1.6.0 'READMEs and README_KO.md' 'sed -i.bak "5s/line/LINE/" README.md README_CN.md README_JA.md; rm -f ./*.bak; rtext ko >README_KO.md'
  t v1.7.0 'the owner patch upstream, otherwise' 'sed -i.bak "5s/.*/upstream/" patch.txt; rm -f patch.txt.bak'
  git checkout -q main
)
rp="$tmp/rp"
git clone -q "$ru" "$rp"
pointer='# Provimux

A pointer (fixture).
Its README is .github/README.md.
'
(
  cd "$rp"
  git checkout -q --detach v1.0.0
  sed -i.bak '5s/.*/p1/' patch.txt && rm -f patch.txt.bak && git commit -q -am 'owner patch'
  mkdir -p .github/provimux && cp "$here/guard.sh" .github/provimux/guard.sh && echo l1 >layer1.txt
  git add -A && git commit -q -m 'layer one' -m 'Provimux-Layer: branding'
  printf '%s' "$pointer" >README.md && git rm -q README_CN.md README_JA.md
  git add -A && git commit -q -m 'docs(provimux): root README points to the Provimux README' -m 'Provimux-Layer: branding
Provimux-Readme: root'
  sed -i.bak '1s/.*/# Provimux, edited/' README.md && rm -f README.md.bak
  git commit -q -am 'edit the pointer' -m 'Provimux-Layer: branding'
  seq 1 10 | sed 's/^/l2 /' >layer2.txt && git add -A && git commit -q -m 'layer two' -m 'Provimux-Layer: branding'
  git branch -q -f main HEAD && git checkout -q main
)
rg() { git -C "$rp" "$@"; }
OB="$(rg rev-parse 'v1.0.0^{commit}')"
OM="$(rg rev-parse main)"
P="$(rg rev-parse main~4)" L1="$(rg rev-parse main~3)" R="$(rg rev-parse main~2)" E="$(rg rev-parse main~1)" L2="$OM"
up_c() { rg rev-parse "$1^{commit}"; }

echo "# README commit: check.sh prepare (rebase, resolver, normal form, guard)"
# prep TAG [OLD_MAIN]: check.sh prepare from a state "check", in $tmp/prep-TAG; prints the state's status.
prep() {
  local t=$1 m=${2:-$OM} d="$tmp/prep-$1"
  rm -rf "$d"; mkdir -p "$d"
  jq -n --arg t "$t" --arg u "$(up_c "$t")" --arg m "$m" --arg ob "$OB" '{status: "check", step: "select", tag: $t,
    base: ($t | ltrimstr("v")), upstream_commit: $u, old_main: $m, old_base_tag: "v1.0.0", old_base_commit: $ob,
    candidate: "", commits: "", guard_ok: ""}' >"$d/state.json"
  (cd "$rp" && PATH="${PREP_PATH:-}$PATH" PMX_UPSTREAM_URL="$ru" PMX_UPSTREAM_REPO=test/up PMX_CHECK_OUT="$d" bash "$here/check.sh" prepare) >"$d/prep.log" 2>&1 || true
  jq -r .status "$d/state.json"
}
pj() { jq -r "$2" "$tmp/prep-$1/$3"; }
# ops FILE: the header lines of a range-diff as "<op> <old> <new>" (the parsers' pattern), in order.
ops() { sed -nE 's/^ *([0-9]+|-+): +([0-9a-f]+|-+) ([=!<>]) +([0-9]+|-+): +([0-9a-f]+|-+) .*$/\3 \2 \5/p' "$1"; }
allsame() { if [[ -n "$(ops "$1")" && -z "$(ops "$1" | grep -v '^=' || true)" ]]; then echo yes; else echo no; fi; }

expect "upstream edits all three READMEs: status test" test "$(prep v1.1.0)"
expect "  ... rebase ok" true "$(pj v1.1.0 .ok rebase.json)"
expect "  ... resolved at the README commit" "$R" "$(pj v1.1.0 .readme.commit rebase.json)"
expect "  ... three paths conflicted" "README.md README_CN.md README_JA.md" "$(pj v1.1.0 '.readme.conflicts | sort | join(" ")' rebase.json)"
expect "  ... readme: R -> R', reapplied" "$R true" "$(pj v1.1.0 '"\(.old) \(.reapplied)"' readme.json)"
expect "  ... result.json carries it" "$R" "$(pj v1.1.0 .readme.old result.json)"
expect "  ... every commit of the segments =" yes "$(allsame "$tmp/prep-v1.1.0/logs/range-diff.txt")"
contains "  ... the README line" "$tmp/prep-v1.1.0/logs/range-diff.txt" "README  $R -> "
expect "  ... guard ok" true "$(pj v1.1.0 .guard_ok state.json)"
expect "  ... no reason" "" "$(cat "$tmp/prep-v1.1.0/reasons.txt")"
c1="$(pj v1.1.0 .candidate state.json)"
prep v1.1.0 >/dev/null
expect "  ... the same candidate on a second run" "$c1" "$(pj v1.1.0 .candidate state.json)"
rg fetch -q "$tmp/prep-v1.1.0/candidate.bundle" "+refs/pmx-check/candidate:refs/t/c1"
for p in README_CN.md README_JA.md; do
  if [[ -z "$(rg ls-tree "$c1" -- "$p")" ]]; then ok "  ... $p absent from the candidate"; else bad "  ... $p absent from the candidate"; fi
done
expect "  ... the candidate's README.md is the old main's" "$(rg rev-parse "$OM:README.md")" "$(rg rev-parse "$c1:README.md")"
expect "  ... R' keeps R's author and message" "$(rg log -1 --format='%an %ae %ad%n%B' "$R")" "$(rg log -1 --format='%an %ae %ad%n%B' "$c1~2")"

expect "upstream rewrites README.md and README_CN.md, deletes README_JA.md: status test" test "$(prep v1.2.0)"
expect "  ... resolved at the README commit" "$R" "$(pj v1.2.0 .readme.commit rebase.json)"
expect "  ... README.md and README_CN.md conflicted" "README.md README_CN.md" "$(pj v1.2.0 '.readme.conflicts | sort | join(" ")' rebase.json)"
expect "  ... every commit of the segments =" yes "$(allsame "$tmp/prep-v1.2.0/logs/range-diff.txt")"
c2="$(pj v1.2.0 .candidate state.json)"
rg fetch -q "$tmp/prep-v1.2.0/candidate.bundle" "+refs/pmx-check/candidate:refs/t/c2"
rg range-diff --no-color --no-notes "$OB..$OM" "$(up_c v1.2.0)..$c2" >"$tmp/plain-v1.2.0.txt"
contains "  ... a plain whole-range range-diff drops the README commit (<)" "$tmp/plain-v1.2.0.txt" "$(rg rev-parse --short "$R") < -:"
contains "  ... and shows R' as not in the old main (>)" "$tmp/plain-v1.2.0.txt" "> 3:  $(rg rev-parse --short "$c2~2")"

# T-115: the header parser tolerates grep's "no match" (1) only; a read error (2) is an error.
mkdir -p "$tmp/fgrepbin"
# shellcheck disable=SC2016 # the wrapper's own text
printf '#!/usr/bin/env bash\nfor a in "$@"; do case "$a" in */logs/range-diff.txt) echo "grep: injected read error" >&2; exit 2 ;; esac; done\nexec %s "$@"\n' "$(command -v grep)" >"$tmp/fgrepbin/grep"
chmod +x "$tmp/fgrepbin/grep"
expect "T-115: grep cannot read logs/range-diff.txt (exit 2): error, not an empty list" error "$(PREP_PATH="$tmp/fgrepbin:" prep v1.1.0)"
contains "  ... says so" "$tmp/prep-v1.1.0/reasons.txt" "cannot read the range-diff"
prep v1.1.0 >/dev/null
expect "upstream changes no README: status test" test "$(prep v1.3.0)"
expect "  ... no stop recorded" "null" "$(pj v1.3.0 .readme rebase.json)"
expect "  ... readme: not reapplied" false "$(pj v1.3.0 .reapplied readme.json)"
expect "  ... every commit =" yes "$(allsame "$tmp/prep-v1.3.0/logs/range-diff.txt")"
rg fetch -q "$tmp/prep-v1.3.0/candidate.bundle" "+refs/pmx-check/candidate:refs/t/c3"

expect "upstream edits the READMEs and a file a later layer commit changes: fail" fail "$(prep v1.4.0)"
expect "  ... stopped at that later commit" "$L2" "$(pj v1.4.0 .stopped_at rebase.json)"
expect "  ... after resolving the README commit once" "$R" "$(pj v1.4.0 .readme.commit rebase.json)"
expect "upstream renames README_CN.md to docs/README_CN.md: fail" fail "$(prep v1.5.0)"
expect "  ... stopped at the README commit" "$R" "$(pj v1.5.0 .stopped_at rebase.json)"
expect "  ... conflict docs/README_CN.md" "docs/README_CN.md" "$(pj v1.5.0 '.conflicts | join(" ")' rebase.json)"
expect "  ... the resolver did not act" null "$(pj v1.5.0 .readme rebase.json)"
expect "upstream edits the READMEs and adds README_KO.md: the guard fails" false "$(prep v1.6.0 >/dev/null; pj v1.6.0 .guard_ok state.json)"
contains "  ... naming README_KO.md" "$tmp/prep-v1.6.0/logs/guard.log" "root entry README_KO.md"
contains "  ... a reason" "$tmp/prep-v1.6.0/reasons.txt" "the guard rejects the candidate"
expect "upstream changes a file an owner patch changes (no README): fail, as before" fail "$(prep v1.7.0)"
expect "  ... stopped at the owner patch" "$P" "$(pj v1.7.0 .stopped_at rebase.json)"
expect "  ... the resolver did not act" null "$(pj v1.7.0 .readme rebase.json)"

echo "# README commit: stack_range_diff and readme_normal"
# shellcheck source=common.sh
. "$here/common.sh"
bw="$tmp/bw"
rg worktree add -q --detach "$bw" "$OB"
bg() { git -C "$bw" "$@"; }
# build BASE STEP...: a candidate on BASE in the worktree $bw; prints its tip. A STEP is
# "pick:<commit>" (cherry-pick), "readme:<commit>" (that commit's README paths, committed
# with its author and message: git commit -C), or a shell command run in $bw.
build() {
  local s c p
  # Forced: a commit with both README.md and readme.md leaves a case-insensitive work tree dirty.
  bg checkout -q -f --detach "$1"; bg clean -q -f -d; shift
  for s in "$@"; do
    case "$s" in
      pick:*) bg cherry-pick --allow-empty "${s#pick:}" >/dev/null ;;
      readme:*)
        c="${s#readme:}"
        for p in "${PMX_README_PATHS[@]}"; do
          if [[ -n "$(readme_entry "$c" "$p" git -C "$rp")" ]]; then bg checkout -q "$c" -- "$p"; else bg rm -q -f --ignore-unmatch -- "$p" >/dev/null; fi
        done
        bg commit -q --allow-empty -C "$c" ;;
      *) (cd "$bw" && eval "$s") ;;
    esac
  done
  bg rev-parse HEAD
}
N3="$(up_c v1.3.0)"
# srd OLD_BASE OLD_MAIN NEW_BASE CANDIDATE: "ok <R> <R'>" or "fail"; the range-diff in $tmp/srd.txt, stderr in $tmp/srd.err
srd() { local o; if o="$(stack_range_diff "$1" "$2" "$3" "$4" "$tmp/srd.txt" git -C "$rp" 2>"$tmp/srd.err")"; then echo "ok${o:+ $o}"; else echo fail; fi; }
# lists: "<op>:<full sha>" of every non-= header line of $tmp/srd.txt, space-separated
lists() { ops "$tmp/srd.txt" | while read -r o a b; do case "$o" in '<' | '!') echo "$o:$(rg rev-parse "$a")" ;; '>') echo "$o:$(rg rev-parse "$b")" ;; esac; done | paste -sd ' ' -; }
# rn OLD NEW OLD_TIP NEW_TIP: readme_normal, "ok" or the condition's number
rn() { if readme_normal "$@" git -C "$rp" 2>"$tmp/rn.err"; then echo ok; else sed -n 's/^README rule, condition \([0-9]\).*/\1/p' "$tmp/rn.err" | head -n 1; fi; }

good="$(build "$N3" pick:"$P" pick:"$L1" pick:"$R" pick:"$E" pick:"$L2")"
expect "the re-applied stack: normal form" ok "$(rn "$R" "$good~2" "$OM" "$good")"
expect "  ... stack_range_diff passes with the pair" "ok $R $(rg rev-parse "$good~2")" "$(srd "$OB" "$OM" "$N3" "$good")"
expect "  ... no list entry" "" "$(lists)"
# No README commit on either side: exactly git range-diff of the whole ranges.
nr_old="$(rg rev-parse main~3)"; nr_new="$(build "$N3" pick:"$P" pick:"$L1")"
srd "$OB" "$nr_old" "$N3" "$nr_new" >/dev/null
rg range-diff --no-color --no-notes "$OB..$nr_old" "$N3..$nr_new" >"$tmp/plain.txt"
if command cmp -s "$tmp/plain.txt" "$tmp/srd.txt" && [[ -s "$tmp/plain.txt" ]]; then ok "no README commit on either side: git range-diff's output, byte for byte"; else bad "no README commit on either side: git range-diff's output, byte for byte"; fi
expect "  ... and no pair" "" "$(stack_range_diff "$OB" "$nr_old" "$N3" "$nr_new" "$tmp/x.txt" git -C "$rp")"

lic="$(build "$N3" pick:"$P" pick:"$L1" pick:"$R" 'echo GPL >LICENSE && git commit -q -a --amend -C HEAD' pick:"$E" pick:"$L2")"
expect "R' also changes LICENSE: condition 4" 4 "$(rn "$R" "$lic~2" "$OM" "$lic")"
expect "  ... stack_range_diff fails" fail "$(srd "$OB" "$OM" "$N3" "$lic")"
cnk="$(build "$N3" pick:"$P" pick:"$L1" pick:"$R" "git checkout -q $OB -- README_CN.md && git commit -q --amend -C HEAD" pick:"$E" pick:"$L2" 'git rm -q README_CN.md && git commit -q -m "rm CN"')"
expect "R' leaves README_CN.md in place (the tip removes it): condition 3 alone" 3 "$(rn "$R" "$cnk~3" "$OM" "$cnk")"
# shellcheck disable=SC2016 # run by build in the work tree
oth="$(build "$N3" pick:"$P" pick:"$L1" pick:"$R" 'echo extra >>README.md && git commit -q -a --amend -C HEAD' pick:"$E" pick:"$L2" 'sed -i.bak "\$d" README.md && rm README.md.bak && git commit -q -am "drop extra"')"
expect "R' has another README.md (the tip restores it): condition 3 alone" 3 "$(rn "$R" "$oth~3" "$OM" "$oth")"
expect "  ... the tip's README.md is the old main's (so only 3 fails)" "$(rg rev-parse "$OM:README.md")" "$(rg rev-parse "$oth:README.md")"
msg="$(build "$N3" pick:"$P" pick:"$L1" pick:"$R" 'git commit -q --amend -m "docs(provimux): another message" -m "Provimux-Layer: branding
Provimux-Readme: root"' pick:"$E" pick:"$L2")"
expect "R' has another message: condition 2" 2 "$(rn "$R" "$msg~2" "$OM" "$msg")"
contains "  ... named: message" "$tmp/rn.err" "another message"
aut="$(build "$N3" pick:"$P" pick:"$L1" pick:"$R" 'git commit -q --amend --no-edit --author="x <x@example.com>"' pick:"$E" pick:"$L2")"
expect "R' has another author: condition 2" 2 "$(rn "$R" "$aut~2" "$OM" "$aut")"
contains "  ... named: author" "$tmp/rn.err" "another author"
two="$(build "$N3" pick:"$P" pick:"$L1" pick:"$R")"
twom="$(cd "$bw" && GIT_AUTHOR_NAME="$(git log -1 --format=%an "$R")" GIT_AUTHOR_EMAIL="$(git log -1 --format=%ae "$R")" GIT_AUTHOR_DATE="$(git log -1 --format=%ad --date=raw "$R")" \
  git commit-tree "$two^{tree}" -p "$two^" -p "$OB" -m "$(git log -1 --format=%B "$R")")"
# The message through -m loses nothing here but its exact bytes; condition 1 is checked first.
expect "R' has two parents: condition 1" 1 "$(rn "$R" "$twom" "$OM" "$twom")"
twot="$(build "$twom" pick:"$E" pick:"$L2")"
expect "  ... stack_range_diff fails (a merge in the range)" fail "$(srd "$OB" "$OM" "$N3" "$twot")"
tip5="$(build "$N3" pick:"$P" pick:"$L1" pick:"$R" pick:"$E" 'echo other >>README.md && git commit -q -a --amend -C HEAD' pick:"$L2")"
expect "the tip has another README.md through a commit above R': condition 5" 5 "$(rn "$R" "$tip5~2" "$OM" "$tip5")"
expect "  ... stack_range_diff fails" fail "$(srd "$OB" "$OM" "$N3" "$tip5")"
expect "  ... and that commit is ! in its segment" "!:$E" "$(lists)"
dbl="$(build "$N3" pick:"$P" pick:"$L1" pick:"$R" pick:"$E" pick:"$L2" 'echo z >z.txt && git add z.txt && git commit -q -m "another" -m "Provimux-Layer: branding
Provimux-Readme: root"')"
expect "two README commits in the candidate: fails" fail "$(srd "$OB" "$OM" "$N3" "$dbl")"
contains "  ... says more than one" "$tmp/srd.err" "more than one README commit"
trl="$(build "$N3" pick:"$P" pick:"$L1" pick:"$R" pick:"$E" pick:"$L2" 'git commit -q --amend -m "layer two" -m "Provimux-Layer: branding
Provimux-Readme: root"')"
expect "the trailer on another commit: fails" fail "$(srd "$OB" "$OM" "$N3" "$trl")"
nor="$(build "$N3" pick:"$P" pick:"$L1" pick:"$L2")"
expect "the candidate without the README commit: no pair (a whole-range range-diff)" "ok" "$(srd "$OB" "$OM" "$N3" "$nor")"
contains "  ... R is dropped" "$tmp/srd.txt" "$(rg rev-parse --short "$R") < -:"
belo="$(build "$N3" pick:"$P" 'echo p2 >>patch.txt && git commit -q -a --amend -C HEAD' pick:"$L1" pick:"$R" pick:"$E" pick:"$L2")"
srd "$OB" "$OM" "$N3" "$belo" >/dev/null
expect "a commit below R changed: it is changed, R is not" "!:$P" "$(lists)"
abov="$(build "$N3" pick:"$P" pick:"$L1" pick:"$R" pick:"$E" pick:"$L2" 'echo l2x >>layer2.txt && git commit -q -a --amend -C HEAD')"
srd "$OB" "$OM" "$N3" "$abov" >/dev/null
expect "a commit above R changed: it is changed, R is not" "!:$L2" "$(lists)"
drp="$(build "$N3" pick:"$P" pick:"$L1" pick:"$R" pick:"$E")"
srd "$OB" "$OM" "$N3" "$drp" >/dev/null
expect "a commit above R dropped: it is dropped" "<:$L2" "$(lists)"
ext="$(build "$N3" pick:"$P" pick:"$L1" pick:"$R" pick:"$E" pick:"$L2" 'echo x >x.txt && git add x.txt && git commit -q -m extra -m "Provimux-Layer: branding"')"
srd "$OB" "$OM" "$N3" "$ext" >/dev/null
expect "an extra commit in the candidate: it is added" ">:$ext" "$(lists)"
mov="$(build "$N3" pick:"$L1" pick:"$R" pick:"$E" pick:"$L2" pick:"$P")"
srd "$OB" "$OM" "$N3" "$mov" >/dev/null
expect "a commit moved from below R to above it: dropped below, added above" "<:$P >:$mov" "$(lists)"
# Empty segments: the README commit first above the base, and at the tip.
r0="$(build "$OB" pick:"$R")"; f_old="$(build "$r0" pick:"$L2")"
f_new="$(build "$N3" pick:"$R" pick:"$L2")"
expect "the README commit first above the base (both segments below empty): passes" "ok $r0 $(rg rev-parse "$f_new^")" "$(srd "$OB" "$f_old" "$N3" "$f_new")"
expect "  ... nothing listed" "" "$(lists)"
f_new2="$(build "$N3" 'echo y >y.txt && git add y.txt && git commit -q -m added-below' pick:"$R" pick:"$L2")"
srd "$OB" "$f_old" "$N3" "$f_new2" >/dev/null
expect "  ... one side empty: its commit is listed (>)" ">:$(rg rev-parse "$f_new2~2")" "$(lists)"
t_old="$(build "$OB" pick:"$P" pick:"$R")"; t_new="$(build "$N3" pick:"$P" pick:"$R")"
expect "the README commit at the tip (both segments above empty): passes" "ok $t_old $t_new" "$(srd "$OB" "$t_old" "$N3" "$t_new")"
t_new2="$(build "$N3" pick:"$P" pick:"$R" 'echo y >y.txt && git add y.txt && git commit -q -m added-above')"
srd "$OB" "$t_old" "$N3" "$t_new2" >/dev/null
expect "  ... one side empty: its commit is listed (>)" ">:$t_new2" "$(lists)"
t_new3="$(build "$N3" pick:"$R")"
srd "$OB" "$t_old" "$N3" "$t_new3" >/dev/null
expect "  ... the other side empty: the old commit is listed (<)" "<:$(rg rev-parse "$t_old~1")" "$(lists)"
# Names are whole and NUL-delimited.
r=''
for n in README.md.bak docs/README.md $'README.md\n' readme.md ' README.md'; do in_readme_set "$n" && r="$r [$n]"; done
expect "README.md.bak, docs/README.md, a name with a newline, readme.md: not paths of the set" "" "$r"
dir="$(build "$N3" pick:"$P" pick:"$L1" pick:"$R" 'mkdir -p docs && echo d >docs/README.md && git add docs && git commit -q --amend -C HEAD' pick:"$E" pick:"$L2")"
expect "R' also changes docs/README.md: condition 4" 4 "$(rn "$R" "$dir~2" "$OM" "$dir")"
nlc="$(build "$N3" pick:"$P" pick:"$L1" pick:"$R" 'printf x >"README.md
" && git add -A && git commit -q --amend -C HEAD' pick:"$E" pick:"$L2")"
expect "R' also adds a file named README.md<newline>: condition 4" 4 "$(rn "$R" "$nlc~2" "$OM" "$nlc")"

echo "# README commit: T-113, a failing git and history the old checks missed"
# fgit ARGS...: git -C $rp, except that git range-diff fails (status 128) when one of its
# arguments is $FAIL_RD.
realgit="$(command -v git)"
fgit() {
  local a
  if [[ "$1" == range-diff ]]; then for a in "$@"; do [[ "$a" == "${FAIL_RD:-}" ]] && { echo "fatal: injected" >&2; return 128; }; done; fi
  "$realgit" -C "$rp" "$@"
}
etree="$(rg hash-object -t tree -w /dev/null)"
# mkreadme PARENT TREE: a commit of TREE on PARENT with R's author line and message.
mkreadme() {
  (cd "$bw" && GIT_AUTHOR_NAME="$(git log -1 --format=%an "$R")" GIT_AUTHOR_EMAIL="$(git log -1 --format=%ae "$R")" \
    GIT_AUTHOR_DATE="$(git log -1 --format=%ad --date=raw "$R")" git commit-tree "$2" -p "$1" -F <(git cat-file commit "$R" | sed '1,/^$/d'))
}
# treewith TREE LINE...: TREE with the root entries given (ls-tree lines) added or replacing those of that name.
treewith() {
  local t=$1 l n x; shift
  { rg ls-tree "$t" | while IFS= read -r l; do
      n="${l#*$'\t'}"; for x in "$@"; do [[ "${x#*$'\t'}" == "$n" ]] && continue 2; done; printf '%s\n' "$l"; done
    printf '%s\n' "$@"; } | rg mktree
}
fsrd() { local o; if o="$(stack_range_diff "$1" "$2" "$3" "$4" "$tmp/srd.txt" fgit 2>"$tmp/srd.err")"; then echo "ok${o:+ $o}"; else echo fail; fi; }
expect "T-113 1: git range-diff of the segment below the README commit fails: fails" fail "$(FAIL_RD="$OB..$R^" fsrd "$OB" "$OM" "$N3" "$good")"
contains "  ... for that reason" "$tmp/srd.err" "segment below the README commit failed"
expect "T-113 1: git range-diff of the segment above fails: fails" fail "$(FAIL_RD="$R..$OM" fsrd "$OB" "$OM" "$N3" "$good")"
contains "  ... for that reason" "$tmp/srd.err" "segment above the README commit failed"
expect "T-113 1: git range-diff of the whole ranges fails (no README commit): fails" fail "$(FAIL_RD="$OB..$nr_old" fsrd "$OB" "$nr_old" "$N3" "$nr_new")"
contains "  ... for that reason" "$tmp/srd.err" "git range-diff of"
expect "  ... and with no failure the same calls pass" "ok $R $(rg rev-parse "$good~2")" "$(FAIL_RD=none fsrd "$OB" "$OM" "$N3" "$good")"
# Blocker 2: R' adds an empty directory (a root tree entry outside the set); the tip keeps
# the old main's README.md, so only condition 4 can see it.
ex_tree="$(treewith "$(rg rev-parse "$good~2^{tree}")" "040000 tree $etree	emptydir")"
ex="$(mkreadme "$good~3" "$ex_tree")"
ex_tip="$(cd "$bw" && git commit-tree "$(treewith "$ex_tree" "$(rg ls-tree "$OM" README.md)")" -p "$ex" -m tip)"
if [[ "$(rg diff-tree -r --no-renames -z --name-only --no-commit-id "$ex^" "$ex" | tr '\0' ' ')" == "README.md README_CN.md README_JA.md " ]]; then ok "  (a recursive diff-tree of that commit names only the three READMEs)"; else bad "  (a recursive diff-tree of that commit names only the three READMEs)"; fi
expect "T-113 2: R' adds an empty directory outside the set: condition 4" 4 "$(rn "$R" "$ex" "$OM" "$ex_tip")"
dx_par="$(build "$good~3" 'git rm -q README_CN.md && mkdir README_CN.md && echo x >README_CN.md/x && git add -A && git commit -q -m dir')"
dx="$(mkreadme "$dx_par" "$(rg rev-parse "$good~2^{tree}")")"
if (readme_outside "$dx_par" "$dx" git -C "$rp"); then bad "T-113 2: a directory at README_CN.md in the parent: readme_outside refuses"; else ok "T-113 2: a directory at README_CN.md in the parent: readme_outside refuses"; fi
if (readme_outside "$good~3" "$good~2" git -C "$rp"); then ok "  ... and accepts the re-applied commit"; else bad "  ... and accepts the re-applied commit"; fi
if (readme_outside "$good~3" "$OB" git -C "$rp" 2>/dev/null; [[ $? -eq 1 ]]) && ! (readme_entry 0000000000000000000000000000000000000001 README.md git -C "$rp" >/dev/null 2>&1); then
  ok "T-113 2: an unreadable commit is a failure of readme_entry, not an empty entry"; else bad "T-113 2: an unreadable commit is a failure of readme_entry, not an empty entry"; fi
# Blocker 3: the old stack has a merge that the new base already contains. One rev-list
# over both ranges would exclude it; each range on its own does not.
ms="$(build "$OB" 'echo s >side.txt && git add side.txt && git commit -q -m side')"
mg="$(build "$(build "$OB" pick:"$P")" "git merge -q --no-ff -m merge $ms")"
om3="$(build "$mg" pick:"$L1" pick:"$R" pick:"$E" pick:"$L2")"
nb3="$(build "$mg" 'echo up >up.txt && git add up.txt && git commit -q -m upstream')"
cand3="$(build "$nb3" pick:"$om3~3" pick:"$om3~2" pick:"$om3~1" pick:"$om3")"
expect "  (git rev-list --merges of both ranges at once lists nothing)" "" "$(rg rev-list --merges "$OB..$om3" "$nb3..$cand3")"
expect "T-113 3: an old-stack merge the new base contains: fails" fail "$(srd "$OB" "$om3" "$nb3" "$cand3")"
contains "  ... says which range" "$tmp/srd.err" "has a merge commit"

echo "# README commit: the resolver refuses outside its conditions (check.sh prepare)"
omlic="$(build "$OB" pick:"$P" pick:"$L1" pick:"$R" 'echo GPL >LICENSE && git commit -q -a --amend -C HEAD' pick:"$E" pick:"$L2")"
expect "the old main's README commit also changes LICENSE: fail" fail "$(prep v1.1.0 "$omlic")"
expect "  ... stopped at it" "$(rg rev-parse "$omlic~2")" "$(pj v1.1.0 .stopped_at rebase.json)"
expect "  ... the resolver did not act" null "$(pj v1.1.0 .readme rebase.json)"
om2="$(build "$OM" 'echo z >z.txt && git add z.txt && git commit -q -m another -m "Provimux-Layer: branding
Provimux-Readme: root"')"
expect "the old main has two README commits: fail" fail "$(prep v1.1.0 "$om2")"
expect "  ... stopped at the first" "$R" "$(pj v1.1.0 .stopped_at rebase.json)"
expect "  ... the resolver did not act" null "$(pj v1.1.0 .readme rebase.json)"
prep v1.1.0 >/dev/null

echo "# README commit: structure"
r="$(grep -lE 'range-diff --' "$here"/*.sh | sed 's|.*/||' | grep -vx 'selftest.sh' | paste -sd ' ' -)"
expect "git range-diff runs in common.sh only" "common.sh" "$r"
expect "guard.sh's set is PMX_README_PATHS" "$(grep -E '^PMX_README_PATHS=' "$here/common.sh" | sed 's/^[^=]*=//')" \
  "$(grep -E '^readme_paths=' "$here/guard.sh" | sed 's/^[^=]*=//')"
if grep -qE '^require_readme=1$' "$here/guard.sh"; then ok "guard.sh requires the README commit (require_readme=1)"; else bad "guard.sh requires the README commit (require_readme=1)"; fi

echo "# README commit: guard.sh rule 5 (on git objects only)"
gbare="$tmp/rguard.git"
git init -q --bare "$gbare"
(cd "$rp/.git/objects" && pwd) >"$gbare/objects/info/alternates"
# Both values of the switch, explicitly, whatever the shipped guard.sh says.
sed 's/^require_readme=[01]$/require_readme=0/' "$here/guard.sh" >"$tmp/guard-req0.sh"
sed 's/^require_readme=[01]$/require_readme=1/' "$here/guard.sh" >"$tmp/guard-req.sh"
# gd COMMIT [GUARD]: the guard on a bare repository whose HEAD is COMMIT; pass | fail, output in $tmp/gd.out
gd() {
  git --git-dir="$gbare" update-ref --no-deref HEAD "$1"
  (cd "$tmp" && GIT_DIR="$gbare" PMX_UPSTREAM="$ru" bash "${2:-$here/guard.sh}") >"$tmp/gd.out" 2>&1 && echo pass || echo fail
}
expect "the stack with the README commit: passes" pass "$(gd "$OM")"
expect "  ... also with require_readme=1" pass "$(gd "$OM" "$tmp/guard-req.sh")"
expect "two README commits: fails" fail "$(gd "$(build "$OM" 'echo z >z.txt && git add z.txt && git commit -q -m another -m "Provimux-Layer: branding
Provimux-Readme: root"')")"
contains "  ... says so" "$tmp/gd.out" "There are 2 README commits"
# shellcheck disable=SC2016 # run by build in the work tree
nlt="$(build "$OB" pick:"$P" pick:"$L1" 'printf "%s" "$pointer" >README.md; git rm -q README_CN.md README_JA.md; git add -A; git commit -q -m readme -m "Provimux-Readme: root"' pick:"$E" pick:"$L2")"
expect "a README commit without the layer trailer: fails" fail "$(gd "$nlt")"
contains "  ... says so" "$tmp/gd.out" "has no 'Provimux-Layer: branding' trailer."
expect "a README commit that also changes LICENSE: fails" fail "$(gd "$(build "$OB" pick:"$P" pick:"$L1" pick:"$R" 'echo GPL >LICENSE && git commit -q -a --amend -C HEAD' pick:"$E" pick:"$L2")")"
contains "  ... names LICENSE" "$tmp/gd.out" "changes the root entries LICENSE"
expect "a README commit that keeps README_JA.md: fails" fail "$(gd "$(build "$OB" pick:"$P" pick:"$L1" pick:"$R" "git checkout -q $OB -- README_JA.md && git commit -q --amend -C HEAD" pick:"$E" pick:"$L2")")"
contains "  ... says absent" "$tmp/gd.out" "must leave README_JA.md absent"
for n in README_KO.md readme.md README README.md.bak; do
  # By the index, not the file system (macOS's is case-insensitive: readme.md would be README.md).
  expect "a root $n at HEAD: fails" fail "$(gd "$(build "$OM" "git update-index --add --cacheinfo 100644,\$(echo k | git hash-object -w --stdin),'$n' && git commit -q -m 'add $n' -m 'Provimux-Layer: branding'")")"
  contains "  ... names it" "$tmp/gd.out" "root entry $n;"
done
expect "README.md a symlink at HEAD: fails" fail "$(gd "$(build "$OM" 'rm README.md && ln -s LICENSE README.md && git add -A && git commit -q -m link -m "Provimux-Layer: branding"')")"
contains "  ... says so" "$tmp/gd.out" "README.md at HEAD must be a regular file"
expect "a README commit that leaves README.md a symlink: fails" fail "$(gd "$(build "$OB" pick:"$P" pick:"$L1" pick:"$R" 'rm README.md && ln -s LICENSE README.md && git add -A && git commit -q --amend -C HEAD')")"
# T-113, blocker 2: an extra root tree entry outside the set that a recursive diff-tree
# does not show (an empty directory), and a directory at a path of the set.
gb="$(build "$OB" pick:"$P" pick:"$L1")"
rx_tree="$(treewith "$(rg rev-parse "$R^{tree}")" "040000 tree $etree	emptydir")"
rx="$(mkreadme "$gb" "$rx_tree")"
expect "T-113: a README commit that adds an empty directory (recursive diff-tree shows nothing): fails" fail "$(gd "$rx")"
contains "  ... names it" "$tmp/gd.out" "changes the root entries emptydir"
gdir="$(build "$gb" 'git rm -q README_CN.md && mkdir README_CN.md && echo x >README_CN.md/x && git add -A && git commit -q -m dir -m "Provimux-Layer: branding"')"
rdir_tree="$(rg ls-tree "$gdir^{tree}" | grep -v -E $'\t''(README_CN.md|README_JA.md|README.md)$' | { cat; rg ls-tree "$R^{tree}" | grep -E $'\t''README.md$'; } | rg mktree)"
expect "T-113: a README commit whose parent has a directory at README_CN.md (it removes it): fails" fail "$(gd "$(mkreadme "$gdir" "$rdir_tree")")"
contains "  ... says so" "$tmp/gd.out" "the parent has a directory at README_CN.md"
nor_stack="$(rg rev-parse main~3)"
expect "no README commit, require_readme=0: passes" pass "$(gd "$nor_stack" "$tmp/guard-req0.sh")"
expect "no README commit, require_readme=1: fails" fail "$(gd "$nor_stack" "$tmp/guard-req.sh")"
expect "no README commit, the shipped guard.sh: fails" fail "$(gd "$nor_stack")"
contains "  ... says so" "$tmp/gd.out" "No README commit"
contains "  ... says so" "$tmp/gd.out" "No README commit"
expect "no README commit, upstream's three root READMEs at HEAD: passes (dormant)" pass "$(gd "$(build "$nor_stack" 'echo k >README_KO.md && git add -A && git commit -q -m ko -m "Provimux-Layer: branding"')" "$tmp/guard-req0.sh")"

echo "# README commit: publish.sh --ci --plan, end to end (stub gh, local remotes)"
# The remote holds the old main; the check's artifact is prepare's out dir for v1.1.0
# (the README case), its result.json turned into a pass.
rremote="$tmp/rremote.git"
git init -q --bare "$rremote"
rg push -q "$rremote" "$OM:refs/heads/main"
rg remote add pmx "$rremote"
rstub="$tmp/rstub"; mkdir -p "$rstub"
rs() { jq -n "$2" >"$rstub/$1.json"; }
rs repos_test_up_releases_tags_v1.1.0 '{draft: false, prerelease: false, published_at: "2026-11-20T00:00:00Z"}'
jq -n --arg sha "$OM" '{id: 42, run_attempt: 1, path: ".github/workflows/provimux-check.yml", head_branch: "main",
  event: "schedule", status: "completed", conclusion: "success", head_sha: $sha,
  repository: {full_name: "test/provimux"}, head_repository: {full_name: "test/provimux"}}' >"$rstub/repos_test_provimux_actions_runs_42_attempts_1.json"
echo '{"jobs": [{"name": "prepare", "id": 800}, {"name": "verdict", "id": 900}]}' >"$rstub/repos_test_provimux_actions_runs_42_attempts_1_jobs_per_page_100.json"
echo '[{"title": "provimux-check-artifact", "message": "501 provimux-check-v1.1.0 sha256:aa"}]' >"$rstub/repos_test_provimux_check-runs_900_annotations_per_page_100.json"
echo '{"id": 501, "name": "provimux-check-v1.1.0", "expired": false, "digest": "sha256:aa", "workflow_run": {"id": 42}}' >"$rstub/repos_test_provimux_actions_artifacts_501.json"
# rart CANDIDATE BUNDLE [JQ]: the artifact 501 with prepare's result.json for v1.1.0 as a pass (JQ edits it) and BUNDLE.
rart() {
  local d="$tmp/rart"
  rm -rf "$d"; mkdir -p "$d"
  jq --arg c "$1" '.status = "pass" | .reasons = [] | .candidate = $c | .tests = {ok: true} | .build = {ok: true} | .guard.ok = true' \
    "$tmp/prep-v1.1.0/result.json" | jq "${3:-.}" >"$d/result.json"
  cp "$2" "$d/candidate.bundle"
  (cd "$d" && zip -q a.zip result.json candidate.bundle) && mv "$d/a.zip" "$rstub/repos_test_provimux_actions_artifacts_501_zip.json"
}
# rplan: publish.sh --ci --plan; "exit N", the output in $tmp/rplan.out, the plan in $tmp/rplan/plan.env
rplan() {
  rm -rf "$tmp/rplan"
  (cd "$rp" && env -u PMX_PUSH_TOKEN -u PROVIMUX_TOKEN PATH="${RPATH:-}$stub/bin:$PATH" STUB_API="$rstub" PMX_REPO=test/provimux \
    PMX_UPSTREAM_REPO=test/up PMX_UPSTREAM_URL="$ru" \
    bash "$here/publish.sh" v1.1.0 --ci --plan "$tmp/rplan" --run 42 --attempt 1 --artifact 501 --expect-main "$OM" --remote pmx) \
    >"$tmp/rplan.out" 2>&1 && echo "exit 0" || echo "exit $?"
}
rart "$c1" "$tmp/prep-v1.1.0/candidate.bundle"
expect "the README case: plan written" "exit 0" "$(rplan)"
expect "  ... with the candidate" "CANDIDATE=$c1" "$(grep '^CANDIDATE=' "$tmp/rplan/plan.env" 2>/dev/null || true)"
contains "  ... the plan names the README commit" "$tmp/rplan.out" "root READMEs    $R -> $(rg rev-parse "$c1~2")"
contains "  ... nothing changed, nothing dropped" "$tmp/rplan.out" "range-diff      4 unchanged, 0 changed, 0 dropped"
# R' also changes LICENSE, and the check's result.json says pass for it.
blic="$(build "$(up_c v1.1.0)" pick:"$c1~4" pick:"$c1~3" pick:"$c1~2" 'echo GPL >LICENSE && git commit -q -a --amend -C HEAD' pick:"$E" pick:"$L2")"
rg update-ref refs/t/blic "$blic"
rg bundle create -q "$tmp/blic.bundle" refs/t/blic "^$(up_c v1.1.0)"
rart "$blic" "$tmp/blic.bundle" ".readme.new = \"$(rg rev-parse "$blic~2")\""
expect "a bundle whose R' also changes LICENSE, result.json pass: refused" "exit 1" "$(rplan)"
contains "  ... by the normal form (condition 4)" "$tmp/rplan.out" "condition 4"
if [[ -e "$tmp/rplan/plan.env" ]]; then bad "  ... and no plan"; else ok "  ... and no plan"; fi
rart "$c1" "$tmp/prep-v1.1.0/candidate.bundle" ".readme.new = \"$(rg rev-parse "$c1~1")\""
expect "result.json's readme names another commit: refused" "exit 1" "$(rplan)"
contains "  ... says so" "$tmp/rplan.out" "is not this range-diff's"
rart "$c1" "$tmp/prep-v1.1.0/candidate.bundle" '.readme = null'
expect "result.json without readme for a README case: refused" "exit 1" "$(rplan)"
# T-113 1: git range-diff fails in publish.sh (a git on PATH that fails it for one range).
mkdir -p "$tmp/fgitbin"
# shellcheck disable=SC2016 # the wrapper's own text
printf '#!/usr/bin/env bash\nrd=0 hit=0\nfor a in "$@"; do [ "$a" = range-diff ] && rd=1; [ "$a" = "${FAIL_RD:-}" ] && hit=1; done\n[ $rd$hit = 11 ] && { echo "fatal: injected" >&2; exit 128; }\nexec %s "$@"\n' "$realgit" >"$tmp/fgitbin/git"
chmod +x "$tmp/fgitbin/git"
rart "$c1" "$tmp/prep-v1.1.0/candidate.bundle"
expect "  (the same artifact with a working git: plan written)" "exit 0" "$(RPATH="$tmp/fgitbin:" FAIL_RD=none rplan)"
r_c1="$(rg rev-parse "$c1~2")"
for seg in "$OB..$R^" "$R..$OM" "$(up_c v1.1.0)..$r_c1^" "$r_c1..$c1"; do
  expect "T-113 1: publish.sh, git range-diff of $(sed "s/$OB/<old base>/; s/$R/R/g; s/$OM/<old main>/; s/$r_c1/R'/g; s/$c1/<candidate>/; s/$(up_c v1.1.0)/<new base>/" <<<"$seg") fails: refused" "exit 1" "$(RPATH="$tmp/fgitbin:" FAIL_RD="$seg" rplan)"
  if [[ -e "$tmp/rplan/plan.env" ]]; then bad "  ... and no plan"; else ok "  ... and no plan"; fi
  contains "  ... because that range-diff failed" "$tmp/rplan.out" "failed"
done
rg worktree remove --force "$bw"
echo
echo "$pass passed, $failed failed"
[[ $failed -eq 0 ]]
