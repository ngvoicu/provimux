# `GET /v8/management/provimux/model-routes`

Per model, every enabled login or key that can serve it, in the order Provimux tries them. A *route* is one credential serving one model.

It is the management-side sibling of `GET /v1/models?include=accounts` (`sdk/api/handlers/openai/openai_models_accounts.go:58-88`). That endpoint is for clients, needs a proxy key and names no credential by design (`:16-25`). This one is for the operator, needs the management key, and names the login the way the credentials page already does.

## 1. Request

| | |
|---|---|
| Method, path | `GET /v8/management/provimux/model-routes` |
| Auth | management key, same middleware as the rest of `/v8/management` (see `05-console-contract.md`) |
| Query `model` | optional, repeatable. When present, only models whose `id` equals one of the values are returned (exact, case-sensitive). `?model=` with an empty value returns `"models": []` without reading any state: the console's cheap presence probe |
| Body | none |

## 2. Response

`200`, `application/json`. Every key shown is always present; a value that is not known is `null`. Times are RFC 3339 in UTC. Full example: `examples/model-routes.json`; schema: `openapi.yaml`.

```json
{
  "observed_at": "2026-11-19T18:04:11Z",
  "last_served": { "recording_since": "2026-11-19T07:00:02Z" },
  "models": [
    {
      "id": "claude-opus-5-5",
      "display_name": "Claude Opus 5.5",
      "owned_by": "anthropic",
      "limits": { "context": 200000, "output": 64000 },
      "routes": [
        {
          "auth_index": "a8d167b7d82c8c15",
          "provider": "claude",
          "kind": "oauth",
          "priority": 10,
          "host": null,
          "login": { "name": "claude-ada@example.com.json", "email": "ada@example.com", "label": "ada@example.com" },
          "limits": { "context": 1000000, "output": 64000 },
          "reasoning": { "levels": ["low", "medium", "high", "max"], "budget": { "min": 1024, "max": 64000, "zero_allowed": true, "dynamic_allowed": false } },
          "available": true,
          "cooldown": null,
          "last_served_at": "2026-11-19T17:58:40Z"
        },
        {
          "auth_index": "5f0c1d2e3a4b5c6d",
          "provider": "claude",
          "kind": "apikey",
          "priority": 0,
          "host": "api.anthropic.com",
          "login": { "name": null, "email": null, "label": "claude-apikey" },
          "limits": { "context": 200000, "output": 64000 },
          "reasoning": { "levels": [], "budget": { "min": 1024, "max": 64000, "zero_allowed": true, "dynamic_allowed": false } },
          "available": false,
          "cooldown": { "scope": "model", "reason": "quota", "retry_at": "2026-11-19T18:31:00Z", "remaining_seconds": 1609, "http_status": 429, "backoff_level": 2 },
          "last_served_at": null
        }
      ]
    }
  ]
}
```

Order: models by `id` (lower-case, then exact). Routes as `include=accounts` orders them (`openai_models_accounts.go:76-86`): priority descending, then provider, then kind; then `login.label`, then `auth_index`, so the order is stable between two calls.

## 3. Where each field comes from

All sources are in memory; the handler makes no network call and writes nothing.

### The set of models and routes

| Step | Source | Evidence |
|---|---|---|
| Credentials | `authManager.List()`, which returns clones | `sdk/cliproxy/auth/conductor_selection.go:2014-2022` |
| Skip disabled | `auth.Disabled`, as `include=accounts` does | `openai_models_accounts.go:65-67` |
| Models of a credential | `registry.GetGlobalRegistry().GetModelsForClient(auth.ID)`: the credential's own copy of each `ModelInfo` | `internal/registry/model_registry.go:2254-2257`, own copies preferred at `:2213-2229`, stored at registration `:598-603` |

A model appears when at least one enabled credential registered it, also while every route of it is cooling. That differs on purpose from `/v1/models`, which lists only models that can be served now (`sdk/api/handlers/openai/openai_handlers.go:82-83`): the page is for seeing why a model is gone.

### Model fields

| Field | Source | Evidence |
|---|---|---|
| `id` | `ModelInfo.ID`: what a client sends, prefix and alias included | `model_registry.go:42` |
| `display_name`, `owned_by` | `registry.GetModelInfo(id, "")`, the published entry; `null` when empty | `model_registry.go:1968-1987`, fields `:55`, `:59` |
| `limits` | the published entry reduced as in "Limits" below. `null` when the registry publishes none, including when routes disagree and one declares nothing | `ContextLengthUnknown`, `MaxCompletionTokensUnknown` at `model_registry.go:79-83`, set in `model_registry_limits.go:98-103`, `:121-134` |

The published limits are the registry's values before a list handler puts in a default (the Claude list fills 200000 and 64000, `model_registry.go:2048-2058`). So `limits.context: null` here means "no limit all routes agree on", which is what the patch `3ae6eb0d` set out to say.

### Route fields

| Field | Source | Evidence |
|---|---|---|
| `auth_index` | `auth.EnsureIndex()` under `authFileEntryMu`, through the existing `lockedAuthIndex` | `auth_files.go:286-293`; `sdk/cliproxy/auth/types.go:744-763` |
| `provider` | `auth.Provider`, e.g. `claude`, `codex`, `openai-compatible-openrouter` | `types.go:65-66`; OpenAI-compatible key `internal/util/provider.go:18-26` |
| `kind` | `auth.AuthKind()`: `oauth` (subscription login) or `apikey`; `null` when it returns "" | `sdk/cliproxy/auth/classification.go:6-7`, `:30-50` |
| `priority` | `auth.Priority()` | `sdk/cliproxy/auth/selector.go:802-819` |
| `host` | host of an API key's `base_url`, lower-case, no port, path, query or user info; `null` for logins and keys without a base URL. Same rule as `baseURLHost` | `openai_models_accounts.go:27-37` |
| `login.name` | `authFileListName(auth)`, only for a credential the credentials list shows (it has a `path`, or is runtime-only) | `auth_files.go:232-240`; listing rule `:659-666` |
| `login.email` | `authEmail(auth)`, under the same condition as `name` | `auth_files.go:1060-1078`; returned by the list at `:719-721` |
| `login.label` | `auth.Label`: for a login what its provider's login stored; for a configured key the entry's name (`openrouter`) or `<provider>-apikey` | `auth_files.go:678`; `internal/watcher/synthesizer/config.go:174`, `:251`, `:341`, `:421`, `:469`, `:542` |
| `limits` | the credential's own `ModelInfo`, reduced as below | `model_registry.go:67-78` |
| `reasoning` | the credential's own `ModelInfo.Thinking`; `null` when it has none | `model_registry.go:103`; `ThinkingSupport` `:149-164` |
| `available` | `!registry.IsModelSuspendedForClient(auth.ID, model.ID)` | `model_registry.go:1493-1510`; the scheduler writes this projection per route model at `sdk/cliproxy/auth/conductor_models.go:222-334` |
| `cooldown` | `coreauth.CooldownSnapshotForAuth(auth, now)` plus the key mapping of section 4 | `sdk/cliproxy/auth/cooldown_view.go:25-88` |
| `last_served_at` | the last-served recorder | `03-last-served.md` |

**Limits.** A credential declares its window in one of three fields and its output in one of two, depending on the provider. The reduction is the registry's own (`model_registry_limits.go:86-87`):

- `context` = the smallest positive of `ContextLength`, `MaxContextLength`, `InputTokenLimit`
- `output` = the smallest positive of `MaxCompletionTokens`, `OutputTokenLimit`
- none positive → `null`

For OpenRouter entries these are the values the OpenRouter patches put into the registry from OpenRouter's catalog; for Devin the per-variant output limits. Nothing provider-specific is needed in the handler.

**Reasoning.** `levels` is `Thinking.Levels` (an empty array for a budget-only model). `budget` is `{min, max, zero_allowed, dynamic_allowed}` from the same struct, or `null` when all four are zero. The console derives its wording ("levels: low to max", "budget 1,024 to 64,000") from these.

**Availability and cooldown are two facts.** `cooldown` is a timer that will end on its own, with a reason code. `available: false` with `cooldown: null` is a block without a timer: the login needs signing in again, its token expired, or the model is disabled for it. The console then links to the credentials page, which already explains those states (`auth_files.go:501-518`).

### What must never be in the response

| Not in | Because |
|---|---|
| `auth.ID` | for a configured key it is derived from the key (`synthesizer/config.go:142-146`) |
| `auth.AccountInfo()` | returns the API key for `apikey` (`types.go:953-957`); the console's own types warn about it (`provimux-ui src/types/authFile.ts:50-54`) |
| `Attributes`, `Metadata`, the base URL beyond its host, proxy URLs, headers | keys, tokens, user info |
| `StatusMessage`, `LastError.Message`, the registry's suspend reason | can carry a provider's raw text (`conductor_cooldown.go:1473-1489`) |

## 4. What is missing

Four things are not there today. None needs an edit to an existing file except the route seam.

| Missing | Why | Hook | Size |
|---|---|---|---|
| The route | no handler | one seam line, `04-layer-commits-tests.md` section 1 | 1 line in an upstream file |
| Last served | nothing stores which credential answered; `X-CPA-AUTH-KIND` is only a response header (`internal/logging/cpa_trace.go:16-18`, `:221`) | a usage plugin in a new file, `03-last-served.md` | new file |
| Which model a cooldown belongs to | see below | a read-only method in a new file of `sdk/cliproxy/auth` | new file |
| The host rule | `baseURLHost` is unexported in package `openai` | the 8 lines are copied into the handler's file; a test compares both endpoints | in the new file |

### The cooldown's model key

`CooldownSnapshotForAuth` returns, per credential, one view of scope `credential` or several of scope `model` with a `model_key`. That key is the scheduler's *selection key*, not the model ID a client sends: the credential's prefix is cut and an `oauth-model-alias` is resolved first (`conductor_models.go:206-220`, used at `:231-239`). A model called `opus` that an alias maps to `claude-opus-5-5` has its cooldown stored under `claude-opus-5-5`.

The only exported function that resolves a route model is `Manager.ResolveExecutionModel` (`conductor_models.go:191-204`). It must not be used here: for an OpenAI-compatible entry whose alias has several upstream models it calls `nextModelPoolOffset`, which advances the round-robin counter (`:174-178`, `:94-116`). Each page load would shift which upstream model the next real request gets.

So the layer adds one method next to the scheduler, using the same unexported key function it uses:

```go
// sdk/cliproxy/auth/provimux_route_cooldowns.go (new file, package auth)

// ProvimuxRouteCooldowns returns, for each of routeModels (model IDs as clients send them), the
// unexpired cooldown that blocks it on auth, if any. It reads only: no state of the manager changes.
func (m *Manager) ProvimuxRouteCooldowns(auth *Auth, routeModels []string, now time.Time) map[string]CooldownView
```

Rule it implements:

1. `views := CooldownSnapshotForAuth(auth, now)`.
2. A view of scope `credential` applies to every route model.
3. A view of scope `model` applies to a route model when `canonicalModelLookupKey(view.ModelKey)` equals `canonicalModelLookupKey(m.selectionModelKeyForAuth(auth, routeModel))`.
4. When both apply, the one with the later `RetryAt` is returned.

`selectionModelKeyForAuth` only cuts the prefix and reads the alias table (`conductor_models.go:206-220`, `oauth_model_alias.go:145-151`); it changes nothing. It is also the key the scheduler uses when it writes `available` into the registry (`conductor_models.go:231-239`), so `cooldown` and `available` are derived from the same key and cannot disagree about which model is meant.

One case stays coarse, as it is in the registry: an alias of an OpenAI-compatible entry that stands for several upstream models. Each upstream model cools under its own name (`conductor_models.go:345-363`), and the alias has no state of its own for the projection to find (`:231-239`), so the route shows `available: true`, `cooldown: null` whatever its members do [infer from those lines, not run]. The members' timers remain on the credentials page. A test in `04-layer-commits-tests.md` pins this behaviour so a change upstream is noticed.

The view becomes the `cooldown` object unchanged in meaning: `scope`, `reason`, `retry_at`, `remaining_seconds`, `http_status`, `backoff_level` (`cooldown_view.go:11-19`). `model_key` is left out: the route already says which model. Reason codes are the closed set of `cooldown_view.go:134-184`: `quota`, `credential_quota`, `transient_rate_limit`, `transient_error`, `unauthorized`, `payment_required`, `not_found`, `model_not_supported`, `invalid_grant`, `cloudflare_challenge`, `unknown`. Devin's per-model cooling (patch `8f11f26b`) shows here as scope `model`, reason `quota`; with the option off, as scope `credential`.

**If the owner declines the new file in `sdk/cliproxy/auth`** (open question 2): the handler matches by name instead, comparing the view's key with the route model after cutting `auth.Prefix + "/"` and the thinking suffix. Models behind an `oauth-model-alias` then show `cooldown: null`; `available` stays right, since the scheduler computes it. The contract does not change.

## 5. Errors

| Status | When | Body |
|---|---|---|
| 401 | key missing or wrong | `{"error": "missing management key"}` / `"invalid management key"` (`handler.go:458-461`, `:477-480`) |
| 403 | remote client while remote management is off; no key configured; address banned after 5 failures | `{"error": "..."}` (`handler.go:425-427`, `:454-456`, `:413-418`) |
| 404 | management not enabled, or home mode (`server_management.go:213-228`); or the server has no such route (stock CLIProxyAPI, older Provimux) | empty |

With no auth manager the handler answers `200` with `"models": []`.

## 6. Size and cost

- One `List()` (clones every credential), one `GetModelsForClient` per credential (clones its models, as `include=accounts` does), one `IsModelSuspendedForClient` per route and one `GetModelInfo` per model, each a short read lock. For 20 credentials and 150 models that is a few thousand lock acquisitions and well under a megabyte of JSON. Nothing is cached; the console asks when the page is open (`05-console-contract.md`).
- No state is written, no provider is contacted, no round-robin position moves.
