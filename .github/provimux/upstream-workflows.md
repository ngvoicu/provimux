# Upstream workflows in Provimux

Provimux carries CLIProxyAPI's `.github/workflows/` files unchanged, so that rebasing onto each upstream release never conflicts on them. All six are switched off in the GitHub settings of `ngvoicu/provimux` (`gh workflow disable`), not by editing or deleting the files.

| Workflow file | Trigger | State in ngvoicu/provimux | Why |
|---|---|---|---|
| `agents-md-guard.yml` | `pull_request_target` | disabled in ngvoicu/provimux | Polices upstream's own PR rule (closes PRs that change `AGENTS.md`); not a Provimux rule. |
| `auto-retarget-main-pr-to-dev.yml` | `pull_request_target` | disabled in ngvoicu/provimux | Enforces upstream's `main`/`dev` branch flow by retargeting PRs to `dev`; Provimux has no `dev` branch. |
| `docker-image.yml` | `push` of tags `v*` | disabled in ngvoicu/provimux | Builds and pushes CLIProxyAPI-named images to router-for-me's Docker Hub repo (`eceasy/cli-proxy-api`) with their `DOCKERHUB_*` secrets. |
| `release.yaml` | `push` of any tag | disabled in ngvoicu/provimux | Publishes CLIProxyAPI-named GitHub releases and binaries; any tag pushed here (including upstream's version tags) would publish a CPA release under Provimux. |
| `pr-path-guard.yml` | `pull_request` | disabled in ngvoicu/provimux | Polices upstream's own PR rule (fails PRs that touch `internal/translator/**`); Provimux patches may need to. |
| `pr-test-build.yml` | `pull_request` | disabled in ngvoicu/provimux | Upstream's PR build check; Provimux will get its own CI. |

Provimux's own workflows will live in separately named files.

| Provimux workflow | Trigger | What it does |
|---|---|---|
| `provimux-guard.yml` | `push` to `main`, `pull_request` | Required check: Provimux-layer commits change no protected path. |
| `provimux-release.yml` | `push` of tags `v*-pmx.*`; manual (dry run) | Builds the 10 archives of `release.yaml` as `provimux_<base>-pmx.<n>_*` and the linux/amd64 and linux/arm64 images of `.github/provimux/Dockerfile`, all with one validated copy of the model catalogs; publishes the GitHub release, then pushes `ghcr.io/ngvoicu/provimux` and updates the Homebrew tap. A manual run publishes nothing: it keeps the files as workflow artifacts and rehearses the image push against a registry on the runner. |
| `provimux-check.yml` | every 3 hours; manual (input `tag`) | Checks each new stable CLIProxyAPI release: rebases `main` onto it, builds, compares `go test ./...` with the pristine release, runs the guard, lists new upstream workflow files and the range-diff of the owner's patches, and reports in the issue `sync: v<base>`. Publishes nothing and holds no owner token; the owner publishes with `publish.sh` (`SYNC.md`). |
