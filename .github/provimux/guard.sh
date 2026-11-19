#!/usr/bin/env bash
# Provimux guard (PMX-03), run by .github/workflows/provimux-guard.yml.
#
# Requests to AI providers must stay byte-identical to stock CPA except where an
# owner patch intends otherwise. main = upstream release tag + owner patches +
# Provimux layer (commits with the trailer "Provimux-Layer: branding").
#
# Checks HEAD:
#   1. the base is the newest vX.Y.Z tag reachable from HEAD that exists in
#      upstream CPA at the same commit, and it is not HEAD itself;
#   2. base..HEAD has at least one layer commit, and once one appears every later
#      commit carries the trailer too;
#   3. no layer commit changes a protected path, against any of its parents;
#   4. for a pull request (PMX_TARGET = the target branch's commit): what the PR
#      introduces touches no protected path. Patch changes reach main only as a
#      rebased stack pushed by the owner or the sync, never through a PR.
#
# Env: PMX_TARGET (optional, pull requests), PMX_UPSTREAM (default: upstream CPA).
set -euo pipefail

upstream=${PMX_UPSTREAM:-https://github.com/router-for-me/CLIProxyAPI}
protected=(internal/runtime/executor internal/auth internal/misc internal/translator sdk/cliproxy/session)
pathspec=()
for p in "${protected[@]}"; do pathspec+=(":(top,literal)$p/"); done
protected_list="${protected[*]/%//}"

fail=0
err() { echo "::error::$*"; fail=1; }

# Print each protected file name NUL-delimited input gives, one per line (names
# with spaces or accents intact; a newline in a name is shown as '?').
names() { while IFS= read -r -d '' f; do printf '%s\n' "${f//$'\n'/?}"; done; }

head=$(git rev-parse HEAD)
short() { git rev-parse --short "$1"; }
subject() { git log -1 --format=%s "$1"; }

# --- 1. Base: a real upstream release tag, not the tip ---------------------------
upstream_tags=$(git ls-remote --tags "$upstream" 'refs/tags/v*')
upstream_commit() { # commit an upstream tag points to (peeled when annotated)
  local t=$1 peeled plain
  peeled=$(awk -v r="refs/tags/$t^{}" '$2==r {print $1}' <<<"$upstream_tags")
  plain=$(awk -v r="refs/tags/$t" '$2==r {print $1}' <<<"$upstream_tags")
  echo "${peeled:-$plain}"
}
base=''
for t in $(git tag --merged HEAD --list 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -rV); do
  local_commit=$(git rev-parse "$t^{commit}")
  up=$(upstream_commit "$t")
  if [ -z "$up" ]; then
    echo "Skipping tag $t: not a tag in $upstream."
  elif [ "$up" != "$local_commit" ]; then
    echo "Skipping tag $t: here it is $(short "$local_commit"), upstream it is ${up:0:8}."
  else
    base=$t; break
  fi
done
if [ -z "$base" ]; then
  echo "::error::No vX.Y.Z tag reachable from HEAD matches a release tag of $upstream; cannot find the base."
  exit 1
fi
base_commit=$(git rev-parse "$base^{commit}")
echo "Base: $base ($(short "$base_commit")), head: $(short "$head")"
if [ "$base_commit" = "$head" ]; then
  echo "::error::HEAD is the upstream release $base itself; there are no Provimux commits to check."
  exit 1
fi

# --- 2 and 3. Layer order and protected paths, per commit --------------------------
in_layer=0
first_layer=''
for c in $(git rev-list --reverse --topo-order "$base..HEAD"); do
  if git log -1 --format='%(trailers:key=Provimux-Layer,valueonly)' "$c" | sed 's/[[:space:]]*$//' | grep -qx 'branding'; then
    if [ "$in_layer" -eq 0 ]; then in_layer=1; first_layer=$(short "$c"); fi
    # Against every parent (-m): a layer merge must not bring protected changes from either side.
    hits=$(git diff-tree -r -m --root --no-commit-id --name-only -z "$c" -- "${pathspec[@]}" | names | sort -u)
    while IFS= read -r f; do
      [ -n "$f" ] && err "Provimux-layer commit $(short "$c") (\"$(subject "$c")\") touches protected path $f. Layer commits must not change anything under: $protected_list"
    done <<<"$hits"
  elif [ "$in_layer" -eq 1 ]; then
    err "Commit $(short "$c") (\"$(subject "$c")\") has no 'Provimux-Layer: branding' trailer but follows layer commit $first_layer. Patch commits must sit below the Provimux layer; rebase it under the layer, or add the trailer if it belongs to the layer."
  fi
done
if [ -z "$first_layer" ]; then
  err "No commit in $base..$(short "$head") carries the 'Provimux-Layer: branding' trailer. A change to main must sit on top of the Provimux layer (is the branch based on a patch commit below the layer?)."
fi

# --- 4. Pull request: what it introduces relative to its target ---------------------
if [ -n "${PMX_TARGET:-}" ]; then
  target=$(git rev-parse "$PMX_TARGET^{commit}")
  echo "Pull request target: $(short "$target")"
  hits=$(git diff --name-only -z "$target...$head" -- "${pathspec[@]}" | names)
  if [ -n "$(git rev-list --merges "$target..$head")" ]; then
    # A merge (of main, say) moves the merge base past changes the PR still brings.
    hits=$(printf '%s\n%s\n' "$hits" "$(git diff --name-only -z "$target" "$head" -- "${pathspec[@]}" | names)")
  fi
  hits=$(sed '/^$/d' <<<"$hits" | sort -u)
  while IFS= read -r f; do
    [ -n "$f" ] && err "This pull request changes protected path $f relative to main ($(short "$target")). Changes under $protected_list reach main only as a rebased patch stack pushed by the owner or the sync, never through a pull request."
  done <<<"$hits"
fi

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "OK: $(git rev-list --count "$base..HEAD") commits since $base; layer starts at $first_layer; no protected path changed."
