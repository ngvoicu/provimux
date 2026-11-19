#!/usr/bin/env bash
# Provimux Homebrew tap (.github/provimux/SYNC.md, "The Homebrew tap"): the formula for
# ngvoicu/homebrew-tap, checked and committed locally, never pushed here. It holds no
# token: the push is the caller's (the publish job's narrow token step, or the owner's
# own git in publish.sh).
#
#   tap.sh verify <version> <checksums.txt> <tap formula> <new formula>
#       The new formula changes only the version, url and sha256 lines of the tap's
#       formula; the version is <version>, every archive URL is one of that version's,
#       and the sha256 after each is checksums.txt's for that file. Exit 1 otherwise.
#       (The release workflow's tap-check job and prepare below.)
#   tap.sh release <v<base>-pmx.<n>> <dir>
#       The published release as anyone sees it: not a draft, not a prerelease,
#       immutable, every expected asset uploaded with a digest, checksums.txt's digest
#       = its bytes, every archive's digest = checksums.txt's. Leaves <dir>/checksums.txt.
#   tap.sh artifact <v<base>-pmx.<n>> <commit> <dir>
#       The formula the release run prepared: the newest provimux-release run for the
#       tag push at <commit>, completed with success (attempt read in the same call);
#       its job "tap-check" of that attempt names the artifact by exactly one annotation
#       "provimux-formula-artifact: <id> formula sha256:<digest>"; the artifact must be
#       that id, of that run, named formula, unexpired, with that digest, and the zip
#       downloaded must have it. Leaves <dir>/provimux.rb.
#   tap.sh prepare <v<base>-pmx.<n>> <dir> [<formula>]
#       release (above), then a clone of the tap in <dir>/tap; the formula is <formula>,
#       or, without it, the tap's formula updated by update-formula.sh from the
#       release's checksums.txt. Only forward: a tap already at this version with this
#       formula, or at a newer version, is left as it is (action=none). Otherwise verify
#       (above) and one commit on the tap's main. Prints, and writes to $GITHUB_OUTPUT:
#       action=push|none, repo=<git dir>, lease=refs/heads/main:<tap main read>,
#       ref=<commit>:refs/heads/main, version=, from=.
#
# Env: PMX_REPO (common.sh), PMX_TAP_URL (default https://github.com/ngvoicu/homebrew-tap.git),
# GH_TOKEN with read access (and actions: read for artifact). Runs with bash 3.2.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=common.sh
. "$here/common.sh"
PMX_TAP_URL="${PMX_TAP_URL:-https://github.com/ngvoicu/homebrew-tap.git}"
PMX_PMX_RE='^v[0-9]+\.[0-9]+\.[0-9]+-pmx\.[0-9]+$'

die() { echo "tap: $*" >&2; exit 1; }
out() { echo "$1=$2"; [[ -z "${GITHUB_OUTPUT:-}" ]] || echo "$1=$2" >>"$GITHUB_OUTPUT"; }

verify() {
  local v=$1 sums=$2 old=$3 new=$4
  awk -v v="$v" '
    function kind(l) {
      if (l ~ /^  version "[^"]*"$/) return "version"
      if (l ~ /^ *url "https:\/\/github\.com\/ngvoicu\/provimux\/releases\/download\/[^"]*"$/) return "url"
      if (l ~ /^ *sha256 "[^"]*"$/) return "sha256"
      return "other"
    }
    function bad(msg) { printf "tap: formula line %d: %s\n", FNR, msg > "/dev/stderr"; err = 1 }
    function indent(l) { sub(/[^ ].*/, "", l); return l }
    function value(l) { sub(/^[^"]*"/, "", l); sub(/"$/, "", l); return l }
    function suffix(u) { sub(/.*\/provimux_[^_]*_/, "", u); return u }
    FILENAME == ARGV[1] { if (NF == 2) sum[$2] = $1; next }
    FILENAME == ARGV[2] { old[++n] = $0; next }
    {
      m = FNR; o = old[FNR]; k = kind($0)
      if (FNR > n) { bad("added: " $0); next }
      if (expect != "") {
        if (k != "sha256") bad("the line after an archive URL is not its sha256: " $0)
        else if (value($0) != expect) bad("sha256 " value($0) " is not the checksums.txt sha256 " expect)
        if (k == "sha256" && indent($0) != indent(o)) bad("indent changed: " $0)
        expect = ""; next
      }
      if (k == "version" && $0 != "  version \"" v "\"") bad("not version " v ": " $0)
      if (k == "url") {
        u = value($0); f = u; sub(/.*\//, "", f)
        if (u != "https://github.com/ngvoicu/provimux/releases/download/v" v "/provimux_" v "_" suffix(u)) bad("not an archive of " v ": " u)
        if (kind(o) != "url" || suffix(u) != suffix(value(o)) || indent($0) != indent(o)) bad("archive URL changed beyond its version: -" o " +" $0)
        if (!(f in sum)) bad(f " is not in checksums.txt")
        else expect = sum[f]
        urls++
        next
      }
      if ($0 != o && !(k == "version" && kind(o) == "version")) bad("only the version, url and sha256 lines may change: -" o " +" $0)
    }
    END {
      if (m != n) { print "tap: the new formula has " m + 0 " lines, the tap'"'"'s " n + 0 > "/dev/stderr"; err = 1 }
      if (expect != "") { print "tap: the last archive URL has no sha256 line" > "/dev/stderr"; err = 1 }
      if (urls == 0) { print "tap: the new formula has no archive URL" > "/dev/stderr"; err = 1 }
      exit err
    }
  ' "$sums" "$old" "$new"
}

release() {
  local tag=$1 dir=$2 json f want got n
  [[ "$tag" =~ $PMX_PMX_RE ]] || die "'$tag' is not v<base>-pmx.<n>"
  mkdir -p "$dir"
  json="$(gh api "repos/$PMX_REPO/releases/tags/$tag")" || die "cannot read the release $tag"
  [[ "$(jq -r '"\(.draft) \(.prerelease) \(.immutable)"' <<<"$json")" == "false false true" ]] ||
    die "release $tag is $(jq -r '"draft=\(.draft) prerelease=\(.prerelease) immutable=\(.immutable)"' <<<"$json"): not a published, immutable release"
  n=0
  while IFS= read -r f; do
    got="$(jq -r --arg f "$f" '[.assets[] | select(.name == $f and .state == "uploaded")] | if length == 1 then (.[0].digest // "") else "" end' <<<"$json")"
    [[ "$got" =~ ^sha256:[0-9a-f]{64}$ ]] || die "release $tag: asset $f is missing, not uploaded or has no digest"
    n=$((n + 1))
  done < <(expected_assets "${tag#v}")
  [[ "$(jq '.assets | length' <<<"$json")" == "$n" ]] || die "release $tag has assets beyond the $n expected"
  gh api -H 'Accept: application/octet-stream' \
    "repos/$PMX_REPO/releases/assets/$(jq -r '.assets[] | select(.name == "checksums.txt") | .id' <<<"$json")" >"$dir/checksums.txt" ||
    die "cannot download checksums.txt of $tag"
  [[ "sha256:$(sha256_of "$dir/checksums.txt")" == "$(jq -r '.assets[] | select(.name == "checksums.txt") | .digest' <<<"$json")" ]] ||
    die "checksums.txt downloaded is not the release's asset"
  while IFS= read -r f; do
    [[ "$f" == checksums.txt ]] && continue
    want="$(awk -v f="$f" 'NF == 2 && $2 == f {print $1}' "$dir/checksums.txt")"
    [[ "$want" =~ ^[0-9a-f]{64}$ ]] || die "checksums.txt has no sha256 for $f"
    [[ "$(jq -r --arg f "$f" '.assets[] | select(.name == $f) | .digest' <<<"$json")" == "sha256:$want" ]] ||
      die "release $tag: asset $f's digest is not checksums.txt's $want"
  done < <(expected_assets "${tag#v}")
  echo "tap: release $tag is published and immutable, with $n assets matching checksums.txt" >&2
}

artifact() {
  local tag=$1 commit=$2 dir=$3 runs run att job ann id name digest meta
  [[ "$tag" =~ $PMX_PMX_RE && "$commit" =~ $PMX_SHA_RE ]] || die "artifact: '$tag' '$commit'"
  runs="$(runs_json "$tag")" || die "cannot list the runs of $tag"
  run="$(jq -r --arg tag "$tag" --arg sha "$commit" --arg repo "$PMX_REPO" "$PMX_RELEASE_RUNS"' | last | .id // empty' <<<"$runs")"
  [[ "$run" =~ ^[0-9]+$ ]] || die "no provimux-release run for $tag at $commit"
  att="$(gh api "repos/$PMX_REPO/actions/runs/$run" --jq 'select(.status == "completed" and .conclusion == "success") | .run_attempt')" ||
    die "cannot read run $run"
  [[ "$att" =~ ^[0-9]+$ ]] || die "the release run $run of $tag did not complete with success"
  job="$(gh api "repos/$PMX_REPO/actions/runs/$run/attempts/$att/jobs?per_page=100" \
    --jq '[.jobs[] | select(.name == "tap-check")] | if length == 1 then .[0].id else "" end')" || die "cannot list the jobs of run $run"
  [[ "$job" =~ ^[0-9]+$ ]] || die "run $run attempt $att has no single job tap-check"
  ann="$(gh api "repos/$PMX_REPO/check-runs/$job/annotations?per_page=100" \
    --jq '[.[] | select(.title == "provimux-formula-artifact") | .message] | if length == 1 then .[0] else "" end')" ||
    die "cannot read the annotations of job $job"
  read -r id name digest _ <<<"$ann" || true
  [[ "$id" =~ ^[0-9]+$ && "$name" == formula && "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] ||
    die "the job tap-check of run $run names no formula artifact (one annotation \"provimux-formula-artifact\": <id> formula sha256:<digest>)"
  meta="$(gh api "repos/$PMX_REPO/actions/artifacts/$id")" || die "cannot read artifact $id"
  jq -e --arg run "$run" --arg d "$digest" '(.workflow_run.id | tostring) == $run and .name == "formula" and (.expired | not) and .digest == $d' <<<"$meta" >/dev/null ||
    die "artifact $id is not the formula of run $run with $digest"
  mkdir -p "$dir"
  gh api "repos/$PMX_REPO/actions/artifacts/$id/zip" >"$dir/formula.zip" || die "cannot download artifact $id"
  [[ "sha256:$(sha256_of "$dir/formula.zip")" == "$digest" ]] || die "artifact $id downloaded is not $digest"
  unzip -q -o "$dir/formula.zip" provimux.rb -d "$dir" || die "artifact $id has no provimux.rb"
  echo "tap: formula of release run $run attempt $att (artifact $id)" >&2
}

prepare() {
  local tag=$1 dir=$2 formula=${3:-} v cur base newest
  v="${tag#v}"
  release "$tag" "$dir/release"
  rm -rf "$dir/tap"
  GIT_TERMINAL_PROMPT=0 git clone -q --depth 1 --no-tags "$PMX_TAP_URL" "$dir/tap" || die "cannot clone $PMX_TAP_URL"
  t() { GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git -C "$dir/tap" -c core.hooksPath=/dev/null "$@"; }
  base="$(t rev-parse HEAD)"
  [[ "$(t symbolic-ref --short HEAD)" == main ]] || die "the tap's default branch is not main"
  local f="$dir/tap/Formula/provimux.rb"
  [[ -s "$f" ]] || die "the tap has no Formula/provimux.rb"
  cp "$f" "$dir/base.rb"
  if [[ -z "$formula" ]]; then
    formula="$dir/new.rb"; cp "$f" "$formula"
    bash "$here/update-formula.sh" "$v" "$dir/release/checksums.txt" "$formula" >&2 || die "update-formula.sh failed"
  fi
  cur="$(sed -n 's/^  version "\(.*\)"$/\1/p' "$f")"
  out version "$v"; out from "$cur"
  if cmp -s "$formula" "$f"; then
    echo "tap: the tap already has this formula ($cur)" >&2; out action none; return 0
  fi
  newest="$(printf '%s\n%s\n' "$cur" "$v" | sort -V | tail -n 1)"
  if [[ "$cur" == "$v" || "$newest" != "$v" ]]; then
    echo "tap: the tap has $cur; $v is not newer, left as it is" >&2; out action none; return 0
  fi
  verify "$v" "$dir/release/checksums.txt" "$f" "$formula" || die "the formula changes more than the version, url and sha256 lines"
  cp "$formula" "$f"
  t -c user.name='github-actions[bot]' -c user.email='41898282+github-actions[bot]@users.noreply.github.com' \
    commit -q -m "provimux $v" -- Formula/provimux.rb
  diff -u "$dir/base.rb" "$f" >&2 || true
  out action push
  out repo "$(cd "$dir/tap/.git" && pwd)"
  out lease "refs/heads/main:$base"
  out ref "$(t rev-parse HEAD):refs/heads/main"
}

case "${1:-}" in
  verify) [[ $# -eq 5 ]] || die "usage: tap.sh verify <version> <checksums> <tap formula> <new formula>"; verify "$2" "$3" "$4" "$5" ;;
  release) release "${2:?}" "${3:?}" ;;
  artifact) artifact "${2:?}" "${3:?}" "${4:?}" ;;
  prepare) prepare "${2:?}" "${3:?}" "${4:-}" ;;
  *) echo "usage: tap.sh verify|release|artifact|prepare ..." >&2; exit 2 ;;
esac
