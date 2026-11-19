# How both endpoints fit the layer: files, seams, commits, tests

For the worker who builds PUI-14. The contract is `openapi.yaml`; the field sources are in `01` to `03`.

## 1. The rules they must fit

- `main` = upstream release tag + owner patches + layer commits; a layer commit carries the trailer `Provimux-Layer: branding`, and no commit without it may follow one (`.github/provimux/guard.sh:112-129`).
- A layer commit must not change anything under `internal/runtime/executor/`, `internal/auth/`, `internal/misc/`, `internal/translator/`, `sdk/cliproxy/session/` (`guard.sh:28`, checked per commit at `:116-122`).
- The layer stays small: new files, and single lines in upstream files.

Reading or importing a guarded package is not a change to it. The new files import `internal/registry`, `sdk/cliproxy/auth` and `internal/api/handlers/management`; none is guarded.

## 2. Files

| File | New | Package | Holds |
|---|---|---|---|
| `internal/api/server_provimux.go` | yes | `api` | `registerProvimuxRoutes`: every Provimux management route |
| `internal/api/handlers/management/provimux_model_routes.go` | yes | `management` | response types, `ProvimuxModelRoutes`, the host rule copied from `openai_models_accounts.go:27-37` |
| `sdk/cliproxy/auth/provimux_route_cooldowns.go` | yes | `auth` | `(*Manager).ProvimuxRouteCooldowns` (`01-model-routes.md` section 4) |
| `internal/api/handlers/management/provimux_live_lists.go` | yes | `management` | the source interface and its holder, response types, `ProvimuxLiveLists` |
| `sdk/cliproxy/provimux_live_list_status.go` | yes | `cliproxy` | the source over the fetcher's store, its `init()` registration, the failure note |
| `internal/api/server_management_v8.go` | **1 line added** | `api` | the seam |
| `sdk/cliproxy/live_model_lists.go` | **1 line added** (open question 1) | `cliproxy` | the failure note's call |

Names start with `provimux_` or `Provimux` so that no later upstream file or symbol can collide with them, as `internal/buildinfo/provimux.go` already does.

### The seam

After the middleware block of the v8 group, `internal/api/server_management_v8.go:20`:

```go
	v8.Use(s.managementAvailabilityMiddleware(), s.mgmt.Middleware(), func(c *gin.Context) {
		c.Set(management.ConfigV8ContextKey, true)
	})
	s.registerProvimuxRoutes(v8) // <- the one added line
	v8.GET("/config", s.mgmt.ConfigV8)
```

```go
// internal/api/server_provimux.go (new file, package api)

// registerProvimuxRoutes adds Provimux's own management endpoints to the authenticated v8 group.
// Stock CLIProxyAPI has none of them, so the console takes a 404 as "not a Provimux server".
func (s *Server) registerProvimuxRoutes(v8 *gin.RouterGroup)
// registers:
//   GET /provimux/model-routes -> s.mgmt.ProvimuxModelRoutes
//   GET /provimux/live-lists   -> s.mgmt.ProvimuxLiveLists
```

Why here and why one line:

- The routes inherit the group's three middlewares, so availability, the management key and the version headers are exactly those of every other v8 route. Nothing about auth is written again.
- Upstream changed this file 8 times since it created the v8 API, always between lines 33 and 55 (routing, observability, credentials, OAuth); lines 17 to 21 have not changed since [ran: `git log -U0`]. An insertion there has the least chance of meeting an upstream hunk.
- The brief allows one route line per endpoint. One call for both is one line less now and none for the next Provimux endpoint. If the owner prefers the literal form, the two `v8.GET` lines go in the same spot and `server_provimux.go` is not needed; nothing else changes.
- No collision is possible on the path: plugins can register management routes under `/v0/management/` only (`internal/api/server_management.go:247-249`, `:267-270`), and the only wildcard in the v8 group is `/config/*path` (`server_management_v8.go:26-28`).

### Why the handlers are methods of `*management.Handler`

The handler needs the auth manager and the current config. `Handler` already holds both and is kept current by the server on hot reload (`handler.go:49`, `:131-154`, `:216-225`); `configSnapshot()` gives a private copy (`:157-171`). A separate Provimux handler type would need its own wiring in `server.go` and its own reload calls: more lines in upstream files. The helpers the endpoints reuse are unexported functions of this package and need no edit: `authFileListName` (`auth_files.go:232-240`), `authEmail` (`:1060-1078`), `authAttribute` (`:1080-1085`), `isRuntimeOnlyAuth` (`:1087-1092`), `lockedAuthIndex` (`:286-293`).

The auth manager is read under `h.mu`, as `SetAuthManager` writes it (`handler.go:220-224`).

### Conventions of the repository that apply (AGENTS.md)

- JSON through `github.com/MoeForge/moego/json`, never `encoding/json`; no standard `regexp`.
- `gofmt`; shadowed errors named `errXxx`; no panics in handlers; no secrets in logs.
- No wall-clock `time.Sleep` in tests about time: inject the clock.
- After changes: `go build -o test-output ./cmd/server && rm test-output`.

## 3. Commits

Three layer commits, in this order, each with the trailer `Provimux-Layer: branding` (the only value the guard accepts, `guard.sh:116`; open question 3). Each builds and passes its tests alone.

| # | Subject | Adds | Edits |
|---|---|---|---|
| 1 | `feat(provimux): model routes for the console` | `server_provimux.go` (with the model-routes line), `provimux_model_routes.go`, `provimux_route_cooldowns.go`, their tests | `server_management_v8.go` +1 |
| 2 | `feat(provimux): live-list status for the console` | `provimux_live_lists.go`, `provimux_live_list_status.go`, their tests | `server_provimux.go` +1 (the layer's own file); `live_model_lists.go` +1 if the owner takes option B |
| 3 | `docs(provimux): management endpoints for the console` | nothing | `.github/README.md`: a short section naming both endpoints and linking `openapi.yaml` of this folder, next to "Which account serves each model" (`:29-60`) and "Live model lists for each login" (`:67-86`); this folder's files where the build made the design wrong |

Commit 1 is first because it carries the seam. Commit 2 touches no upstream-owned file unless option B is taken. They ship together in one Provimux release (`v8.0.78-pmx.<n>`), published by the existing automatic flow; the release notes name the two paths, since the console's pages appear only against a Provimux that has them.

If the owner chooses option C for the failure reason (in the patch), that is a fourth change of another kind: an amendment of the live-lists patch below the layer, pushed as a rebased stack, before commit 2.

## 4. Tests

All in new `provimux_*_test.go` files, so no upstream test file changes. Fixtures follow `sdk/api/handlers/openai/openai_models_accounts_test.go:21-51`: a `coreauth.NewManager(nil, nil, nil)`, credentials registered on it, models registered in the global registry and unregistered in `t.Cleanup`.

### `internal/api/handlers/management/provimux_model_routes_test.go`

| Test | Asserts |
|---|---|
| lists the routes of each model | a login, an API key and an OpenAI-compatible entry on one model: `provider`, `kind`, `priority`, `host`; order priority descending; a disabled credential is absent |
| names a login as the credentials list does | a file-backed login has `name`, `email`, `label`; a configured key has `label` only, `name` and `email` null |
| carries no secret | with a key `secret-key`, a base URL `https://user:secret-pass@OpenRouter.ai:443/api/v1?token=secret-token`, tokens in `Metadata` and a `LastError.Message` `upstream said secret-body`: none of those strings, no auth ID of the configured key and no `/api/v1` is in the body; `host` is `openrouter.ai` |
| limits per route and for the model | two routes with windows 1,000,000 and 200,000: each route shows its own, the model shows 200,000; a third route declaring no window makes the model's `context` null and that route's `context` null |
| limits read every field a provider uses | a Gemini-style route (`InputTokenLimit`, `OutputTokenLimit`) and a configured override (`MaxContextLength`) reduce as `model_registry_limits.go:86-87` |
| reasoning | levels and budget from `Thinking`; `null` without it; `budget` null when all four are zero |
| recorded cooldown of one model | a model-scope quota cooldown shows on that model's route only, with `scope: "model"`, `model`, `reason: "quota"`, `until`; the other models of the credential show `null` |
| recorded cooldown of the credential | a `credential_quota` cooldown shows on every route of the credential, with `model: null` |
| names only listed credentials | a runtime credential (AI Studio channel ID as ID, label, email) and a login the credentials list hides: `name`, `email`, `label` null, `auth_index` set, the ID and the email nowhere in the body |
| `?model=` | one value returns that model only; two values both; an empty value returns `[]`; unknown returns `[]` |
| empty | no credentials: `"models": []`, not `null` |
| needs the management key | through `h.Middleware()` as `provimux_version_test.go:24-43` does: 401 without a key, 200 with it |

### `sdk/cliproxy/auth/provimux_route_cooldowns_test.go`

| Test | Asserts |
|---|---|
| prefix | credential prefix `team`, route model `team/m`, state under `m`: the cooldown is returned for `team/m` |
| OAuth alias | `oauth-model-alias` maps `opus` to `claude-opus-5-5`, state under `claude-opus-5-5`: returned for `opus` |
| thinking suffix and case | state under `M`, route `m(high)`: returned |
| credential and model | both present: the later `RetryAt` wins |
| expired | a state whose retry time is past: nothing returned |
| agrees with the scheduler | for the fixtures above (credential enabled and authorized), a cooldown is returned exactly when `clientModelProjectionForAuth(...).Suspended` is true with a deadline. A disabled or unauthorized credential is left out of this comparison: its projection is open-ended while a timer can still exist (`cooldown_view.go:21-24`) |
| pooled alias | an OpenAI-compatible alias with two upstream models, one cooling: nothing returned for the alias (pins the behaviour described in `01`, section 4) |
| **changes nothing** | with that pooled alias: after one `ProvimuxRouteCooldowns` call the manager's pool offsets are equal and the next `ResolveExecutionModel` result is the one without the call (one read is enough to move a rotating pool); the credential's `ModelStates` are deep-equal before and after |

### `internal/api/handlers/management/provimux_live_lists_test.go`

A fake `ProvimuxLiveListSource`, so the handler is tested without `sdk/cliproxy`.

| Test | Asserts |
|---|---|
| live | `HasList`: `source: "live"`, `fetched_at`, `listed_models` |
| catalog | a disabled login, a failed one and one still fetching: `source: "catalog"` without `fetched_at`; `last_failure`, `disabled`, `fetching` carried; the option off gives `catalog` even with a held list |
| stale | a list plus a failure: `source: "live"` with `last_failure` |
| failure without a status | a `network` failure: `"category": "network", "http_status": null` |
| rows | only credentials the source reports `Covered`; an API key of Claude is absent; a disabled login is present |
| registered count | equals the registry's count for the credential |
| no source | 503, not 404 |
| needs the management key | 401 without, 200 with |

### `sdk/cliproxy/provimux_live_list_status_test.go`

Uses the patch's `resetLiveModelLists` helper (`live_model_lists_test.go:18-30`) and `liveModelListNow`.

| Test | Asserts |
|---|---|
| covered | Claude, Codex and Meta logins are; an API key and a Gemini login are not |
| after `put` | `HasList`, `FetchedAt`, `ListedModels`; no failure |
| another account | a list stored under the same auth ID with another identity: `HasList` false |
| after `fail` | `Failure.At = now`; category and status for each error format, including a wrapped Meta key-exchange error (`credentials`, no status) and a `*url.Error` (`network`) |
| success clears | `fail`, then `put`: `Failure` nil, and the layer's note for that ID is gone |
| in flight | between `start` and `finish`: `Fetching` |
| replaced account, removed logins, newer failure | another account in the file does not see the failure; 1,000 removed logins leave no notes after one read; a note stops counting when a newer failure replaces its retry |
| pruner without polling | 1,000 removed logins' notes are gone after one tick of the pruner, with no read and no new failure |
| registered at init | the handler answers 200 in this test binary (503 without a source) |

### `internal/api/server_provimux_test.go`

The table of `TestManagementV8RoutesShareAccessControl` (`internal/api/server_management_v8_test.go:23-34`), for both paths: authorized 200, missing key 401, management disabled 404, home mode 404. Plus: `GET /v8/management/provimux/nope` is 404 with a valid key.

### `test/provimux/model_routes_parity_test.go`

One set of credentials and models; `GET /v1/models?include=accounts` and `GET /v8/management/provimux/model-routes` both called. For every model both list the same routes in the same order by (`provider`, `kind`, `priority`, `host`). This is what catches the copied host rule, or the order, drifting from the patch.

### What the checks already in the repository do with this

- `provimux-guard.yml`: the three commits carry the trailer and touch no protected path.
- `provimux-check.yml` compares `go test ./...` with upstream's: the new tests are additions; a new failure fails the check.
- After each upstream release the sync check rebuilds the stack. The two places where this layer leans on unexported names (`sdk/cliproxy/auth`: `selectionModelKeyForAuth`, `canonicalModelLookupKey`; `sdk/cliproxy`: the live-list store) fail at compile time there if upstream or the patch renames them, before anything is published.

## 5. Acceptance of PUI-14 against this design

1. Both paths answer as `openapi.yaml` says; `examples/*.json` are possible outputs.
2. `git diff <base of the layer commits>..HEAD --stat` shows only new files, plus one added line in `server_management_v8.go` (and one in `live_model_lists.go` under option B).
3. The guard passes; `go test ./...` has no new failure; `go build ./cmd/server` passes.
4. On a running Provimux: `POST /v8/management/models/refresh` followed by live-lists shows `fetching` and then a new `fetched_at` or `last_failure.at`.
5. A stock `cliproxyapi` still answers 404 on both paths.
