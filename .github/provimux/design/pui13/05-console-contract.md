# What the console (provimux-ui) relies on

For PUI-16. Server side in `01` to `04`; schema in `openapi.yaml`.

## 1. Auth

Nothing new. Both routes are in the v8 management group, so they pass the same three middlewares as `GET /v8/management/credentials` (`internal/api/server_management_v8.go:17-20`):

| Middleware | Effect | Evidence |
|---|---|---|
| availability | 404 when management is not enabled or in home mode | `internal/api/server_management.go:204-228` |
| management key | `Authorization: Bearer <key>` or `X-Management-Key`; 401 missing or wrong, 403 remote access off or address banned after 5 failures; sets `X-CPA-VERSION`, `X-CPA-COMMIT`, `X-CPA-BUILD-DATE`, `X-CPA-SUPPORT-PLUGIN` | `internal/api/handlers/management/handler.go:351-382`, `:386-484` |
| v8 marker | sets the v8 context key | `server_management_v8.go:18-20` |

The console calls them through its existing API client with the prefix `/v8/management` (`provimux-ui src/utils/constants.ts:17`), which already attaches the key and turns a 401 into the logout event (`src/services/api/client.ts:175-190`). No proxy API key is needed, unlike `/v1/models?include=accounts`.

## 2. Detecting that an endpoint is absent

The console must work against stock CLIProxyAPI and against a Provimux older than these endpoints. Both answer **404** on the two paths: an unknown path falls to the no-route handler, which returns 404 for everything outside `/v0/management` (`internal/api/server_management.go:255-270`), without running the key check.

[ran] `GET /v8/management/provimux/model-routes` without a key against a stock `cliproxyapi` 8.0.53 running locally: 404.

| Answer | The page |
|---|---|
| 200 | shown |
| 404 | hidden: no nav item, the route redirects to the dashboard |
| 401 | the client's existing logout handling |
| anything else, or no answer | shown, with the error and a retry. Never hidden: a server that is restarting must not make the page vanish |

The client keeps the status on the error (`client.ts:181`), and the console already has this pattern: `'loading' | 'ready' | 'unsupported' | 'error'`, with 404 mapped to `unsupported` (`src/features/authFiles/modelRules/useModelRulesData.ts:9`, `:65-68`).

**Each page probes its own endpoint, once per connection:**

| Page | Probe | Why this request |
|---|---|---|
| Model routes | `GET /v8/management/provimux/model-routes?model=` | an empty `model` value returns `{"models": []}` without reading any state; the full list can be hundreds of kilobytes |
| Live lists | `GET /v8/management/provimux/live-lists` | small: one row per login |

- Probe after login and again when the server's `X-CPA-VERSION` changes (the server was upgraded while the tab stayed open).
- Optional saving: skip both probes when the version has no `-pmx.N` suffix; `provimuxBaseVersion` already tells (`src/provimux/version.ts:13-18`). The 404 stays the rule; the version is only a shortcut, and a `dev` build of Provimux has no suffix, so there the probes must run. I would not take the shortcut.
- A wrong key cannot be mistaken for absence: stock answers 404 before any key check, Provimux answers 401.

## 3. Refreshing

| Page | When to ask |
|---|---|
| Model routes | on open, on the refresh button, and every 30 s while the page is visible. Count cooldowns down in the browser from `retry_at`; `observed_at` gives the server's clock for the offset |
| Live lists | on open and on the refresh button. After `POST /v8/management/models/refresh` (the existing button), every 2 s until each login has `fetching: false` and either a `fetched_at` or a `last_failure.at` later than the click, for at most 60 s. The POST returns before the lists arrive |

Neither endpoint is cached by the server. Neither should be polled while the tab is hidden.

## 4. Compatibility rule

The console replaces itself within 3 hours of a release, whatever the server's version; the two release independently (decided for provimux-ui: independent release trains). So:

- The server only **adds** fields. It never removes one, changes a type or changes a meaning. A different meaning gets a new field or a new path.
- The console **ignores fields it does not know** and treats a field that is missing as `null`. A newer console against an older Provimux then shows "unknown" where a later field would be.
- The console treats an **enum value it does not know** as unknown, not as an error: `kind`, `cooldown.scope`, `cooldown.reason`, `source`, `reason`.
- There is no version number in the response; presence of a field is the signal.

## 5. Types

For `src/provimux/api/` (new files; the console's guard keeps layer commits out of the quota clients in `src/services/api/`, not out of new files elsewhere).

```ts
/** RFC 3339, UTC. */
type Timestamp = string;

interface ProvimuxLogin {
  /** Auth file name, as the credentials page shows it. Null for a configured API key. */
  name: string | null;
  /** Account email, as the credentials page shows it. Null when there is none, and for API keys. */
  email: string | null;
  /** For a configured key: the entry's name, e.g. "openrouter", or "<provider>-apikey". */
  label: string | null;
}

interface ProvimuxLimits {
  /** Context window in tokens; null when not known. */
  context: number | null;
  /** Largest output in tokens; null when not known. */
  output: number | null;
}

interface ProvimuxReasoning {
  /** Discrete effort levels; empty for a budget-only model. */
  levels: string[];
  budget: { min: number; max: number; zero_allowed: boolean; dynamic_allowed: boolean } | null;
}

interface ProvimuxCooldown {
  scope: 'credential' | 'model' | (string & {});
  /** A code, never a provider's text. */
  reason:
    | 'quota' | 'credential_quota' | 'transient_rate_limit' | 'transient_error' | 'unauthorized'
    | 'payment_required' | 'not_found' | 'model_not_supported' | 'invalid_grant' | 'cloudflare_challenge'
    | 'unknown' | (string & {});
  retry_at: Timestamp;
  /** At observed_at. */
  remaining_seconds: number;
  http_status: number | null;
  backoff_level: number | null;
}

interface ProvimuxModelRoute {
  /** Joins with auth_index of GET /credentials and auth-index of GET /config. */
  auth_index: string | null;
  provider: string;
  /** 'oauth' = subscription login (green), 'apikey' = API key (amber). */
  kind: 'oauth' | 'apikey' | null;
  priority: number;
  /** Host of an API key's base URL. */
  host: string | null;
  login: ProvimuxLogin;
  limits: ProvimuxLimits;
  reasoning: ProvimuxReasoning | null;
  /** False with cooldown null: blocked without a timer (sign in again, token expired, model disabled). */
  available: boolean;
  cooldown: ProvimuxCooldown | null;
  /** Null: not since last_served.recording_since. */
  last_served_at: Timestamp | null;
}

interface ProvimuxModel {
  id: string;
  display_name: string | null;
  owned_by: string | null;
  /** What all routes agree on; null where they do not or none declares it. */
  limits: ProvimuxLimits;
  /** In the order Provimux tries them: highest priority first. Never empty. */
  routes: ProvimuxModelRoute[];
}

interface ProvimuxModelRoutesResponse {
  observed_at: Timestamp;
  last_served: { recording_since: Timestamp };
  models: ProvimuxModel[];
}

interface ProvimuxLiveListLogin {
  auth_index: string | null;
  provider: string;
  login: ProvimuxLogin;
  disabled: boolean;
  source: 'live' | 'catalog' | (string & {});
  /** Why catalog; null when live. */
  reason: 'option_off' | 'login_disabled' | 'failed' | 'pending' | (string & {}) | null;
  fetching: boolean;
  fetched_at: Timestamp | null;
  /** Model ids the provider returned. */
  listed_models: number | null;
  /** Models the login serves now. */
  registered_models: number;
  last_failure: { at: Timestamp; http_status: number | null; message: string | null } | null;
  next_retry_at: Timestamp | null;
  refresh_due_at: Timestamp | null;
}

interface ProvimuxLiveListsResponse {
  observed_at: Timestamp;
  /** models.live-lists */
  enabled: boolean;
  providers: string[];
  timing: { refresh_seconds: number; retry_seconds: number; scan_seconds: number };
  logins: ProvimuxLiveListLogin[];
}
```

## 6. What the pages can show from this, and what they still take from elsewhere

| What a console page needs | From |
|---|---|
| accounts per model: sub or api, provider, host, priority | `routes[].kind`, `.provider`, `.host`, `.priority`; green for `oauth`, amber for `apikey` |
| which login | `routes[].login`; a link to the credentials page by `auth_index` |
| model limits per route | `routes[].limits`, with the model's `limits` as "what clients are told" |
| reasoning levels | `routes[].reasoning` |
| Devin per-model cooling: state | `routes[].cooldown` with `scope: "model"` on Devin routes |
| Devin per-model cooling: switch | still `PUT /v8/management/config/oauth/providers/devin/model-level-cooling` |
| OpenRouter entries with their limits | routes with `host: "openrouter.ai"`; the entries themselves still from `GET /v8/management/config` |
| last served | `routes[].last_served_at`, `last_served.recording_since` |
| live lists: per-login status | `live-lists` |
| live lists: switch, refresh | still `PUT /v8/management/config/models/live-lists` and `POST /v8/management/models/refresh` |

Not in either endpoint, on purpose: the upstream model behind an alias (resolving it changes routing state for pooled aliases, `01-model-routes.md` section 4), request counts and token counts (that is usage statistics), and the model IDs of a live list (`GET /v8/management/credentials/models?name=`).
