# `GET /v8/management/provimux/live-lists`

Per login the `models.live-lists` patch covers: whether its models come from the list its provider returned for that account ("live") or from the built-in catalog, when that list was fetched, how many models, the last failed fetch and when the next one is due.

The patch (`c5d06f40` and three fixes) fetches the list of every Claude, Codex and Meta login in the background and registers the catalog filtered by it (`sdk/cliproxy/live_model_lists.go:26-33`). Today its state is visible only as two log lines (`:355`, `:361`).

## 1. Request

| | |
|---|---|
| Method, path | `GET /v8/management/provimux/live-lists` |
| Auth | management key, same middleware as the rest of `/v8/management` |
| Query, body | none |

## 2. Response

`200`, `application/json`. Every key shown is always present; unknown is `null`. Times are RFC 3339 in UTC. Full example: `examples/live-lists.json`.

```json
{
  "observed_at": "2026-11-19T18:04:11Z",
  "enabled": true,
  "providers": ["claude", "codex", "meta"],
  "timing": { "refresh_seconds": 10800, "retry_seconds": 600, "scan_seconds": 600 },
  "logins": [
    {
      "auth_index": "a8d167b7d82c8c15",
      "provider": "claude",
      "login": { "name": "claude-ada@example.com.json", "email": "ada@example.com", "label": "ada@example.com" },
      "disabled": false,
      "source": "live",
      "reason": null,
      "fetching": false,
      "fetched_at": "2026-11-19T16:10:44Z",
      "listed_models": 11,
      "registered_models": 9,
      "last_failure": null,
      "next_retry_at": null,
      "refresh_due_at": "2026-11-19T19:10:44Z"
    },
    {
      "auth_index": "0b7e2a91c4d3f6e8",
      "provider": "codex",
      "login": { "name": "codex-ada@example.com-plus.json", "email": "ada@example.com", "label": "ada@example.com" },
      "disabled": false,
      "source": "catalog",
      "reason": "failed",
      "fetching": false,
      "fetched_at": null,
      "listed_models": null,
      "registered_models": 14,
      "last_failure": { "at": "2026-11-19T17:58:02Z", "http_status": 401, "message": "HTTP 401 from /backend-api/codex/models" },
      "next_retry_at": "2026-11-19T18:08:02Z",
      "refresh_due_at": null
    }
  ]
}
```

Order of `logins`: provider, then `login.name` (lower-case), then `auth_index`.

### Top level

| Field | Meaning | Source |
|---|---|---|
| `enabled` | `models.live-lists` is on | `cfg.Models.LiveLists` from the handler's config snapshot (`internal/registry/catalog_config.go:15-17`; `handler.go:157-171`). Home mode, the patch's other condition (`live_model_lists.go:219`, `:284`), cannot reach a management handler |
| `providers` | providers whose logins have live lists | the cases of `liveModelListProvider` (`live_model_lists.go:207-209`) |
| `timing.refresh_seconds` | a list older than this is fetched again | `registry.ModelsRefreshInterval`, 3 h (`internal/registry/model_updater.go:16-17`), used at `live_model_lists.go:147` |
| `timing.retry_seconds` | wait after a failed fetch | `liveModelListRetry`, 10 min (`live_model_lists.go:37`, `:128`) |
| `timing.scan_seconds` | how often due logins are looked for | `liveModelListScanInterval`, 10 min (`:36`, `:300`) |

### One login

A row exists for every credential for which `liveModelListProvider(auth)` is not empty: a login (not an API key) of Claude, Codex or Meta (`live_model_lists.go:203-212`), disabled ones included. Logins of other providers and all API keys keep the catalog by design (`:33`) and are not listed; `providers` tells the console which ones are.

| Field | Meaning | Source |
|---|---|---|
| `auth_index`, `provider`, `login` | as in model-routes | `01-model-routes.md` section 3 |
| `disabled` | the login is switched off; it is never fetched | `auth.Disabled`; skipped at `live_model_lists.go:328` |
| `source` | `live`: a list of this account is held and filters the login's models. `catalog`: the catalog is used unchanged | `liveModelLists.get(auth.ID, liveModelListIdentity(auth))` (`:105-113`), the test `applyLiveModelList` itself makes (`:222-227`) |
| `reason` | why `catalog`: see below; `null` when `live` | derived |
| `fetching` | a fetch for this login is running now | `inFlight[auth.ID]` (`:58`, `:151-167`) |
| `fetched_at` | when the held list arrived; `null` without one | `liveModelList.fetchedAt` (`:51`) |
| `listed_models` | how many model IDs the provider returned; `null` without a list | `len(list.ids)`, the number in the log line (`:361`) |
| `registered_models` | how many models the login serves now, after the filter, the provider's additions, built-ins and exclusions | `len(registry.GetModelsForClient(auth.ID))` (`internal/registry/model_registry.go:2254-2257`) |
| `last_failure` | the latest fetch attempt failed; `null` once a fetch succeeds | section 4 |
| `next_retry_at` | not fetched again before this, after a failure; `null` otherwise | `nextRetry[auth.ID]` (`:57`, `:125-129`), removed on success (`:121`) |
| `refresh_due_at` | when the held list becomes due again: `fetched_at` + `refresh_seconds`; `null` without a list | `due` (`:147`) |

`reason` for `source: "catalog"`, first that applies:

| `reason` | When |
|---|---|
| `option_off` | `enabled` is false. The fetcher then drops every list (`:313-317`) |
| `login_disabled` | `disabled` is true and no list is held |
| `failed` | no list is held and a failure is waiting for its retry |
| `pending` | no list is held yet: the first fetch has not finished, or the auth file now holds another account and the old list was set aside (`:109-110`, `:141-143`) |

A login can be `source: "live"` and carry `last_failure`: the newest fetch failed and the previous list is kept (`:355`, "keeping the previous list"). The console shows that as a live list that is getting old.

`next_retry_at` and `refresh_due_at` are earliest times, not appointments: the fetch happens at the next scan after them, up to `scan_seconds` later, or at once on `POST /v8/management/models/refresh`, which forces every login and ignores the retry wait (`internal/api/handlers/management/model_definitions.go:35-37`; `live_model_lists.go:186-194`, `:328`).

## 3. How the handler reaches the fetcher's state

The state is a package variable of `sdk/cliproxy` with unexported fields (`live_model_lists.go:54-69`). The handler's package cannot import `sdk/cliproxy`, which imports `internal/api` (`sdk/cliproxy/service.go:12`). The patch solved the same problem in the other direction with a function pointer held by `internal/registry` (`internal/registry/live_model_lists.go:5-23`). This design does the same, with the holder in the handler's own package because the function takes a `*coreauth.Auth`, which `internal/registry` cannot import (`sdk/cliproxy/auth` imports it).

```go
// internal/api/handlers/management/provimux_live_lists.go (new file, package management)

// ProvimuxLiveListState is one login's live model list as the fetcher holds it.
type ProvimuxLiveListState struct {
	Covered      bool      // the login's provider has live lists and it is not an API key
	HasList      bool      // a list of this account is held
	FetchedAt    time.Time // zero without a list
	ListedModels int
	Fetching     bool
	NextRetryAt  time.Time // zero unless a failure is waiting for its retry
	Failure      *ProvimuxLiveListFailure
}

// ProvimuxLiveListFailure describes the latest fetch attempt when it failed.
type ProvimuxLiveListFailure struct {
	At         time.Time
	HTTPStatus int    // 0 when the failure was not an HTTP status
	Message    string // "" when not recorded
}

// ProvimuxLiveListSource reads the live-model-list fetcher's state. sdk/cliproxy provides it.
type ProvimuxLiveListSource interface {
	Providers() []string
	Timing() (refresh, retry, scan time.Duration)
	State(auth *coreauth.Auth) ProvimuxLiveListState
}

// SetProvimuxLiveListSource registers the source; nil removes it.
func SetProvimuxLiveListSource(source ProvimuxLiveListSource)

// ProvimuxLiveLists handles GET /v8/management/provimux/live-lists.
func (h *Handler) ProvimuxLiveLists(c *gin.Context)
```

```go
// sdk/cliproxy/provimux_live_list_status.go (new file, package cliproxy)

type provimuxLiveListSource struct{}

func (provimuxLiveListSource) Providers() []string
func (provimuxLiveListSource) Timing() (refresh, retry, scan time.Duration)
func (provimuxLiveListSource) State(auth *coreauth.Auth) management.ProvimuxLiveListState

func init() { management.SetProvimuxLiveListSource(provimuxLiveListSource{}) }
```

`State` uses `liveModelListProvider(auth)` and `liveModelListIdentity(auth)` and reads `lists`, `nextRetry` and `inFlight` under `liveModelLists.mu`, copying scalars only. It is in the same package as the store, so it needs no change to the patch file. Registration at `init()` is right because the store is a package variable, alive whether or not a service is running.

The price is a compile-time tie between a layer file and the patch's unexported names (`liveModelLists`, its three maps, `fetchedAt`, `ids`, `liveModelListProvider`, `liveModelListIdentity`, `liveModelListRetry`, `liveModelListScanInterval`). If the patch is reshaped, or merged upstream and changed there, the layer stops compiling and the sync check reports it; the fix is in the one layer file.

Without a registered source (a test binary that does not link `sdk/cliproxy`) the handler answers `503 {"error": "live-list status unavailable"}`. Not 404: the console must not take that for an absent endpoint.

## 4. The last failure: what the store has and what it lacks

| | Stored today | Evidence |
|---|---|---|
| That the latest attempt failed | yes: `nextRetry[authID]` exists | `:125-129`; cleared by a success at `:121` |
| When it failed | derivable: `nextRetry[authID]` minus `liveModelListRetry` | `:128` |
| Why | **no**: the error goes to the log at warn and is dropped | `:353-357` |

The reasons that occur: `HTTP <status> from <path>` (`:409`), `prepare credentials: ...` (`:376`), `no <provider> executor` (`:369`), `<provider> model list: invalid JSON` (`:491`, `:534`, `:585`), `claude model list: incomplete (has_more with limit 1000)` (`:495`), `<provider> returned no models` (`:483`), and transport errors from the executor's HTTP client (`:395-398`).

Three ways to give the page the reason:

| | What changes | The page shows | Cost |
|---|---|---|---|
| **A. None** | nothing | "failed 17:58, retry 18:08" and a link to the log | the operator reads the log to learn it was a 401 |
| **B. One line in the layer commit (recommended)** | after `live_model_lists.go:354`: `noteLiveModelListFailure(snapshot.ID, err, liveModelListNow())`, a function of the new layer file | the status code and the message | a layer commit edits one line of a patch-owned file. A rebase conflicts only if the patch changes lines 353 to 357, and both sides are the owner's |
| **C. In the patch** | `fail(authID, now, err)` keeps the error in the store | the same | the live-lists patch grows, and a patch commit must sit below the layer (`guard.sh:123-125`), so the stack is rewritten |

With B the layer file keeps `map[authID]{at, message}` under its own mutex. `State` reports it only while `nextRetry[authID]` exists and drops entries without one, so a success or a removed login clears it without a second hook. `HTTPStatus` is read from a message that starts with `HTTP ` (the format at `:409`), with `strconv`, not a regular expression (AGENTS.md forbids the standard `regexp`).

**The message is not new information for this audience.** It is the text already written to the log (`:355`), which the same management key reads through `GET /v8/management/observability/logs`. It is still cut to one line and 200 characters. A request URL can appear in a transport error; its query holds `limit=1000` or `client_version=99.0.0` (`:426`, `:444`) and Go's `url.Error` hides a password in the URL.

With A the contract is unchanged: `last_failure` is `{"at": ..., "http_status": null, "message": null}`.

## 5. Errors

| Status | When |
|---|---|
| 401, 403 | as for every management route (`01-model-routes.md` section 5) |
| 404 | management not enabled or home mode; or the server has no such route |
| 503 | no source registered (never in a released binary) |

With no auth manager: `200` with `"logins": []`.

## 6. What this endpoint does not do

- It starts no fetch. Refreshing stays `POST /v8/management/models/refresh`, which the stock console already calls; it returns before the lists arrive (`model_definitions.go:34-37` only wakes the loop), so the console polls this endpoint afterwards (`05-console-contract.md`).
- It does not switch the option. That stays `PUT /v8/management/config/models/live-lists`.
- It does not list the models of a login. That is `GET /v8/management/credentials/models?name=` (`auth_files.go:336-381`).
