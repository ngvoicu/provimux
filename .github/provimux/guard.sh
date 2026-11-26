#!/usr/bin/env bash
# Provimux guard (PMX-03), run by .github/workflows/provimux-guard.yml.
#
# Requests to AI providers must stay byte-identical to stock CPA except where an
# owner patch intends otherwise. main = upstream release tag + owner patches +
# Provimux layer (commits with the trailer "Provimux-Layer: branding").
#
# Checks HEAD:
#   1. the base is the newest vX.Y.Z tag of upstream CPA (its refs, not local tags)
#      whose commit is an ancestor of HEAD, and it is not HEAD itself. A vX.Y.Z tag
#      reachable here, as new as the base or newer, that upstream withdrew or moved is
#      an error, never a reason to fall back to an older base. With PMX_EXPECT_BASE
#      (and PMX_EXPECT_BASE_COMMIT) the base must be exactly that upstream tag at that
#      commit;
#   2. base..HEAD has at least one layer commit, and once one appears every later
#      commit carries the trailer too;
#   3. no layer commit changes a protected path, against any of its parents;
#   5. the README commit (trailer "Provimux-Readme: root"; design/root-readme.md): at
#      exactly one in base..HEAD (none is an error: require_readme=1); it carries the
#      layer trailer, has one parent, changes only the README paths, leaves README.md a
#      100644 blob and the other README paths absent; and then, at HEAD, the only root
#      entry whose name begins with "readme" (any case) is the 100644 blob README.md;
#   6. no test of the layer depends on today's date (SYNC.md, "Tests and the date"): every
#      line the layer adds to a test (*_test.go, test/provimux/) that holds a literal date
#      (time.Date with a year, a YYYY-MM-DD string, time.Unix of a 10-digit epoch) is
#      covered by a comment "date-safe: <why>" on that line or above it with no empty line
#      between (a line that is only a comment is not looked at);
#   4. for a pull request (PMX_TARGET = the target branch's commit): the PR's own
#      commits since the merge base touch no protected path, it merges cleanly, and
#      the merged tree differs from the target in no protected path. Patch changes reach main only as a
#      rebased stack pushed by the owner or the sync, never through a PR.
#
# Env: PMX_TARGET (optional, pull requests), PMX_UPSTREAM (default: upstream CPA),
# PMX_EXPECT_BASE / PMX_EXPECT_BASE_COMMIT (optional: the sync check and publish.sh).
set -euo pipefail

upstream=${PMX_UPSTREAM:-https://github.com/router-for-me/CLIProxyAPI}
protected=(internal/runtime/executor internal/auth internal/misc internal/translator sdk/cliproxy/session)
pathspec=()
for p in "${protected[@]}"; do pathspec+=(":(top,literal)$p/"); done
protected_list="${protected[*]/%//}"
# The README paths: the same set as PMX_README_PATHS in common.sh (selftest.sh checks it).
readme_paths=(README.md README_CN.md README_JA.md)
# 1: the README commit is on main (since 2026-11-20); 0 would let a stack without it pass.
require_readme=1

fail=0
err() { echo "::error::$*"; fail=1; }

# Print each protected file name NUL-delimited input gives, one per line (names
# with spaces or accents intact; a newline in a name is shown as '?').
names() { while IFS= read -r -d '' f; do printf '%s\n' "${f//$'\n'/?}"; done; }

head=$(git rev-parse HEAD)
short() { git rev-parse --short "$1"; }
subject() { git log -1 --format=%s "$1"; }

# --- 1. Base: a real upstream release tag, not the tip ---------------------------
# Upstream's refs decide (git ls-remote), not local tags: the base is the newest vX.Y.Z
# tag of upstream whose commit is an ancestor of HEAD. A vX.Y.Z tag reachable from HEAD
# here, as new as that base or newer, that upstream no longer has (withdrawn) or has at
# another commit (moved) is an error, never a reason to fall back to an older base.
# Same rule as upstream_base in common.sh (this file stays self-contained: workflows
# and publish.sh run a copy of it alone).
if ! upstream_tags=$(git ls-remote --tags "$upstream" 'refs/tags/v*'); then
  echo "::error::Cannot list the tags of $upstream."
  exit 1
fi
upstream_commit() { # commit an upstream tag points to (peeled when annotated)
  local t=$1 peeled plain
  peeled=$(awk -v r="refs/tags/$t^{}" '$2==r {print $1}' <<<"$upstream_tags")
  plain=$(awk -v r="refs/tags/$t" '$2==r {print $1}' <<<"$upstream_tags")
  echo "${peeled:-$plain}"
}
stable_re='^v[0-9]+\.[0-9]+\.[0-9]+$'
base=''
base_commit=''
for t in $(awk '{print $2}' <<<"$upstream_tags" | sed 's|^refs/tags/||; s/\^{}$//' | grep -E "$stable_re" | sort -u | sort -r -V); do
  up=$(upstream_commit "$t")
  git cat-file -e "$up^{commit}" 2>/dev/null || continue
  if git merge-base --is-ancestor "$up" HEAD; then
    base=$t
    base_commit=$up
    break
  fi
done
if [ -z "$base" ]; then
  echo "::error::No vX.Y.Z release tag of $upstream is an ancestor of HEAD; cannot find the base."
  exit 1
fi
for t in $(git tag --merged HEAD --list 'v*' | grep -E "$stable_re" || true); do
  # Only tags as new as the base or newer.
  [ "$(printf '%s\n%s\n' "$t" "$base" | sort -V | tail -n 1)" = "$t" ] || continue
  local_commit=$(git rev-parse "$t^{commit}")
  up=$(upstream_commit "$t")
  if [ -z "$up" ]; then
    echo "::error::Tag $t ($(short "$local_commit")) is reachable from HEAD but is no longer a tag in $upstream (withdrawn); refusing to fall back to $base."
    exit 1
  elif [ "$up" != "$local_commit" ]; then
    echo "::error::Tag $t is $(short "$local_commit") here but ${up:0:8} in $upstream (moved); refusing to fall back to $base. Fix the tag."
    exit 1
  fi
done
if [ -n "${PMX_EXPECT_BASE:-}" ]; then
  # The sync: the base must be exactly the given upstream tag, at the given commit.
  up=$(upstream_commit "$PMX_EXPECT_BASE")
  if [ -z "$up" ]; then
    echo "::error::Expected base $PMX_EXPECT_BASE is not a tag in $upstream."
    exit 1
  fi
  if [ -n "${PMX_EXPECT_BASE_COMMIT:-}" ] && [ "$up" != "$PMX_EXPECT_BASE_COMMIT" ]; then
    echo "::error::Expected base $PMX_EXPECT_BASE at ${PMX_EXPECT_BASE_COMMIT:0:8}, but upstream's tag is ${up:0:8}."
    exit 1
  fi
  if [ "$base" != "$PMX_EXPECT_BASE" ] || [ "$base_commit" != "$up" ]; then
    echo "::error::Expected base $PMX_EXPECT_BASE (${up:0:8}), but the base of HEAD is $base (${base_commit:0:8})."
    exit 1
  fi
fi
echo "Base: $base ($(short "$base_commit")), head: $(short "$head")"
if [ "$base_commit" = "$head" ]; then
  echo "::error::HEAD is the upstream release $base itself; there are no Provimux commits to check."
  exit 1
fi

# --- 2 and 3. Layer order and protected paths, per commit --------------------------
in_layer=0
first_layer=''
first_layer_commit=''
for c in $(git rev-list --reverse --topo-order "$base_commit..HEAD"); do
  if git log -1 --format='%(trailers:key=Provimux-Layer,valueonly)' "$c" | sed 's/[[:space:]]*$//' | grep -qx 'branding'; then
    if [ "$in_layer" -eq 0 ]; then in_layer=1; first_layer=$(short "$c"); first_layer_commit=$c; fi
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

# --- 5. The README commit ------------------------------------------------------------
# Every git read here is checked: a failure stops the guard (fatal), never an empty answer.
in_readme_paths() { local p; for p in "${readme_paths[@]}"; do [ "$1" = "$p" ] && return 0; done; return 1; }
fatal() { echo "::error::$*" >&2; echo "::error::$*"; exit 1; }
rtmp=$(mktemp -d "${TMPDIR:-/tmp}/provimux-guard.XXXXXX")
trap 'rm -rf "${rtmp:?}"' EXIT
# root_records COMMIT FILE: COMMIT's root tree entries, NUL-ended "<mode> <type> <oid>\t<name>".
root_records() { git ls-tree -z "$1^{tree}" >"$2" || fatal "Cannot read the tree of $1."; }
# root_entry COMMIT NAME: "<mode> <type> <oid>" of the root entry NAME, or "".
root_entry() {
  local e
  root_records "$1" "$rtmp/entry"
  while IFS= read -r -d '' e; do
    [ "${e#*$'\t'}" = "$2" ] && { printf '%s\n' "${e%%$'\t'*}"; return 0; }
  done <"$rtmp/entry"
  return 0
}
list=$(git rev-list --reverse --topo-order "$base_commit..HEAD") || fatal "Cannot list $base..HEAD."
readme_commits=''
for c in $list; do
  t=$(git log -1 --format='%(trailers:key=Provimux-Readme,valueonly)' "$c") || fatal "Cannot read commit $c."
  # shellcheck disable=SC2001 # a trailer value may span lines
  if sed 's/[[:space:]]*$//' <<<"$t" | grep -qx 'root'; then
    readme_commits="$readme_commits $c"
  fi
done
# shellcheck disable=SC2086 # full SHAs, split on purpose
set -- $readme_commits
if [ $# -gt 1 ]; then
  err "There are $# README commits (trailer 'Provimux-Readme: root') in $base..$(short "$head"): $(for c in "$@"; do printf '%s ' "$(short "$c")"; done); there must be exactly one."
elif [ $# -eq 0 ]; then
  [ "$require_readme" -eq 0 ] || err "No README commit (trailer 'Provimux-Readme: root') in $base..$(short "$head")."
else
  rc=$1
  rs="$(short "$rc") (\"$(subject "$rc")\")"
  t=$(git log -1 --format='%(trailers:key=Provimux-Layer,valueonly)' "$rc") || fatal "Cannot read commit $rc."
  # shellcheck disable=SC2001 # a trailer value may span lines
  sed 's/[[:space:]]*$//' <<<"$t" | grep -qx 'branding' ||
    err "The README commit $rs has no 'Provimux-Layer: branding' trailer."
  pl=$(git rev-list --parents -n 1 "$rc") || fatal "Cannot read commit $rc."
  if [ "$(wc -w <<<"$pl" | tr -d ' ')" -ne 2 ]; then
    err "The README commit $rs does not have exactly one parent."
  else
    # The root entries outside the set (name, mode, type, object id: a subtree's id covers
    # everything under it, an empty directory included) are its parent's, byte for byte;
    # and neither has a directory at a path of the set.
    for w in parent commit; do
      if [ "$w" = parent ]; then root_records "$rc^" "$rtmp/$w"; else root_records "$rc" "$rtmp/$w"; fi
      : >"$rtmp/$w.out"
      while IFS= read -r -d '' e; do
        name="${e#*$'\t'}"
        if in_readme_paths "$name"; then
          case "${e%%$'\t'*}" in *" tree "*) err "The README commit $rs: the $w has a directory at $name." ;; esac
        else
          printf '%s\0' "$e" >>"$rtmp/$w.out"
        fi
      done <"$rtmp/$w"
    done
    if ! cmp -s "$rtmp/parent.out" "$rtmp/commit.out"; then
      changed=$( { tr '\0' '\n' <"$rtmp/parent.out"; tr '\0' '\n' <"$rtmp/commit.out"; } | sort | uniq -u | sed 's/^[^	]*	//' | sort -u | paste -sd ',' - | sed 's/,/, /g')
      err "The README commit $rs changes the root entries $changed, which are not among: ${readme_paths[*]}."
    fi
  fi
  for p in "${readme_paths[@]}"; do
    e=$(root_entry "$rc" "$p")
    if [ "$p" = README.md ]; then
      case "$e" in "100644 blob "*) ;; *) err "The README commit $rs must leave README.md a regular file (100644 blob), not '${e:-absent}'." ;; esac
    elif [ -n "$e" ]; then
      err "The README commit $rs must leave $p absent."
    fi
  done
  # At HEAD: no other root README, of any case (a root README upstream adds, or a later
  # commit brings back).
  n=0
  root_records HEAD "$rtmp/head"
  while IFS= read -r -d '' e; do
    name="${e#*$'\t'}"
    # shellcheck disable=SC2018,SC2019 # ASCII only, on purpose
    lower=$(printf '%s' "$name" | LC_ALL=C tr 'A-Z' 'a-z')
    case "$lower" in readme*) ;; *) continue ;; esac
    n=$((n + 1))
    if [ "$name" != README.md ]; then
      err "HEAD has the root entry $(printf '%s' "${name//$'\n'/?}"); the only root README is README.md (the README commit's set: ${readme_paths[*]}). A root README upstream added or moved needs the owner (SYNC.md, \"Upstream added or moved a root README\")."
    else
      case "${e%%$'\t'*}" in "100644 blob "*) ;; *) err "README.md at HEAD must be a regular file (100644 blob), not '${e%%$'\t'*}'." ;; esac
    fi
  done <"$rtmp/head"
  [ "$n" -gt 0 ] || err "HEAD has no root README.md."
fi

# --- 6. Dates in the layer's tests -------------------------------------------------------
# A test that passes today and fails on a later date stops the automatic sync for nothing
# wrong in the code. Each line the layer adds to a test file (its commits' net diff) with a
# literal date must be covered by a "date-safe: <why>" comment: on the line itself, or on a
# line above it with no empty line between (one comment covers a block). The why says what
# keeps the date away from today's: a clock the test sets, a pure function, a fixture parsed.
# A line that is only a comment is not looked at, nor are lines the layer does not add
# (upstream's, the patches'); a sync's
# rebase does not change the layer's lines, so this rule cannot stop a sync by itself.
date_re='time\.Date\([[:space:]]*[0-9]{4}|(^|[^0-9A-Za-z_.-])(19|20)[0-9]{2}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])|time\.Unix(Milli|Micro|Nano)?\([[:space:]]*[1-9][0-9]{9}'
if [ -n "$first_layer_commit" ]; then
  git diff --name-only -z --diff-filter=AMR "$first_layer_commit^" HEAD -- ':(glob)**/*_test.go' ':(top,literal)test/provimux/' >"$rtmp/dated" ||
    fatal "Cannot list the layer's test files."
  while IFS= read -r -d '' f; do
    case "$f" in *_test.go) ;; *) continue ;; esac
    git show "HEAD:$f" >"$rtmp/dated.file" || fatal "Cannot read $f at HEAD."
    # The line numbers at HEAD that the layer added (hunk headers "@@ -a,b +c,d @@").
    git diff -U0 --no-color "$first_layer_commit^" HEAD -- "$f" >"$rtmp/dated.diff" || fatal "Cannot diff $f."
    awk '/^@@ / { split($3, a, ","); s = substr(a[1], 2) + 0; n = (2 in a) ? a[2] + 0 : 1; for (i = 0; i < n; i++) print s + i }' \
      "$rtmp/dated.diff" >"$rtmp/dated.added"
    # Each line's number and whether a date-safe comment covers it.
    awk '/^[[:space:]]*$/ { m = 0 } /date-safe: *[^ ]/ { m = 1 } { print NR, m }' "$rtmp/dated.file" >"$rtmp/dated.cover"
    # A line that is only a comment cannot reach the test.
    grep -nE "$date_re" "$rtmp/dated.file" | grep -vE '^[0-9]+:[[:space:]]*//' | cut -d: -f1 >"$rtmp/dated.hits" || true
    awk 'FILENAME == ARGV[1] { added[$1] = 1; next } FILENAME == ARGV[2] { cover[$1] = $2; next }
      ($1 in added) && cover[$1] != 1 { print $1 }' "$rtmp/dated.added" "$rtmp/dated.cover" "$rtmp/dated.hits" >"$rtmp/dated.bad"
    while IFS= read -r n; do
      err "$(printf '%s' "${f//$'\n'/?}"):$n: a literal date in a layer test with no \"date-safe: <why>\" comment over it. A test of ours never depends on today's date: put the time on a clock the test sets, or time.Now() plus a margin the test cannot cross; if the date never meets today's (a fixed clock, a pure function), say why in the comment (SYNC.md, \"Tests and the date\"). Line: $(sed -n "${n}p" "$rtmp/dated.file" | sed 's/^[[:space:]]*//' | cut -c1-160)"
    done <"$rtmp/dated.bad"
  done <"$rtmp/dated"
fi

# --- 4. Pull request: what it introduces relative to its target ---------------------
if [ -n "${PMX_TARGET:-}" ]; then
  target=$(git rev-parse "$PMX_TARGET^{commit}")
  echo "Pull request target: $(short "$target")"
  # What the PR's own commits change since the merge base.
  hits=$(git diff --name-only -z "$target...$head" -- "${pathspec[@]}" | names)
  # What merging it would change on the target: the merged tree against the target.
  # Not the branch tips: a PR that merged main earlier still has the old blob of a
  # protected file the target changed since, and a clean merge keeps the target's.
  # -z output: the merged tree's id, then (on a conflict) each conflicted file name.
  merge_out=$(mktemp)
  set +e
  git merge-tree --write-tree --no-messages --name-only -z "$target" "$head" >"$merge_out"
  merge_status=$?
  set -e
  if [ "$merge_status" -eq 1 ]; then
    conflicts=$({ IFS= read -r -d '' _ || true; names; } <"$merge_out" | sort -u | paste -sd ',' - | sed 's/,/, /g')
    err "This pull request does not merge cleanly into main ($(short "$target")); conflicts in: $conflicts. The guard checks what the merge would change, so a conflicting PR cannot pass: merge main into the branch (or rebase onto it), resolve the conflicts, and push again."
  elif [ "$merge_status" -ne 0 ]; then
    err "git merge-tree failed (exit $merge_status) merging $(short "$head") into $(short "$target"); cannot check what this pull request would change."
  else
    IFS= read -r -d '' tree <"$merge_out"
    hits=$(printf '%s\n%s\n' "$hits" "$(git diff --name-only -z "$target" "$tree" -- "${pathspec[@]}" | names)")
  fi
  rm -f "$merge_out"
  hits=$(sed '/^$/d' <<<"$hits" | sort -u)
  while IFS= read -r f; do
    [ -n "$f" ] && err "This pull request changes protected path $f relative to main ($(short "$target")). Changes under $protected_list reach main only as a rebased patch stack pushed by the owner or the sync, never through a pull request."
  done <<<"$hits"
fi

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "OK: $(git rev-list --count "$base_commit..HEAD") commits since $base ($(short "$base_commit")); layer starts at $first_layer; no protected path changed."
