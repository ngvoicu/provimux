# Provimux sync

`main` is a CLIProxyAPI release tag, then the owner's patch commits, then the Provimux layer (commits with the trailer `Provimux-Layer: branding`). When CLIProxyAPI publishes a release, `.github/workflows/provimux-sync.yml` moves that whole stack onto it and publishes `v<base>-pmx.1`, but only when the result is no worse than the release itself. Otherwise it opens an issue and publishes nothing.

## What the workflow does

It runs every 3 hours, one run at a time, and can be started by hand (Actions → provimux-sync → Run workflow; inputs: `tag`, `dry_run`, which is ticked by default).

**The workflow ships disabled** (no schedule, no manual runs). Enable it with `gh workflow enable provimux-sync -R ngvoicu/provimux` once the secret `SYNC_TOKEN` exists and a review of the sync has passed.

1. **Release.** It reads the latest release of `router-for-me/CLIProxyAPI`, or uses the `tag` input. It stops quietly when this repository already has a `v<base>-pmx.*` tag for that release, or when the release is not newer than the base of `main`.
2. **Token.** A real run without the secret `SYNC_TOKEN` fails at this point, before it rebases anything.
3. **Fetch.** It fetches the upstream tag. It fails if the release adds a workflow file (see [New upstream workflow files](#new-upstream-workflow-files)).
4. **Rebase.** It runs `git rebase <tag>`, never `--onto`. Commits from a PR that upstream merged as it was drop out by themselves. A PR that upstream squashed or reworked stops the rebase at its first commit.
5. **Build.** It runs `go build ./...`.
6. **Tests.** It runs `go test` on every package whose Go files the stack and the layer change, once on the rebased tree and once on the pristine tag. A test that fails on Provimux and does not fail on the tag fails the sync, but only if it fails again when rerun. Tests that fail on the tag as well are listed and do not count. The gate is "no worse than upstream", because upstream does not gate its releases on tests.
7. **Guard.** It runs `.github/provimux/guard.sh` on the rebased `main`, the same check as on a push.
8. **Result.**
   - **Pass:** one atomic push with `SYNC_TOKEN`: `main` with `--force-with-lease=refs/heads/main:<old main>`, upstream's tag `v<base>`, and `v<base>-pmx.1` on the new `main`. The `-pmx` tag starts `provimux-release.yml` (archives, GitHub release, Homebrew tap) and `provimux-docker.yml`. The push to `main` starts `provimux-guard`. The run then closes the open `sync: v<base>` issue, if there is one.
   - **Fail:** the run opens one issue `sync: v<base>`, or updates it if it is already open. The issue gives the step, the commit where the rebase stopped with the conflicting files, or the tests that fail, plus a link to the run. Nothing is pushed. A dry run that fails opens the issue too.
   - **Dry run:** the run does every step except the push. The job summary lists the old and new `main`, the commits the rebase dropped, the test comparison and the tags it would push.

Each run that gets past step 1 keeps its logs (rebase, build, `go test -json` of both trees, guard) as the artifact `sync-v<base>`.

The logic lives in `.github/provimux/sync.sh` (steps 1 to 7, pushes nothing) and `.github/provimux/sync-issue.sh` (the issue). Both scripts run on a Mac as well.

## The token: `SYNC_TOKEN`

The workflow's own `GITHUB_TOKEN` can do neither push:

- a push or tag made with `GITHUB_TOKEN` starts no other workflow, so the release would never run;
- `GITHUB_TOKEN` is not a repository admin, so ruleset 27584853 (required check `provimux-guard`, which only admins bypass) refuses its push to `main`.

`SYNC_TOKEN` is therefore a **fine-grained personal access token of the owner** (a repository admin), stored as an Actions secret in `ngvoicu/provimux`:

- Resource owner `ngvoicu`; Repository access: **Only select repositories → `ngvoicu/provimux`**.
- Repository permissions:
  - **Contents: Read and write**, to push `main` (a force push) and the tags.
  - **Workflows: Read and write**, because the rebased `main` contains every upstream commit since the previous base. GitHub refuses a push that brings in commits changing `.github/workflows/*` unless the token has this permission.
  - **Metadata: Read-only**, which GitHub adds by itself.
- Nothing else. Issues are handled with `GITHUB_TOKEN` (`issues: write`). The ruleset bypass comes from the owner's admin role, not from a token permission.
- Expiration: fine-grained tokens expire. When this one does, real runs fail at the push with an authentication error, so renew it before then.

```sh
gh secret set SYNC_TOKEN -R ngvoicu/provimux   # paste the token
```

## Manual procedures

All the procedures below run in a scratch worktree, never in a clone where other work is in progress:

```sh
git fetch provimux main --tags
git worktree add --detach ../pmx-sync provimux/main && cd ../pmx-sync
```

Here `provimux` is the remote for `github.com/ngvoicu/provimux` (in Actions it is `origin`). Pushes from your own machine use your own credentials, so they bypass the ruleset and start the workflows just as `SYNC_TOKEN` does.

### Run the sync by hand

```sh
PMX_REMOTE=provimux PMX_SYNC_TAG=v8.0.78 bash .github/provimux/sync.sh
```

The script prints `status=pass|fail|skip|error`, `old`, `new`, `tag`, `base` and `out`. The directory `out` holds `summary.md`, `issue.md` (on a failure) and the logs. Leave out `PMX_SYNC_TAG` to use upstream's latest release (this needs `gh`). On `pass`, HEAD is the new `main`. Publish it with the same push the workflow makes:

```sh
git push --atomic --force-with-lease=refs/heads/main:<old> provimux \
  HEAD:refs/heads/main refs/tags/v<base>:refs/tags/v<base> HEAD:refs/tags/v<base>-pmx.1
```

Then close the issue, if there is one: `GH_REPO=ngvoicu/provimux bash .github/provimux/sync-issue.sh close v<base> "Published by hand."`

### Upstream took a patch in another form

The issue says the rebase stopped at a commit of the owner's PR #N, which upstream squashed or reworked. To continue, build a `main` without that PR's commits, still on the old base, then sync from there:

```sh
git log --oneline v<old base>..HEAD        # find PR #N's commits (all of them, not just the one where the rebase stopped)
git rebase -i v<old base>                  # delete their lines; keep everything else in order
bash .github/provimux/guard.sh             # still a valid stack
PMX_REMOTE=provimux PMX_SYNC_TAG=v<base> bash .github/provimux/sync.sh
```

The `old` the script prints is now your local commit. The lease must be against the `main` on GitHub, so use `--force-with-lease=refs/heads/main:$(git rev-parse provimux/main)` in the push above. If upstream's version only covers part of the PR, keep the remaining commits and resolve their conflicts during the `rebase -i`. Record what was dropped in the commit or the issue.

### Move a changed patch from its PR branch onto `main`

The owner's patches live as PR branches in the fork `ngvoicu/CLIProxyAPI` (remote `github` in the owner's clone). When a PR branch changes after review, `main` must get the new commits in place of the old ones, below the layer:

```sh
git fetch github <pr-branch>
layer=$(git log --reverse --format=%H --grep='^Provimux-Layer: branding$' v<base>..HEAD | head -n 1)
old=$(git rev-parse HEAD)
git checkout --detach "$layer^"            # the top of the patch stack
git rebase -i v<base>                      # delete the PR's old commits
git cherry-pick <first>^..<last>           # the PR branch's commits (rebase the branch onto v<base> first if it is based elsewhere)
git cherry-pick "$layer^..$old"            # the layer back on top
bash .github/provimux/guard.sh
go build ./... && go test ./<packages the PR touches>/...
git push --force-with-lease=refs/heads/main:$old provimux HEAD:refs/heads/main
```

Leave the fork's branches as they are. To release the change, cut the next `-pmx.<n>` (next section).

### Cut `-pmx.2` on the same base (a Provimux-only fix)

The sync never touches a base that already has a `v<base>-pmx.*` tag, so later releases on the same base are cut by hand:

1. Put the fix on `main`. A layer change is a commit ending with the trailer `Provimux-Layer: branding`, pushed on top of `main`. A patch change goes through the previous section.
2. Wait until `provimux-guard` passes on that commit.
3. Tag it and push the tag:

   ```sh
   git tag v<base>-pmx.2 <main sha>
   git push provimux refs/tags/v<base>-pmx.2
   ```

The tag starts `provimux-release.yml` and `provimux-docker.yml`. The Homebrew tap moves only forward, so `-pmx.2` replaces `-pmx.1`.

### New upstream workflow files

Upstream workflows stay in Provimux unchanged and are disabled one by one in the repository settings (`upstream-workflows.md`). A workflow file that is new in a release would be active as soon as it reaches `main`, and a tag-triggered one could run on the very tags the sync pushes. So the sync fails at the `workflows` step. To finish that sync by hand:

1. Run the sync by hand.
2. Push only `main`.
3. Run `gh workflow disable <name> -R ngvoicu/provimux` for each new workflow, and add a row for it to `upstream-workflows.md` (a layer commit).
4. Push the two tags.
