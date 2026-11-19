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
# notify, env: TAG (may be empty or invalid: then the one open "sync: v*" issue, or the
# issue "provimux-publish: failed"), OUTCOME (no-token | failed), WHY (the select job's
# reason, if any), LOG (the publish job's log, if any), PLAN (its plan.env, if any),
# EXPECT_MAIN (the checked main, if known), RUN_URL, GH_TOKEN with issues: write and
# actions: read. PMX_NOTIFY_DRY_RUN=1 prints instead.
#   It writes no recovery instruction from what the run believes: it reads the remote
#   first (main, v<base>, v<base>-pmx.* with each release and its release run) and says
#   what is true there and what to do about that:
#     a -pmx tag exists      released / release running / pushed, release not complete
#                            (rerun the release run; never publish again)
#     no -pmx tag, main is the checked main (or unknown)
#                            nothing was pushed: waits for the token, or refused (owner)
#     no -pmx tag, main is the candidate
#                            main pushed without its tags (owner: push the tags)
#     no -pmx tag, main is another commit
#                            main moved; nothing of this run is on it; the next check decides
#     the remote cannot be read
#                            state unknown (owner: look before acting)
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=common.sh
. "$here/common.sh"

out() { echo "$1=$2" >>"${GITHUB_OUTPUT:-/dev/null}"; echo "$1=$2"; }
skip() { out action skip; out why "$1"; exit 0; }
die() { echo "autopublish: $*" >&2; out action error; out why "$*"; exit 1; }

decide() {
  local tmp bound name tag status existing
  [[ "${PMX_CHECK_RUN:-}" =~ ^[0-9]+$ ]] || die "no check run id"
  [[ "${PMX_EXPECT_MAIN:-}" =~ $PMX_SHA_RE ]] || die "no expected main"
  [[ -z "${PMX_TAG_INPUT:-}" || "$PMX_TAG_INPUT" =~ $PMX_STABLE_RE ]] || die "input tag '${PMX_TAG_INPUT:-}' is not vX.Y.Z"
  if [[ -n "${PMX_TAG_INPUT:-}" ]]; then
    out tag "$PMX_TAG_INPUT"
    # A typed tag that already has a -pmx tag is refused outright (red), not skipped.
    existing="$(git ls-remote --tags "$PMX_REPO_URL" "refs/tags/${PMX_TAG_INPUT}-pmx.*")" ||
      die "cannot read the tags of $PMX_REPO_URL"
    [[ -z "$existing" ]] ||
      die "$PMX_TAG_INPUT already has $(awk '{print $2}' <<<"$existing" | sed 's|^refs/tags/||; s/\^{}$//' | sort -u | paste -sd ' ' -); a manual publish of it is refused (SYNC.md: \"Check a release\", \"Cut -pmx.2\")"
  fi
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
  [[ -n "${PMX_TAG_INPUT:-}" ]] || out tag "$tag"
  [[ -s "$tmp/a/result.json" ]] || die "$name has no result.json"
  status="$(jq -r '.status // "" | tostring' "$tmp/a/result.json")" || die "$name: result.json is not JSON"
  [[ "$(jq -r '.upstream.tag // ""' "$tmp/a/result.json")" == "$tag" ]] || die "$name: result.json is for another tag"
  [[ -z "${PMX_TAG_INPUT:-}" || "$PMX_TAG_INPUT" == "$tag" ]] ||
    die "run $PMX_CHECK_RUN checked $tag, not the requested $PMX_TAG_INPUT"
  [[ "$status" == pass ]] || skip "the check's status for $tag is '$(tr -cd 'a-z-' <<<"$status")', not pass"
  existing="$(git ls-remote --tags "$PMX_REPO_URL" "refs/tags/${tag}-pmx.*")" || die "cannot read the tags of $PMX_REPO_URL"
  [[ -z "$existing" ]] || skip "${tag}-pmx.* already exists"
  out action publish; out run "$PMX_CHECK_RUN"; out artifact "${bound%% *}"
  out why "check run $PMX_CHECK_RUN passed for $tag (verdict artifact ${bound%% *}, main $PMX_EXPECT_MAIN)"
}

notify() {
  local tag="${TAG:-}" base='' outcome="${OUTCOME:-failed}" log="${LOG:-}" expect="${EXPECT_MAIN:-}" cand=''
  local tmp refs main_now='' base_now='' rs='' title marker numbers number rid
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/provimux-notify.XXXXXX")"
  # shellcheck disable=SC2064 # expand now
  trap "rm -rf '${tmp:?}'" EXIT
  [[ "$expect" =~ $PMX_SHA_RE ]] || expect=''
  if [[ -n "${PLAN:-}" && -s "${PLAN:-}" ]]; then
    cand="$(sed -n 's/^CANDIDATE=//p' "$PLAN" | head -n 1)"; [[ "$cand" =~ $PMX_SHA_RE ]] || cand=''
  fi
  fence() { echo '~~~~text'; sed 's/~~~/~ ~ ~/g' | cut -c1-400; echo '~~~~'; }
  # why: the reasons this run gave (select's, then publish.sh's and the push step's).
  why() {
    [[ -z "${WHY:-}" ]] || printf '%s\n' "$WHY" | head -n 5
    if [[ -n "$log" && -s "$log" ]]; then grep -E '^publish: ' "$log" | tail -n 40 |
      sed -E 's|[^ ]*/provimux-publish\.[A-Za-z0-9]+|<tmp>|g' || true; fi
  }
  why >"$tmp/why.txt"
  [[ -s "$tmp/why.txt" ]] || echo "the run ended without a reason (see the run log)" >"$tmp/why.txt"

  # --- what is true on the remote ---
  : >"$tmp/facts.md"; : >"$tmp/todo.md"
  if [[ "$tag" =~ $PMX_STABLE_RE ]]; then
    base="${tag#v}"
    if refs="$(git ls-remote "$PMX_REPO_URL" refs/heads/main "refs/tags/$tag" "refs/tags/$tag^{}")"; then
      main_now="$(awk '$2 == "refs/heads/main" {print $1}' <<<"$refs")"
      base_now="$(ls_remote_tag "$refs" "$tag")"
      : >"$tmp/rel.txt"
      rs="$(releases_status "$base" "$tmp/rel.json" "$tmp/rel.txt")"
    else
      rs=error
    fi
  else
    tag=''
  fi
  what_main() {
    if [[ -z "$main_now" ]]; then echo "unknown"
    elif [[ -n "$cand" && "$main_now" == "$cand" ]]; then echo "\`$main_now\` (this run's candidate)"
    elif [[ -n "$expect" && "$main_now" == "$expect" ]]; then echo "\`$main_now\` (the checked main: unchanged)"
    else echo "\`$main_now\` (another commit)"; fi
  }
  if [[ -n "$tag" && "$rs" != error ]]; then
    {
      echo "Remote \`$PMX_REPO\` now:"
      echo
      echo "| ref | state |"
      echo "|---|---|"
      echo "| \`main\` | $(what_main) |"
      echo "| \`$tag\` | ${base_now:+\`$base_now\`}${base_now:-absent} |"
      jq -r '.[] | "| `\(.tag)` | `\(.sha[0:12])`; release: \(.release); release run for that commit: \(.run) |"' "$tmp/rel.json"
      [[ "$rs" != none ]] || echo "| \`$tag-pmx.*\` | absent |"
    } >"$tmp/facts.md"
  fi

  # --- title and what to do, from those facts ---
  if [[ -z "$tag" ]]; then
    title="provimux-publish: failed"
    echo "**provimux-publish failed before it knew the release.** Nothing was pushed by this run (it fails before its push step). The owner looks at the run; the next check retries." >"$tmp/head.md"
  elif [[ "$rs" == error ]]; then
    title="sync: $tag — publish failed, state unknown, owner needed"
    { echo "**provimux-publish failed and the remote could not be read.** Do not publish or rerun anything before checking:"
      echo; echo '```sh'; echo "git ls-remote $PMX_REPO_URL refs/heads/main 'refs/tags/$tag*'"; echo '```'
      echo "then follow SYNC.md, \"Check a release\" (a \`$tag-pmx.*\` tag exists) or \"Publishing by hand\" (none does)."; } >"$tmp/head.md"
  elif [[ "$rs" == published ]]; then
    title="sync: $tag — released"
    echo "**The release is complete** (every \`$tag-pmx.*\` release is public with all its assets and its release run succeeded); this run failed after that. Nothing to publish. The next check closes this issue." >"$tmp/head.md"
  elif [[ "$rs" == in-progress ]]; then
    title="sync: $tag — pushed, release running"
    echo "**\`main\` and the tags are pushed; the release run is still running.** Nothing to do now; the next check reports the outcome (SYNC.md, \"Check a release\"). Do not publish again." >"$tmp/head.md"
  elif [[ "$rs" == incomplete ]]; then
    title="sync: $tag — pushed, release not complete, owner needed"
    { echo "**\`main\` and the tags are pushed, but the release is not complete.** Do not publish again (an automatic retry skips the existing tag; \`publish.sh\` refuses it). The owner reruns the failed release jobs and checks the release (SYNC.md, \"Check a release\"):"
      echo; echo '```sh'
      jq -r '.[] | .tag' "$tmp/rel.json" | while IFS= read -r t; do
        rid="$(release_run_id "$t" "$(jq -r --arg t "$t" '.[] | select(.tag == $t) | .sha' "$tmp/rel.json")")"
        if [[ "$rid" =~ ^[0-9]+$ ]]; then echo "gh run rerun $rid --failed -R $PMX_REPO"; else echo "# $t: no release run for its commit; see SYNC.md, \"Check a release\""; fi
      done
      echo '```'; } >"$tmp/head.md"
  elif [[ -n "$cand" && "$main_now" == "$cand" ]]; then
    title="sync: $tag — main pushed without its tags, owner needed"
    { echo "**\`main\` is this run's candidate, but no \`$tag-pmx.*\` tag exists.** The owner pushes the tags (the same commits; SYNC.md, \"New upstream workflow files\", second push):"
      echo; echo '```sh'
      echo "git push provimux $(sed -n 's/^UPSTREAM_COMMIT=//p' "$PLAN" | head -n 1):refs/tags/$tag $cand:refs/tags/$tag-pmx.1"
      echo '```'; } >"$tmp/head.md"
  elif [[ -n "$expect" && -n "$main_now" && "$main_now" != "$expect" ]]; then
    title="sync: $tag — main moved, waits for the next check"
    echo "**\`main\` moved since the check; nothing of this run is on it.** No action needed: the next check (every 3 hours, or Actions → provimux-check) checks the new \`main\` and publishes it." >"$tmp/head.md"
  elif [[ "$outcome" == no-token ]]; then
    title="sync: $tag — ready, publishing waits for PROVIMUX_TOKEN"
    { echo "**Automatic publishing waits for the token.** The check passed, but the secret \`PROVIMUX_TOKEN\` of the environment \`provimux-publish\` is not set; nothing was pushed."
      echo
      echo "The owner either sets it (SYNC.md, \"The token\"; the next passing check publishes), or publishes by hand (SYNC.md, \"Publishing by hand\"):"
      echo; echo '```sh'; echo "bash .github/provimux/publish.sh $tag --dry-run"; echo "bash .github/provimux/publish.sh $tag"; echo '```'; } >"$tmp/head.md"
  else
    title="sync: $tag — publish refused, owner needed"
    { echo "**Automatic publishing refused; nothing was pushed** (the remote shows no \`$tag-pmx.*\` tag$([[ -n "$expect" ]] && echo " and \`main\` unchanged" || echo ", and this run stopped before its push step")). The owner reads the reasons, fixes what they name or decides (SYNC.md, \"Manual cases\"), then publishes by hand (SYNC.md, \"Publishing by hand\"):"
      echo; echo '```sh'; echo "bash .github/provimux/publish.sh $tag --dry-run"; echo "bash .github/provimux/publish.sh $tag"; echo '```'; } >"$tmp/head.md"
  fi
  {
    cat "$tmp/head.md"
    echo
    if [[ -s "$tmp/facts.md" ]]; then cat "$tmp/facts.md"; echo; fi
    echo "Why this run failed:"
    fence <"$tmp/why.txt"
    echo
    echo "Run: ${RUN_URL:-}"
  } >"$tmp/main.md"
  # The marker repeats only for the same title, facts and reasons: a check every 3 hours
  # that meets the same refusal does not comment again.
  marker="<!-- provimux-publish $(cat <(echo "$tag $title") "$tmp/facts.md" "$tmp/why.txt" | git hash-object --stdin) -->"
  cat <(echo "$marker") "$tmp/main.md" >"$tmp/body.md"

  if [[ -n "${PMX_NOTIFY_DRY_RUN:-}" ]]; then
    echo "title=$title"
    cat "$tmp/body.md"
    return 0
  fi
  if [[ -n "$tag" ]]; then
    numbers="$(sync_issues "$base")"
  else
    numbers="$(gh api --paginate "repos/$PMX_REPO/issues?state=open&per_page=100" \
      --jq '.[] | select(.pull_request == null) | select((.title | startswith("sync: v")) or .title == "provimux-publish: failed") | .number' | sort -n)"
    [[ "$(grep -c . <<<"$numbers" || true)" -le 1 ]] ||
      numbers="$(gh api --paginate "repos/$PMX_REPO/issues?state=open&per_page=100" \
        --jq '.[] | select(.pull_request == null) | select(.title == "provimux-publish: failed") | .number' | sort -n)"
  fi
  number="$(head -n 1 <<<"$numbers")"
  if [[ -z "$number" ]]; then
    gh issue create -R "$PMX_REPO" --title "$title" --body-file "$tmp/body.md"
    return 0
  fi
  [[ -z "$tag" ]] || gh issue edit "$number" -R "$PMX_REPO" --title "$title" >/dev/null
  if gh api --paginate "repos/$PMX_REPO/issues/$number/comments?per_page=100" --jq '.[].body' | grep -qF -- "$marker"; then
    echo "#$number already says this ($marker); title: $title"
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
