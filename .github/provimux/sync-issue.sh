#!/usr/bin/env bash
# The one "sync: v<base>" issue per upstream release (PMX-14), used by provimux-sync.yml.
#   sync-issue.sh open  v<base> BODY-FILE   open it, or update the open one (new body + a comment)
#   sync-issue.sh close v<base> MESSAGE     comment and close it (every open one with that title)
# Needs gh with issues: write on $GH_REPO (default: the current repository).
set -euo pipefail
action=${1:?open or close}
tag=${2:?v<base>}
arg=${3:?body file or message}
title="sync: $tag"

# Open issues with exactly this title, oldest first. The REST list can lag a new issue
# by a few seconds; sync runs are hours apart and never overlap.
numbers="$(gh api --paginate "repos/{owner}/{repo}/issues?state=open&per_page=100" \
  --jq ".[] | select(.pull_request == null and .title == \"$title\") | .number" | sort -n)"
number="$(head -n 1 <<<"$numbers")"

case "$action" in
  open)
    if [[ -n "$number" ]]; then
      gh issue edit "$number" --body-file "$arg" >/dev/null
      gh issue comment "$number" --body "Failed again; the description now shows the latest run. $(tail -n 1 "$arg")" >/dev/null
      echo "Updated issue #$number: $title"
    else
      url="$(gh issue create --title "$title" --body-file "$arg")"
      echo "Opened $url"
    fi
    ;;
  close)
    if [[ -n "$numbers" ]]; then
      for n in $numbers; do
        gh issue close "$n" --comment "$arg" >/dev/null
        echo "Closed issue #$n: $title"
      done
    else
      echo "No open issue \"$title\"."
    fi
    ;;
  *) echo "usage: $0 open|close v<base> ARG" >&2; exit 2 ;;
esac
