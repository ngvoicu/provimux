# The root README commit and the sync (T-100)

Design only. It says how Provimux replaces upstream's root `README.md`, `README_CN.md` and `README_JA.md` with one
short pointer of its own, and how the sync (`SYNC.md`) still publishes every upstream release with no step by the
owner. provimux-ui has the same rule; its file (`.github/provimux/design/root-readme.md` there) gives what differs.

**Scope.** The goal is that no referral code reaches a user of Provimux: not in the README GitHub shows, not in
the root READMEs of the tree, not in a release archive, not in the console's pages. This design covers the root
READMEs and the release archives. It does not rewrite other upstream files: the sponsor images under `assets/`
stay (they carry no link, and nothing shows them once the root READMEs are gone), and so do the upstream source
files of provimux-ui whose sponsor links its layer already hides, so that syncs do not conflict on them.

## Why a rule is needed

Checked on 2026-11-20 (`main` = `ff02809c`, on `v8.0.79`; git 2.55.0; a scratch repository for the git behaviour):

- Upstream changed a root README in 9 of its last 29 release-to-release steps (`v8.0.50`..`v8.0.79`). No commit of
  the stack touches them today.
- A layer commit that replaces `README.md` and deletes the two translations conflicts on the rebase whenever upstream
  changed one of them: a content conflict on `README.md`, a modify/delete conflict on each translation. Today that
  is "the rebase stops", an owner step.
- Resolving the conflict is not enough. The commit's patch then differs from the old one (the removed lines are
  upstream's new text), so `git range-diff` marks it `!` (changed) for a small upstream edit, and for a large one
  it no longer pairs the two commits at all: the old one is `<` (dropped) and the new one `>` (not in the old
  `main`). `publish.sh` refuses the first without `--accept-changed` and dies on the second.
- `provimux-release.yml` copies `README.md` and `README_CN.md` into every archive (the jobs that build them, four
  places). The referral links ship in the archives today, and without `README_CN.md` those `cp` lines fail. The
  check does not build archives, so that failure would show only after the push.
- Nothing else reads the root READMEs: no Go file, no script, no Dockerfile (`.dockerignore` lists two of them),
  and the Homebrew formula installs only the binary and `config.example.yaml`.

## The README commit

One commit of the layer, **the README commit**:

| | |
|---|---|
| Trailers | `Provimux-Layer: branding` and `Provimux-Readme: root`, in that one trailer block |
| Subject | `docs(provimux): root README points to the Provimux README` |
| Paths, **the set** | exactly `README.md`, `README_CN.md`, `README_JA.md` (root, literal). It changes no other path |
| State it leaves | `README.md` is the pointer (a regular file, mode 100644); `README_CN.md` and `README_JA.md` do not exist |
| How many | exactly one in `<base>..main` |

A commit is the README commit when, and only when, its trailer `Provimux-Readme` has the value `root`. The subject
is for people.

**The translations are deleted, not replaced.** A Chinese or Japanese pointer to an English README says nothing
the English pointer does not, and two more files would need the same care on every sync.

**The pointer** (the whole of `README.md`):

```markdown
# Provimux

**Provimux**, by [providers.dev](https://providers.dev), is one local endpoint for your AI subscriptions and API keys.

Its README is [`.github/README.md`](.github/README.md), the page GitHub shows at
<https://github.com/ngvoicu/provimux>. In a release archive it is `README_PROVIMUX.md`.

Provimux is a distribution of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) by router-for-me: each
Provimux release is a CLIProxyAPI release with Provimux's changes on top. CLIProxyAPI's own README, in English,
Chinese and Japanese, is in [its repository](https://github.com/router-for-me/CLIProxyAPI#readme). Thanks to its
authors and contributors.

MIT licensed, like CLIProxyAPI. [LICENSE](LICENSE) keeps CLIProxyAPI's copyright notices.
```

To change the pointer later, add a layer commit on top that edits `README.md`. It is an ordinary layer commit: the
README commit below it always leaves the same `README.md`, so it rebases cleanly and range-diff calls it `=`.

## Release archives

**Before the README commit lands**, one layer commit changes `provimux-release.yml`: in each of the four steps
that fill an archive, `README_CN.md` leaves the `cp` line and the `tar` line (the Windows zip packs the whole
directory, so the `cp` line decides it). Nothing else of the workflow changes. Every archive, all ten, then holds
exactly:

| File | What it is |
|---|---|
| `provimux` (`provimux.exe` on Windows) | the binary |
| `LICENSE` | the tree's, with CLIProxyAPI's copyright notices and Provimux's line |
| `README.md` | the tree's root `README.md`: the pointer, once the README commit is on `main` |
| `README_PROVIMUX.md` | the tree's `.github/README.md`, Provimux's README |
| `config.example.yaml` | the tree's, upstream's example config |

No translation, and no file with a referral link (checked today for `LICENSE`, `.github/README.md` and
`config.example.yaml`). Inside an archive the pointer's link to `.github/README.md` leads nowhere, which is why
the pointer names `README_PROVIMUX.md`; its other links are absolute, or to `LICENSE`, which is next to it.

The order matters one way only. With the workflow changed and the README commit not yet on `main` (the time of
the review), an archive still carries upstream's `README.md`, as every archive does today. The other order, the
README commit first, breaks the release build at `cp`. A release cut from a `main` that has both is the first one
with no referral link in its archives.

## The normal form

Everything below rests on one definition. Let `R` be the README commit of the old `main` and `R'` the README commit
of a candidate. **`R'` is `R` re-applied** when all of these hold, on git objects:

1. `R` and `R'` each have exactly one parent.
2. Their author lines and their messages are byte for byte the same (the committer may differ: the check commits
   as `github-actions[bot]`).
3. For every path of the set, `R'` has what `R` has: the same mode and blob, or no entry in both.
4. `R'` differs from its parent in no path outside the set (`git diff-tree -r --no-renames -z --name-only`,
   whole names compared).
5. For every path of the set, the candidate's tip has what the old `main`'s tip has.

3 and 4 together say that the tree of `R'` is a function of its parent's tree and of `R`'s three paths, and of
nothing else. So `R'` cannot carry a change nobody approved: its parent chain is checked by range-diff as today,
and the three paths are `R`'s, which is on the `main` the owner approved. 5 says the same about the result: the
root READMEs of the candidate are those of the old `main`.

## Rebase: what the check does when git stops

`check.sh prepare` runs the same command as today, `git rebase --committer-date-is-author-date <tag commit>`. When
it succeeds, nothing new runs. When it stops, the check resolves the stop itself only if **all** of these hold;
otherwise the stop fails the check exactly as today (`rebase.json` with the commit and the conflicting files):

- the old `main` has exactly one README commit `R`, and the stop is at it (`REBASE_HEAD` = `R`);
- it is the first stop of this rebase (the check resolves at most one);
- `R` changes only paths of the set;
- every unmerged path is a path of the set (NUL-delimited names, whole names compared);
- every path the index holds differently from `HEAD` is a path of the set.

Then, for each path of the set: if `R` has it, `git checkout <R> -- <path>`; if not, `git rm -q -f
--ignore-unmatch -- <path>`. It requires that no path is unmerged any more and the work tree matches the index,
and runs `git rebase --continue` with `GIT_EDITOR=true` and the same `-c` options as the rebase (identity,
`core.hooksPath=/dev/null`, `rebase.autoStash=false`, `rebase.updateRefs=false`). A second stop, at any commit,
fails the check as today. `rebase.json` records the README commit and the paths that conflicted.

After the rebase, with or without a stop, the check requires the normal form (next section, the same function
`publish.sh` uses); a failure is a reason in `result.json` and the status `fail`. A README commit that picked
cleanly is in normal form by itself when upstream changed none of the three paths.

What still fails, on purpose: upstream renames a translation (git reports a conflict at the new path, which is
not in the set); a commit other than `R` conflicts on a README; upstream adds a root README the set does not name
(the guard, below).

### The options compared

| Option | What is new and must be trusted | Any other conflict | Verdict |
|---|---|---|---|
| **A. Resolve at the stop** (above) | About a dozen lines of `check.sh` that run only when git already stopped at `R`, with `git checkout <commit> -- <path>`, `git rm` and `git rebase --continue`. The rebase command itself is unchanged | fails as today | **chosen** |
| B. Build `R'` fresh on each base: an interactive rebase whose sequence editor turns `pick <R>` into `break`, then `git commit-tree` | The todo list's format and abbreviation, a sequence editor, `break`, a commit made by plumbing. It runs on every sync, and changes the rebase command the earlier reviews passed | fails as today | not chosen. It never stops on a README, so it also stays silent when upstream moves a translation elsewhere in the tree |
| C. A merge driver for the three paths | A driver command git runs during the merge, and attributes that name it: from `.gitattributes` in the tree (upstream can change it) or from `info/attributes` of the clone. A driver is not called for a modify/delete conflict, so deleted translations still stop | fails as today | rejected |
| D. `git rebase -X theirs` | Every conflict in every file of every commit is resolved in favour of the commit being replayed | **no longer fails** | rejected |

A and B give the same candidate: in the scratch repository the two produced the same commit ids for a small
upstream edit, a rewrite, no change, and an added translation. So the verifier below does not depend on the
choice, and B can replace A later without touching `publish.sh`.

## Verifier: the stack in three segments

`git range-diff` cannot be told to skip one commit, and its pairing of `R` with `R'` is a similarity heuristic
(see "Why a rule is needed"). So the README commit is taken out of the range-diff by its position, and checked by
the normal form instead:

```text
old:  <old base> .. below .. R  .. above .. <old main>
new:  <new base> .. below'.. R' .. above'.. <candidate>

range-diff  <old base>..R^   <new base>..R'^      as today: =, !, <, >
normal form R, R'                                 no flag accepts a failure
range-diff  R..<old main>    R'..<candidate>      as today
```

One function in `common.sh`, called by `check.sh prepare` and by `publish.sh` in place of their `git range-diff`
call. No other code of either script runs `git range-diff` (the selftest checks that).

```sh
PMX_README_PATHS=(README.md README_CN.md README_JA.md)

# readme_commits RANGE GIT...: the commits of RANGE (A..B) whose trailer Provimux-Readme is
# "root": full SHAs, oldest first, one per line.
# readme_normal OLD NEW OLD_TIP NEW_TIP GIT...: status 0 when NEW is OLD re-applied (the five
# conditions); status 1 and the first failed condition on stderr.
# stack_range_diff OLD_BASE OLD_MAIN NEW_BASE CANDIDATE OUT GIT...: writes the stack's
# range-diff to OUT and prints "<R> <R'>" (or nothing when neither side has a README commit).
# Status 1, the reason on stderr, when the README rule is broken.
```

`stack_range_diff`, case by case:

| Old `main` | Candidate | What it does |
|---|---|---|
| no README commit | none | one `git range-diff` of the whole ranges: the output of today, byte for byte |
| one, `R` | one, `R'` | requires the normal form (fails otherwise); writes the range-diff of the segments below, the line `README  <R> -> <R'>  <subject>`, the range-diff of the segments above |
| one, `R` | none | one range-diff of the whole ranges: `R` shows as dropped (`<`) and is refused like any dropped commit |
| none | one | one range-diff of the whole ranges: `R'` shows as `>`, which both scripts already refuse |
| more than one on either side | | fails |

Details the build must keep:

- **Both ranges are linear** (no merge commit), and the three parts add up: the commits below `R`, plus one, plus
  the commits above `R`, are the commits of `<old base>..<old main>`; the same for the candidate. Otherwise it
  fails.
- **An empty segment is not given to `git range-diff`**, which refuses an empty range (`fatal: need two commit
  ranges`, git 2.55.0). Both sides empty: nothing to write. One side empty: every commit of the other side is
  written as a header line in range-diff's own format, `<` for the old side and `>` for the new one, with full
  SHAs, so the existing parsers list it as dropped or as not in the old `main`.
- The `README` line does not match the header pattern the parsers read, so the lists (`dropped`, `changed`,
  `added`) come from the two segments only. The README commit is in none of them.

`publish.sh` after this:

- computes `stack_range_diff` in its temporary repository and dies on status 1 (**no `--accept-*` flag covers a
  README commit that is not in normal form**);
- requires its own `<R> <R'>` to equal `result.json`'s (new key `readme`: `null`, or `{old, new, reapplied}` with
  the paths that conflicted), next to the three comparisons it already makes;
- refuses dropped and changed commits exactly as today, from the lists of the two segments;
- prints one more line in the plan: `root READMEs    <R> -> <R'>`.

So: a README commit that touches any other path fails condition 4; one whose READMEs differ from the old `main`'s
fails 3 or 5; one with another message or author fails 2; a second README commit, or the trailer on another
commit, fails the count or 2; any other commit that changed is `!` in its segment and refused as today; any
dropped commit is `<` in its segment; a commit that moved across `R` is `<` in one segment and `>` in the other.

What does not change: the workflow files of the check and of the publish, the inline verifier, `check_artifact`,
`plan.env` (10 lines), the token steps, `autopublish.sh`. The token still reaches only the fixed `main`-only steps;
the verdict still binds one artifact; `publish.sh` still recomputes everything and takes nothing from
`result.json` on trust.

## Guard: rule 5

`guard.sh` stays self-contained, so it has its own copy of the set (the selftest checks that it equals
`PMX_README_PATHS`). For `<base>..HEAD`:

- more than one README commit: error; none: error once `require_readme=1` (the last commit of the work sets it);
- the README commit carries `Provimux-Layer: branding`, has one parent, changes only paths of the set, and leaves
  `README.md` a `100644` blob and the other paths of the set absent;
- at `HEAD`, the root entries whose name begins with `readme` (ASCII, any case) are exactly one, the `100644` blob
  `README.md`.

The last line is the tripwire for a root README upstream adds (`README_KO.md`): the candidate then fails the guard
in the check, with the file named, and nothing is published. It also stops a later commit from bringing one back.

## Selftest cases

One fixture: an upstream with `README.md`, `README_CN.md`, `README_JA.md` and a code file at `v1`; a stack of an
owner patch, a layer commit, the README commit, a layer commit that edits the pointer, a layer commit; and one
upstream tag per case.

Rebase (`check.sh prepare`, offline, from a state file as the verdict tests make one):

| Upstream tag | Expected |
|---|---|
| edits all three READMEs | status `test`; `rebase.json` ok with the README commit and three paths; the same candidate on a second run |
| rewrites `README.md` and `README_CN.md`, deletes `README_JA.md` | the same; plain `git range-diff` of the whole ranges shows `<` and `>` for the README commit (the reason for the segments) |
| changes no README | the same, no stop recorded; the range-diff is all `=` |
| edits the READMEs, and a file a later layer commit also changes | `fail`, stopped at that later commit |
| renames `README_CN.md` to `docs/README_CN.md` | `fail`, stopped at the README commit, conflict `docs/README_CN.md` |
| edits the READMEs, and adds `README_KO.md` | `fail`: the guard names `README_KO.md` |
| edits a file an owner patch changes (no README) | `fail`, as today: the resolver did not act |

`stack_range_diff` and `readme_normal`, on candidates built by hand:

- no README commit on either side: the output equals `git range-diff` of the whole ranges, byte for byte;
- `R'` also changes `LICENSE`; `R'` leaves `README_CN.md` in place; `R'` has another `README.md`; `R'` has another
  message; another author; two parents: each fails, with its condition named;
- the candidate's tip has another `README.md` than the old `main`'s tip, through a commit above `R'`: fails
  (condition 5) and that commit is `!`;
- two README commits in the candidate; the trailer on another commit: fail;
- the candidate without the README commit: `R` is in `dropped`;
- a commit below `R` changed, a commit above `R` changed: each is in `changed`, and `R` is not;
- a commit above `R` dropped; an extra commit in the candidate; a commit moved from below `R` to above it: in
  `dropped`, `added`, or both;
- the README commit first above the base, and at the tip (empty segments): passes, and the one-sided cases list
  their commits.

`guard.sh`: passes with the README commit; fails for two README commits, a README commit without the layer trailer,
one that touches another path, one that keeps `README_JA.md`, a root `README_KO.md`, `readme.md` or `README` at
`HEAD`, a `README.md` that is a symlink; without a README commit, passes before `require_readme=1` and fails
after; on git objects only (the bare repository, as `publish.sh` runs it).

`publish.sh --ci --plan`, offline, end to end (the stub `gh`, local repositories for both remotes, an artifact
with a bundle): the README case writes `plan.env` with the candidate; a bundle whose `R'` also changes `LICENSE`,
with a `result.json` that says `pass`, is refused; a `result.json` whose `readme` names another commit is refused.
This is the first offline run of that block of `publish.sh`; if the stub cannot carry it, say so in the result
rather than leave it out silently.

Structure: `git range-diff` appears in `common.sh` only; the set in `guard.sh` equals `PMX_README_PATHS`.

## Rehearsal

No newer upstream tag exists today, and the last README change upstream is `v8.0.73`..`v8.0.74`, before any
Provimux stack. So, before the README commit reaches `main`:

1. **The real stack on a scratch upstream tag.** A local bare repository holds upstream's `v8.0.79` and one commit
   on it that edits `README.md` and `README_CN.md` and nothing else, tagged `v8.0.80`. In a scratch clone, the
   branch to check is `main` plus the work's commits. `PMX_UPSTREAM_URL=<that repository> PMX_CHECK_OUT=<dir> bash
   check.sh prepare`, from a state file with status `check`. Expected: status `test`, the guard ok, every commit
   `=`, and **the candidate's tree is that branch's tree** (`git rev-parse <candidate>^{tree}`), since upstream
   changed only READMEs. Then three more tags on the same repository: one that
   adds `README_KO.md` (the guard fails, the file named), one that renames `README_CN.md` (the rebase fails at the
   README commit), one that also changes a file of the layer in conflict (the rebase fails at that commit).
2. **Upstream's real edit.** A scratch stack on the real `v8.0.73`: a layer commit, the README commit made for that
   tree, a layer commit. `check.sh prepare` for the real `v8.0.74`. Expected: status `test`. Keep the plain
   whole-range `git range-diff` of that pair in the log: it shows what today's rule would have refused.
3. **The archives.** A dry run of `provimux-release.yml` (`gh workflow run provimux-release.yml -R ngvoicu/provimux
   --ref <a temporary branch with all the work's commits, the README commit included>`): every build job passes.
   For one tar archive and the Windows zip, from the run's artifacts: the file list is exactly the five of
   "Release archives"; `README.md` is the pointer byte for byte; `grep -r -I -l -E '[?&](aff|utm_source|track_id)='`
   over the unpacked files lists nothing. Delete the branch after.
4. **The first real sync** after the work is on `main`: the issue of the next upstream release shows the `README`
   line when upstream changed a README, "Decisions needed: none", and it publishes by itself.

## SYNC.md

- The first paragraph: one commit of the layer, the README commit, replaces upstream's root READMEs.
- "The check", step 4: the stop at the README commit, its five conditions, and that any other stop fails. Step 5:
  the range-diff by segments; the README commit by its normal form; the `readme` key. Step 7: rule 5.
- The states table: nothing new. A resolved README stop is not a state.
- "Publishing by hand", step 3: the segments, the normal form, the comparison with `result.json`; step 4: no flag
  accepts a README commit out of normal form.
- "What still needs the owner": a root README upstream adds, renames or moves. Not: upstream editing its READMEs.
- "Manual cases", a new one, **Upstream added or moved a root README**. Added: read it (`git show
  v<base>:<path>`); on the old `main`, one layer commit adds the path to the set in `guard.sh` and `common.sh`;
  start a check of the tag. A release that adds a translation also edits the language line of `README.md`, so the
  rebase stops at the README commit and the check removes the new file with the rest. If it stops nowhere, the
  check says the README commit is not in normal form: rebase by hand, amend the README commit to remove the file,
  push `main` with the lease, and cut `v<base>-pmx.1` as in "Cut `-pmx.2` on the same base". Moved or renamed: the
  file is then elsewhere in the tree with its links; decide there, then the same.
- Every hand rebase onto a new base ("The rebase stops", "New upstream workflow files" takes the check's
  candidate and needs nothing): at the README commit, `git checkout REBASE_HEAD -- README.md`, `git rm -q -f
  --ignore-unmatch README_CN.md README_JA.md`, `GIT_EDITOR=true git rebase --continue`.
- "Testing the scripts": the new selftest cases, and rehearsal 1 as a recipe.

## Order of the work

Layer commits on top of `main`, each with the trailer, in two pushes with the review between them:

1. `provimux-release.yml`: the archives without `README_CN.md` ("Release archives").
2. The rule: `common.sh`, `check.sh`, `publish.sh`, `guard.sh`, `report.sh` (one line for the `readme` key),
   `selftest.sh`, `SYNC.md`. With no README commit on `main`, every script behaves as before.

   Review here: the rule is on `main` and not yet used.
3. The README commit (the three paths only; both trailers).
4. `guard.sh`: `require_readme=1`, with its selftest case and its line in `SYNC.md`.

## What a reviewer must check

1. With no README commit, `stack_range_diff` writes exactly `git range-diff`'s output, and both scripts get the
   lists they got before.
2. The resolver acts only under its five conditions, at most once, and passes the same `-c` options to
   `--continue`. Break each condition in turn; the check must fail as it does today.
3. `publish.sh` calls `stack_range_diff` on its own temporary repository, not on anything from the artifact but
   the bundle, and compares its pair with `result.json`'s.
4. No path leads from a failed normal form to a push: not `--accept-changed`, not `--accept-dropped`, not `--yes`.
5. Each of the five conditions of the normal form has a selftest case that fails for that condition alone.
6. Names are compared whole and NUL-delimited: `README.md.bak`, `docs/README.md`, a name with a newline are not
   paths of the set.
7. The set is the same in `guard.sh` and `common.sh`, and the guard runs on git objects only.
8. Empty segments: the README commit first above the base, and at the tip.
9. No workflow file of the check or the publish changed; `provimux-release.yml` changed only in the `cp` and `tar`
   lines of the four archive steps, and the dry run's archives hold exactly the five files of "Release archives".
10. The README commit's diff: the three paths, the pointer above byte for byte, both trailers.
11. After the work: `git ls-tree --name-only main | grep -i '^readme'` prints `README.md` only; `git grep -I -l -E
    '[?&]aff='` on `main` lists no file (today it lists the three root READMEs and nothing else); the rehearsal's
    results are in the task's log.
