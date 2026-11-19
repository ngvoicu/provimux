#!/usr/bin/env bash
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
#                 failure on both trees; and the two waivers that must still hold (same test fails
#                 on the tag; explicit pass on the rerun)
#   guard.sh      base only upstream (no local tag); a reachable base tag withdrawn or
#                 moved upstream; the expected base
#   release-meta  the base tag only upstream; a withdrawn base
#   report.sh     a successful release run for the wrong SHA (not published), the right
#                 SHA (published), a failed check job (not published); a failure result
#                 (every detail shown)
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
git -C "$pmx" tag v1.1.0 "$v110"
git -C "$up" tag -d v1.1.0 >/dev/null
expect "reachable base tag withdrawn upstream: fails" fail "$(guard env)"
contains "  ... says withdrawn" "$tmp/guard.out" "withdrawn"
expect "withdrawn, with the expected base: fails" fail "$(guard env PMX_EXPECT_BASE=v1.1.0 PMX_EXPECT_BASE_COMMIT="$v110")"
git -C "$up" tag v1.1.0 "$other"
expect "reachable base tag moved upstream: fails" fail "$(guard env)"
contains "  ... says moved" "$tmp/guard.out" "moved"
git -C "$up" tag -d v1.1.0 >/dev/null && git -C "$up" tag -a -m v1.1.0 v1.1.0 "$v110"

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
# Stub gh: "gh api PATH" prints $STUB_API/<PATH with / ? & = as _>.json, else a 404.
[ "$1" = api ] || { echo "stub gh: $*" >&2; exit 1; }
shift
p=''
for a in "$@"; do case "$a" in -*) ;; *) [ -n "$p" ] || p=$a ;; esac; done
f="$STUB_API/$(printf %s "$p" | tr '/?&=' '____').json"
if [ -f "$f" ]; then cat "$f"; else echo "gh: Not Found (HTTP 404)" >&2; exit 1; fi
EOF
printf '#!/bin/sh\nexit 22\n' >"$stub/bin/curl"
chmod +x "$stub/bin/gh" "$stub/bin/curl"
# Provimux's remote: tag v1.1.0-pmx.1 at the layer commit.
remote="$tmp/remote.git"
git init -q --bare "$remote"
git -C "$pmx" push -q "$remote" "HEAD:refs/heads/main" "refs/tags/v1.1.0-pmx.1"
pmx_sha="$(git -C "$pmx" rev-parse HEAD)"
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

echo
echo "$pass passed, $failed failed"
[[ $failed -eq 0 ]]
