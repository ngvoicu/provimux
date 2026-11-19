#!/usr/bin/env bash
# Homebrew formula update (PMX-13), run by the formula job of
# .github/workflows/provimux-release.yml after a release is published.
#
# Usage: update-formula.sh <provimux-version> <checksums.txt> <Formula/provimux.rb>
#   e.g. update-formula.sh 8.0.77-pmx.1 dist/checksums.txt tap/Formula/provimux.rb
#
# Rewrites the formula in place: the `version` line, every release archive URL
# (.../releases/download/v<version>/provimux_<version>_<os>_<arch>.tar.gz) and the
# `sha256` line that follows each URL, taken from the release's checksums.txt.
# Everything else in the formula is left as it is. Fails, leaving the formula
# untouched, if the formula has no version line or no archive URL, or if an
# archive is missing from checksums.txt.
set -euo pipefail

if [ $# -ne 3 ]; then
  echo "usage: $0 <provimux-version> <checksums.txt> <formula.rb>" >&2
  exit 2
fi
version=$1 checksums=$2 formula=$3

if ! [[ "$version" =~ ^[0-9]+(\.[0-9]+)+-pmx\.[0-9A-Za-z.-]+$ ]]; then
  echo "error: '$version' is not a Provimux version (<base>-pmx.<n>[...])" >&2
  exit 1
fi

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT

awk -v version="$version" '
  # checksums.txt: "<sha256>  <file>"
  FNR == NR { if (NF == 2) sum[$2] = $1; next }

  /^  version "/ {
    sub(/"[^"]*"/, "\"" version "\"")
    versions++
  }

  /^ *url "https:\/\/github\.com\/ngvoicu\/provimux\/releases\/download\// {
    # Keep the indent and the <os>_<arch>.tar.gz suffix; put in the new version.
    indent = $0; sub(/url.*/, "", indent)
    u = $0; sub(/^ *url "/, "", u); sub(/".*/, "", u)
    file = u; sub(/.*\//, "", file)
    suffix = file
    if (sub(/^provimux_[^_]+_/, "", suffix) != 1) { print "error: unexpected archive name " file > "/dev/stderr"; bad = 1 }
    file = "provimux_" version "_" suffix
    if (!(file in sum)) { print "error: " file " is not in checksums.txt" > "/dev/stderr"; bad = 1 }
    $0 = indent "url \"https://github.com/ngvoicu/provimux/releases/download/v" version "/" file "\""
    pending = sum[file]
    urls++
    print; next
  }

  pending != "" && /^ *sha256 "/ {
    sub(/"[^"]*"/, "\"" pending "\"")
    pending = ""
    shas++
  }

  { print }

  END {
    if (versions != 1) { print "error: expected one version line, found " versions + 0 > "/dev/stderr"; bad = 1 }
    if (urls == 0) { print "error: no release archive URL in the formula" > "/dev/stderr"; bad = 1 }
    if (shas != urls) { print "error: " urls + 0 " archive URLs but " shas + 0 " sha256 lines after them" > "/dev/stderr"; bad = 1 }
    exit bad
  }
' "$checksums" "$formula" > "$tmp"

cat "$tmp" > "$formula"
echo "Formula $formula set to $version."
