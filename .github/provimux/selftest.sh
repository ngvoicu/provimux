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
#                 none, promises no check); remote unreadable; no tag
#   publish.sh    --ci: refuses a push token in its environment, and bad mode flags
#   check.sh      state.json parsed, never sourced; verdict step outcomes and job
#                 conclusions; run_artifact by id, run, name, digest and bytes
#   releases_status / report.sh  the newest -pmx.<n> decides; the sweep of older bases
#   tap.sh        release (immutable, assets, digests), verify (only version, url,
#                 sha256 lines), prepare (only forward, local commit, lease as read,
#                 nothing pushed), artifact (bound by tap-check of a successful run)
#   provenance    the release workflow's inline check (run from the workflow): only the
#                 owner's newest success status for exactly this tag counts; a bot's,
#                 another tag's, a moved tag, an unreadable list (told apart) refuse
#   latest        release-latest (run from the workflow): only its own release id,
#                 never backward, stops on an unreadable or tagless latest, fails a
#                 wrong readback
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
)
guard() { (cd "$pmx" && PMX_UPSTREAM="$up" "$@" bash "$here/guard.sh") >"$tmp/guard.out" 2>&1 && echo pass || echo fail; }

echo "# guard.sh"
expect "base tag here and upstream: passes" pass "$(guard env)"
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
jq '.draft = false' "$stub/api/repos_test_provimux_releases_tags_v1.1.0-pmx.1.json" >"$tmp/d.json"
mv "$tmp/d.json" "$stub/api/repos_test_provimux_releases_tags_v1.1.0-pmx.1.json"
expect "push reported a failure but the remote has the complete release (lost acknowledgement): released" "sync: v1.1.0 — released" \
  "$(notify v1.1.0 failed 'publish: git push reported a failure' HAS_TOKEN=false)"
lacks "  ... not 'nothing was pushed'" "$tmp/notify.out" "nothing was pushed"
lacks "  ... promises no next check" "$tmp/notify.out" "next check"
printf 'PMX_TAG=v1.1.0-pmx.1\nCANDIDATE=%s\n' "$pmx_sha" >"$tmp/plan-tap.env"
expect "complete release, the tap update failed: released, tap not updated, owner needed" "sync: v1.1.0 — released, Homebrew tap not updated, owner needed" \
  "$(notify v1.1.0 tap-failed 'publish: the Homebrew tap update was refused (see the log)' PLAN="$tmp/plan-tap.env")"
contains "  ... gives the owner's tap command" "$tmp/notify.out" "publish.sh --tap v1.1.0-pmx.1"
lacks "  ... does not close the issue" "$tmp/notify.out" "this closes the issue"
# The same for real (stub gh records issue commands): no open issue -> none is opened;
# an open "sync: v1.1.0 ..." issue -> it is closed.
notify_real() {
  : >"$stub/issue.log"
  env PATH="$stub/bin:$PATH" STUB_API="$stub/api" PMX_REPO=test/provimux PMX_REPO_URL="$remote" \
    TAG=v1.1.0 OUTCOME=failed LOG="$tmp/publish.log" EXPECT_MAIN="$pmx_sha" RUN_URL=https://example.invalid/run \
    bash "$here/autopublish.sh" notify >"$tmp/notify.out" 2>&1 || { echo "exit $?"; return; }
  tr '\n' ' ' <"$stub/issue.log" | cut -c1-14
}
echo '[]' >"$stub/api/repos_test_provimux_issues_state_open_per_page_100.json"
expect "released, no open issue: opens none" "" "$(notify_real)"
echo '[{"number": 5, "title": "sync: v1.1.0 — ready, publishing waits for PROVIMUX_TOKEN"}]' >"$stub/api/repos_test_provimux_issues_state_open_per_page_100.json"
expect "released, the open issue: closes it" "issue close 5 " "$(notify_real)"
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
echo "# tap.sh: release, verify, prepare (only forward), artifact"
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
    done; } | jq -s '{id: 77001, tag_name: "v'"$TV"'", draft: false, prerelease: false, immutable: true, assets: .}' | jq "${1:-.}" \
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
# Only forward: the tap at a newer version is left as it is.
(cd "$tmp/tapw" && sed -i.bak 's/version "1.2.0-pmx.1"/version "1.9.0-pmx.1"/' Formula/provimux.rb && rm -f Formula/provimux.rb.bak && git commit -qam newer && git push -q "$tapr" HEAD:main)
expect "the tap is newer: ok, nothing to do" ok "$(tapsh bash "$here/tap.sh" prepare "v$TV" "$tmp/tp")"
contains "  ... action none" "$tmp/tap.out" "action=none"
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
# A readback that is not this release (GitHub kept another one latest).
# shellcheck disable=SC2016 # literal text for sed
sed -i.bak 's|cp "\$f" "\$(dirname|true "$(dirname|' "$stub/bin/gh" && rm -f "$stub/bin/gh.bak"
latest '{"tag_name": "v1.2.0-pmx.1"}'
expect "latest reads back as another release after the change: fails" refused "$(lrun TAG=v1.3.0-pmx.1 ID=301)"
contains "  ... says so" "$tmp/latest.out" "reads back as"

echo
echo "$pass passed, $failed failed"
[[ $failed -eq 0 ]]
