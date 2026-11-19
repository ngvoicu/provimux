# PUI-13: two Provimux-only management endpoints for the console

Design for item PUI-13 of the provimux-ui plan (Phase 6, "Provimux pages"), 2026-11-19. This folder is the design only: no code exists yet. PUI-14 builds the server side from it, PUI-16 the console pages.

Read from `main` at `aeb33d3d` (CLIProxyAPI v8.0.78 + 33 owner patch commits + 44 layer commits). Every `file:line` below is in that commit. The commit after it, `ba2b4f4a` (PUI-11: the console's download address), changes none of the files cited here [ran: `git diff --stat` over them is empty]. The console lines are from the `provimux-ui` clone (base v1.28.3).

Tags: **[read]** in code, **[ran]** executed, **[infer]** reasoned and not verified.

## What is designed

| Endpoint | Answers | Document |
|---|---|---|
| `GET /v8/management/provimux/model-routes` | per model, every login or key that can serve it: provider, kind, priority, host, login label, limits, reasoning, availability, cooldown, last served | `01-model-routes.md` |
| `GET /v8/management/provimux/live-lists` | per Claude, Codex and Meta login: live list or catalog, fetched at, model counts, last failure, next retry | `02-live-lists.md` |

Both sit behind the same management key middleware as every other `/v8/management` route, are read-only, and are absent (404) on stock CLIProxyAPI.

## Files in this folder

| File | Fixes |
|---|---|
| `01-model-routes.md` | where each field of model-routes comes from, what is missing, the JSON shape |
| `02-live-lists.md` | the same for live-lists, and the one hook the failure reason needs |
| `03-last-served.md` | how "last served" is recorded with usage statistics off, and what it costs |
| `04-layer-commits-tests.md` | how both fit the layer (new files, one seam line), the frame of the code, the commits, the Go tests |
| `05-console-contract.md` | auth, absence detection (404), polling, compatibility rule, TypeScript types for PUI-16 |
| `openapi.yaml` | the contract a build is checked against (OpenAPI 3.1) |
| `examples/model-routes.json`, `examples/live-lists.json` | full example responses; they validate against `openapi.yaml` |

## Decisions taken in this design

| # | Decision | Why |
|---|---|---|
| D1 | Handlers are methods on the existing `*management.Handler`, in new `provimux_*.go` files of `internal/api/handlers/management/` | They get the auth manager and the config that hot reloads already keep current (`handler.go:157-171`, `:216-225`); a route needs one line. The layer already has a file there (`provimux_version_test.go`) |
| D2 | One seam line in `internal/api/server_management_v8.go` for both endpoints, calling `registerProvimuxRoutes` in a new `internal/api/server_provimux.go` | The brief allows one route line each; one line in total is less. Later Provimux endpoints then touch no upstream file. The line goes after `:20`, a spot upstream has not changed in its last 8 edits of the file [ran: `git log`] |
| D3 | The identifier of a route is `auth_index`, never the auth ID | `auth_index` is what the console already holds for both logins (`auth_files.go:674`) and configured keys (`config_auth_index.go:17-63`). The ID of a configured key is a hash made from the key (`synthesizer/config.go:142-146`, `helpers.go:29-52`) |
| D4 | A login is named by `name`, `email` and `label`, exactly as `GET /v8/management/credentials` names it; a configured API key only by `label` and `host` | The credentials list already returns the email (`auth_files.go:719-721`) and the console shows it (`provimux-ui src/types/authFile.ts:50-55`). `Auth.AccountInfo()` is never used: for an API key it returns the key (`types.go:953-957`) |
| D5 | Cooldowns reuse `coreauth.CooldownSnapshotForAuth`, the projection the credentials list already shows | It gives reason codes only, never a provider's error text (`cooldown_view.go:9-19`, `:165-184`). The registry's own suspend reason can be raw text (`conductor_cooldown.go:1473-1489`) and is not used |
| D6 | The model a cooldown belongs to is resolved inside `sdk/cliproxy/auth` by a new read-only method, not by `Manager.ResolveExecutionModel` | `ResolveExecutionModel` advances the round-robin offset of an OpenAI-compatible alias pool (`conductor_models.go:174-178`, `:94-116`); a status page must not change which upstream model the next request gets |
| D7 | "Last served" comes from a new in-process usage plugin holding one timestamp per login and model, in memory | Usage records reach plugins whatever `usage-statistics-enabled` says [ran]; cost 84 ns per upstream attempt, off the request path [ran]. See `03-last-served.md` |
| D8 | Live-list state is read by a new file inside package `cliproxy` and handed to the handler through a source registered at `init()` | The store is unexported (`live_model_lists.go:54-69`) and the management package cannot import `sdk/cliproxy` (`sdk/cliproxy/service.go:12` imports `internal/api`); the other direction is free [ran: `go list -deps`] |
| D9 | Responses only grow: fields are added, never removed or redefined; a change of meaning gets a new path | Console and server release independently and the console replaces itself within 3 hours (decided for provimux-ui: independent release trains), so any console must work with any server |
| D10 | Home mode is not handled | With `home.enabled` every management route answers 404 before a handler runs (`server_management.go:213-228`) |

## Open questions for the owner

1. **Reason of a failed live-list fetch.** The fetcher only logs it (`live_model_lists.go:355`); the store keeps the retry time alone (`:125-129`). Showing it needs one added line in `sdk/cliproxy/live_model_lists.go`, a file of your live-lists patch, inside the layer commit. I recommend it: "HTTP 401" against "network" is what tells you to sign in again. Without the line the page shows when the fetch failed and when it retries, and sends you to the log for the reason. Third way: put the line in the patch itself, below the layer, which rewrites the stack. Details and costs in `02-live-lists.md`, section 4.
2. **A new file in `sdk/cliproxy/auth/`.** D6 adds `provimux_route_cooldowns.go` there: one read-only method, no edit to an existing file. The folder is not guarded (`guard.sh:28` protects `sdk/cliproxy/session/` only), but it is the scheduler's package. The alternative stays in the management package and matches cooldowns by model name; it then shows no cooldown for a model reached through `oauth-model-alias`. I recommend the new file.
3. **Trailer.** The guard accepts only `Provimux-Layer: branding` (`guard.sh:116`), so these feature commits carry "branding". Keep that (my recommendation, no guard change), or teach the guard a second value such as `console` (script, selftest and SYNC.md change)?
4. **Last served is lost on restart.** The response says since when it has been recording, so the console can write "not since 07:00" instead of "never". Keeping it across restarts would mean writing a file about traffic while usage statistics are off. I recommend memory only.
5. **Emails.** D4 returns a login's email because the credentials list and the console already do. If you would rather keep these two endpoints email-free, the console can join on `auth_index` instead; say so and `email` leaves the contract before it is built.

## What I ran

| Check | Result |
|---|---|
| A usage plugin with `usage-statistics-enabled` off: `redisqueue.SetUsageStatisticsEnabled(false)`, a named plugin, `helps.NewUsageReporter(...).EnsurePublished` and `.PublishFailure` | Both records delivered, with `AuthID`, `AuthIndex`, `Model`, `Alias` (the model as the client asked, suffix included), `AuthType`, `Failed` |
| Benchmark of the recorder's hot path (suffix strip, lower-case, one map write under a mutex), Apple M4 Pro | 84 ns per record, 0 allocations |
| `GET http://127.0.0.1:8317/v8/management/provimux/model-routes` without a key, against a stock `cliproxyapi` 8.0.53 running locally | 404 |
| `go list -deps ./internal/api/handlers/management` | depends on `internal/registry`, `internal/thinking`, `sdk/cliproxy/auth`, `sdk/cliproxy/usage`; not on `sdk/cliproxy` |
| `git log` of `internal/api/server_management_v8.go` | 8 upstream edits since the v8 API was made, all between lines 33 and 55 |
| `openapi.yaml` through `openapi-spec-validator`; both example files and the two inline examples against its schemas, with unknown properties forbidden | valid OpenAPI 3.1.0; 0 errors; a copy with four planted faults (a missing key, a wrong type, a bad time, an extra key) gave four errors |

The probe test and the benchmark were scratch files and are in no commit.
