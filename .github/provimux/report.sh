#!/usr/bin/env bash
# Provimux sync report (.github/provimux/SYNC.md), the "report" job of provimux-check.yml:
# turns the check's result.json (or its absence) into the one issue "sync: v<base>".
#
#   pass                     "sync: v<base> — ready to publish" (provimux-publish.yml then
#                            publishes it, or retitles the issue and says why it cannot)
#   fail, error, no result   "sync: v<base> — failed", with the step and the details
#   incomplete               "sync: v<base> — release incomplete" (a v<base>-pmx.* tag
#                            whose release is not complete and whose release run ended)
#   unreleased               "sync: v<base> — main on base, not released"
#   published                closes the open issue, only after this script verified it
#                            itself: for the newest v<base>-pmx.* tag (it replaces the
#                            earlier ones), the GitHub release is public (not a draft,
#                            not a prerelease) with all its assets
#                            and the newest provimux-release run for the tag AT THE TAG'S
#                            COMMIT succeeded (releases_status in common.sh), and every
#                            job of the check succeeded. Otherwise it reports what it found.
#   in-progress              nothing (a release run for the tag's commit is still running)
#
# Older bases: every other open "sync: v<other>" issue is checked too (sweep), since a
# check only looks at the newest stable release: its release complete -> closed; a
# release that did not complete -> "release incomplete (older base), owner needed" with
# the rerun command; no -pmx tag -> "not published" (the owner decides). Each state is
# commented once.
#
# One open issue per base: an open one is edited (title and body); a comment is added
# when the status or the candidate changed. result.json comes from a job that ran
# upstream's code, so it is data: the tag and SHAs are checked against patterns, and
# every free text is shown inside code blocks.
#
# Env: RESULT (path to result.json; may not exist), CHECK_JOB (the check job's result:
# success, failure, cancelled, ...), TAG_HINT (the tag the check selected, if any),
# RUN_URL, GH_TOKEN with issues: write on PMX_REPO, PMX_REPORT_DRY_RUN=1 to print only
# (the release state is still read from GitHub).
#
# Only issues titled "sync: v..." are read or written. The Homebrew tap's issue
# ("homebrew tap: <tag> needs the owner") is the owner's to close: never touched here.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=common.sh
. "$here/common.sh"

result="${RESULT:-}"
check_job="${CHECK_JOB:-unknown}"
run_url="${RUN_URL:-}"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/provimux-report.XXXXXX")"
trap 'rm -rf "${tmp:?}"' EXIT
body="$tmp/body.md"

if [[ -n "$result" && -s "$result" ]] && jq -e 'type == "object"' "$result" >/dev/null 2>&1; then
  r="$result"
else
  r="$tmp/result.json"
  jq -n --arg tag "${TAG_HINT:-}" --arg job "$check_job" \
    '{status: "error", step: "check job", upstream: {tag: $tag},
      reasons: ["the check job ended \"\($job)\" without a result.json (see the run log)"]}' >"$r"
fi
j() { jq -r "$1 // empty" "$r"; }
# isfalse PATH: the field is the boolean false (not "missing": `false // empty` is empty).
isfalse() { [[ "$(jq -r "$1 | tostring" "$r")" == false ]]; }

status="$(j .status)"
case "$status" in
  pass | fail | error | published | in-progress | incomplete | unreleased) ;;
  *) status=error ;;
esac
# A job that failed after writing "pass" is not a pass.
if [[ "$status" == pass && "$check_job" != success ]]; then
  status=error
  jq --arg job "$check_job" '.reasons += ["result.json says pass but the check job ended \"\($job)\""]' "$r" >"$tmp/r2" && r="$tmp/r2"
fi

tag="$(j .upstream.tag)"
[[ -n "$tag" ]] || tag="${TAG_HINT:-}"
if [[ "$tag" =~ $PMX_STABLE_RE ]]; then
  label="$tag"
else
  # Not a release tag (a bad manual input): a sanitized label, one issue per label.
  label="$(printf '%s' "$tag" | tr -cd 'A-Za-z0-9._-' | cut -c1-40)"
  [[ -n "$label" ]] || label="unknown"
  label="v${label#v}"
fi
base="${label#v}"

# published / in-progress / incomplete: verified here, never taken from result.json.
if [[ "$status" == published || "$status" == in-progress || "$status" == incomplete ]]; then
  : >"$tmp/rel-reasons.txt"
  if [[ "$label" =~ $PMX_STABLE_RE ]]; then
    rs="$(releases_status "$base" "$tmp/releases.json" "$tmp/rel-reasons.txt")"
  else
    rs=error; echo '[]' >"$tmp/releases.json"; echo "'$label' is not a release tag" >"$tmp/rel-reasons.txt"
  fi
  case "$rs" in
    published)
      if [[ "$check_job" != success ]]; then
        status=error
        echo "the releases look complete, but the check ended \"$check_job\"; not closing" >>"$tmp/rel-reasons.txt"
      else
        status=published
      fi
      ;;
    in-progress | incomplete) status=$rs ;;
    none) echo "result.json says $status, but no v$base-pmx.* tag exists" >>"$tmp/rel-reasons.txt"; status=error ;;
    *) status=error ;;
  esac
  jq --slurpfile rel "$tmp/releases.json" --rawfile why "$tmp/rel-reasons.txt" \
    '.release = $rel[0] | .reasons = ($why | split("\n") | map(select(length > 0)))' "$r" >"$tmp/r3" && r="$tmp/r3"
fi
sha() { local v; v="$(j "$1")"; [[ "$v" =~ $PMX_SHA_RE ]] && echo "$v" || echo ""; }
upstream_commit="$(sha .upstream.commit)"
old_main="$(sha .old.main)"
candidate="$(sha .candidate)"
old_base_tag="$(j .old.base_tag)"; [[ "$old_base_tag" =~ $PMX_STABLE_RE ]] || old_base_tag=''

# fence: free text as a code block that the text cannot close.
fence() { echo '~~~~text'; sed 's/~~~/~ ~ ~/g' | cut -c1-400; echo '~~~~'; }
list() { jq -r "$1" "$r" | fence; }
# warnings: the candidate's test warnings (check.sh verdict, testwarn.jq), if any. They
# never fail the check; the text comes from the candidate's test output (data, fenced).
warnings() {
  [[ "$(jq '.warnings // [] | length' "$r")" != 0 ]] || return 0
  echo "**Warnings from the candidate's tests** (reported, not counted; see SYNC.md, \"Warnings from the tests\"):"
  list '.warnings[] | "\(.test): \(.text)"'
}

# flaky_retries: the known flaky tests (flaky-tests.txt) that failed on the candidate, and
# each retry alone in a fresh process, with the count per test; a pass on any retry waived
# the failure. A test that needed its last retry gets a line of its own.
flaky_retries() {
  [[ "$(jq '.tests.known_flaky_retries // [] | length' "$r")" != 0 ]] || return 0
  echo "Known flaky tests that failed on the candidate (\`.github/provimux/flaky-tests.txt\`; waived only when a retry passed):"
  list '.tests.known_flaky_retries[] | "\(.test): \(.retries | length) of \(.allowed) retries used: \(.retries | if length == 0 then "not retried" else join(", ") end)\(if .waived then "" else " (counted as a failure)" end)"'
  jq -r '.tests.known_flaky_retries[] | select(.last_needed) | .test' "$r" | tr -cd 'A-Za-z0-9 ._/\n-' | while IFS= read -r t; do
    echo
    echo "**\`$t\` needed its last retry** ($(jq -r --arg t "$t" '.tests.known_flaky_retries[] | select(.test == $t) | "\(.allowed) of \(.allowed)"' "$r" | head -n 1 | tr -cd '0-9 of')): it may fail more often than its line in \`flaky-tests.txt\` says; measure it again (SYNC.md, \"Known flaky tests\")."
  done
  echo
}

marker="<!-- provimux-sync status=$status candidate=${candidate:-none} -->"
case "$status" in
  pass) title="sync: $label — ready to publish" ;;
  fail | error) title="sync: $label — failed" ;;
  incomplete) title="sync: $label — release incomplete" ;;
  unreleased) title="sync: $label — main on base, not released" ;;
  *) title='' ;;
esac

{
  echo "$marker"
  case "$status" in
    pass)
      echo "**Ready to publish:** Provimux \`main\` rebased onto CLIProxyAPI [\`$tag\`](https://github.com/$PMX_UPSTREAM_REPO/releases/tag/$tag) (\`${upstream_commit:0:12}\`). \`provimux-publish\` publishes it automatically once this check run has finished (SYNC.md, \"Publishing automatically\"); if it cannot, it says why here and the owner publishes by hand."
      echo
      echo "| | |"
      echo "|---|---|"
      echo "| old \`main\` | \`$old_main\` on \`$old_base_tag\` |"
      echo "| candidate | \`$candidate\`, $(j .commits) commits on \`$tag\` |"
      echo "| build | ok |"
      echo "| tests | $(jq -r '.tests | "candidate: \(.candidate.packages) packages, \(.candidate.passed) tests passed, \(.candidate.failed) failed; tag: \(.tag.packages) packages, \(.tag.passed) passed, \(.tag.failed) failed; new failures: \(.new_failures | length); flaky (passed on the package rerun): \(.flaky | length); known flaky tests (passed on a retry): \(.known_flaky // [] | length); failing on the tag too: \(.failing_on_tag_too | length)"' "$r" | tr -cd 'A-Za-z0-9 ,.;:()_-') |"
      echo "| guard | ok |"
      [[ "$(jq -r '.readme // null | type' "$r")" != object ]] || echo "| root READMEs | $(jq -r '.readme | "README commit \(.old[0:12]) -> \(.new[0:12]), in normal form" + (if .reapplied then "; re-applied at the rebase stop (" + (.conflicts | join(", ")) + ")" else "" end)' "$r" | tr -cd 'A-Za-z0-9 ,.;:()_>-') |"
      echo
      nd="$(jq '.decisions.dropped | length' "$r")"; nc="$(jq '.decisions.changed | length' "$r")"; nw="$(jq '.decisions.new_workflows | length' "$r")"
      if [[ "$nd$nc$nw" == "000" ]]; then
        echo "**Decisions needed:** none (no owner patch dropped or changed, no new workflow file)."
      else
        echo "**Decisions needed** before publishing: \`publish.sh\` refuses until each is accepted by name (SYNC.md), so the automatic publish refuses and the owner publishes by hand."
        echo
        if [[ "$nd" != 0 ]]; then echo "Patches the rebase **dropped** (accept with \`--accept-dropped <sha,...>\`):"; list '.decisions.dropped[] | "\(.commit[0:12]) \(.subject)"'; fi
        if [[ "$nc" != 0 ]]; then echo "Patches the rebase **changed** (accept with \`--accept-changed <sha,...>\`):"; list '.decisions.changed[] | "\(.commit[0:12]) \(.subject)"'; fi
        if [[ "$nw" != 0 ]]; then echo "**New workflow files** in \`$tag\`: \`publish.sh\` refuses this release; follow \"New upstream workflow files\" in SYNC.md:"; list '.decisions.new_workflows[]'; fi
      fi
      echo
      echo "**Publish by hand** only if the automatic publish refused or waits for its token (it says so here). On the owner's Mac, in a worktree of \`provimux/main\`, with the owner's own credentials: \`publish.sh\` authorizes the release and updates the Homebrew tap itself; see \`.github/provimux/SYNC.md\`:"
      echo
      echo '```sh'
      echo "bash .github/provimux/publish.sh $tag --dry-run"
      echo "bash .github/provimux/publish.sh $tag"
      echo '```'
      echo
      warnings
      if [[ "$(jq '.tests.flaky | length' "$r")" != 0 ]]; then echo "Flaky on the candidate (failed, then passed when the package was rerun):"; list '.tests.flaky[]'; fi
      flaky_retries
      if [[ "$(jq '.tests.failing_on_tag_too | length' "$r")" != 0 ]]; then echo "Failing on \`$tag\` too (not counted):"; list '.tests.failing_on_tag_too[]'; fi
      ;;
    fail | error)
      echo "**The sync check for CLIProxyAPI \`$label\` failed** at step **$(j .step | tr -cd 'A-Za-z0-9 _-')**. Nothing was published."
      echo
      echo "Why:"
      list '.reasons[]?'
      if isfalse .rebase.ok; then
        echo "The rebase stopped at:"
        list '.rebase | "\(.stopped_at[0:12]) \(.subject)\nconflicting files:\n" + (.conflicts | map("  " + .) | join("\n"))'
        echo "If upstream took this patch in another form, see \"Upstream took a patch in another form\" in SYNC.md."
      fi
      if isfalse .tests.ok; then
        echo "Tests:"
        list '.tests | (["new failures (fail on the candidate, also on the package rerun, not on the tag):"] + (.new_failures | map("  " + .))
                        + ["packages that do not build on the candidate:"] + (.candidate_build_failures | map("  " + .))
                        + ["packages without a result: candidate \(.missing_results.candidate), tag \(.missing_results.tag)"]
                        + ["exit status: candidate \(.candidate.exit), tag \(.tag.exit), package rerun \(.rerun.exit // "none")"]) | join("\n")'
      fi
      if isfalse .build.ok; then echo "Build (last lines):"; list '.build.tail'; fi
      flaky_retries
      warnings
      if isfalse .guard.ok; then echo "Guard:"; list '.guard.output'; fi
      echo "Fix it by hand following \`.github/provimux/SYNC.md\`. The next check (every 3 hours, or Actions → provimux-check → Run workflow) updates this issue."
      ;;
    incomplete)
      echo "**A Provimux tag for \`$label\` exists but its release is not complete**, and no release run is still running."
      echo
      list '.reasons[]?'
      echo "Releases:"
      list "$PMX_RUN_TEXT"' .release[]? | "\(.tag) (\(.sha[0:12] // "?")): release \(.release), release run for that commit \(.run | run_text)"'
      echo "Rerun the failed provimux-release run of the newest tag (\`gh run rerun <id> --failed -R $PMX_REPO\`), or cut the next \`-pmx.<n>\`; then check the release (\"Check a release\" in SYNC.md). A later check closes this issue once the newest \`v$base-pmx.*\` release is complete (an earlier one that failed does not count once a later one is complete)."
      ;;
    unreleased)
      echo "**\`main\` is already on \`$label\` but no \`v$base-pmx.*\` tag exists.** This is the state between the two pushes of \"New upstream workflow files\" in SYNC.md."
      echo
      list '.reasons[]?'
      echo "Finish that procedure (push the two tags, SYNC.md)."
      ;;
  esac
  echo
  echo "Run: $run_url"
  if [[ "$status" == pass || "$status" == fail ]] && [[ -n "$(j .range_diff)" ]]; then
    echo
    echo "<details><summary>git range-diff old base..old main vs new base..candidate</summary>"
    echo
    jq -r '.range_diff' "$r" | head -c 50000 | fence
    echo
    echo "</details>"
  fi
} >"$body"

# close_released N LABEL MESSAGE: closes issue N of a base whose newest release is
# complete. A sync issue is about the release only: the Homebrew tap has its own issue
# ("homebrew tap: <tag> needs the owner", common.sh tap_issue), which this script never
# reads, edits or closes. Prints what it does; with PMX_REPORT_DRY_RUN only prints.
close_released() {
  if [[ -n "${PMX_REPORT_DRY_RUN:-}" ]]; then echo "close #$1 ($2)"; return 0; fi
  gh issue close "$1" -R "$PMX_REPO" --comment "$3" >/dev/null
  echo "Closed #$1 ($2)"
}

# sweep: the open sync issues of other bases (see the header). Prints what it does; with
# PMX_REPORT_DRY_RUN only prints.
sweep() {
  local issues n t ob rs t2 mk m rid
  [[ "$label" =~ $PMX_STABLE_RE ]] || return 0
  issues="$(gh api --paginate "repos/$PMX_REPO/issues?state=open&per_page=100" \
    --jq '.[] | select(.pull_request == null) | select(.title | startswith("sync: v")) | "\(.number)\t\(.title)"')" || return 0
  while IFS=$'\t' read -r n t; do
    [[ "$n" =~ ^[0-9]+$ ]] || continue
    ob="$(sed -E 's/^sync: (v[0-9]+\.[0-9]+\.[0-9]+)( .*)?$/\1/' <<<"$t")"
    [[ "$ob" =~ $PMX_STABLE_RE && "$ob" != "$label" ]] || continue
    : >"$tmp/o-reasons.txt"
    rs="$(releases_status "${ob#v}" "$tmp/o-rel.json" "$tmp/o-reasons.txt")"
    case "$rs" in
      published)
        m="Verified: the newest \`$ob-pmx.*\` release is public with all its assets and its provimux-release run for the tag's commit succeeded. $run_url"
        echo "sweep: $(close_released "$n" "$ob" "$m")"
        continue ;;
      incomplete)
        t2="sync: $ob — release incomplete (older base), owner needed"
        { echo "**A Provimux release of the older base \`$ob\` did not complete.** The check now checks \`$label\` and does not look at \`$ob\` again; this issue stays open until its newest \`-pmx\` release is complete (a later check closes it then)."
          echo
          jq -r "$PMX_RUN_TEXT"' .[] | "- `\(.tag)` (`\(.sha[0:12])`): release \(.release), release run for that commit \(.run | run_text)"' "$tmp/o-rel.json"
          echo
          echo '```sh'
          jq -r '.[] | select(.newest) | "\(.tag) \(.sha)"' "$tmp/o-rel.json" | while read -r ot os; do
            rid="$(release_run_id "$ot" "$os")"
            if [[ "$rid" =~ ^[0-9]+$ ]]; then echo "gh run rerun $rid --failed -R $PMX_REPO"; else echo "# $ot: no release run for its commit; see SYNC.md, \"Check a release\""; fi
          done
          echo '```'; } >"$tmp/o-body.md" ;;
      none)
        t2="sync: $ob — not published; the check now checks $label"
        echo "**\`$ob\` was never published** (no \`$ob-pmx.*\` tag), and the check now checks the newer \`$label\`, so nothing publishes \`$ob\` automatically. The owner closes this issue, or publishes \`$ob\` by hand (SYNC.md)." >"$tmp/o-body.md" ;;
      *) continue ;;
    esac
    mk="<!-- provimux-sweep $(cat <(echo "$t2") "$tmp/o-body.md" | git hash-object --stdin) -->"
    if [[ -n "${PMX_REPORT_DRY_RUN:-}" ]]; then
      echo "sweep: #$n title=$t2"; cat "$tmp/o-body.md"; continue
    fi
    [[ "$t" == "$t2" ]] || gh issue edit "$n" -R "$PMX_REPO" --title "$t2" >/dev/null
    if ! gh api --paginate "repos/$PMX_REPO/issues/$n/comments?per_page=100" --jq '.[].body' | grep -qF -- "$mk"; then
      { echo "$mk"; cat "$tmp/o-body.md"; echo; echo "Run: $run_url"; } >"$tmp/o-comment.md"
      gh issue comment "$n" -R "$PMX_REPO" --body-file "$tmp/o-comment.md" >/dev/null
    fi
    echo "Swept #$n: $t2"
  done <<<"$issues"
}

# published: this base's open issues, each through close_released.
close_base() {
  local n any=''
  for n in $(sync_issues "$base"); do
    any=1
    close_released "$n" "$label" \
      "Verified: the newest \`v$base-pmx.*\` release is public with all its assets, and its provimux-release run for the tag's commit succeeded. $run_url"
  done
  [[ -n "$any" ]] || echo "Published; no open issue for $label."
}

if [[ -n "${PMX_REPORT_DRY_RUN:-}" ]]; then
  echo "status=$status title=${title:-<none>}"
  cat "$body"
  [[ "$status" != published ]] || close_base
  sweep
  exit 0
fi

numbers="$(sync_issues "$base")"
number="$(head -n 1 <<<"$numbers")"

case "$status" in
  published)
    close_base
    ;;
  in-progress)
    echo "Release run in progress for $label; nothing to report."
    ;;
  *)
    if [[ -n "$number" ]]; then
      before="$(gh api "repos/$PMX_REPO/issues/$number" --jq '.body' | head -n 1)"
      gh issue edit "$number" -R "$PMX_REPO" --title "$title" --body-file "$body" >/dev/null
      if [[ "$before" != "$marker" ]]; then
        gh issue comment "$number" -R "$PMX_REPO" --body "Now: **${title#sync: $label — }** (the description shows this run). $run_url" >/dev/null
      fi
      echo "Updated #$number: $title"
    else
      gh issue create -R "$PMX_REPO" --title "$title" --body-file "$body"
    fi
    ;;
esac

sweep
