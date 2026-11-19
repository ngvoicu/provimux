#!/usr/bin/env bash
# Provimux release metadata, shared by provimux-release.yml and provimux-docker.yml.
# Tag push: GITHUB_REF_NAME = v<base>-pmx.<n>. Manual run (dry run): INPUT_VERSION =
# <base>-pmx.<anything>, or empty for <newest base tag>-pmx.0-dryrun.
# Writes to $GITHUB_OUTPUT: base, provimux_version, go_version, commit, build_date,
# ldflags (the archives' ldflags; main.Version = base without "v", as upstream's
# release.yaml sets it).
set -euo pipefail
fail() { echo "::error::$*"; exit 1; }

if [[ "${GITHUB_EVENT_NAME:-}" == "push" ]]; then
  [[ "$GITHUB_REF_NAME" =~ ^v([0-9]+\.[0-9]+\.[0-9]+)-pmx\.([0-9]+)$ ]] ||
    fail "Tag $GITHUB_REF_NAME is not v<major>.<minor>.<patch>-pmx.<n>."
  base="${BASH_REMATCH[1]}"
  provimux_version="${GITHUB_REF_NAME#v}"
  dry_run=no
else
  provimux_version="${INPUT_VERSION:-}"
  provimux_version="${provimux_version#v}"
  if [[ -z "$provimux_version" ]]; then
    base_tag="$(git tag --merged HEAD --list 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1 || true)"
    [[ -n "$base_tag" ]] || fail "No vX.Y.Z tag reachable from HEAD."
    provimux_version="${base_tag#v}-pmx.0-dryrun"
  fi
  [[ "$provimux_version" =~ ^([0-9]+\.[0-9]+\.[0-9]+)-pmx\.[0-9A-Za-z.-]+$ ]] ||
    fail "Provimux version $provimux_version is not <base>-pmx.<n>[...]."
  base="${BASH_REMATCH[1]}"
  dry_run=yes
fi

# The base must be the CLIProxyAPI release this commit is built on.
git rev-parse -q --verify "refs/tags/v${base}^{commit}" >/dev/null ||
  fail "Base tag v${base} does not exist in this repository."
git merge-base --is-ancestor "v${base}" HEAD ||
  fail "Base tag v${base} is not an ancestor of $(git rev-parse --short HEAD)."

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
} >> "$out"
{
  echo "### Provimux ${provimux_version}"
  echo
  echo "- CLIProxyAPI base: \`${base}\`"
  echo "- Go (archives, from release.yaml): \`${go_version}\`"
  echo "- Commit: \`${commit}\`, built at \`${build_date}\`"
  echo "- Dry run: ${dry_run}"
} >> "${GITHUB_STEP_SUMMARY:-/dev/stderr}"
