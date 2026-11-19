# shellcheck shell=bash
# Shared by check.sh, report.sh and publish.sh (.github/provimux/SYNC.md). Sourced, not run.
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

# release_run_state TAG: the state of the newest provimux-release run for the push of TAG:
# running | success | failure:<conclusion> | none | unknown. The check job's token has no
# actions: read, so a refused API call is retried without a token (the repository is public).
release_run_state() {
  local q s json
  q='[.workflow_runs[] | select(.path == ".github/workflows/provimux-release.yml")] | sort_by(.created_at) | last
     | if . == null then "none" else "\(.status) \(.conclusion // "")" end'
  if ! json="$(gh api "repos/$PMX_REPO/actions/runs?event=push&branch=$1&per_page=20" 2>/dev/null)"; then
    json="$(curl -fsSL -H 'Accept: application/vnd.github+json' \
      "https://api.github.com/repos/$PMX_REPO/actions/runs?event=push&branch=$1&per_page=20" 2>/dev/null)" ||
      { echo unknown; return 0; }
  fi
  s="$(jq -r "$q" <<<"$json")" || { echo unknown; return 0; }
  case "$s" in
    none) echo none ;;
    "completed success") echo success ;;
    completed*) echo "failure:${s#completed }" ;;
    *) echo running ;;
  esac
}

# release_run_id TAG: the id of the newest provimux-release run for the push of TAG, or "".
release_run_id() {
  gh api "repos/$PMX_REPO/actions/runs?event=push&branch=$1&per_page=20" \
    --jq '[.workflow_runs[] | select(.path == ".github/workflows/provimux-release.yml")] | sort_by(.created_at) | last | .id // empty' 2>/dev/null || true
}

# release_state TAG (v<base>-pmx.<n>): complete | missing | draft | prerelease | partial:<missing files>.
# Complete = a published, non-draft GitHub release with every expected asset uploaded.
release_state() {
  local json have missing='' f
  if ! json="$(gh api "repos/$PMX_REPO/releases/tags/$1" 2>&1)"; then
    case "$json" in *"Not Found"* | *"HTTP 404"*) echo missing; return 0 ;; esac
    echo "error:$(head -n 1 <<<"$json")"; return 0
  fi
  [[ "$(jq -r '.draft' <<<"$json")" == "false" ]] || { echo draft; return 0; }
  [[ "$(jq -r '.prerelease' <<<"$json")" == "false" ]] || { echo prerelease; return 0; }
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
