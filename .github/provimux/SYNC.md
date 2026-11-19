# Provimux sync

`main` is a CLIProxyAPI release tag, then the owner's patch commits, then the Provimux layer (commits with the trailer `Provimux-Layer: branding`). When CLIProxyAPI publishes a release, Provimux moves that whole stack onto it.

**Check and publish automatically; the owner only for what needs a decision.** A workflow checks each new release and reports in an issue. When the check passes, a second workflow, started only by that check run's completion, publishes the release with the owner's token `PROVIMUX_TOKEN`, a secret of the environment `provimux-token` that only one fixed `git push` step sees. When it refuses or fails, it pushes nothing and says why in the issue; the owner decides, and publishes by hand from their own Mac with their own credentials when that is the fix. "Publish now" is a fresh check run (Actions → provimux-check), never a hand-started publish run.

| Piece | Where it runs | What it may do |
|---|---|---|
| `provimux-check.yml`, job `prepare` (`check.sh select`, `check.sh prepare`) | GitHub Actions | `contents: read`. git only: runs no repository code. |
| `provimux-check.yml`, jobs `test (candidate)` and `test (tag)` (`check.sh tree`, `check.sh test`) | GitHub Actions | `contents: read`. The only jobs that run upstream's and the candidate's code (build, tests), one tree per job. They write only raw `go` output and decide nothing. |
| `provimux-check.yml`, job `verdict` (`check.sh verdict`) | GitHub Actions | `contents: read`. Reads the raw output as data and writes `result.json`. Runs no repository code. |
| `provimux-check.yml`, job `report` (`report.sh`) | GitHub Actions | `issues: write`. Runs only the run's own scripts, never candidate code or `go`. |
| `provimux-publish.yml`, job `publish`, step `Verify` (inline, before any checkout) | GitHub Actions, environment `provimux-token` | `contents`, `actions`, `checks: read`, no secret. Proves this workflow revision is the live `main`, the check run's exact attempt passed on it, and that attempt's verdict binds one artifact. |
| `provimux-publish.yml`, job `publish`, steps `Decide`, `Plan`, `Push values`, `Wait` (`autopublish.sh decide`, `publish.sh --ci`) | GitHub Actions, environment `provimux-token` | `GITHUB_TOKEN` with `issues: write`. `main`'s own scripts: git, gh, jq and the guard of the current `main` on git objects; never candidate or upstream code, `go` or `make`. Never see `PROVIMUX_TOKEN` (`publish.sh --ci` refuses to run with it). |
| `provimux-publish.yml`, job `publish`, step `Push` | GitHub Actions, environment `provimux-token` | The only step with `PROVIMUX_TOKEN`: one inline `git push` of values an earlier step checked, from a bare repository, hooks off, no global or system config; it runs `git` and nothing else. |
| `provimux-publish.yml`, job `notify` (`autopublish.sh notify`) | GitHub Actions | `issues: write`, no environment, no secret. The current `main`'s script tells the sync issue after a failure. |
| `.github/provimux/publish.sh` by hand | the owner's Mac | Pushes `main` and the tags with the owner's `git` and `gh` credentials, when the automatic publish refused. |

## The check

It runs every 3 hours and can be started by hand (Actions → provimux-check → Run workflow, input `tag`; or `gh workflow run provimux-check -R ngvoicu/provimux -f tag=v8.0.78`). One run at a time. Every job checks out the run's own commit (`github.sha`, the run's `head_sha`) and runs that commit's scripts, so a run checks exactly the `main` it names.

**Job `prepare`** (`check.sh select`, then `check.sh prepare`; git and the API only):

1. **Release.** It takes the newest *stable* release of `router-for-me/CLIProxyAPI`: published, neither a draft nor a prerelease, with a `vX.Y.Z` tag. An explicit `tag` must be a stable release too. It records the commit the tag points to.
2. **Old base.** The base of `main` is the newest `vX.Y.Z` tag of upstream, read from upstream's refs (`git ls-remote`), whose commit is an ancestor of `main`; no local tag is needed. If a `vX.Y.Z` tag reachable from `main` here, as new as that base or newer, was withdrawn or moved upstream, the check fails: it never falls back to an older tag. `guard.sh` and `release-meta.sh` use the same rule.
3. **Already done?**
   - If `v<base>-pmx.*` exists, the **newest** such tag decides (a later `-pmx.<n>` replaces an earlier one: the tap, the image and the GitHub "latest" follow it). The status is `published` when its GitHub release is public (not a draft, not a prerelease) with all its assets and the newest `provimux-release` run for that tag **at the tag's commit** succeeded; `in-progress` while that run runs; and `incomplete` otherwise (that case is reported). A run for another commit does not count. An earlier tag that did not complete (a failed `-pmx.1` replaced by a good `-pmx.2`) is named in the reasons and does not block.
   - If `main` is already on the tag but no `-pmx` tag exists, the status is `unreleased` (reported).
   - If the tag does not descend from the old base, the check fails.
4. **Rebase.** It runs `git rebase --committer-date-is-author-date <tag commit>` in a temporary worktree, never `--onto`, so the same inputs give the same candidate commit.
5. **range-diff.** It runs `git range-diff <old base>..<old main> <new base>..<candidate>`. Patches that the rebase **dropped** (`<`) or **changed** (`!`) need the owner's decision.
6. **Workflow files.** It compares the complete path sets of `.github/workflows/` at the old base and at the new base. Every new path (added, renamed or copied) is listed, and `publish.sh` refuses such a release (see "New upstream workflow files").
7. **Guard.** It runs `guard.sh` on the candidate with `PMX_EXPECT_BASE=<tag>` and `PMX_EXPECT_BASE_COMMIT=<commit>`. The base the guard finds must be exactly that commit.
8. **Tested tree = released tree.** The test jobs build and test `git archive` of each commit; the release builds a checkout of it. For the candidate and the tag, `archive_matches` (`common.sh`) compares the archive's files with the commit's tree, as NUL-delimited records byte for byte (a file name may hold a newline): names, blob ids (hashed without filters) and modes (regular, executable, symlink; submodules are in neither). Any difference, for example `export-ignore` or `export-subst` in upstream's `.gitattributes`, fails the check, and `logs/archive-<tree>.txt` lists it. `check.sh tree` makes the same comparison on the tree it hands to the tests and stops on a difference.

**Jobs `test (candidate)` and `test (tag)`** (two jobs, not a matrix, so that each hands the verdict its own outputs), one runner each: before any of the tree's code runs, the prepare job's artifact by the id and digest that job output (`check.sh fetch`, `run_artifact` in `common.sh`); the tree (`check.sh tree`, from the upstream tag and the candidate bundle, without `.git`); then `check.sh test <tree> <part>` as separate steps, each exiting with its command's status (`continue-on-error`): `build` (`go build ./...`, candidate), `test` (`go list ./...`, `go test -json ./...`), `rerun` (candidate: every package with a failing test again, as a whole package). Each job outputs its artifact's id and digest and each step's `outcome`, which the runner records (the tree's code cannot write it). They keep the raw output and the exit statuses, and decide nothing.

**How the jobs hand over** (a job that runs upstream's or the candidate's code can upload, delete or re-upload artifacts of any name in the run): every artifact is read by the id and digest its producing job output, checked to be of this run with that name, unexpired, and downloaded byte-identical to that digest (`run_artifact`); a re-upload under the same name gets a new id and is never read, and the original's absence makes the result missing (an error). The state that travels from `prepare` (`state.json`) is JSON read with `jq`, each value checked against its pattern (`load_state` in `check.sh`), never sourced. The report job reads the verdict's artifact the same way.

**Job `verdict`** (`check.sh verdict`, `actions: read`) first requires that each test job concluded `success` (ran to the end; jobs API, this run's attempt) and that each step's outcome agrees with the exit status in the job's artifact (`success` ⇔ 0); any disagreement or missing outcome is an error. Then it reads the output as data (`testevents.jq`, `testsum.jq`, `testcmp.jq`):

- `go build ./...` must succeed on the candidate, and every package must build, on the first run and on the package rerun. A rerun build failure always fails the check, whatever failed upstream.
- The exit status of each `go test` must match its results. Both trees must have a terminal result (pass, fail or skip) for every package that `go list ./...` lists.
- The candidate's failures are those of the first run **and of the rerun**: a test the first run never reached (an earlier test stopped the package) that fails on the rerun is a failure. Every failure (a test, or a package that failed without a failing test) fails the check, unless one of two waivers holds, and nothing else waives it:
  - **baseline:** the *same named* test (package and test name) failed on the pristine tag. An unrelated failure upstream waives nothing, and a **package-level** failure (build, setup, `TestMain`, a panic outside a test) is never waived by the tag, even if the tag's package failed too;
  - **flaky:** the whole-package rerun has an *explicit pass* for it: the rerun exited with a valid status, its package has a result and built, and the test passed (for a package-level failure: the whole package passed). A rerun that did not build, was killed, has no result for the test, or failed it, waives nothing.
- A test that the tag does not have counts against the candidate, because it is the owner's test.

The gate is "no worse than upstream", because upstream does not gate its releases on tests. Upstream always has failing tests of its own, so the waivers are exercised on every run; the issue lists them.

The verdict job uploads the artifact `provimux-check-<tag>` and binds it to itself: its step "Bind the artifact to the verdict" writes the artifact's id, name and digest as the job's one annotation titled `provimux-check-artifact`. `publish.sh` and `autopublish.sh` take only that artifact (`check_artifact` in `common.sh`): of one given attempt of the run (the attempt whose success was read in the same API call, or for the automatic publish the attempt the event names), the job named `verdict`, exactly one such annotation, and an artifact of that id, of that run, with that name and digest, not expired. The test jobs, which run upstream's and the candidate's code, can upload artifacts of any name in the same run but cannot annotate the verdict job; whatever they upload is never read. The artifact holds:

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
| release incomplete (older base), owner needed | An open issue of an older base whose newest `-pmx` release did not complete (sweep, below). |
| not published; the check now checks v<newer> | An open issue of an older base that was never published (sweep, below). |

An open issue is updated in place. It gets a comment when its state or candidate changes. It is closed only when a check finds the newest `v<base>-pmx.*` release complete with a successful release run for the tag's commit, or when `publish.sh` has verified both.

**Older bases (sweep).** A check only checks the newest stable release, so `report.sh` also looks at every other open `sync: v<other>` issue on each run: if the newest `v<other>-pmx.*` release is complete it closes the issue; if it did not complete it retitles the issue `release incomplete (older base), owner needed` with the rerun command; if `v<other>` has no `-pmx` tag it says that nothing publishes it automatically any more (the owner closes the issue or publishes by hand). Each state is commented once.

## Publishing automatically: `provimux-publish.yml`

`provimux-publish.yml` has one trigger: `workflow_run` of a completed `provimux-check` run on `main`. It cannot be started by hand (no `workflow_dispatch`): to publish now, start a fresh check (Actions → provimux-check → Run workflow, or `gh workflow run provimux-check.yml -R ngvoicu/provimux -f tag=v<base>`); its completion starts the publish. One publish at a time (`concurrency: provimux-publish`, never cancelled midway).

**Job `publish`** (only for a check run that concluded `success`, on `main`, event `schedule` or `workflow_dispatch`, of this repository; environment `provimux-token`; `contents`, `actions`, `checks: read`, `issues: write`). One job selects, plans and pushes, so no run id, attempt or artifact id crosses a job boundary:

1. **Verify, inline, before any checkout** (runs no repository code; `selftest.sh` runs these exact lines, between the markers `# >>> verify` and `# <<< verify`): the event is `workflow_run`; the workflow is `provimux-publish.yml` on `refs/heads/main` (`github.workflow_ref`); `github.workflow_sha` **=** the check run's `head_sha` from the event **=** the live `refs/heads/main` (`git ls-remote`). A rerun keeps its original `workflow_sha`, so a rerun after `main` moved, or a run of an older revision, is refused here. Then the check run's **exact attempt** from the event (`github.event.workflow_run.run_attempt`, read at `actions/runs/<id>/attempts/<n>`) must be that attempt, `provimux-check.yml`, on `main`, `schedule` or `workflow_dispatch`, `completed`/`success`, of this repository, at that `main`; that attempt must have one job `verdict` with exactly one `provimux-check-artifact` annotation. It outputs the run id, the attempt, the artifact id and `main`. Anything else: red, nothing else runs.
2. **Checkout of exactly that commit** (`actions/checkout` pinned by SHA, no stored credential), and a check that `HEAD` is it.
3. **Decide** (`autopublish.sh decide`): `check_artifact` for exactly that run and attempt; the artifact must be the verified id. The tag comes from it. It skips (green) when the check selected no release, when the status is not `pass` (a check run is green for every status; the report job handled the others), or when `v<base>-pmx.*` exists (a second passing run, a re-run). A different or malformed artifact fails.
4. **Plan** (`publish.sh <tag> --ci --plan <dir> --run <run> --attempt <n> --artifact <id> --expect-main <sha>`, no token): everything in "Publishing by hand" up to the push, for exactly that run, attempt and artifact id (it refuses any other): the re-checks, the verification in a temporary repository, the guard of the current `main` run on git objects only (a bare repository whose `HEAD` is the candidate; nothing of the candidate is checked out), the decisions (no `--accept-*`: a dropped or changed patch, or a new workflow file, is refused), the re-checks again; it requires the remote `main`, the run's `head_sha` and the lease to be the one commit. It leaves `plan.env` (tag, `-pmx.1` tag, old `main`, candidate, upstream commit, whether to push the base tag, the repository path, run, attempt, artifact) and the verified repository.
5. **Push values** (inline, no token): reads `plan.env` (exactly 10 lines, each value checked against its pattern; tag, old `main`, run, attempt, artifact must be the verified ones, the repository must be `<dir>/work/repo.git`) and writes the push's repository, lease and refs as step outputs.
6. **`PROVIMUX_TOKEN` is set**: if not, red with that message; nothing is pushed.
7. **Push**, the only step with the token: it reads only the outputs of step 5 and runs `git` and nothing else (no `sed`, `head` or other parsing): one `git push --atomic --porcelain --force-with-lease=refs/heads/main:<the verified main>` of `main` and the tags from the plan's bare repository, with `core.hooksPath=/dev/null`, no global or system git config, `BASH_ENV` empty, and a credential helper that reads the token from git's environment (never a file, URL, argument or log).
8. **Record the push** (no token, only after `Push` succeeded): the artifact `pmx-pushed` with one line `<v<base>-pmx.1> <candidate>`, which `provimux-release.yml` requires before it publishes (see "Release provenance").
9. **Wait** (`publish.sh --ci --wait <dir>`, no token): waits for the whole `provimux-release` run of the `-pmx.1` tag at the candidate and the complete release, then closes the issue with the release link, as by hand.

**Job `notify`** (after a failure or cancellation of `publish`; no environment; `issues: write`): it checks out the *current* `main` (inline `git ls-remote` first, then a `HEAD` check) and runs its `autopublish.sh notify`, with the verifier's or decide's reason, the publish log and plan (artifact `pmx-publish-log`), and whether `PROVIMUX_TOKEN` is set (yes or no only). Before writing any instruction it reads the remote: `main`, `v<base>`, every `v<base>-pmx.*` with its release and its release run for the tag's commit. It never promises that a later check fixes an older release (a scheduled check checks only the newest stable release); where a check is the next step it gives the command for a check of that tag. It retitles `sync: v<base>` and comments what is true there, the reasons, and what to do:
- a `-pmx` tag exists: `released` (complete: it closes the open issue and opens none), `pushed, release running` (wait; the owner checks the release when it ends), or `pushed, release not complete, owner needed` with the `gh run rerun <id> --failed` command (never "publish again": an automatic retry skips the existing tag and `publish.sh` refuses it). This also covers a push whose acknowledgement was lost: the remote decides, not the run;
- no `-pmx` tag and `main` is the checked commit (or the run stopped before its push): `ready, publishing waits for PROVIMUX_TOKEN` (the secret is missing: set it, then start a check of the tag; **no** hand publish is offered, because the release's `tap-publish` needs the same token and a hand publish would go out without the Homebrew update) or `publish refused, owner needed` (dropped or changed patches, new workflow files, upstream release withdrawn or moved, a verification mismatch, a failed push), with the commands to publish by hand;
- no `-pmx` tag and `main` is this run's candidate: `main pushed without its tags, owner needed`, with the tag push;
- no `-pmx` tag and `main` is another commit: `main moved, nothing published`, with the command for a check of the tag on the new `main`;
- the remote cannot be read: `publish failed, state unknown, owner needed` (look before acting);
- the run failed before it knew the tag (the verifier refused): a comment on the one open `sync: v*` issue, or the issue `provimux-publish: failed`.

While the token is missing, every text that leads to a release (by hand or by a rerun) adds that it would go out without the Homebrew tap update. The run stays red. Each distinct state and reason is commented once, so a check that passes again every 3 hours and meets the same refusal only resets the title.

Why a token of the owner and not `GITHUB_TOKEN`: events caused by `GITHUB_TOKEN` start no workflow runs (except `workflow_dispatch` and `repository_dispatch`), so its tag push would start no `provimux-release` run; and `GITHUB_TOKEN` has no `workflows` permission, which pushing commits that change `.github/workflows/` needs (a rebase onto a new upstream release usually does). The owner's token also bypasses ruleset 27584853 as the owner's own pushes do (bypass: repository admins).

### The token

One fine-grained token of the owner serves both Provimux writes outside `GITHUB_TOKEN`: the `Push` step of `provimux-publish.yml` (to `ngvoicu/provimux`) and the job `tap-publish` of `provimux-release.yml` (to `ngvoicu/homebrew-tap`; no checkout, no repository script). It is a secret of the **environment `provimux-token`** only, never a repository secret. The environment may deploy only from the branch `main` and the tags `v*-pmx.*`; only the job `publish` and the job `tap-publish` (on a tag push only: a branch dry run of the release requests no environment and checks the formula without the token) declare it. No earlier revision of any workflow names `provimux-token` (earlier revisions named `provimux-publish`, which was deleted), so a rerun of an old run, or an old revision, gets no token.

The token:

- **Fine-grained personal access token** (Settings → Developer settings → Fine-grained tokens), resource owner **ngvoicu**;
- **Repository access:** only `ngvoicu/provimux`, `ngvoicu/homebrew-tap`, `ngvoicu/provimux-ui`;
- **Repository permissions** (a fine-grained token has one set for all its repositories):
  - **Contents: Read and write**: the tap's formula commit; `main` and the tags of `provimux`;
  - **Workflows: Read and write**: the pushed `provimux` commits change `.github/workflows/` files (each rebase onto a new upstream release can);
  - **Metadata: Read**: required, added automatically.

  Nothing else for these two: gh in both workflows uses the run's `GITHUB_TOKEN`. Whatever `provimux-ui` needs is added when its workflow is written;
- **Expiration:** *No expiration* (GitHub allows it for a token of a personal account; an organization's policy could block it). With a date, set a reminder: when it expires, publishing fails at the push and the issue says so, and `tap-publish` fails.

The environment exists (created 2026-11-19 with these calls; no reviewers, no wait timer):

```sh
echo '{"deployment_branch_policy": {"protected_branches": false, "custom_branch_policies": true}}' |
  gh api -X PUT repos/ngvoicu/provimux/environments/provimux-token --input -
gh api -X POST repos/ngvoicu/provimux/environments/provimux-token/deployment-branch-policies -f name=main -f type=branch
gh api -X POST repos/ngvoicu/provimux/environments/provimux-token/deployment-branch-policies -f name='v*-pmx.*' -f type=tag
```

The owner stores the token there, once: Settings → Environments → `provimux-token` → Environment secrets → Add secret `PROVIMUX_TOKEN`, or `gh secret set PROVIMUX_TOKEN --env provimux-token -R ngvoicu/provimux` (it asks for the value). Then the repository secret `TAP_TOKEN` can be deleted (`gh secret delete TAP_TOKEN -R ngvoicu/provimux`); a repository secret named `PROVIMUX_TOKEN` must not exist. Then a check of the waiting release publishes it: `gh workflow run provimux-check.yml -R ngvoicu/provimux -f tag=v<base>`.

Without the secret nothing is published: each passing check opens "ready to publish" and the publish run retitles it "publishing waits for PROVIMUX_TOKEN". Do not publish by hand meanwhile: `tap-publish` fails without the token, so such a release has no Homebrew update until the token exists and the job is rerun.

### Release provenance, immutable releases, "latest"

The environment `provimux-token` must admit tags (`v*-pmx.*`) because `tap-publish` runs on the tag push, and a tag's run uses the tag's own workflow file. So who may create a tag is what protects the environment:

- **Tag ruleset** `provimux tags v*` (27670572): on `refs/tags/v*`, creation, update and deletion are blocked for everyone except repository admins (bypass: role admin, always). The owner (and the owner's `PROVIMUX_TOKEN`, which acts as the owner) can still push `v<base>` and `v<base>-pmx.<n>`; `GITHUB_TOKEN` and anyone else cannot.
- **Job `provenance`** of `provimux-release.yml` (on a tag push; `publish-release` and therefore every later publishing job, and `tap-publish`, need it): the tag must still be this run's commit, and either a `provimux-publish` run of `main` recorded pushing exactly this tag and commit (artifact `pmx-pushed`, written only after its `Push` step succeeded; it waits up to 10 minutes for the record), or the repository variable `PMX_HAND_RELEASES` (only the owner can set it) has the line `<tag> <commit>`. Otherwise nothing is released. `publish.sh` by hand records its `-pmx.1` there before pushing; for a hand `-pmx.2` the owner records it ("Cut `-pmx.2`"). A tag pushed without a record: record it, then rerun the release run.
- **Immutable releases** are on for the repository (Settings → General → Releases): a published release's assets and tag cannot change. `publish-release` requires the release to read back `immutable: true` after publishing, and every promotion job (`tap-publish`, `image-push`, `image-latest`, `release-latest`) requires it again first.
- **"latest"**: `publish-release` publishes with `--latest=false` (GitHub would otherwise make every newly published release latest, so an older base's `-pmx.<n>` published later, or a rerun, would move it back). The job `release-latest` marks the highest published `v*-pmx.*` release latest, one job at a time across all runs (`concurrency: provimux-release-latest`), never to an older version, stops when the current latest cannot be read (anything but a 404), and fails when latest does not read back as the version it set. The image `:latest` follows the same rules in `image-latest` (`concurrency: provimux-image-latest`, only forward, stops on an unreadable `:latest`, reads it back).

Set on 2026-11-19 with:

```sh
gh api -X PUT repos/ngvoicu/provimux/immutable-releases
gh api -X POST repos/ngvoicu/provimux/rulesets --input - <<'JSON'
{"name": "provimux tags v*", "target": "tag", "enforcement": "active",
 "conditions": {"ref_name": {"include": ["refs/tags/v*"], "exclude": []}},
 "rules": [{"type": "creation"}, {"type": "update"}, {"type": "deletion"}],
 "bypass_actors": [{"actor_id": 5, "actor_type": "RepositoryRole", "bypass_mode": "always"}]}
JSON
```

### What still needs the owner

The automatic publish only ever does what `publish.sh` would do unasked. These still need the owner, who then publishes by hand (next section) or follows the named procedure: the token, once (or when it expires); a dropped or changed patch (`--accept-dropped`/`--accept-changed`); a release with new workflow files ("New upstream workflow files"); a failed check, a rebase conflict, failing tests ("Manual cases"); a release that did not complete ("Check a release"); a `-pmx.2` on the same base; a release older than the newest stable one (the scheduled check only checks the newest: start a check with its `tag`).

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

1. **Check run.** It takes the newest `provimux-check` run of `ngvoicu/provimux`, on `main` (event `schedule` or `workflow_dispatch`), completed with conclusion `success`, whose `head_sha` is the remote `main` now and whose bound verdict artifact (`check_artifact`, see "The check", of the attempt read in the same call as the success) is `provimux-check-<tag>`. `--run <id>` only picks among such runs; `--expect-main <sha>` also requires that commit. It downloads that artifact by its id; an artifact found by name only is never used.
2. **Re-checks.** It refuses unless all of these hold:
   - `result.json` says `pass` (tests, build and guard ok) for this tag, and its old `main` is the run's `head_sha`;
   - the bundle's only head is the recorded candidate;
   - upstream's tag still points at the recorded commit and is still a stable release (and the old base tag still at its commit);
   - the remote `main` still is the recorded old `main`;
   - no `v<base>-pmx.*` exists yet.
3. **Verifies again locally.** `result.json` is not taken on trust for the structure. Locally, `publish.sh` checks that:
   - the new base descends from the old base, and the candidate from the new base, with no merge commits;
   - the range-diff and the new workflow paths, recomputed here, match `result.json`;
   - the `guard.sh` of the owner's current `main` (the remote `main`, which is the old `main` and the run's `head_sha`) passes on the candidate with the exact expected base, on git objects only (a bare repository whose `HEAD` is the candidate). Never the candidate's copy, and nothing of the candidate is checked out.
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

Before step 3, record the tag (see "Release provenance"; without it the release run publishes nothing):

```sh
cur="$(gh variable get PMX_HAND_RELEASES -R ngvoicu/provimux 2>/dev/null || true)"
printf '%s\n%s\n' "$cur" "v<base>-pmx.2 $sha" | sed '/^$/d' | sort -u | gh variable set PMX_HAND_RELEASES -R ngvoicu/provimux
```

The tag starts `provimux-release.yml`. The Homebrew tap, the image `:latest` and the GitHub "latest" move only forward, so `-pmx.2` replaces `-pmx.1`. From then on the check judges the base by its newest release (`-pmx.2`).

### Check a release

A release is complete when it is published (not a draft, not a prerelease) with all 11 assets and its `provimux-release` run (image push and tap included) succeeded:

```sh
gh release view v<base>-pmx.<n> -R ngvoicu/provimux --json isDraft,isPrerelease,assets --jq '{isDraft, isPrerelease, assets: [.assets[].name]}'
gh run list -R ngvoicu/provimux -w provimux-release -b v<base>-pmx.<n>
```

If the release run failed, rerun its failed jobs (`gh run rerun <id> --failed -R ngvoicu/provimux`). Until every release of the base is complete, each check reports "release incomplete". The next check after completion closes the issue.

## Testing the scripts

- `bash .github/provimux/selftest.sh` runs the fixture tests offline (local repositories, a stub `gh`): the test gate's waivers, the guard and `release-meta.sh` with a base only upstream or withdrawn or moved, `report.sh` with a release run for the wrong commit and with a failure, `archive_matches` (`export-ignore`, `export-subst`, executables, symlinks, a file name with a newline), the inline verifier of `provimux-publish.yml` (run from the workflow file), `autopublish.sh` (which artifact publishes, the issue text for each remote state) and `publish.sh --ci`. Run it after changing any of these scripts.
- `check.sh`, `report.sh`, `publish.sh`, `autopublish.sh` and `selftest.sh` run with macOS bash 3.2.
- `PMX_NOTIFY_DRY_RUN=1 TAG=v8.0.78 OUTCOME=failed LOG=<publish log> bash .github/provimux/autopublish.sh notify` prints what a refusal would write in the issue.
- `PMX_REPORT_DRY_RUN=1 RESULT=<result.json> CHECK_JOB=success bash .github/provimux/report.sh` prints the issue that a result would produce (it still reads the release state from GitHub).
- `check.sh select` and `check.sh prepare` run no repository code.
- `check.sh test` and `check.sh` (all steps) run `go test ./...`: only in a throwaway environment without credentials (above). Some tests use the proxy's default port 8317, so don't run it on a machine where the proxy is serving.
