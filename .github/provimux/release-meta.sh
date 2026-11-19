#!/usr/bin/env bash
# Provimux release metadata for provimux-release.yml.
# Tag push: GITHUB_REF_NAME = v<base>-pmx.<n>. Manual run (dry run): INPUT_VERSION =
# <base>-pmx.<anything>, or empty for <base>-pmx.0-dryrun.
# Writes to $GITHUB_OUTPUT: base, provimux_version, go_version, commit, build_date,
# ldflags (the archives' ldflags; main.Version = base without "v", as upstream's
# release.yaml sets it), publish (true only for a tag push).
#
# <base> must be exactly the base of the tagged commit, found by the same rule as
# guard.sh (upstream_base in common.sh): the newest vX.Y.Z tag of upstream CLIProxyAPI,
# read from upstream's refs (git ls-remote), whose commit is an ancestor of HEAD; no
# local tag is needed. A reachable local vX.Y.Z tag that upstream withdrew or moved is
# an error, never a reason to fall back to an older base.
# A tag naming any other release (an older one that is merely an ancestor, or
# one that is not reachable) is refused, so no artifact can claim a base it is
# not built on.
# Env: PMX_UPSTREAM (default: upstream CPA), as for guard.sh.
set -euo pipefail
fail() { echo "::error::$*"; exit 1; }

here="$(cd "$(dirname "$0")" && pwd)"
PMX_UPSTREAM_URL="${PMX_UPSTREAM:-${PMX_UPSTREAM_URL:-https://github.com/router-for-me/CLIProxyAPI}}"
# shellcheck source=common.sh
. "$here/common.sh"

# --- The base of HEAD (same rule as guard.sh: upstream's refs, not local tags) ----
upstream_tags=$(git ls-remote --tags "$PMX_UPSTREAM_URL" 'refs/tags/v*') ||
  fail "Cannot list the tags of $PMX_UPSTREAM_URL."
err="$(mktemp)"
b="$(upstream_base HEAD "$upstream_tags" 2>"$err")" || fail "$(cat "$err")"
rm -f "$err"
base_tag="${b%% *}"
[[ "${b#* }" != "$(git rev-parse HEAD)" ]] ||
  fail "HEAD is the upstream release $base_tag itself, not a Provimux commit."

# --- The version asked for -----------------------------------------------------------
if [[ "${GITHUB_EVENT_NAME:-}" == "push" ]]; then
  [[ "${GITHUB_REF:-refs/tags/$GITHUB_REF_NAME}" == "refs/tags/$GITHUB_REF_NAME" ]] ||
    fail "$GITHUB_REF is not a tag."
  [[ "$GITHUB_REF_NAME" =~ ^v([0-9]+\.[0-9]+\.[0-9]+)-pmx\.([0-9]+)$ ]] ||
    fail "Tag $GITHUB_REF_NAME is not v<major>.<minor>.<patch>-pmx.<n>."
  base="${BASH_REMATCH[1]}"
  provimux_version="${GITHUB_REF_NAME#v}"
  [[ "$(git rev-parse "refs/tags/$GITHUB_REF_NAME^{commit}")" == "$(git rev-parse HEAD)" ]] ||
    fail "Tag $GITHUB_REF_NAME is not HEAD ($(git rev-parse --short HEAD))."
  publish=true
else
  provimux_version="${INPUT_VERSION:-}"
  provimux_version="${provimux_version#v}"
  [[ -n "$provimux_version" ]] || provimux_version="${base_tag#v}-pmx.0-dryrun"
  [[ "$provimux_version" =~ ^([0-9]+\.[0-9]+\.[0-9]+)-pmx\.[0-9A-Za-z.-]+$ ]] ||
    fail "Provimux version $provimux_version is not <base>-pmx.<n>[...]."
  base="${BASH_REMATCH[1]}"
  publish=false
fi

[[ "v$base" == "$base_tag" ]] ||
  fail "Version $provimux_version names base v$base, but $(git rev-parse --short HEAD) is built on $base_tag (the newest upstream release tag whose commit is an ancestor of it). Tag it ${base_tag}-pmx.<n>."

# Follow upstream's Go version: read it from the tree's own release.yaml.
go_version="$(sed -nE "s/^[[:space:]]*GO_VERSION:[[:space:]]*['\"]?([0-9][0-9.]*)['\"]?.*$/\1/p" .github/workflows/release.yaml | head -n 1)"
[[ -n "$go_version" ]] || fail "No GO_VERSION in .github/workflows/release.yaml."

module="$(awk '$1 == "module" { print $2; exit }' go.mod)"
[[ -n "$module" ]] || fail "No module line in go.mod."

commit="$(git rev-parse --short HEAD)"
build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ldflags="-s -w -X main.Version=${base} -X main.Commit=${commit} -X main.BuildDate=${build_date} -X ${module}/internal/buildinfo.ProvimuxVersion=${provimux_version}"

out="${GITHUB_OUTPUT:-/dev/stdout}"
{
  echo "base=$base"
  echo "provimux_version=$provimux_version"
  echo "go_version=$go_version"
  echo "commit=$commit"
  echo "build_date=$build_date"
  echo "ldflags=$ldflags"
  echo "publish=$publish"
} >> "$out"
{
  echo "### Provimux ${provimux_version}"
  echo
  echo "- CLIProxyAPI base: \`${base}\` (newest upstream release tag whose commit is an ancestor of the commit)"
  echo "- Go (archives, from release.yaml): \`${go_version}\`"
  echo "- Commit: \`${commit}\`, built at \`${build_date}\`"
  echo "- Publish: ${publish}"
} >> "${GITHUB_STEP_SUMMARY:-/dev/stderr}"
