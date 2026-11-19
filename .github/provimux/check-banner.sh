#!/usr/bin/env bash
# Provimux release check: run a built binary with --help (it prints the startup
# banner and the flag list, then exits; no server starts) in an empty folder and
# require the banner to show the expected Provimux and CLIProxyAPI versions.
# Usage: check-banner.sh <binary> <provimux-version> <base-version>
# For the Docker image the base version carries upstream's "v" (v8.0.77).
set -euo pipefail
binary=$1 provimux_version=$2 base_version=$3
case "$binary" in /*|[A-Za-z]:*) ;; *) binary="$PWD/$binary" ;; esac
expected="Provimux Version: ${provimux_version} (based on CLIProxyAPI ${base_version}) by providers.dev"
dir="$(mktemp -d)"
out="$(cd "$dir" && "$binary" --help 2>&1 || true)"
rm -rf "${dir:?}"
banner="$(printf '%s\n' "$out" | grep -m1 '^Provimux Version:' || true)"
echo "Banner: $banner"
if [[ "$banner" != "$expected"* ]]; then
  printf '%s\n' "$out" | head -n 20 >&2
  echo "::error::Banner does not start with: $expected"
  exit 1
fi
