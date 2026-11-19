# Provimux sync

`main` is a CLIProxyAPI release tag, then the owner's patch commits, then the Provimux layer (commits with the trailer `Provimux-Layer: branding`). When CLIProxyAPI publishes a release, Provimux moves that whole stack onto it.

**Check and publish automatically; the owner only for what needs a decision.** A workflow checks each new release and reports in an issue. When the check passes, a second workflow publishes the release with the owner's token `PROVIMUX_TOKEN`, which only that workflow's one step running `publish.sh` can see. When it refuses or fails, it pushes nothing, says why in the issue, and the owner publishes by hand from their own Mac with their own credentials, as before.

| Piece | Where it runs | What it may do |
|---|---|---|
| `provimux-check.yml`, job `prepare` (`check.sh select`, `check.sh prepare`) | GitHub Actions | `contents: read`. git only: runs no repository code. |
| `provimux-check.yml`, jobs `test (candidate)` and `test (tag)` (`check.sh tree`, `check.sh test`) | GitHub Actions | `contents: read`. The only jobs that run upstream's and the candidate's code (build, tests), one tree per job. They write only raw `go` output and decide nothing. |
| `provimux-check.yml`, job `verdict` (`check.sh verdict`) | GitHub Actions | `contents: read`. Reads the raw output as data and writes `result.json`. Runs no repository code. |
| `provimux-check.yml`, job `report` (`report.sh`) | GitHub Actions | `issues: write`. Runs only the run's own scripts, never candidate code or `go`. |
| `provimux-publish.yml`, steps `Decide` and `Tell the sync issue` (`autopublish.sh`) | GitHub Actions | `actions: read`, `issues: write` (the run's `GITHUB_TOKEN`). Read the check run and its artifact as data; retitle and comment on the issue. git, gh and jq only. |
| `provimux-publish.yml`, step `Publish` (`publish.sh --ci`) | GitHub Actions | The only step that sees `PROVIMUX_TOKEN`, and only `git push` uses it. Runs `publish.sh` of `main`'s own commit: git, gh, jq and the guard of the current `main`; never candidate or upstream code, `go` or `make`. |
| `.github/provimux/publish.sh` by hand | the owner's Mac | Pushes `main` and the tags with the owner's `git` and `gh` credentials, when the automatic publish refused. |

## The check

It runs every 3 hours and can be started by hand (Actions → provimux-check → Run workflow, input `tag`; or `gh workflow run provimux-check -R ngvoicu/provimux -f tag=v8.0.78`). One run at a time. Every job checks out the run's own commit (`github.sha`, the run's `head_sha`) and runs that commit's scripts, so a run checks exactly the `main` it names.

**Job `prepare`** (`check.sh select`, then `check.sh prepare`; git and the API only):

1. **Release.** It takes the newest *stable* release of `router-for-me/CLIProxyAPI`: published, neither a draft nor a prerelease, with a `vX.Y.Z` tag. An explicit `tag` must be a stable release too. It records the commit the tag points to.
2. **Old base.** The base of `main` is the newest `vX.Y.Z` tag of upstream, read from upstream's refs (`git ls-remote`), whose commit is an ancestor of `main`; no local tag is needed. If a `vX.Y.Z` tag reachable from `main` here, as new as that base or newer, was withdrawn or moved upstream, the check fails: it never falls back to an older tag. `guard.sh` and `release-meta.sh` use the same rule.
3. **Already done?**
   - If `v<base>-pmx.*` exists, the status is `published` when, for every such tag, the GitHub release is public (not a draft, not a prerelease) with all its assets and the newest `provimux-release` run for that tag **at the tag's commit** succeeded; `in-progress` while such a run runs; and `incomplete` otherwise (that case is reported). A run for another commit does not count.
   - If `main` is already on the tag but no `-pmx` tag exists, the status is `unreleased` (reported).
   - If the tag does not descend from the old base, the check fails.
4. **Rebase.** It runs `git rebase --committer-date-is-author-date <tag commit>` in a temporary worktree, never `--onto`, so the same inputs give the same candidate commit.
5. **range-diff.** It runs `git range-diff <old base>..<old main> <new base>..<candidate>`. Patches that the rebase **dropped** (`<`) or **changed** (`!`) need the owner's decision.
6. **Workflow files.** It compares the complete path sets of `.github/workflows/` at the old base and at the new base. Every new path (added, renamed or copied) is listed, and `publish.sh` refuses such a release (see "New upstream workflow files").
7. **Guard.** It runs `guard.sh` on the candidate with `PMX_EXPECT_BASE=<tag>` and `PMX_EXPECT_BASE_COMMIT=<commit>`. The base the guard finds must be exactly that commit.

**Jobs `test (candidate)` and `test (tag)`**, one runner each: the tree (`check.sh tree`, from the upstream tag and the candidate bundle, without `.git`), then `check.sh test`: `go build ./...` (candidate), `go list ./...`, `go test -json ./...`, and on the candidate every package with a failing test again, as a whole package. They keep the raw output and the exit statuses, and decide nothing.

**Job `verdict`** (`check.sh verdict`) reads that output as data (`testevents.jq`, `testsum.jq`, `testcmp.jq`):

- `go build ./...` must succeed on the candidate, and every package must build, on the first run and on the package rerun. A rerun build failure always fails the check, whatever failed upstream.
- The exit status of each `go test` must match its results. Both trees must have a terminal result (pass, fail or skip) for every package that `go list ./...` lists.
- The candidate's failures are those of the first run **and of the rerun**: a test the first run never reached (an earlier test stopped the package) that fails on the rerun is a failure. Every failure (a test, or a package that failed without a failing test) fails the check, unless one of two waivers holds, and nothing else waives it:
  - **baseline:** the *same named* test (package and test name) failed on the pristine tag. An unrelated failure upstream waives nothing, and a **package-level** failure (build, setup, `TestMain`, a panic outside a test) is never waived by the tag, even if the tag's package failed too;
  - **flaky:** the whole-package rerun has an *explicit pass* for it: the rerun exited with a valid status, its package has a result and built, and the test passed (for a package-level failure: the whole package passed). A rerun that did not build, was killed, has no result for the test, or failed it, waives nothing.
- A test that the tag does not have counts against the candidate, because it is the owner's test.

The gate is "no worse than upstream", because upstream does not gate its releases on tests. Upstream always has failing tests of its own, so the waivers are exercised on every run; the issue lists them.

The verdict job uploads the artifact `provimux-check-<tag>`. An artifact name is unique in a run, and the run succeeds only if this upload does, so in a successful run that artifact is the verdict job's:

- `candidate.bundle`: a git bundle of `<tag commit>..candidate`;
- `result.json`: the status (`pass`, `fail`, `error`, `published`, `in-progress`, `incomplete`, `unreleased`) and why, the upstream tag and commit, the old `main` and its base, the candidate, the dropped and changed commits, the new workflow paths, the build, guard and test results, and the range-diff;
- `logs/`: rebase, range-diff, guard, build, and the test summaries. The raw `go test -json` output of each tree is in the artifacts `pmx-test-candidate` and `pmx-test-tag`.

**Job `report`** (`report.sh`, `if: always()`): reads the artifact, or notes that there is none, and keeps **one issue per base**, `sync: v<base> — <state>`. A `pass` counts only when every job of the run succeeded. `published`, `in-progress` and `incomplete` are never taken from `result.json`: `report.sh` reads the releases and their release runs itself (same rule as step 3), and closes the issue only when they are published and every job of the check succeeded.

| State | Meaning |
|---|---|
| ready to publish | The check passed. The issue gives the summary, the decisions needed and the publish command. |
| failed | The issue gives the step and the details: the commit where the rebase stopped and the conflicting files, the failing tests, the build output, the guard output, or a check job that ended without a result. |
| release incomplete | A `-pmx` tag exists, no release run is still running, and the release is incomplete or its `provimux-release` run did not succeed. |
| main on base, not released | `main` is on the base but no `-pmx` tag exists: the state between the two pushes of "New upstream workflow files". |

An open issue is updated in place. It gets a comment when its state or candidate changes. It is closed only when a check finds every `v<base>-pmx.*` release complete with a successful release run for the tag's commit, or when `publish.sh` has verified both.

## Publishing automatically: `provimux-publish.yml`

`provimux-publish.yml` runs when a `provimux-check` run completes (`workflow_run`). Its job runs only for a check run of this repository on `main`, started by `schedule` or `workflow_dispatch`, that concluded `success`, which is what `publish.sh` accepts. A check run is green for every status, so the job then decides from the artifact. Its permissions are `contents: read`, `actions: read` and `issues: write`, and the workflow has `concurrency: provimux-publish` with no cancelling, so one publish runs at a time.

1. **Checkout** of the check run's `head_sha`: the `main` that was checked, already on `main`. `persist-credentials: false`.
2. **Decide** (`autopublish.sh decide`, with `GITHUB_TOKEN`). It reads the check run again and its artifacts. The tag comes from the artifact's name `provimux-check-<vX.Y.Z>` and must match `result.json`'s tag; it never comes from free text. It skips the run (green, nothing done) when the check selected no release, when the status is not `pass` (`fail`, `published`, `in-progress`, ...: the report job already handled those), or when `v<base>-pmx.*` already exists (a second passing run, or a re-run). A malformed run or artifact fails the run.
3. **`PROVIMUX_TOKEN` is set**: if the secret is missing the run fails here, red, with that message. Nothing is pushed.
4. **Publish**: `publish.sh <tag> --ci --yes --run <check run id>`, everything in "Publishing by hand" below except the question. `--ci` makes git authenticate to github.com with `PMX_PUSH_TOKEN` (the secret) through a credential helper given in the environment (`GIT_CONFIG_*`), so the token is never written to disk, into a remote URL or to the log, and the script removes it from its environment right after the push. gh uses the run's `GITHUB_TOKEN`: reading the check run and its artifact, upstream's releases, the release run, closing the issue. On success it waits for the whole `provimux-release` run and the complete release, then closes the issue with the link to the release, exactly as by hand.
5. **Tell the sync issue** (`autopublish.sh notify`, `GITHUB_TOKEN`, only after a refusal or failure). It retitles `sync: v<base>` and comments with the `publish:` lines of the log:
   - `ready, publishing waits for PROVIMUX_TOKEN`: the secret is missing;
   - `publish refused, owner needed`: nothing was pushed (dropped or changed patches, new workflow files, remote `main` moved, upstream release withdrawn or moved, a verification mismatch, any failure before the push);
   - `pushed, release not complete`: the push happened but the release run failed or the wait timed out (see "Check a release").

   The run stays red. Each distinct reason is commented once, so the check that passes again every 3 hours and meets the same refusal changes only the title back. The next check rewrites the title to "ready to publish"; the next publish run sets it again.

A started `workflow_dispatch` (Actions → provimux-publish → Run workflow, on `main`) publishes one tag: input `tag`, optional `run` (else `publish.sh` takes the newest qualifying check run on the current `main`), and `dry_run` (default on: everything except the push, including `git push --dry-run` with the token; it does not wait and does not touch the issue). A dispatched run that is not a dry run tells the issue like an automatic one.

Why a token of the owner and not `GITHUB_TOKEN`: events caused by `GITHUB_TOKEN` start no workflow runs (except `workflow_dispatch` and `repository_dispatch`), so its tag push would start no `provimux-release` run; and `GITHUB_TOKEN` has no `workflows` permission, which pushing commits that change `.github/workflows/` needs (a rebase onto a new upstream release usually does). The owner's token also bypasses ruleset 27584853 as the owner's own pushes do (bypass: repository admins).

### The token

One fine-grained token of the owner serves every Provimux workflow that writes outside its own `GITHUB_TOKEN`: `provimux-publish.yml` (step `Publish`, push to `ngvoicu/provimux`) and `provimux-release.yml` (job `tap-publish`, push to `ngvoicu/homebrew-tap`; no checkout, no repository script). It is stored as the repository secret `PROVIMUX_TOKEN` (Settings → Secrets and variables → Actions, or `gh secret set PROVIMUX_TOKEN -R ngvoicu/provimux`); `ngvoicu/provimux-ui` gets the same secret when it needs it.

- **Fine-grained personal access token** (Settings → Developer settings → Fine-grained tokens), resource owner **ngvoicu**;
- **Repository access:** only `ngvoicu/provimux`, `ngvoicu/homebrew-tap`, `ngvoicu/provimux-ui`;
- **Repository permissions** (a fine-grained token has one set for all its repositories):
  - **Contents: Read and write**: the tap's formula commit; `main` and the tags of `provimux`;
  - **Workflows: Read and write**: the pushed `provimux` commits change `.github/workflows/` files (each rebase onto a new upstream release can);
  - **Metadata: Read**: required, added automatically.

  Nothing else for these two: gh in both workflows uses the run's `GITHUB_TOKEN`. Whatever `provimux-ui` needs is added when its workflow is written;
- **Expiration:** *No expiration* (GitHub allows it for a token of a personal account; an organization's policy could block it). With a date, set a reminder: when it expires, publishing fails at the push and the issue says so, and `tap-publish` fails.

The token can push `main` and `.github/workflows/` of `provimux`, so it is given only to steps that run no candidate or upstream code: the `Publish` step (`publish.sh` of `main`'s own commit) and the `tap-publish` job (inline steps on prepared files).

Without the secret, everything still works as before: each passing check opens "ready to publish" and the publish run retitles it "publishing waits for PROVIMUX_TOKEN"; the owner publishes by hand.

### What still needs the owner

The automatic publish only ever does what `publish.sh` would do unasked. These still need the owner, who then publishes by hand (next section) or follows the named procedure: a dropped or changed patch (`--accept-dropped`/`--accept-changed`); a release with new workflow files ("New upstream workflow files"); a failed check, a rebase conflict, failing tests ("Manual cases"); a release that did not complete ("Check a release"); a `-pmx.2` on the same base; the token, once (or when it expires).

## Publishing by hand: `publish.sh`

Run it on the owner's Mac from a clone of `ngvoicu/provimux`, such as a scratch worktree of `provimux/main`. It changes nothing in the clone: it works in a temporary repository that borrows the clone's objects. It runs no code of the candidate or of upstream: only `git`, `gh`, `jq` and the guard of the owner's current `main`.

```sh
git fetch provimux
git worktree add --detach ../pmx-publish provimux/main && cd ../pmx-publish
bash .github/provimux/publish.sh v8.0.78 --dry-run    # everything except the push
bash .github/provimux/publish.sh v8.0.78              # asks: type "publish v8.0.78"
cd - && git worktree remove ../pmx-publish
```

`publish.sh` works through these steps:

1. **Check run.** It takes the newest run with an artifact `provimux-check-<tag>` that is a `provimux-check` run of `ngvoicu/provimux`, on `main` (event `schedule` or `workflow_dispatch`), completed with conclusion `success`, and whose `head_sha` is the remote `main` now. `--run <id>` only picks among such runs. It downloads that artifact.
2. **Re-checks.** It refuses unless all of these hold:
   - `result.json` says `pass` (tests, build and guard ok) for this tag, and its old `main` is the run's `head_sha`;
   - the bundle's only head is the recorded candidate;
   - upstream's tag still points at the recorded commit and is still a stable release (and the old base tag still at its commit);
   - the remote `main` still is the recorded old `main`;
   - no `v<base>-pmx.*` exists yet.
3. **Verifies again locally.** `result.json` is not taken on trust for the structure. Locally, `publish.sh` checks that:
   - the new base descends from the old base, and the candidate from the new base, with no merge commits;
   - the range-diff and the new workflow paths, recomputed here, match `result.json`;
   - the `guard.sh` of the owner's current `main` (the remote `main`, which is the old `main` and the run's `head_sha`) passes on the candidate with the exact expected base. Never the candidate's copy.
4. **Decisions.** Dropped and changed patches are refused unless each one is named: `--accept-dropped <sha,...>` and `--accept-changed <sha,...>` take the old `main`'s commits, at least 7 hex digits each. A name that matches nothing is refused too. A release that adds or renames workflow files is refused outright: see "New upstream workflow files".
5. **Plan and confirmation.** It prints what it will push and asks for confirmation (`--yes` skips the question). After the confirmation it runs the re-checks of step 2 again (upstream's release state and tag commit, the remote `main`, no `-pmx` tag), immediately before pushing. Then it makes **one atomic push**, always all three refs:
   - `main`, with `--force-with-lease=refs/heads/main:<recorded old main>`, a literal SHA;
   - the tag `v<base>`, at upstream's commit;
   - the tag `v<base>-pmx.1`, at the candidate.

   The `-pmx` tag starts `provimux-release.yml` (archives, image, GitHub release, Homebrew tap).
6. **Wait and close.** It polls until the whole `provimux-release` run for `v<base>-pmx.1` at the candidate commit has finished, including the image push and `tap-publish`, then prints the run's conclusion and each job's result. It closes the issue only when that run succeeded and the GitHub release is published with all 11 assets (10 archives and `checksums.txt`). If the run fails or `--timeout` (120 minutes by default) runs out, it stops and the issue stays open.

The owner's credentials (or `PROVIMUX_TOKEN`, above) do what `GITHUB_TOKEN` could not: they bypass ruleset 27584853 (required check `provimux-guard`, admins only), they may push commits that change `.github/workflows/*` (gh's `workflow` scope), and their tag push starts the release workflows.

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

Provimux carries upstream's workflow files unchanged and disables each one in the repository settings (`upstream-workflows.md`). A new file would run as soon as it reaches `main` if it triggers on a push, and on the tags if it triggers on tags. A workflow can only be disabled once it exists on `main`, so `publish.sh` refuses such a release and the owner publishes it by hand, in two pushes, with Actions off in between.

In the owner's credentialed shell this procedure runs only `git`, `gh` and `jq`: never `go`, `go test`, `publish.sh`, `guard.sh` or any other script of the repository. The checks come from the CI check run, which built, tested and guarded this candidate. To repeat a check yourself, do it in a throwaway environment without credentials (see "Run the tests without credentials").

1. Take the facts from the check run, not from a local tree. The issue "ready to publish" links the run; it must be a successful `provimux-check` run on `main` whose `head_sha` is the remote `main`:

   ```sh
   run=<the run id from the issue>
   gh api repos/ngvoicu/provimux/actions/runs/$run --jq '[.path, .event, .head_branch, .conclusion, .head_sha] | @tsv'
   git ls-remote https://github.com/ngvoicu/provimux refs/heads/main          # must be that head_sha
   art=$(mktemp -d) && gh run download $run -R ngvoicu/provimux -n provimux-check-v<base> -D "$art"
   jq -r '.status, .upstream.commit, .old.main, .candidate, .decisions.new_workflows[]' "$art/result.json"
   ```

   `status` must be `pass` and `old.main` the `head_sha`. Note `upstream.commit` as `$up`, `old.main` as `$lease`, `candidate` as `$cand`.
2. Read each new file: `git fetch https://github.com/router-for-me/CLIProxyAPI tag v<base> && git show v<base>:<path>`.
3. Get the candidate from the bundle and look at it with git only:

   ```sh
   git fetch "$art/candidate.bundle" refs/pmx-check/candidate && [ "$(git rev-parse FETCH_HEAD)" = "$cand" ]
   git range-diff "$(jq -r .old.base_commit "$art/result.json")..$lease" "$up..$cand"
   ```
4. Turn Actions off for the repository. While it is off, no workflow runs, and pushes queue nothing:

   ```sh
   gh api -X PUT repos/ngvoicu/provimux/actions/permissions -F enabled=false
   ```
5. Push only `main`, with the literal lease:

   ```sh
   git push --atomic --force-with-lease=refs/heads/main:$lease https://github.com/ngvoicu/provimux "$cand:refs/heads/main"
   ```
6. Disable each new workflow and check that it shows `disabled_manually`. If a workflow is not listed, don't go on; leave Actions off and investigate.

   ```sh
   gh workflow disable <file name> -R ngvoicu/provimux
   gh workflow list -a -R ngvoicu/provimux
   ```
7. Turn Actions back on. With the new workflows disabled, nothing new can run:

   ```sh
   gh api -X PUT repos/ngvoicu/provimux/actions/permissions -F enabled=true -f allowed_actions=all
   ```
8. Check upstream's release once more, then push both tags in one atomic push. The `-pmx` tag starts `provimux-release.yml`:

   ```sh
   gh release view v<base> -R router-for-me/CLIProxyAPI --json isDraft,isPrerelease --jq '[.isDraft, .isPrerelease]'   # [false,false]
   git ls-remote https://github.com/router-for-me/CLIProxyAPI "refs/tags/v<base>" "refs/tags/v<base>^{}"           # still $up
   [ "$(git ls-remote https://github.com/ngvoicu/provimux refs/heads/main | cut -f1)" = "$cand" ] &&
     git push --atomic https://github.com/ngvoicu/provimux "$up:refs/tags/v<base>" "$cand:refs/tags/v<base>-pmx.1"
   ```

   Leave out `$up:refs/tags/v<base>` if Provimux already has that tag (at `$up`).
9. Check the release ("Check a release"). The next check closes the issue once it is complete.
10. Add a row for each new workflow to `upstream-workflows.md` (a layer commit, `Provimux-Layer: branding`).

Between steps 5 and 8, a check run finds `main` on the new base without a `-pmx` tag and opens "main on base, not released"; the check after step 8 replaces it.

### Run the tests without credentials

When a manual procedure needs `go test` or `go build`, run it in a throwaway container that has none of the owner's credentials (no home directory, no SSH agent, no keychain), on an exported copy of the tree:

```sh
git archive <commit> | docker run --rm -i golang:<go version> sh -c 'mkdir /src && tar -x -C /src && cd /src && go build ./... && go test ./...'
```

Or push the stack to a branch of a fork and let CI run it. Never in the shell that holds the owner's `git` and `gh` credentials.

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
git show "$lease:.github/provimux/guard.sh" >"${TMPDIR:-/tmp}/guard.sh" && bash "${TMPDIR:-/tmp}/guard.sh"   # main's guard, not the branch's
git archive HEAD | docker run --rm -i golang:<go version> sh -c 'mkdir /src && tar -x -C /src && cd /src && go build ./... && go test ./...'   # "Run the tests without credentials"
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

The tag starts `provimux-release.yml`. The Homebrew tap moves only forward, so `-pmx.2` replaces `-pmx.1`. From then on the check verifies both releases of that base on every run.

### Check a release

A release is complete when it is published (not a draft, not a prerelease) with all 11 assets and its `provimux-release` run (image push and tap included) succeeded:

```sh
gh release view v<base>-pmx.<n> -R ngvoicu/provimux --json isDraft,isPrerelease,assets --jq '{isDraft, isPrerelease, assets: [.assets[].name]}'
gh run list -R ngvoicu/provimux -w provimux-release -b v<base>-pmx.<n>
```

If the release run failed, rerun its failed jobs (`gh run rerun <id> --failed -R ngvoicu/provimux`). Until every release of the base is complete, each check reports "release incomplete". The next check after completion closes the issue.

## Testing the scripts

- `bash .github/provimux/selftest.sh` runs the fixture tests offline (local repositories, a stub `gh`): the test gate's waivers, the guard and `release-meta.sh` with a base only upstream or withdrawn or moved, `report.sh` with a release run for the wrong commit and with a failure, `autopublish.sh` (which check runs publish, the issue text after a refusal) and `publish.sh --ci` without its token. Run it after changing any of these scripts.
- `check.sh`, `report.sh`, `publish.sh`, `autopublish.sh` and `selftest.sh` run with macOS bash 3.2.
- `PMX_NOTIFY_DRY_RUN=1 TAG=v8.0.78 OUTCOME=failed LOG=<publish log> bash .github/provimux/autopublish.sh notify` prints what a refusal would write in the issue.
- `PMX_REPORT_DRY_RUN=1 RESULT=<result.json> CHECK_JOB=success bash .github/provimux/report.sh` prints the issue that a result would produce (it still reads the release state from GitHub).
- `check.sh select` and `check.sh prepare` run no repository code.
- `check.sh test` and `check.sh` (all steps) run `go test ./...`: only in a throwaway environment without credentials (above). Some tests use the proxy's default port 8317, so don't run it on a machine where the proxy is serving.
