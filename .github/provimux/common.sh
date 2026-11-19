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

# A provimux-check run (actions/runs/<id> JSON, --arg repo PMX_REPO) that publish.sh and
# autopublish.sh accept: of this repository, on main, event schedule or workflow_dispatch,
# completed with conclusion success, with a commit SHA.
# shellcheck disable=SC2034,SC2016 # used by publish.sh and autopublish.sh; jq, not shell
PMX_CHECK_RUN_OK='select(.path == ".github/workflows/provimux-check.yml" and .head_branch == "main"
   and (.event == "schedule" or .event == "workflow_dispatch")
   and .status == "completed" and .conclusion == "success"
   and .repository.full_name == $repo and .head_repository.full_name == $repo
   and (.head_sha | test("^[0-9a-f]{40}$")))'

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

# releases_status BASE JSON REASONS: whether every v<BASE>-pmx.* tag of PMX_REPO is
# released. Prints none | published | in-progress | incomplete | error; writes the
# per-tag states to the file JSON ([{tag, sha, release, run}]) and appends reasons to
# the file REASONS. Published = for every such tag, the GitHub release is complete
# (release_state) and the newest provimux-release run for the tag AT THE TAG'S COMMIT
# succeeded.
releases_status() {
  local base=$1 json=$2 rfile=$3 refs tags t sha s run st=published
  if ! refs="$(git ls-remote --tags "$PMX_REPO_URL" "refs/tags/v$base-pmx.*")"; then
    echo '[]' >"$json"; echo "cannot list the tags of $PMX_REPO_URL" >>"$rfile"; echo error; return 0
  fi
  tags="$(awk '{print $2}' <<<"$refs" | sed 's|^refs/tags/||; s/\^{}$//' | sort -u -V)"
  if [[ -z "$tags" ]]; then echo '[]' >"$json"; echo none; return 0; fi
  : >"$json.tmp"
  for t in $tags; do
    sha="$(ls_remote_tag "$refs" "$t")"
    s="$(release_state "$t")"
    run="$(release_run_state "$t" "$sha")"
    jq -nc --arg tag "$t" --arg sha "$sha" --arg release "$s" --arg run "$run" \
      '{tag: $tag, sha: $sha, release: $release, run: $run}' >>"$json.tmp"
    if [[ "$s" != complete || "$run" != success ]]; then
      if [[ "$run" == running ]]; then
        [[ "$st" == incomplete ]] || st=in-progress
      else
        st=incomplete
        echo "tag $t (${sha:0:12}) exists but its release is $s and its release run for that commit is $run" >>"$rfile"
      fi
    fi
  done
  jq -s '.' "$json.tmp" >"$json"
  rm -f "$json.tmp"
  echo "$st"
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
