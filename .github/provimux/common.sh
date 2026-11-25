# shellcheck shell=bash
# Shared by check.sh, report.sh, publish.sh and release-meta.sh (.github/provimux/SYNC.md). Sourced, not run.
# Needs git, jq, and gh (authenticated with at least read access) for the API helpers.

PMX_UPSTREAM_REPO="${PMX_UPSTREAM_REPO:-router-for-me/CLIProxyAPI}"
PMX_UPSTREAM_URL="${PMX_UPSTREAM_URL:-https://github.com/$PMX_UPSTREAM_REPO}"
PMX_REPO="${PMX_REPO:-ngvoicu/provimux}"
PMX_REPO_URL="${PMX_REPO_URL:-https://github.com/$PMX_REPO}"
PMX_STABLE_RE='^v[0-9]+\.[0-9]+\.[0-9]+$'
# shellcheck disable=SC2034 # used by the scripts that source this file
PMX_SHA_RE='^[0-9a-f]{40}$'

# ls_remote_tag LS-REMOTE-OUTPUT TAG: the commit TAG points to (peeled when annotated), or "".
ls_remote_tag() {
  local peeled plain
  peeled="$(awk -v r="refs/tags/$2^{}" '$2==r {print $1}' <<<"$1")"
  plain="$(awk -v r="refs/tags/$2" '$2==r {print $1}' <<<"$1")"
  echo "${peeled:-$plain}"
}

# remote_tag_commit URL TAG: the commit TAG points to on URL, or "".
remote_tag_commit() {
  ls_remote_tag "$(git ls-remote --tags "$1" "refs/tags/$2" "refs/tags/$2^{}")" "$2"
}

# upstream_release_state TAG: stable | prerelease | draft | missing | error:<message>.
# Only "stable" (a published release, neither draft nor prerelease) may be synced to.
upstream_release_state() {
  local json
  if ! json="$(gh api "repos/$PMX_UPSTREAM_REPO/releases/tags/$1" 2>&1)"; then
    case "$json" in
      *"Not Found"* | *"HTTP 404"*) echo missing ;;
      *) echo "error:$(head -n 1 <<<"$json")" ;;
    esac
    return 0
  fi
  jq -r 'if .draft then "draft" elif .prerelease then "prerelease"
         elif (.published_at // "") == "" then "draft" else "stable" end' <<<"$json"
}

# newest_stable_release: the highest vX.Y.Z among upstream's published, non-draft,
# non-prerelease releases (not "releases/latest", which the upstream owner can point anywhere).
newest_stable_release() {
  gh api "repos/$PMX_UPSTREAM_REPO/releases?per_page=100" \
    --jq '.[] | select((.draft | not) and (.prerelease | not) and ((.published_at // "") != "")) | .tag_name' |
    grep -E "$PMX_STABLE_RE" | sort -V | tail -n 1
}

# expected_assets VERSION (<base>-pmx.<n>): the files provimux-release.yml publishes.
expected_assets() {
  local p="provimux_$1"
  printf '%s\n' checksums.txt \
    "${p}_darwin_aarch64.tar.gz" "${p}_darwin_amd64.tar.gz" \
    "${p}_freebsd_aarch64_no-plugin.tar.gz" "${p}_freebsd_amd64.tar.gz" \
    "${p}_linux_aarch64.tar.gz" "${p}_linux_aarch64_no-plugin.tar.gz" \
    "${p}_linux_amd64.tar.gz" "${p}_linux_amd64_no-plugin.tar.gz" \
    "${p}_windows_aarch64.zip" "${p}_windows_amd64.zip"
}

# A provimux-check run (actions/runs/<id>[/attempts/<n>] JSON, --arg repo PMX_REPO) that
# publish.sh accepts: of this repository, on main, event schedule or workflow_dispatch,
# completed with conclusion success, with a commit SHA.
# shellcheck disable=SC2034,SC2016 # used by publish.sh; jq, not shell
PMX_CHECK_RUN_OK='select(.path == ".github/workflows/provimux-check.yml" and .head_branch == "main"
   and (.event == "schedule" or .event == "workflow_dispatch")
   and .status == "completed" and .conclusion == "success"
   and .repository.full_name == $repo and .head_repository.full_name == $repo
   and (.head_sha | test("^[0-9a-f]{40}$")))'

# Paths are NUL-delimited throughout (a file name may hold a newline): each record is
# "<mode> <blob> <path>" ended by a NUL byte.
#
# tree_files GIT_DIR COMMIT: COMMIT's files as such records, sorted: what a checkout of
# COMMIT holds (the release builds from a checkout). Submodules (mode 160000) are left out:
# neither a checkout without submodules nor git archive has their files.
tree_files() {
  git --git-dir="$1" ls-tree -r -z --full-tree "$2" |
    while IFS= read -r -d '' e; do
      local meta="${e%%$'\t'*}" path="${e#*$'\t'}" mode type oid
      read -r mode type oid <<<"$meta"
      [[ "$type" == blob ]] || continue
      printf '%s %s %s\0' "$mode" "$oid" "$path"
    done | LC_ALL=C sort -z
}

# dir_files DIR: the files under DIR (an extracted git archive) as the same records, the
# blob ids hashed as git would store them (no filters): 120000 for a symlink, 100755 for
# an executable file, 100644 otherwise.
dir_files() {
  local dir=$1 lst p h t
  lst="$(mktemp "${TMPDIR:-/tmp}/provimux-files.XXXXXX")"
  (
    cd "$dir" || exit 1
    find . -type f -print0 >"$lst"
    if [[ -s "$lst" ]]; then
      # git hash-object prints one id per file argument, in order.
      while IFS= read -r -d '' p <&3 && IFS= read -r h <&4; do
        p="${p#./}"
        if [[ -x "$p" ]]; then printf '100755 %s %s\0' "$h" "$p"; else printf '100644 %s %s\0' "$h" "$p"; fi
      done 3<"$lst" 4< <(xargs -0 git hash-object --no-filters -- <"$lst")
    fi
    find . -type l -print0 | while IFS= read -r -d '' p; do
      # The target exactly (command substitution would drop trailing newlines).
      t="$(readlink "$p"; echo x)"; t="${t%x}"; t="${t%$'\n'}"
      printf '120000 %s %s\0' "$(printf '%s' "$t" | git hash-object --no-filters --stdin)" "${p#./}"
    done
  ) | LC_ALL=C sort -z
  rm -f "$lst"
}

# nul_lines: NUL-ended records as one line each, for a report only: "\" is written "\\"
# and a newline "\n", so every record stays one line and two records never look alike.
nul_lines() {
  local r
  while IFS= read -r -d '' r; do
    r="${r//\\/\\\\}"; printf '%s\n' "${r//$'\n'/\\n}"
  done
}

# archive_matches GIT_DIR COMMIT DIR REPORT: DIR, extracted from `git archive COMMIT`, has
# exactly COMMIT's files (names, blob ids, modes); the records are compared byte for byte.
# An upstream .gitattributes with export-ignore or export-subst would make the tested tree
# differ from the checkout the release builds; then it fails (status 1) and REPORT lists
# the differences ("- " only in the commit, "+ " only in the archive; paths escaped as in
# nul_lines).
archive_matches() {
  local gd=$1 c=$2 dir=$3 report=$4
  tree_files "$gd" "$c" >"$report.want"
  dir_files "$dir" >"$report.got"
  if command cmp -s "$report.want" "$report.got"; then
    rm -f "$report.want" "$report.got"; : >"$report"; return 0
  fi
  nul_lines <"$report.want" >"$report.want.txt"
  nul_lines <"$report.got" >"$report.got.txt"
  { diff "$report.want.txt" "$report.got.txt" | sed -n 's/^< /- /p; s/^> /+ /p' | head -n 100; } >"$report"
  # Same lines but different bytes cannot happen (the escaping is one-to-one); say so if it does.
  [[ -s "$report" ]] || echo "the file lists differ (no line differs after escaping)" >"$report"
  rm -f "$report.want" "$report.got" "$report.want.txt" "$report.got.txt"
  return 1
}

# check_artifact RUN ATTEMPT DIR: the artifact provimux-check-<tag> that the verdict job of
# attempt ATTEMPT of RUN uploaded, downloaded and unpacked into DIR; prints "<artifact id>
# <name>". The attempt is given, never looked up here: the caller read it, in the same
# read as the run's success (publish.sh), or took it from the event and verified it
# (provimux-publish.yml).
# The binding: the verdict job (it runs no repository code) writes the id, name and digest
# of its own upload as its only annotation titled "provimux-check-artifact"; the jobs that
# run upstream's and the candidate's code can upload artifacts of any name in the same
# run, but cannot annotate the verdict job. The artifact must be that id, of RUN, with
# that name (and digest when both are known), unexpired. Any other artifact, of any name,
# is ignored. Fails (status 1, the reason on stderr) otherwise. Used by publish.sh and
# autopublish.sh alike. Needs gh with actions: read and checks: read, and unzip.
check_artifact() {
  local run=$1 attempt=$2 dir=$3 job ann id name digest meta
  [[ "$run" =~ ^[0-9]+$ && "$attempt" =~ ^[0-9]+$ ]] || { echo "check_artifact: run '$run' attempt '$attempt'" >&2; return 1; }
  job="$(gh api "repos/$PMX_REPO/actions/runs/$run/attempts/$attempt/jobs?per_page=100" \
    --jq '[.jobs[] | select(.name == "verdict")] | if length == 1 then .[0].id else "" end')" ||
    { echo "cannot list the jobs of run $run attempt $attempt" >&2; return 1; }
  [[ "$job" =~ ^[0-9]+$ ]] || { echo "run $run attempt $attempt has no single job \"verdict\"" >&2; return 1; }
  ann="$(gh api "repos/$PMX_REPO/check-runs/$job/annotations?per_page=100" \
    --jq '[.[] | select(.title == "provimux-check-artifact") | .message] | if length == 1 then .[0] else "" end')" ||
    { echo "cannot read the annotations of the verdict job $job" >&2; return 1; }
  read -r id name digest _ <<<"$ann" || true
  [[ "$id" =~ ^[0-9]+$ && "$name" =~ ^provimux-check-(none|v[0-9]+\.[0-9]+\.[0-9]+)$ ]] ||
    { echo "the verdict job $job of run $run attempt $attempt names no artifact (one annotation \"provimux-check-artifact\": <id> <name> [digest])" >&2; return 1; }
  meta="$(gh api "repos/$PMX_REPO/actions/artifacts/$id")" || { echo "cannot read artifact $id" >&2; return 1; }
  jq -e --arg run "$run" --arg name "$name" --arg digest "${digest:-}" \
    '(.workflow_run.id | tostring) == $run and .name == $name and (.expired | not)
     and ($digest == "" or (.digest // "") == "" or .digest == $digest)' <<<"$meta" >/dev/null ||
    { echo "artifact $id is not the verdict's $name of run $run (or it expired)" >&2; return 1; }
  mkdir -p "$dir"
  gh api "repos/$PMX_REPO/actions/artifacts/$id/zip" >"$dir/.artifact.zip" || { echo "cannot download artifact $id" >&2; return 1; }
  unzip -q -o "$dir/.artifact.zip" -d "$dir" || { echo "artifact $id is not a zip" >&2; return 1; }
  rm -f "$dir/.artifact.zip"
  echo "$id $name"
}

# runs_json TAG: the newest push runs of TAG in PMX_REPO (actions/runs API). The check
# job's token has no actions: read, so a refused API call is retried without a token
# (the repository is public).
runs_json() {
  gh api "repos/$PMX_REPO/actions/runs?event=push&branch=$1&per_page=50" 2>/dev/null ||
    curl -fsSL -H 'Accept: application/vnd.github+json' \
      "https://api.github.com/repos/$PMX_REPO/actions/runs?event=push&branch=$1&per_page=50" 2>/dev/null
}
# The provimux-release runs of PMX_REPO for the push of tag $tag at commit $sha, newest last.
PMX_RELEASE_RUNS='[.workflow_runs[] | select(.path == ".github/workflows/provimux-release.yml"
   and .event == "push" and .head_branch == $tag and .head_sha == $sha
   and .repository.full_name == $repo)] | sort_by(.created_at)'

# release_run_state TAG SHA: the state of the newest provimux-release run for the push of
# TAG at commit SHA (a run for another commit does not count):
# running | success | failure:<conclusion> | none | unknown.
release_run_state() {
  local s json
  json="$(runs_json "$1")" || { echo unknown; return 0; }
  s="$(jq -r --arg tag "$1" --arg sha "$2" --arg repo "$PMX_REPO" "$PMX_RELEASE_RUNS"' | last
        | if . == null then "none" else "\(.status) \(.conclusion // "")" end' <<<"$json")" ||
    { echo unknown; return 0; }
  case "$s" in
    none) echo none ;;
    "completed success") echo success ;;
    completed*) echo "failure:${s#completed }" ;;
    *) echo running ;;
  esac
}

# The words for a release_run_state in an issue: succeeded, running, failed, cancelled
# (any other conclusion as itself), none, unknown (cannot be read). PMX_RUN_TEXT is the
# same as a jq function: run_text.
run_state_text() { jq -rn --arg s "$1" "$PMX_RUN_TEXT"' $s | run_text'; }
PMX_RUN_TEXT='def run_text: if type != "string" then "unknown (cannot be read)" elif . == "success" then "succeeded"
  elif startswith("failure:") then (.[8:] | if . == "failure" then "failed" elif . == "" then "ended" else . end)
  elif . == "unknown" then "unknown (cannot be read)" else . end;'

# release_run_id TAG SHA: the id of that run, or "".
release_run_id() {
  local json
  json="$(runs_json "$1")" || return 0
  jq -r --arg tag "$1" --arg sha "$2" --arg repo "$PMX_REPO" "$PMX_RELEASE_RUNS"' | last | .id // empty' <<<"$json" || true
}

# release_state TAG (v<base>-pmx.<n>): complete | missing | draft | prerelease | partial:<missing files>.
# Complete = a published GitHub release (not a draft, not a prerelease) with every expected
# asset uploaded.
release_state() {
  local json have missing='' f
  if ! json="$(gh api "repos/$PMX_REPO/releases/tags/$1" 2>&1)"; then
    case "$json" in *"Not Found"* | *"HTTP 404"*) echo missing; return 0 ;; esac
    echo "error:$(head -n 1 <<<"$json")"; return 0
  fi
  [[ "$(jq -r '.draft' <<<"$json")" == "false" ]] || { echo draft; return 0; }
  [[ "$(jq -r '.prerelease' <<<"$json")" == "false" ]] || { echo prerelease; return 0; }
  [[ "$(jq -r '.published_at // ""' <<<"$json")" != "" ]] || { echo draft; return 0; }
  have="$(jq -r '.assets[] | select(.state == "uploaded") | .name' <<<"$json")"
  while IFS= read -r f; do
    grep -qxF "$f" <<<"$have" || missing="$missing $f"
  done < <(expected_assets "${1#v}")
  if [[ -n "$missing" ]]; then echo "partial:${missing# }"; else echo complete; fi
}

# sync_issues BASE: numbers of the open issues "sync: v<BASE>" or "sync: v<BASE> — ...", oldest first.
sync_issues() {
  gh api --paginate "repos/$PMX_REPO/issues?state=open&per_page=100" \
    --jq ".[] | select(.pull_request == null) | select(.title == \"sync: v$1\" or (.title | startswith(\"sync: v$1 \"))) | .number" |
    sort -n
}

# tap_issue TAG WHY [RUN_URL]: a Homebrew tap failure (prepare, verify or push) for the
# release TAG, in its own issue "homebrew tap: TAG needs the owner": opened, or commented
# on once per distinct WHY. Separate from "sync: v<base>": no reporter, sweep or notifier
# edits its title or closes it; the owner closes it after publish.sh --tap and tap.sh check
# pass (SYNC.md, "The Homebrew tap"). With PMX_NOTIFY_DRY_RUN it prints instead.
tap_issue() {
  local t=$1 why=$2 run=${3:-} title n body mk seen
  [[ "$t" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-pmx\.[0-9]+$ ]] || { echo "tap_issue: '$t' is not v<base>-pmx.<n>" >&2; return 1; }
  title="homebrew tap: $t needs the owner"
  mk="<!-- provimux-tap-issue $(printf '%s\n%s\n' "$t" "$why" | git hash-object --stdin) -->"
  body="$mk
**The Homebrew tap was not updated to \`$t\`.** No automation closes or retitles this issue. The tap only ever moves to the newest release; if a newer \`-pmx\` release exists by now, use that tag below instead (\`publish.sh --tap\` of an older tag leaves the tap alone). The owner, with their own credentials (SYNC.md, \"The Homebrew tap\"):

\`\`\`sh
bash .github/provimux/publish.sh --tap $t
bash .github/provimux/tap.sh check $t \"\$(mktemp -d)\"
\`\`\`

and closes this issue once \`tap.sh check\` prints \`ok\`.

Why:
~~~~text
$(printf '%s\n' "$why" | sed 's/~~~/~ ~ ~/g' | cut -c1-400 | tail -n 40)
~~~~
${run:+
Run: $run}"
  if [[ -n "${PMX_NOTIFY_DRY_RUN:-}" ]]; then printf 'tap_title=%s\n%s\n' "$title" "$body"; return 0; fi
  n="$(gh api --paginate "repos/$PMX_REPO/issues?state=open&per_page=100" \
    --jq ".[] | select(.pull_request == null) | select(.title == \"$title\") | .number" | sort -n | head -n 1)" || return 1
  if [[ -z "$n" ]]; then
    gh issue create -R "$PMX_REPO" --title "$title" --body "$body" >/dev/null || return 1
    echo "Opened \"$title\""
    return 0
  fi
  seen="$({ gh api "repos/$PMX_REPO/issues/$n" --jq '.body'; gh api --paginate "repos/$PMX_REPO/issues/$n/comments?per_page=100" --jq '.[].body'; } 2>/dev/null || true)"
  if grep -qF -- "$mk" <<<"$seen"; then
    echo "#$n already says this"
  else
    gh issue comment "$n" -R "$PMX_REPO" --body "$body" >/dev/null || return 1
    echo "Commented on #$n ($title)"
  fi
}

# releases_status BASE JSON REASONS: whether v<BASE> is released. Prints none | published
# | in-progress | incomplete | error; writes the per-tag states to the file JSON ([{tag,
# sha, release, run, newest}]) and appends reasons to the file REASONS. The NEWEST
# v<BASE>-pmx.<n> tag decides, because a later -pmx.<n> replaces an earlier one (the tap,
# the image and the "latest" release follow the newest): published = its GitHub release is
# complete (release_state) and the newest provimux-release run for that tag AT THE TAG'S
# COMMIT succeeded; in-progress while that run runs; incomplete otherwise. An earlier tag
# that failed (a failed -pmx.1 replaced by a good -pmx.2) is listed and named in REASONS
# as replaced, and does not block.
releases_status() {
  local base=$1 json=$2 rfile=$3 refs tags t sha s run newest st
  if ! refs="$(git ls-remote --tags "$PMX_REPO_URL" "refs/tags/v$base-pmx.*")"; then
    echo '[]' >"$json"; echo "cannot list the tags of $PMX_REPO_URL" >>"$rfile"; echo error; return 0
  fi
  tags="$(awk '{print $2}' <<<"$refs" | sed 's|^refs/tags/||; s/\^{}$//' | grep -E "^v$base-pmx\.[0-9]+$" | sort -u -V || true)"
  if [[ -z "$tags" ]]; then echo '[]' >"$json"; echo none; return 0; fi
  newest="$(tail -n 1 <<<"$tags")"
  : >"$json.tmp"
  for t in $tags; do
    sha="$(ls_remote_tag "$refs" "$t")"
    s="$(release_state "$t")"
    run="$(release_run_state "$t" "$sha")"
    jq -nc --arg tag "$t" --arg sha "$sha" --arg release "$s" --arg run "$run" --argjson newest "$([[ "$t" == "$newest" ]] && echo true || echo false)" \
      '{tag: $tag, sha: $sha, release: $release, run: $run, newest: $newest}' >>"$json.tmp"
    if [[ "$t" == "$newest" ]]; then
      if [[ "$s" == complete && "$run" == success ]]; then st=published
      elif [[ "$run" == running ]]; then st=in-progress
      else
        st=incomplete
        echo "tag $t (${sha:0:12}) exists but its release is $s and its release run for that commit is $(run_state_text "$run")" >>"$rfile"
      fi
    elif [[ "$s" != complete || "$run" != success ]]; then
      echo "tag $t (${sha:0:12}) did not complete (release $s, release run $(run_state_text "$run")); $newest replaces it" >>"$rfile"
    fi
  done
  jq -s '.' "$json.tmp" >"$json"
  rm -f "$json.tmp"
  echo "$st"
}

# sha256_of FILE: the file's SHA-256 (hex).
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

# run_artifact RUN ID NAME DIGEST DIR: downloads artifact ID and unpacks it into DIR, only
# when it is an unexpired artifact of RUN named NAME whose digest is DIGEST (hex, as
# upload-artifact's output artifact-digest gives it) and the zip downloaded has exactly
# that digest. The id and digest come from the producing job's own upload step (a job
# output), never from a lookup by name: an artifact of the same name that another job of
# the run uploads (or replaces, which gives a new id) is never read. Fails (status 1, the
# reason on stderr) otherwise. Needs gh with actions: read, and unzip.
run_artifact() {
  local run=$1 id=$2 name=$3 digest=$4 dir=$5 meta
  [[ "$run" =~ ^[0-9]+$ && "$id" =~ ^[0-9]+$ && "$digest" =~ ^[0-9a-f]{64}$ ]] ||
    { echo "no artifact id and digest for $name (id '$id', digest '$digest')" >&2; return 1; }
  meta="$(gh api "repos/$PMX_REPO/actions/artifacts/$id")" || { echo "cannot read artifact $id ($name)" >&2; return 1; }
  jq -e --arg run "$run" --arg name "$name" --arg d "sha256:$digest" \
    '(.workflow_run.id | tostring) == $run and .name == $name and (.expired | not) and .digest == $d' <<<"$meta" >/dev/null ||
    { echo "artifact $id is not $name of run $run with digest $digest (or it expired)" >&2; return 1; }
  mkdir -p "$dir"
  gh api "repos/$PMX_REPO/actions/artifacts/$id/zip" >"$dir/.artifact.zip" || { echo "cannot download artifact $id ($name)" >&2; return 1; }
  [[ "$(sha256_of "$dir/.artifact.zip")" == "$digest" ]] ||
    { echo "artifact $id ($name) downloaded is not digest $digest" >&2; rm -f "$dir/.artifact.zip"; return 1; }
  unzip -q -o "$dir/.artifact.zip" -d "$dir" || { echo "artifact $id ($name) is not a zip" >&2; return 1; }
  rm -f "$dir/.artifact.zip"
}

# ver_ge A B: version A >= version B (vX.Y.Z).
ver_ge() { [[ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -n 1)" == "$1" ]]; }

# upstream_base REV LS-REMOTE-OUTPUT: the base of REV, printed "<tag> <commit>": the newest
# vX.Y.Z tag of upstream (from LS-REMOTE-OUTPUT, upstream's own refs, not local tags) whose
# commit is an ancestor of REV. Fails (status 1, the reason on stderr) when there is none,
# or when a vX.Y.Z tag reachable from REV here, as new as that base or newer, is missing
# upstream (withdrawn) or points at another commit there (moved): never falls back.
# Same rule as guard.sh and release-meta.sh.
upstream_base() {
  local rev=$1 refs=$2 t c lc base='' base_commit=''
  while IFS= read -r t; do
    c="$(ls_remote_tag "$refs" "$t")"
    git cat-file -e "$c^{commit}" 2>/dev/null || continue
    if git merge-base --is-ancestor "$c" "$rev"; then base=$t base_commit=$c; break; fi
  done < <(awk '{print $2}' <<<"$refs" | sed 's|^refs/tags/||; s/\^{}$//' | grep -E "$PMX_STABLE_RE" | sort -u | sort -r -V)
  if [[ -z "$base" ]]; then
    echo "no vX.Y.Z tag of $PMX_UPSTREAM_REPO is an ancestor of $(git rev-parse --short "$rev")" >&2; return 1
  fi
  for t in $(git tag --merged "$rev" --list 'v*' | grep -E "$PMX_STABLE_RE" || true); do
    ver_ge "$t" "$base" || continue
    lc="$(git rev-parse "$t^{commit}")"
    c="$(ls_remote_tag "$refs" "$t")"
    if [[ -z "$c" ]]; then
      echo "tag $t (${lc:0:8}), reachable from $(git rev-parse --short "$rev"), is no longer a tag of $PMX_UPSTREAM_REPO (withdrawn); refusing to fall back to $base" >&2; return 1
    elif [[ "$c" != "$lc" ]]; then
      echo "tag $t is ${lc:0:8} here but ${c:0:8} in $PMX_UPSTREAM_REPO (moved); refusing to fall back to $base" >&2; return 1
    fi
  done
  echo "$base $base_commit"
}

# --- the README commit (design: .github/provimux/design/root-readme.md) -------------------
# One commit of the layer, the README commit (trailer "Provimux-Readme: root"), replaces
# upstream's root READMEs: README.md becomes Provimux's pointer, the translations are
# deleted. The set of its paths, root and literal; guard.sh has its own copy (selftest).
# Every git read below is checked: a failure of git is a failure of the function (status
# 1), never an empty answer.
PMX_README_PATHS=(README.md README_CN.md README_JA.md)

# in_readme_set NAME: NAME is a path of the set (whole name compared).
in_readme_set() {
  local p
  for p in "${PMX_README_PATHS[@]}"; do [[ "$1" == "$p" ]] && return 0; done
  return 1
}

# readme_commits RANGE GIT...: the commits of RANGE (A..B) whose trailer Provimux-Readme is
# "root": full SHAs, oldest first, one per line. GIT... is the git command (git, or
# git --git-dir=<repo>). Status 1 when git fails.
# shellcheck disable=SC2001 # a trailer value may span lines
readme_commits() {
  local range=$1 list c t; shift
  list="$("$@" rev-list --reverse "$range")" || return 1
  for c in $list; do
    t="$("$@" log -1 --format='%(trailers:key=Provimux-Readme,valueonly)' "$c")" || return 1
    if sed 's/[[:space:]]*$//' <<<"$t" | grep -qx 'root'; then echo "$c"; fi
  done
}

# readme_root_records COMMIT OUT GIT...: COMMIT's root tree entries as NUL-ended records
# "<mode> <type> <oid>\t<name>" (git ls-tree -z, not recursive) in the file OUT.
readme_root_records() {
  local c=$1 o=$2; shift 2
  "$@" ls-tree -z "$c^{tree}" >"$o"
}

# readme_entry COMMIT NAME GIT...: "<mode> <type> <oid>" of the root entry NAME of COMMIT's
# tree, or "" when there is none (NUL-delimited, whole names compared). Status 1 when git
# fails.
readme_entry() {
  local c=$1 name=$2 e f; shift 2
  f="$(mktemp "${TMPDIR:-/tmp}/provimux-readme-entry.XXXXXX")"
  readme_root_records "$c" "$f" "$@" || { rm -f "$f"; return 1; }
  while IFS= read -r -d '' e; do
    [[ "${e#*$'\t'}" == "$name" ]] && { printf '%s\n' "${e%%$'\t'*}"; break; }
  done <"$f"
  rm -f "$f"
}

# readme_outside A B GIT...: status 0 when the trees of commits A and B have the same root
# entries outside the set (name, mode, type and object id, byte for byte: a subtree's id
# covers everything under it, an empty directory included) and neither has a tree (a
# directory) at a path of the set; 1 otherwise or when git fails.
readme_outside() {
  local a=$1 b=$2 d c e rc=0; shift 2
  d="$(mktemp -d "${TMPDIR:-/tmp}/provimux-readme-out.XXXXXX")"
  for c in a b; do
    readme_root_records "${!c}" "$d/$c" "$@" || { rm -rf "${d:?}"; return 1; }
    : >"$d/$c.out"
    while IFS= read -r -d '' e; do
      if in_readme_set "${e#*$'\t'}"; then
        case "${e%%$'\t'*}" in *" tree "*) rc=1 ;; esac
      else
        printf '%s\0' "$e" >>"$d/$c.out"
      fi
    done <"$d/$c"
  done
  [[ $rc -eq 0 ]] && command cmp -s "$d/a.out" "$d/b.out" || rc=1
  rm -rf "${d:?}"
  return $rc
}

# readme_split COMMIT GIT...: COMMIT's raw object split into $rn_dir/<sha>.author (its
# author line) and $rn_dir/<sha>.msg (its message, every byte after the header). Status 1
# when git fails.
readme_split() {
  local c=$1 n; shift
  "$@" cat-file commit "$c" >"$rn_dir/$c.raw" || return 1
  n="$(sed '/^$/q' "$rn_dir/$c.raw" | wc -c | tr -d ' ')"
  tail -c +"$((n + 1))" "$rn_dir/$c.raw" >"$rn_dir/$c.msg"
  sed '/^$/q' "$rn_dir/$c.raw" | grep '^author ' >"$rn_dir/$c.author" || true
}

# readme_normal OLD NEW OLD_TIP NEW_TIP GIT...: status 0 when NEW is OLD re-applied:
#   1. OLD and NEW each have exactly one parent;
#   2. their author lines and messages are byte for byte the same (the committer may differ);
#   3. for every path of the set, NEW has what OLD has (mode, type and object, or no entry
#      in both);
#   4. NEW differs from its parent in no root entry outside the set (name, mode, type,
#      object id), and neither has a directory at a path of the set;
#   5. for every path of the set, NEW_TIP has what OLD_TIP has.
# Status 1 and the first failed condition (or git's failure) on stderr otherwise.
# shellcheck disable=SC2015 # "A && B || fail": B is an assignment, fail when either fails
readme_normal() {
  local o=$1 n=$2 ot=$3 nt=$4 c p x y pl rc=0; shift 4
  local rn_dir
  for c in "$o" "$n"; do
    pl="$("$@" rev-list --parents -n 1 "$c")" || { echo "README rule: cannot read ${c:0:12}" >&2; return 1; }
    [[ "$(wc -w <<<"$pl" | tr -d ' ')" == 2 ]] ||
      { echo "README rule, condition 1: ${c:0:12} does not have exactly one parent" >&2; return 1; }
  done
  rn_dir="$(mktemp -d "${TMPDIR:-/tmp}/provimux-readme.XXXXXX")"
  readme_split "$o" "$@" && readme_split "$n" "$@" || { echo "README rule: cannot read ${o:0:12} or ${n:0:12}" >&2; rm -rf "${rn_dir:?}"; return 1; }
  if ! command cmp -s "$rn_dir/$o.author" "$rn_dir/$n.author"; then
    echo "README rule, condition 2: ${n:0:12} has another author line than ${o:0:12}" >&2; rc=1
  elif ! command cmp -s "$rn_dir/$o.msg" "$rn_dir/$n.msg"; then
    echo "README rule, condition 2: ${n:0:12} has another message than ${o:0:12}" >&2; rc=1
  fi
  rm -rf "${rn_dir:?}"
  [[ $rc -eq 0 ]] || return 1
  for p in "${PMX_README_PATHS[@]}"; do
    x="$(readme_entry "$o" "$p" "$@")" && y="$(readme_entry "$n" "$p" "$@")" || { echo "README rule: cannot read the trees" >&2; return 1; }
    [[ "$x" == "$y" ]] || { echo "README rule, condition 3: ${n:0:12} does not leave $p as ${o:0:12} does" >&2; return 1; }
  done
  readme_outside "$n^" "$n" "$@" ||
    { echo "README rule, condition 4: ${n:0:12} changes a root entry outside $(printf '%s ' "${PMX_README_PATHS[@]}")(or a directory at one of them)" >&2; return 1; }
  for p in "${PMX_README_PATHS[@]}"; do
    x="$(readme_entry "$ot" "$p" "$@")" && y="$(readme_entry "$nt" "$p" "$@")" || { echo "README rule: cannot read the trees" >&2; return 1; }
    [[ "$x" == "$y" ]] || { echo "README rule, condition 5: the candidate's $p is not the old main's" >&2; return 1; }
  done
  return 0
}

# readme_one_sided SIDE RANGE GIT...: every commit of RANGE (A..B, oldest first) as a
# range-diff header line with full SHAs: SIDE "<" (only in the old range, dropped) or ">"
# (only in the new one), so the parsers of check.sh and publish.sh list it. Status 1 when
# git fails.
readme_one_sided() {
  local side=$1 range=$2 list c s i=0 none; shift 2
  none="$(printf '%040d' 0 | tr 0 -)"
  list="$("$@" rev-list --reverse "$range")" || return 1
  for c in $list; do
    i=$((i + 1))
    s="$("$@" log -1 --format=%s "$c")" || return 1
    if [[ "$side" == '<' ]]; then
      printf '%s:  %s < -:  %s %s\n' "$i" "$c" "$none" "$s"
    else
      printf -- '-:  %s > %s:  %s %s\n' "$none" "$i" "$c" "$s"
    fi
  done
}

# readme_segment OLD_RANGE NEW_RANGE GIT...: the range-diff of one segment on stdout. An
# empty range is never given to git range-diff (it refuses one): both empty, nothing;
# one empty, the other's commits one-sided. Status 1 when git fails (git range-diff's own
# status is kept).
readme_segment() {
  local a=$1 b=$2 na nb; shift 2
  na="$("$@" rev-list --count "$a")" && nb="$("$@" rev-list --count "$b")" || return 1
  if [[ "$na" == 0 && "$nb" == 0 ]]; then return 0
  elif [[ "$na" == 0 ]]; then readme_one_sided '>' "$b" "$@"
  elif [[ "$nb" == 0 ]]; then readme_one_sided '<' "$a" "$@"
  else "$@" range-diff --no-color --no-notes "$a" "$b"
  fi
}

# stack_range_diff OLD_BASE OLD_MAIN NEW_BASE CANDIDATE OUT GIT...: writes the stack's
# range-diff to OUT and prints "<R> <R'>" (or nothing when not both sides have a README
# commit). The only place the sync scripts run git range-diff.
#   - no README commit on either side, or on one side only: one git range-diff of the
#     whole ranges, as before (a README commit on one side only is "<" or ">" in it);
#   - one on each side, R and R': each range linear (no merge commit, each range queried
#     on its own); the normal form (readme_normal) is required; OUT holds the range-diff
#     of the segments below (<old base>..R^ against <new base>..R'^), the line
#     "README  <R> -> <R'>  <subject>" (which no header pattern matches), and the
#     segments above (R..<old main> against R'..<candidate>);
#   - more than one on either side, a merge commit in a range, or segments that do not
#     add up: OUT holds the whole-range range-diff, for the log.
# Status 1, the reason on stderr, when the README rule is broken or any git command fails
# (git range-diff of any segment, or of the whole ranges, included); OUT is then for the
# log only.
# shellcheck disable=SC2015 # "A && B || fail": B is an assignment, fail when either fails
stack_range_diff() {
  local ob=$1 om=$2 nb=$3 cand=$4 out=$5 ro rn no nn a b r spec m s c1 c2 c3; shift 5
  ro="$(readme_commits "$ob..$om" "$@")" && rn="$(readme_commits "$nb..$cand" "$@")" ||
    { echo "README rule: cannot list the commits of the ranges" >&2; return 1; }
  no="$(grep -c . <<<"$ro" || true)" nn="$(grep -c . <<<"$rn" || true)"
  if [[ "$no" -gt 1 || "$nn" -gt 1 ]]; then
    "$@" range-diff --no-color --no-notes "$ob..$om" "$nb..$cand" >"$out" || true
    echo "README rule: more than one README commit (old main: $no, candidate: $nn)" >&2; return 1
  fi
  if [[ "$no" == 0 || "$nn" == 0 ]]; then
    "$@" range-diff --no-color --no-notes "$ob..$om" "$nb..$cand" >"$out" ||
      { echo "git range-diff of $ob..$om and $nb..$cand failed" >&2; return 1; }
    return 0
  fi
  for spec in "$ob $om $ro" "$nb $cand $rn"; do
    read -r a b r <<<"$spec"
    # Each range on its own: git rev-list of two ranges would merge their exclusions.
    m="$("$@" rev-list --merges "$a..$b")" || { echo "README rule: cannot list the commits of $a..$b" >&2; return 1; }
    if [[ -n "$m" ]]; then
      "$@" range-diff --no-color --no-notes "$ob..$om" "$nb..$cand" >"$out" || true
      echo "README rule: ${a:0:12}..${b:0:12} has a merge commit" >&2; return 1
    fi
    c1="$("$@" rev-list --count "$a..$r^")" && c2="$("$@" rev-list --count "$r..$b")" && c3="$("$@" rev-list --count "$a..$b")" ||
      { echo "README rule: cannot count the commits of $a..$b" >&2; return 1; }
    [[ $((c1 + 1 + c2)) -eq $c3 ]] || {
      "$@" range-diff --no-color --no-notes "$ob..$om" "$nb..$cand" >"$out" || true
      echo "README rule: the segments around ${r:0:12} do not add up to ${a:0:12}..${b:0:12}" >&2; return 1
    }
  done
  : >"$out"
  readme_segment "$ob..$ro^" "$nb..$rn^" "$@" >>"$out" ||
    { echo "README rule: the range-diff of the segment below the README commit failed" >&2; return 1; }
  s="$("$@" log -1 --format=%s "$ro")" || { echo "README rule: cannot read ${ro:0:12}" >&2; return 1; }
  printf 'README  %s -> %s  %s\n' "$ro" "$rn" "$s" >>"$out"
  readme_segment "$ro..$om" "$rn..$cand" "$@" >>"$out" ||
    { echo "README rule: the range-diff of the segment above the README commit failed" >&2; return 1; }
  readme_normal "$ro" "$rn" "$om" "$cand" "$@" || return 1
  echo "$ro $rn"
}
