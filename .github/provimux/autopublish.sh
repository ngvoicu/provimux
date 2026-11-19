#!/usr/bin/env bash
# Provimux automatic publish (.github/provimux/SYNC.md, "Publishing automatically"): the
# two steps of provimux-publish.yml around publish.sh that hold no write token.
#
#   autopublish.sh decide   whether to publish, and which tag. Reads the check run and
#                           its artifact's result.json (data) with gh; writes action=
#                           publish|skip, tag=, run=, why= to $GITHUB_OUTPUT.
#   autopublish.sh notify   after a refusal or failure: sets the title of the issue
#                           "sync: v<base>" and comments why (once per distinct reason).
#
# Runs only git, gh and jq: no code of the candidate or of upstream.
#
# decide, env: PMX_CHECK_RUN (the provimux-check run id the workflow's first step chose and
# verified), PMX_EXPECT_MAIN (the current main, = that run's head_sha, = the checkout),
# PMX_TAG_INPUT (workflow_dispatch: the tag the owner typed; it only confirms the tag of the
# run's result). GH_TOKEN with actions: read and checks: read.
#   Skip (green) unless the run is a successful provimux-check run of PMX_REPO on main
#   (schedule or workflow_dispatch) at PMX_EXPECT_MAIN whose verdict artifact (the one
#   its verdict job bound to itself: check_artifact in common.sh) is provimux-check-<tag>
#   with result.json status "pass" for that tag, and no v<base>-pmx.* exists yet; then
#   publish <tag> from that run. Exit 1 when the run or its artifact is malformed, or when
#   a typed tag differs from the result's.
# notify, env: TAG, OUTCOME (no-token | failed), LOG (publish.sh's output and the push
# step's message; may not exist), STATE (file the push step writes "pushed" to; may not exist),
# RUN_URL, GH_TOKEN with issues: write. PMX_NOTIFY_DRY_RUN=1 prints instead.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=common.sh
. "$here/common.sh"

out() { echo "$1=$2" >>"${GITHUB_OUTPUT:-/dev/null}"; echo "$1=$2"; }
skip() { out action skip; out why "$1"; exit 0; }
die() { echo "autopublish: $*" >&2; out action error; out why "$*"; exit 1; }

decide() {
  local tmp bound name tag status
  [[ "${PMX_CHECK_RUN:-}" =~ ^[0-9]+$ ]] || die "no check run id"
  [[ "${PMX_EXPECT_MAIN:-}" =~ $PMX_SHA_RE ]] || die "no expected main"
  [[ -z "${PMX_TAG_INPUT:-}" || "$PMX_TAG_INPUT" =~ $PMX_STABLE_RE ]] || die "input tag '${PMX_TAG_INPUT:-}' is not vX.Y.Z"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/provimux-autopublish.XXXXXX")"
  # shellcheck disable=SC2064 # expand now
  trap "rm -rf '${tmp:?}'" EXIT
  gh api "repos/$PMX_REPO/actions/runs/$PMX_CHECK_RUN" >"$tmp/run.json" || die "cannot read run $PMX_CHECK_RUN"
  jq -e --arg repo "$PMX_REPO" "$PMX_CHECK_RUN_OK" "$tmp/run.json" >/dev/null ||
    skip "run $PMX_CHECK_RUN is not a successful provimux-check run of $PMX_REPO on main"
  [[ "$(jq -r .head_sha "$tmp/run.json")" == "$PMX_EXPECT_MAIN" ]] ||
    die "run $PMX_CHECK_RUN checked $(jq -r .head_sha "$tmp/run.json"), not the current main $PMX_EXPECT_MAIN"
  bound="$(check_artifact "$PMX_CHECK_RUN" "$tmp/a" 2>"$tmp/err")" || die "run $PMX_CHECK_RUN: $(cat "$tmp/err")"
  name="${bound#* }"
  [[ "$name" != provimux-check-none ]] || skip "the check selected no release"
  tag="${name#provimux-check-}"
  [[ -s "$tmp/a/result.json" ]] || die "$name has no result.json"
  status="$(jq -r '.status // "" | tostring' "$tmp/a/result.json")" || die "$name: result.json is not JSON"
  [[ "$(jq -r '.upstream.tag // ""' "$tmp/a/result.json")" == "$tag" ]] || die "$name: result.json is for another tag"
  [[ -z "${PMX_TAG_INPUT:-}" || "$PMX_TAG_INPUT" == "$tag" ]] ||
    die "run $PMX_CHECK_RUN checked $tag, not the requested $PMX_TAG_INPUT"
  [[ "$status" == pass ]] || skip "the check's status for $tag is '$(tr -cd 'a-z-' <<<"$status")', not pass"
  if [[ -n "$(git ls-remote --tags "$PMX_REPO_URL" "refs/tags/${tag}-pmx.*")" ]]; then
    skip "${tag}-pmx.* already exists"
  fi
  out action publish; out tag "$tag"; out run "$PMX_CHECK_RUN"; out artifact "${bound%% *}"
  out why "check run $PMX_CHECK_RUN passed for $tag (verdict artifact ${bound%% *}, main $PMX_EXPECT_MAIN)"
}

notify() {
  local tag="${TAG:-}" base outcome="${OUTCOME:-failed}" log="${LOG:-}" title key marker numbers number tmp
  [[ "$tag" =~ $PMX_STABLE_RE ]] || { echo "autopublish: no valid tag ('$tag'); nothing to notify" >&2; return 0; }
  base="${tag#v}"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/provimux-notify.XXXXXX")"
  # shellcheck disable=SC2064 # expand now
  trap "rm -rf '${tmp:?}'" EXIT
  fence() { echo '~~~~text'; sed 's/~~~/~ ~ ~/g' | cut -c1-400; echo '~~~~'; }
  why() { if [[ -n "$log" && -s "$log" ]]; then grep -E '^publish: ' "$log" | tail -n 40 |
    sed -E 's|[^ ]*/provimux-publish\.[A-Za-z0-9]+|<tmp>|g' || true; fi; }
  if [[ "$outcome" == no-token ]]; then
    title="sync: $tag — ready, publishing waits for PROVIMUX_TOKEN"
    key="no-token"
    {
      echo "**Automatic publishing waits for the token.** The check passed, but the secret \`PROVIMUX_TOKEN\` of the environment \`provimux-publish\` is not set, so \`provimux-publish\` pushed nothing."
      echo
      echo "The owner either creates the token (SYNC.md, \"The token\"; the next passing check publishes), or publishes by hand (SYNC.md, \"Publishing by hand\"):"
    } >"$tmp/main.md"
  elif [[ -n "${STATE:-}" && "$(cat "$STATE" 2>/dev/null || true)" == pushed ]]; then
    title="sync: $tag — pushed, release not complete"
    key="pushed $(why)"
    {
      echo "**\`main\` and the tags were pushed, but the release did not finish.** The owner is needed: SYNC.md, \"Check a release\"."
      echo
      echo "Why:"
      why | fence
    } >"$tmp/main.md"
  else
    title="sync: $tag — publish refused, owner needed"
    key="refused $(why)"
    {
      echo "**Automatic publishing refused; nothing was pushed.** The owner is needed: read the reasons, fix what they name or decide (SYNC.md, \"Manual cases\"), then publish by hand (SYNC.md, \"Publishing by hand\")."
      echo
      echo "Why:"
      { why; [[ -n "$(why)" ]] || echo "publish.sh ended without a reason (see the run log)"; } | fence
    } >"$tmp/main.md"
  fi
  : >"$tmp/tail.md"
  [[ "$key" == pushed* ]] || {
    echo
    echo '```sh'
    echo "bash .github/provimux/publish.sh $tag --dry-run"
    echo "bash .github/provimux/publish.sh $tag"
    echo '```'
  } >"$tmp/tail.md"
  printf '\nRun: %s\n' "${RUN_URL:-}" >>"$tmp/tail.md"
  # The marker repeats only for the same outcome and reasons: a check every 3 hours that
  # meets the same refusal does not comment again.
  marker="<!-- provimux-publish $(printf '%s %s' "$tag" "$key" | git hash-object --stdin) -->"
  cat <(echo "$marker") "$tmp/main.md" "$tmp/tail.md" >"$tmp/body.md"

  if [[ -n "${PMX_NOTIFY_DRY_RUN:-}" ]]; then
    echo "title=$title"
    cat "$tmp/body.md"
    return 0
  fi
  numbers="$(sync_issues "$base")"
  number="$(head -n 1 <<<"$numbers")"
  if [[ -z "$number" ]]; then
    gh issue create -R "$PMX_REPO" --title "$title" --body-file "$tmp/body.md"
    return 0
  fi
  gh issue edit "$number" -R "$PMX_REPO" --title "$title" >/dev/null
  if gh api --paginate "repos/$PMX_REPO/issues/$number/comments?per_page=100" --jq '.[].body' | grep -qF -- "$marker"; then
    echo "#$number already says this ($marker); title set: $title"
  else
    gh issue comment "$number" -R "$PMX_REPO" --body-file "$tmp/body.md" >/dev/null
    echo "Commented on #$number: $title"
  fi
}

case "${1:-}" in
  decide) decide ;;
  notify) notify ;;
  *) echo "usage: $0 decide|notify" >&2; exit 2 ;;
esac
