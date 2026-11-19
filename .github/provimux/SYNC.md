# Provimux sync

`main` is a CLIProxyAPI release tag, then the owner's patch commits, then the Provimux layer (commits with the trailer `Provimux-Layer: branding`). When CLIProxyAPI publishes a release, Provimux moves that whole stack onto it.

**Check automatically, publish on the owner's command.** A workflow checks each new release and reports in an issue. Only the owner publishes, from their own Mac with their own credentials. No token of the owner is stored in GitHub.

| Piece | Where it runs | What it may do |
|---|---|---|
| `.github/workflows/provimux-check.yml`, job `check` (`check.sh`) | GitHub Actions | `contents: read`, nothing else. It runs upstream's and the candidate's code (build, tests). |
| `.github/workflows/provimux-check.yml`, job `report` (`report.sh`) | GitHub Actions | `issues: write`. It runs only the default branch's scripts, never candidate code or `go`. |
| `.github/provimux/publish.sh` | the owner's Mac | Pushes `main` and the tags with the owner's `git` and `gh` credentials. |

## The check

It runs every 3 hours and can be started by hand (Actions → provimux-check → Run workflow, input `tag`; or `gh workflow run provimux-check -R ngvoicu/provimux -f tag=v8.0.78`). One run at a time.

**Job `check`** (`check.sh select`, then `check.sh run`):

1. **Release.** It takes the newest *stable* release of `router-for-me/CLIProxyAPI`: published, neither a draft nor a prerelease, with a `vX.Y.Z` tag. An explicit `tag` must be a stable release too. It records the commit the tag points to.
2. **Old base.** The base of `main` is the newest `vX.Y.Z` tag reachable from it. It must be upstream's tag at the same commit; the check never falls back to an older tag.
3. **Already done?**
   - If `v<base>-pmx.*` exists, the status is `published` when every such release is complete, `in-progress` while a release run runs, and `incomplete` otherwise (that case is reported).
   - If `main` is already on the tag but no `-pmx` tag exists, the status is `unreleased` (reported).
   - If the tag does not descend from the old base, the check fails.
4. **Rebase.** It runs `git rebase --committer-date-is-author-date <tag commit>` in a temporary worktree, never `--onto`, so the same inputs give the same candidate commit.
5. **range-diff.** It runs `git range-diff <old base>..<old main> <new base>..<candidate>`. Patches that the rebase **dropped** (`<`) or **changed** (`!`) need the owner's decision.
6. **Workflow files.** It compares the complete path sets of `.github/workflows/` at the old base and at the new base. Every new path (added, renamed or copied) needs the owner's decision.
7. **Guard.** It runs `guard.sh` on the candidate with `PMX_EXPECT_BASE=<tag>` and `PMX_EXPECT_BASE_COMMIT=<commit>`. The base the guard reports must be exactly that commit.
8. **Build.** It runs `go build ./...` on the candidate.
9. **Tests.** It runs `go test -json ./...` on the candidate and on the pristine tag, each in a copy of the tree without `.git` (`testsum.jq`, `testcmp.jq`):
   - The exit status of `go test` is kept. Both trees must have a terminal result (pass, fail or skip) for every package that `go list ./...` lists.
   - Every candidate package with a failing test is rerun as a whole package.
   - A test fails the check when it fails on the candidate, fails again in the package rerun, and does not fail on the tag. A test that the tag does not have counts against the candidate, because it is the owner's test.
   - A package that does not build on the candidate fails the check.
   - Tests that also fail on the tag are listed but not counted.

   The gate is "no worse than upstream", because upstream does not gate its releases on tests.

The job uploads one artifact, `provimux-check-<tag>`:

- `candidate.bundle`: a git bundle of `<tag commit>..candidate`;
- `result.json`: the status (`pass`, `fail`, `error`, `published`, `in-progress`, `incomplete`, `unreleased`) and why, the upstream tag and commit, the old `main` and its base, the candidate, the dropped and changed commits, the new workflow paths, the build, guard and test results, and the range-diff;
- `logs/`: rebase, range-diff, guard, build, and `go test -json` of both trees and of the reruns.

**Job `report`** (`report.sh`, `if: always()`): reads the artifact, or notes that there is none, and keeps **one issue per base**, `sync: v<base> — <state>`:

| State | Meaning |
|---|---|
| ready to publish | The check passed. The issue gives the summary, the decisions needed and the publish command. |
| failed | The issue gives the step and the details: the commit where the rebase stopped and the conflicting files, the failing tests, the build output, the guard output, or a check job that ended without a result. |
| release incomplete | A `-pmx` tag exists but its release is incomplete and no release run is still running. |
| main on base, not released | The state after `publish.sh --main-only`. |

An open issue is updated in place. It gets a comment when its state or candidate changes. It is closed only when a check finds every `v<base>-pmx.*` release complete, or when `publish.sh` has verified the release.

## Publishing: `publish.sh`

Run it on the owner's Mac from a clone of `ngvoicu/provimux`, such as a scratch worktree of `provimux/main`. It changes nothing in the clone: it works in a temporary repository that borrows the clone's objects.

```sh
git fetch provimux
git worktree add --detach ../pmx-publish provimux/main && cd ../pmx-publish
bash .github/provimux/publish.sh v8.0.78 --dry-run    # everything except the push
bash .github/provimux/publish.sh v8.0.78              # asks: type "publish v8.0.78"
cd - && git worktree remove ../pmx-publish
```

`publish.sh` works through these steps:

1. **Artifact.** It downloads `provimux-check-<tag>` from the newest successful provimux-check run on `main` (or from `--run <id>`).
2. **Re-checks.** It refuses unless all of these hold:
   - `result.json` says `pass` (tests, build and guard ok) for this tag;
   - the bundle's only head is the recorded candidate;
   - upstream's tag still points at the recorded commit and is still a stable release;
   - the remote `main` still is the recorded old `main`;
   - no `v<base>-pmx.*` exists yet.
3. **Verifies again locally.** The check job ran untrusted code, so its claims are not taken on trust. Locally, `publish.sh` checks that:
   - the new base descends from the old base, and the candidate from the new base, with no merge commits;
   - the range-diff and the new workflow paths, recomputed here, match `result.json`;
   - the old `main`'s own `guard.sh` passes on the candidate with the exact expected base.
4. **Decisions.** Dropped patches, changed patches and new workflow paths are refused unless each one is named:
   - `--accept-dropped <sha,...>` and `--accept-changed <sha,...>` take the old `main`'s commits, at least 7 hex digits each;
   - `--accept-workflows <path,...>` takes the full paths.

   A name that matches nothing is refused too. New workflow files also require the two-step publish below.
5. **Plan and confirmation.** It prints what it will push and asks for confirmation (`--yes` skips the question). Then it makes **one atomic push**:
   - `main`, with `--force-with-lease=refs/heads/main:<recorded old main>`, a literal SHA;
   - the tag `v<base>`, at upstream's commit;
   - the tag `v<base>-pmx.1`, at the candidate.

   The `-pmx` tag starts `provimux-release.yml` and `provimux-docker.yml`.
6. **Wait and close.** It polls until the GitHub release `v<base>-pmx.1` is published with all 11 assets (10 archives and `checksums.txt`), then closes the issue. If the release run fails or `--timeout` (120 minutes by default) runs out, it stops and the issue stays open.

The owner's credentials do what no workflow token could: they bypass ruleset 27584853 (required check `provimux-guard`, admins only), they may push commits that change `.github/workflows/*` (gh's `workflow` scope), and their tag push starts the release workflows.

## Manual cases

Every procedure below runs in a scratch worktree, never in a clone where other work is in progress. Before changing `main`, **capture the remote `main` as a literal SHA** and use that value as the lease. Never use `provimux/main` resolved at push time, because a fetch in between would move it:

```sh
git fetch provimux
lease=$(git rev-parse provimux/main)      # e.g. 176f71c204cff20a48d82e922bbf294411d1c5e7
echo "$lease"                             # note it down
git worktree add --detach ../pmx-fix "$lease" && cd ../pmx-fix
```

Every push below uses `--force-with-lease=refs/heads/main:<that SHA>`. It is refused when someone else moved `main` in the meantime. If that happens, start again; don't re-read the lease.

### The rebase stops: upstream took a patch in another form

The issue says the rebase stopped at a commit of the owner's PR #N, which upstream squashed or reworked. Remove that PR's commits from `main` **on the old base**, push that `main`, and let the check rebase it:

```sh
git log --oneline v<old base>..HEAD             # find PR #N's commits (all of them)
git rebase -i v<old base>                       # delete their lines; keep the rest in order
bash .github/provimux/guard.sh                  # still a valid stack
git push --force-with-lease=refs/heads/main:$lease provimux HEAD:refs/heads/main
gh workflow run provimux-check -R ngvoicu/provimux -f tag=v<base>
```

If upstream's version covers only part of the PR, keep the remaining commits and resolve their conflicts during the `rebase -i`. The check's range-diff compares against the `main` you pushed. Record what was dropped and why in the issue.

### A patch was dropped or changed by the rebase

The issue's "decisions needed" lists the commits. A clean rebase drops a patch whose effect is already in the new tree, for example a patch that upstream cherry-picked and then reverted. In that case the fix is gone from Provimux. Read the range-diff in the issue, or in the artifact's `logs/range-diff.txt`:

- **Intended** (upstream has the fix, or the change is only context): publish with `--accept-dropped <sha>` or `--accept-changed <sha>` naming each commit.
- **Not intended:** fix the stack on the old base as in the previous section, push it, and run the check again.

### New upstream workflow files

Provimux carries upstream's workflow files unchanged and disables each one in the repository settings (`upstream-workflows.md`). A new file would run as soon as it reaches `main` if it triggers on a push, and on the tags if it triggers on tags. So never push `main` and then disable. Close the window first:

1. Read each new file in the issue's list (`git show <tag>:<path>`).
2. Turn Actions off for the repository. While it is off, no workflow runs, and pushes queue nothing:

   ```sh
   gh api -X PUT repos/ngvoicu/provimux/actions/permissions -F enabled=false
   ```
3. Push only `main`:

   ```sh
   bash .github/provimux/publish.sh v<base> --main-only --accept-workflows <path,...> [--accept-...]
   ```
4. Disable each new workflow and check that it shows `disabled_manually`. If a workflow is not listed, don't go on; leave Actions off and investigate.

   ```sh
   gh workflow disable <file name> -R ngvoicu/provimux
   gh workflow list -a -R ngvoicu/provimux
   ```
5. Turn Actions back on. With the new workflows disabled, nothing new can run:

   ```sh
   gh api -X PUT repos/ngvoicu/provimux/actions/permissions -F enabled=true -f allowed_actions=all
   ```
6. Push the tags. `--tags-only` requires the remote `main` to be the candidate. It then waits for the release and closes the issue:

   ```sh
   bash .github/provimux/publish.sh v<base> --tags-only --accept-workflows <path,...> [--accept-...]
   ```
7. Add a row for each new workflow to `upstream-workflows.md` (a layer commit, `Provimux-Layer: branding`).

Between steps 3 and 6, a check run finds `main` on the new base without a `-pmx` tag and opens "main on base, not released". `--tags-only` closes that issue.

### Move a changed patch from the fork `ngvoicu/CLIProxyAPI` onto `main`

The owner's patches live as PR branches in the fork `ngvoicu/CLIProxyAPI` (remote `github` in the owner's clone). When a PR branch changes after review, `main` must get the new commits in place of the old ones, below the layer. Start from the scratch worktree at `$lease` (above):

```sh
git fetch github <pr-branch>
base=v<base>                                               # main's current base
layer=$(git log --reverse --format=%H --grep='^Provimux-Layer: branding$' "$base..$lease" | head -n 1)
git checkout --detach "$layer^"                            # the top of the patch stack
git rebase -i "$base"                                      # delete the PR's old commits
git cherry-pick <first>^..<last>                           # the PR branch's commits (rebase the branch onto $base first if it is based elsewhere)
git cherry-pick "$layer^..$lease"                          # the layer back on top
bash .github/provimux/guard.sh
go build ./... && go test ./...                            # in a scratch worktree, not next to a running proxy
git range-diff "$base..$lease" "$base..HEAD"               # only the PR's commits differ
git push --force-with-lease=refs/heads/main:$lease provimux HEAD:refs/heads/main
```

Leave the fork's branches as they are. To release the change, cut the next `-pmx.<n>` (next section).

### Cut `-pmx.2` on the same base (a Provimux-only fix)

The check never builds a base that already has a `v<base>-pmx.*` tag, so later releases on the same base are cut by hand:

1. Put the fix on `main`. A layer change is a commit ending with the trailer `Provimux-Layer: branding`, pushed on top of `main` with the literal lease. A patch change goes through the previous section.
2. Wait until `provimux-guard` passes on that commit (`gh run list -R ngvoicu/provimux -w provimux-guard -c <sha>`).
3. Tag that exact commit and push the tag:

   ```sh
   sha=<the main commit that passed>
   [ "$(git ls-remote provimux refs/heads/main | cut -f1)" = "$sha" ] && git push provimux "$sha:refs/tags/v<base>-pmx.2"
   ```
4. Check the release (next section).

The tag starts `provimux-release.yml` and `provimux-docker.yml`. The Homebrew tap moves only forward, so `-pmx.2` replaces `-pmx.1`. From then on the check verifies both releases of that base on every run.

### Check a release

A release is complete when it is published (not a draft, not a prerelease) with all 11 assets:

```sh
gh release view v<base>-pmx.<n> -R ngvoicu/provimux --json isDraft,isPrerelease,assets --jq '{isDraft, isPrerelease, assets: [.assets[].name]}'
gh run list -R ngvoicu/provimux -w provimux-release -b v<base>-pmx.<n>
```

If the release run failed, rerun its failed jobs (`gh run rerun <id> --failed -R ngvoicu/provimux`). Until every release of the base is complete, each check reports "release incomplete". The next check after completion closes the issue.

## Testing the scripts

- `check.sh`, `report.sh` and `publish.sh` run with macOS bash 3.2.
- `PMX_REPORT_DRY_RUN=1 RESULT=<result.json> CHECK_JOB=success bash .github/provimux/report.sh` prints the issue that a result would produce.
- `check.sh select` alone runs no repository code.
- `check.sh run` runs `go test ./...`. Some tests use the proxy's default port 8317, so don't run it on a machine where the proxy is serving.
