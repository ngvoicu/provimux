# Key model lists: every API key's list keeps itself up to date

Design, 2026-11-24. This is the design only: no code exists yet.

**The owner's decision (2026-11-24).** Subscriptions, Gemini keys and built-in keys with an empty list already get new models by themselves. A key with its own `models` list, which includes every `openai-compatibility` key, changes only when the owner presses "Get the list from …" in the console and saves. He asked "why not have all refresh themselves?", and the answer is to do it: **a key's list refreshes itself by default.**

**The chief's addition (2026-11-24).** `openai-compatibility` entries get `excluded-models`, with wildcards and the meaning it has on the built-in key types, applied after the refresh. Once a list refreshes itself, taking a model out of `models` no longer keeps it out; `excluded-models` is what does. This is part of the design, not an option: section 6.1.

**Read from.**

| Project | Commit |
|---|---|
| Provimux | `main` at `e6655b15e` (CLIProxyAPI `v8.1.2`, release `v8.1.2-pmx.1`) |
| Provimux, parked | `providers-dev-lists` at `548a3b7db` and `combined-names` at `51cefac25`, both on `003d1f273` |
| provimux-ui | `main` at `fd1346d7` |

Every `file:line` of Provimux below is in `e6655b15e`. This design builds on `model-lists.md` (live lists for logins); "section 13" below means that document's.

**Tags.** **[read]** in code. **[ran]** executed by me. **[infer]** reasoned, not verified. I sent no request to any service and never contacted port 8317.

## 1. The design in one page

**The rule.** A key that is asked offers:

1. every row of its `models` in `config.yaml`, exactly as written: name, alias and every setting;
2. plus every model its service lists that no row already names, under the service's own id;
3. minus its `excluded-models`.

The service's list is read by the mechanism that reads a login's list: at start, every 3 hours, on `POST /v8/management/models/refresh`, and when a key is added or replaced; a failure is retried after 10 minutes; the list is kept in memory and in the disk cache, and stops counting after 7 days without a refresh. **Nothing is ever written to `config.yaml`.**

**The off switch.** `fixed-models: true` on a key's entry. The key is then never asked and offers exactly what the config says. Unset means the list refreshes. `models.live-lists: false` turns it off for every key and login at once, as today.

**A model the service stops listing.** One it only learned from the service is dropped at the next complete answer. A row in `config.yaml` is never dropped, listed or not; the status endpoint counts the rows the service does not list, so the console can mark them (section 3.3).

| Piece | Where | What it does |
|---|---|---|
| The fetch | the live-list fetcher, widened to keys (`sdk/cliproxy/live_model_lists.go`, a layer file) | Asks each key's service for `/models`, the request the console's button sends |
| The merge | new file `sdk/cliproxy/provimux_key_lists.go`, three added lines in `service_models.go` | Adds a row, in memory only, for each listed model no row names, and builds it with the key type's own builder |
| Two config fields | four structs in `internal/config/config_types.go`, one line each plus one; section 6.1 for the rest of `excluded-models` | `fixed-models` on every key entry; `excluded-models` on `openai-compatibility` entries, which have none today |
| Status | `GET /v8/management/provimux/model-lists` grows a `keys` array | Per key: refreshed when, how many models learned, the last failure, why it is not asked |

Four findings shaped it.

- **A request for a model no row names already passes through unchanged** [read]. The conductor resolves a requested name through the key's rows; with no match it sends the name as it is (`sdk/cliproxy/auth/conductor_models.go:602-622`, and `:139-161`, `:540-544` for `openai-compatibility`). So a learned model needs only to be registered: no routing code changes, and the config need not know it.
- **The config does not record how a row got there.** A row typed by hand and a row saved from the button are the same YAML. "A model the owner typed by hand" can therefore only mean "a row in `config.yaml`", and a refresh that writes nothing cannot remove one.
- **`openai-compatibility` entries have no `excluded-models`** [read: the struct has no such field, `internal/config/config_types.go:1118-1161`; `registerCompat` applies none, `service_models.go:265-286`]. Today the owner keeps a model out by not listing it. Once the list refreshes itself that no longer works, so the field is added with the meaning it has on the built-in key types (section 6.1).
- **On by default changes what existing keys offer on the day of the upgrade, and may move traffic to a paid key.** Section 12, decision 1. This one needs the owner's word.

## 2. Today

### 2.1 What a key offers

| Key | `models` empty | `models` has rows | Where |
|---|---|---|---|
| `openai-compatibility` entry | nothing; the key is not registered | exactly the rows | `service_models.go:265-286`, `:897-956` |
| `claude-api-key`, `xai-api-key`, `meta-api-key` | the catalog of its type | exactly the rows | `:148-158`, `:188-198`, `:215-225` |
| `codex-api-key` | the Codex Pro catalog | exactly the rows | `:159-166`, `:1233-1284` |
| `gemini-api-key` on Google's host | Google's live list (section 13) | exactly the rows; the live list is fetched and not used | `:94-106` |
| `gemini-api-key` with its own `base-url`, `interactions-api-key`, `vertex-api-key` | the catalog | exactly the rows | `:94-129` |

`excluded-models` is applied after that for the built-in key types (`:345`); an `openai-compatibility` key returns before that line.

The catalog refreshes itself every 3 hours, so the left column already moves. The right column is what the owner asked about.

### 2.2 What "Get the list from …" does

The console builds a URL from the form's base URL and sends it through `POST /v8/management/api-call` with the key (`provimux-ui`, `src/features/providers/sheets/forms/useModelDiscovery.ts:64-146`, `src/services/api/models.ts`) [read].

| Key type | URL | Credential |
|---|---|---|
| `openai-compatibility` | `<base-url>/models` (as it is when it already ends in `/models`) | `Authorization: Bearer`, the entry's headers; on failure once more with no credential and no headers |
| Codex, xAI, Meta | `<base-url>/v1/models`, or `<base-url>/models` when the base ends in `/v1` | `Authorization: Bearer` |
| Claude | `<base-url or api.anthropic.com>/v1/models` | `x-api-key`, `anthropic-version: 2023-06-01` |
| Gemini, Interactions | `<base-url or Google's host>/v1beta/models`, pages until no `nextPageToken`, at most 20 | `x-goog-api-key` |
| Vertex | no button | |

From the answer it reads the array `data`, else `models`, else the answer itself; per entry the id from `id`, else `name`, `model`, `value`; and `display_name` (`src/utils/models.ts:48-101`). The owner then picks models and saves them as rows.

## 3. The rule in full

### 3.1 What a key offers

For a key that is asked (section 4) and holds a list of its service:

| | Rule |
|---|---|
| The config's rows | every row stands exactly as written, whether the service lists its model or not |
| Learned models | each id the service lists, except: an id a row already names (3.2), an id that reads as a thinking suffix (`name(high)`), an empty id. Offered under the service's own id |
| `excluded-models` | applied last, after the refresh, to rows and learned models alike, with the wildcards it has today (`internal/modelconfig/exclusion.go`). On `openai-compatibility` entries too (6.1), and there also when the key is fixed or holds no list |
| On top, unchanged | the key's `prefix`, plugin models, `force-model-prefix` |
| No list held (not fetched yet, failed, older than 7 days) | the rows alone, as today |
| `models` empty, `openai-compatibility` | the service's whole list. Today such a key offers nothing |
| `models` empty, built-in key type | unchanged: the catalog, or for a Gemini key on Google's host its live list. The catalog there already refreshes itself, and a live list would filter it, which is a different rule (section 12, decision 5) |

### 3.2 Names stay on their models

- A row is the owner's word on a model: its alias, its display name, its thinking, its window. A refresh never changes a row and never adds a second name for a model that has one.
- **A row names a listed id** when the row's `name`, without a thinking suffix and compared without case, equals it. `{name: anthropic/claude-opus-5.5(max), alias: opus-max}` names `anthropic/claude-opus-5.5`, so that model stays offered as `opus-max` only.
- **A row's client name is taken.** A listed id equal to a row's alias (or to its `name` when it has no alias) is not learned: on this key that name means what the row says. `{name: gpt-5.5-2026-03-01, alias: gpt-5.5}` keeps `gpt-5.5` pointing at the dated model even when the service lists `gpt-5.5`.
- Several rows with one alias (a model pool on `openai-compatibility`) are untouched.

### 3.3 A model the service stops listing

**Decision: learned models go, rows stay.** This is the chief's lean, and the reasons are:

| | Why |
|---|---|
| A learned model the service no longer lists is dropped at the next accepted answer | The list was the only reason it was offered. A retired id only produces errors, and a login already behaves this way. There is no grace period: an answer is accepted only when it is complete and names a model (section 5), which is the protection against a half answer |
| A row is never dropped | The config cannot say whether a row was typed or saved from the button, so "learned from the service" can only mean "not in the config". Removing a row would mean writing `config.yaml`, which a refresh never does. And a row the service does not list is often deliberate: a private deployment, a fine-tune, a dated version behind an alias |
| The cost | A row the owner once saved from the button stays after its service retires the model, until he removes it. The status endpoint reports per key how many rows the service does not list, and with `?auth_index=` which ones (section 7), so the console marks them "not in the service's list" beside the remove button |

A known limit: a service that answers `200` with a shorter but well-formed list makes the missing models disappear until its next answer. A login has the same limit today.

### 3.4 A learned model's values

**A learned model is the row the button would save**: `{name: <id>}`, with `display-name` when the service states one that differs from the id. It is built by the key type's own builder, so it gets what such a row gets today and nothing is invented.

| Key type | Builder | A learned model gets |
|---|---|---|
| `openai-compatibility` | `buildOpenAICompatibilityConfigModels` (`service_models.go:897`) | type `openai-compatibility`; upstream's default reasoning levels for a row without `thinking` (low, medium, high); no window. On OpenRouter: OpenRouter's window, output limit, reasoning efforts and vendor, through the lookup that exists (`:928-952`) |
| Claude, xAI, Meta, Gemini keys | `buildConfigModels` (`:997`) | the catalog's window, output limit and thinking when the catalog has the id on that channel; else none |
| Codex key | `buildCodexConfigModels` (`:1233`) | the same, with `support-configuration-update` off |

This gives the tests one sentence to hold: **what a key offers equals the builder's result for its rows followed by one bare row per learned id, minus `excluded-models`.**

Per-model settings that executors read from a row (`use-max-completion-tokens`, `is-compat`, `force-mapping`, image handling) find no row for a learned model and take their defaults [read: `internal/runtime/executor/helps/openai_compat_max_tokens.go:16-19`, `codex_executor_auth.go:613-621`]. A model that needs one of them needs a row, as today.

Not in this version: reading a window or limits from a service's own `/models` fields (they differ per service), and providers.dev's values for a key's learned models (section 9).

### 3.5 Two examples

An OpenRouter entry with these rows, whose service lists `anthropic/claude-opus-5.5`, `openai/gpt-5.5` and `x-ai/grok-5`:

```yaml
openai-compatibility:
  - name: openrouter
    base-url: https://openrouter.ai/api/v1
    excluded-models: ["x-ai/*"]
    models:
      - name: anthropic/claude-opus-5.5
        alias: or-opus
      - name: acme/retired-model      # no longer in OpenRouter's list
        alias: old
```

| Offered | Why |
|---|---|
| `or-opus` | the row; `anthropic/claude-opus-5.5` is not offered a second time |
| `old` | the row stays; status counts it as not listed |
| `openai/gpt-5.5` | learned |
| not `x-ai/grok-5` | excluded |

The same entry with `fixed-models: true` offers `or-opus` and `old`, and OpenRouter is never asked.

## 4. Which keys are asked, and which can't be reached

A key is asked when live lists are on, Provimux is not in home mode, its entry does not say `fixed-models: true`, and its type has a list endpoint below. The request is the one the console's button sends (2.2), to the host the owner configured for that key, with that key, its headers and its proxy.

| Key | Asked | Request |
|---|---|---|
| `openai-compatibility`, every key of the entry | yes | `GET <base-url>/models`. On `401` or `403` only, once more without the key and the entry's headers: some services publish `/models` openly and refuse the key there |
| `claude-api-key` with rows | yes | `GET <base-url or https://api.anthropic.com>/v1/models?limit=1000`, `anthropic-version: 2023-06-01`; a trailing `/v1` of the base is not doubled |
| `codex-api-key`, `xai-api-key`, `meta-api-key` with rows | yes | `GET <base-url>/v1/models`, or `<base-url>/models` when the base ends in `/v1`. Codex and xAI entries always have a `base-url` (`internal/config/config_normalization.go:310-326`, `:364-378`); a Meta key without one is asked at Meta's own host, `https://api.meta.ai/v1/models`, where a Meta login's list is already read |
| `gemini-api-key` with rows, own `base-url` | yes | `GET <base-url>/v1beta/models?pageSize=1000`, every page, at most 20 |
| `gemini-api-key` with rows, Google's host | already asked (section 13) | the held list is now added to the rows instead of ignored |
| a built-in key with no rows | no change | the catalog; a Gemini key on Google's host its live list |

**This reverses one line of `model-lists.md`** (section 3 there: "a key with its own `base-url` is never asked"). That line was about Provimux deciding by itself to ask a vendor's list on a host the user had pointed elsewhere. Here the request goes to the user's own host with the user's own key, and it is the request his console already sends when he presses the button. No host learns anything it did not already receive.

**Not reached, and why.**

| Key | Why | What it offers |
|---|---|---|
| A key with `fixed-models: true` | the owner's choice | exactly the config |
| Any key while `models.live-lists: false`, or in home mode | the global switch; home mode gets its models from the control plane | exactly the config |
| `vertex-api-key` | Vertex-style services have no list call in a shape Provimux knows; the console has no button for them either | its rows, or the catalog |
| `interactions-api-key` | not in this version: it shares the Gemini key's entry type but has no executor of its own with a raw request [read: no `HttpRequest` beyond `gemini_executor.go`]; added later if wanted | its rows, or the catalog |
| A service whose `<base-url>/models` does not exist (`404`), needs another path, or answers something that is not a list | nothing to read. The key keeps its rows; the status endpoint shows the failure (`http_status 404` or `invalid_response`), and the fetch is retried on the normal schedule | its rows |
| A service that lists only part of what it serves | its list is not the whole truth; rows cover the rest, which is why rows are never dropped | rows plus what is listed |
| A service that lists what the key cannot use, or non-chat models (embeddings, speech, images) | `/models` in OpenAI's shape says nothing about either. They are offered and fail when called. `excluded-models` with a wildcard (`text-embedding-*`) hides them | everything listed |
| Logins, AI Studio, Copilot, Antigravity | not keys; unchanged | section 13 |

## 5. Fetching, failures and the cache

Everything below is the live-list mechanism as built (`model-lists.md` 13.2), applied to keys. Nothing new is invented for keys.

| | |
|---|---|
| When | at start in the background; every 3 hours per key; on `POST /v8/management/models/refresh`; when a key is added or its key, `base-url` or headers change; after a failure, again in 10 minutes |
| Per key, not per entry | each key of an `openai-compatibility` entry is asked with its own key and proxy, and holds its own list: two keys of one service can see different models |
| Deadline and size | 30 seconds for the whole fetch; an answer above 8 MB is a failed fetch |
| Accepted only when | `200`, well formed, complete (Claude: no `has_more`; Gemini: every page), at least one model. Anything else keeps the held list |
| Failure | recorded as a code (`http_status` with the status, `network`, `invalid_response`, `credentials`, `other`), never the error text; logged as a warning with what is kept |
| Disk cache | `model-lists/live-<hash>.json`, the same files and format. The hash is of the key's type, host and a hash of the key: no key, header or name is in the file. Read before any request, so a start without network offers what the last run offered. Files are never deleted |
| After 7 days without a refresh | the list stops counting and the key offers its rows, with one warning |

**What must not change, and how it holds.**

| Must hold | How |
|---|---|
| A refresh writes nothing to `config.yaml` | learned rows exist only in the value the registration builds. The config struct is never changed, so a save from the console or a hot reload cannot carry them |
| A fetch never blocks a request or the start | registration reads the store under its mutex and returns; fetches run in their own goroutines, as for logins |
| A failed or missing `/models` keeps the last good list or the configured one | the held list stays until a complete answer replaces it or it turns 7 days old; with none held, the rows |
| A changed switch applies without a restart | a config reload registers every config key again (`service_config.go:317-360`) and the hook reads the entry from the new config. The worker proves it with a test (section 10) |

**Parsing.** For OpenAI-shaped answers, what the button reads (2.2): the array `data`, else `models`, else the answer itself; an entry is a string or an object with `id`, else `name`, `model`, `value`; `display_name` or `displayName` is kept when it differs from the id. Claude and Gemini answers go through the parsers that exist (`parseClaudeLiveModelList`, `parseGeminiLiveModelList`); on a Gemini-style host of the owner's a model that states no `supportedGenerationMethods` is kept, one that states them without `generateContent` is not.

**A key's list never feeds the catalog's learned models.** A login's list teaches the registry models of its type (`internal/registry/provimux_learned_models.go`). A key's list must not: a gateway behind a Claude key's `base-url` can list anything. Keys therefore have list kinds of their own (section 8), and only login kinds update that record.

## 6. The config

```yaml
openai-compatibility:
  - name: openrouter
    base-url: https://openrouter.ai/api/v1
    # Provimux: offer exactly the models below and never ask the service for its list.
    # Unset: the list refreshes itself (rows below, plus what the service lists).
    fixed-models: true
    # Provimux: hide models this entry would offer; wildcards as for other keys.
    excluded-models: ["text-embedding-*"]
    models: [...]
```

| Field | On | Type | Default |
|---|---|---|---|
| `fixed-models` | `openai-compatibility[]`, `claude-api-key[]`, `codex-api-key[]`, `xai-api-key[]`, `meta-api-key[]`, `gemini-api-key[]` | boolean, omitted when false | false: the list refreshes |
| `excluded-models` | `openai-compatibility[]` (new there) | list of patterns | none |
| `models.live-lists` | global, exists | boolean | on; `false` stops every key's and login's fetch |

- **Why `fixed-models: true` and not `refresh-models: false`.** The default is the unset state, so the default writes nothing, a plain boolean is enough (no pointer to survive a save, the trouble `live-lists` had: section 13.3), and the line reads as what it does.
- `fixed-models: true` on a built-in key with no rows gives the catalog; on a Gemini key on Google's host it turns that key's live list off too. The field means one thing everywhere: this key is never asked.
- **How the console writes them.** It saves keys with `PUT` of the whole list, which decodes into the config structs, so both fields pass with no handler edit [read: `config_lists.go:869-899`, `provimux-ui` `src/services/api/providers.ts:841-850`]. The `PATCH` handlers name their fields one by one and are left as they are: a `PATCH` keeps a field it does not name.

### 6.1 `excluded-models` on `openai-compatibility` entries

**Why it is required.** Today an `openai-compatibility` key offers exactly its rows, so the owner keeps a model out by not listing it. With a list that refreshes itself, a model taken out of `models` comes back at the next refresh if the service lists it. `excluded-models` is then the only way to say "not this one" without fixing the whole list.

**The meaning is the built-in key types', unchanged.**

| | |
|---|---|
| Patterns | exact ids and `*` wildcards, matched without case by `modelconfig.MatchExcludedModelPattern`, through `applyExcludedModels` (`service_models.go:728-762`): `x-ai/*`, `*-preview`, `text-embedding-*` |
| Matched against | the name a client uses, before the entry's `prefix` is added: a row's alias, or its `name` when it has none; a learned model's id. As for the built-in keys, where the list is applied before prefixes (`:345`, `:353`) |
| Applied | after the refresh: to the rows and the learned models together, as the last step before plugin models and the prefix |
| Applied when | always. Also for a key with `fixed-models: true`, with no list held, with `live-lists: false`, and in home mode: it is the entry's setting, not the refresh's |
| A list that excludes everything | the key offers its plugin models, or nothing, through the code that handles an entry without models today (`:277-284`) |
| Per entry | one list for all keys of the entry, as `models` is |

**Everything a built-in key's `excluded-models` passes through, and the same for these entries.**

| Place | Built-in keys today | Added for `openai-compatibility` |
|---|---|---|
| The struct | `ExcludedModels []string` with `yaml:"excluded-models,omitempty"` (`config_types.go:810`, `:941`, `:1058`) | the same field on `OpenAICompatibility` |
| Normalising | `NormalizeExcludedModels`: trimmed, lower case, no duplicates (`config_normalization.go:357`, `:374`, `:494`, `:520`) | one line in `SanitizeOpenAICompatibility` (`:290-308`), which runs at load and after every management write |
| The credential | `ApplyAuthExcludedModelsMeta` writes `excluded_models` and `excluded_models_hash` on the synthesized credential (`internal/watcher/synthesizer/helpers.go:60-90`), so a changed list changes the credential and it is registered again | the same call at the two places an entry's credentials are made (`synthesizer/config.go`, beside `models_hash` at `:414` and `:462`), with an empty auth kind so that `auth_kind` is not touched |
| Registration | `excluded = entry.ExcludedModels`, then `applyExcludedModels` (`service_models.go:104`, `:155`, `:345`) | inside the hook, read from the entry (section 8): this path returns before `:345` |
| `PUT` | decodes into the struct | nothing to add |
| `PATCH` | a field `excluded-models` in the patch body, normalised (`config_lists.go:212`, `:289`) | the same two lines in `PatchOpenAICompat` (`:906-1003`) |
| The reload log | `…excluded-models: updated (2 -> 3 entries)` (`internal/watcher/diff/config_diff.go:265`) | one detail line in `describeOpenAICompatibilityUpdate` (`internal/watcher/diff/openai_compat.go:73`), and one for `fixed-models` |
| The console | the excluded-models field of a key's sheet | the same field for this kind (7.2) |

Not copied: the built-in keys' "disable this key" switch, which the management API stores as a pattern in `excluded-models` (`config_apikey_disable.go`). These entries have `disabled` for that.

## 7. Status and the console

### 7.1 `GET /v8/management/provimux/model-lists`

The response grows a top-level array **`keys`**, one row per key, in the row shape `logins` has, plus an object `key`.

**Why a new array and not more rows in `logins`.** groundtruth's discovery reads `logins` with `?view=live` and publishes what each login type's vendor listed. A Claude key behind a gateway would arrive there as provider `claude` and its gateway's list would be published as Claude's. Released consoles also read `logins` as logins. A reader that does not know `keys` ignores it.

```json
{
  "observed_at": "2026-11-24T18:04:11Z",
  "logins": [],
  "keys": [
    {
      "auth_index": "c41d9a07b2e8f113",
      "provider": "openrouter",
      "login": { "name": null, "email": null, "label": "openrouter" },
      "disabled": false,
      "source": "live",
      "live": {
        "supported": true, "held": true, "expired": false,
        "fetched_at": "2026-11-24T16:10:44Z", "from_cache": false, "fetching": false,
        "listed_models": 342, "last_failure": null
      },
      "registered_models": 343,
      "learned_models": 341,
      "models": null,
      "key": {
        "type": "openai-compatibility",
        "fixed": false,
        "not_asked": null,
        "configured_models": 2,
        "unlisted_models": 1
      }
    }
  ]
}
```

| Field | For a key |
|---|---|
| `source` | `live`: a list of its service is held and added to its rows. `config` (new value): its rows alone decide, because no list is held, the list is older than 7 days, or the key is not asked. Clients already treat an unknown value as unknown |
| `live.*` | as for a login: `fetched_at` is when it was last refreshed, `listed_models` what the service named, `last_failure` the last failed fetch as `{at, category, http_status}` |
| `learned_models` | models this key offers that no row names |
| `key.type` | `openai-compatibility`, `claude`, `codex`, `xai`, `meta`, `gemini`, `interactions`, `vertex` |
| `key.fixed` | the entry says `fixed-models: true` |
| `key.not_asked` | `null` when asked; else `fixed`, `live_lists_off` or `no_list_endpoint`. `live.supported` is false exactly when this is not null |
| `key.configured_models` | rows in the config |
| `key.unlisted_models` | rows whose model the held list does not name; `null` without a held list |

- **Which keys get a row:** every `openai-compatibility` key, and every built-in key that has rows. A Gemini key on Google's host keeps its row in `logins`, as released, and has a second one in `keys` when it has rows.
- `?auth_index=` finds a key as it finds a login and fills `models`: a learned model has `source: "live"` and `learned: true`; a row has `source: "other"`, as config models have today, and a new boolean `listed` (`null` without a held list).
- `?view=live` adds `live_list` to key rows too: the service's list as it arrived.
- `GET /v8/management/provimux/live-lists` stays as it is and shows no keys.
- The contract goes into `.github/provimux/design/pui13/openapi.yaml` with one example file, `examples/model-lists-keys.json`; the worker adds both when the endpoint is built, so the existing test that checks examples against the OpenAPI covers them.

**Log.** One line when a key's list or count changes, a warning per failure, in the form logins have:

```
model list for openrouter key c41d9a07…: live, 342 listed, 341 added to 2 configured (1 configured not listed: acme/retired-model)
model list for openrouter key c41d9a07…: fetch failed (http_status 404); keeping the configured models
```

### 7.2 The console (`provimux-ui`)

| Where | Shows |
|---|---|
| A key's settings sheet, under Models | a line "Refreshes itself: 341 models from OpenRouter, 2 hours ago", or the failure, or why it is not asked; a switch **Fixed list** (writes `fixed-models`); rows the service does not list marked "not in the service's list". "Get the list from …" stays: it is how a model gets a row, and with it an alias or settings |
| The same sheet, for `openai-compatibility` | the excluded-models field the other key types have (`keySettings.ts:364` leaves it out for this kind today) |
| Model lists page | a group "Keys" from `keys`: per key the source, the age, the learned count, the failure |
| An older Provimux | no `keys` in the answer: the group and the status line are not shown; the switch is hidden when the server's version is older, since an older server drops the unknown field on save |

## 8. Where the code sits

Everything is in layer commits (trailer `Provimux-Layer: branding`). No protected path is touched (`guard.sh`: `internal/runtime/executor`, `internal/auth`, `internal/misc`, `internal/translator`, `sdk/cliproxy/session`): requests go through each executor's exported `HttpRequest`, which sets the key, the entry's headers and the proxy (`openai_compat_executor.go:70-101`, and the same in the Claude, Gemini, Codex, xAI and Meta executors) [read].

**New files.**

| File | Holds |
|---|---|
| `sdk/cliproxy/provimux_key_lists.go` | which keys are asked and why not; the request per key type; the OpenAI-shape parser; the merge of 3.1 and 3.2; the hook |
| `sdk/cliproxy/provimux_key_list_status.go` | the `keys` rows |
| `sdk/cliproxy/provimux_key_lists_test.go`, `provimux_key_list_status_test.go`, `testdata/provimux-keys/` | section 10 |

**The frame.** Names are fixed here; bodies are the worker's.

```go
// keyModelListKind is the list kind of a config API key, or "" with the reason it is not asked
// ("fixed", "live_lists_off", "no_list_endpoint"). Kinds: "openai-compatibility", "claude-key",
// "codex-key", "xai-key", "meta-key", "gemini-key". A Gemini key on Google's host keeps the
// login kind "gemini". Kinds never equal a login type: only login types feed learned models.
func keyModelListKind(cfg *config.Config, auth *coreauth.Auth) (kind, notAsked string)

// keyModelListURL is the list URL of section 4 for the key, "" when its type has none.
func keyModelListURL(kind string, auth *coreauth.Auth) string

// fetchKeyModelList asks the key's service and returns its list (ids, display names).
func fetchKeyModelList(kind string, auth *coreauth.Auth, get liveModelListGet) (liveModelList, error)

// parseOpenAIModelList reads an OpenAI-shaped /models answer as the console's button does.
func parseOpenAIModelList(body []byte, now time.Time) (liveModelList, error)

// keyLearnedModels returns the listed ids no row names (3.1, 3.2), in the list's order.
func keyLearnedModels[T modelEntry](rows []T, list liveModelList) []keyLearnedModel

type keyLearnedModel struct{ ID, DisplayName string }

// applyKeyModelList returns models plus the key's learned models, built by its type's builder.
// It adds nothing for a login, a key that is not asked, or a key with no held list (and asks for
// a fetch). For an openai-compatibility key it then applies the entry's excluded-models, whether
// or not anything was added (6.1); the built-in key types get theirs from the line that follows
// the hook. It never changes cfg or the slice it is given.
func (s *Service) applyKeyModelList(cfg *config.Config, auth *coreauth.Auth, models []*ModelInfo) []*ModelInfo

// In internal/api/handlers/management: the source of the keys rows, beside ProvimuxModelListSource.
type ProvimuxKeyModelListSource interface {
	KeyModelList(auth *coreauth.Auth, liveListsOn, withModels, withLive bool) (ProvimuxModelListLogin, ProvimuxModelListKey, bool)
}
type ProvimuxModelListKey struct {
	Type       string
	Fixed      bool
	NotAsked   string // "", fixed, live_lists_off, no_list_endpoint
	Configured int
	Unlisted   int // -1 without a held list
}
```

**Edits to upstream's lines: additions only, no existing line changed.**

| File | Edit | Why there |
|---|---|---|
| `sdk/cliproxy/service_models.go` | one line before `:345` (`models = applyExcludedModels(…)`): `models = s.applyKeyModelList(cfg, a, models)` | every built-in key type passes this line with its rows built; `excluded-models`, plugin models and prefixes follow it |
| the same | one line after `:270` and one after `:296`: `ms = s.applyKeyModelList(cfg, a, ms)` | the two places an `openai-compatibility` key's models are built (the entry, and the registration cache). For these keys the hook also applies the entry's `excluded-models`, always, asked or not (6.1): this path never reaches `:345`. The cached slice is shared between keys and must be copied, not appended to or filtered in place |
| `internal/config/config_types.go` | `FixedModels bool` on `ClaudeKey`, `CodexKey` (also xAI and Meta, which alias it), `GeminiKey`, `OpenAICompatibility`; `ExcludedModels []string` on `OpenAICompatibility` | five added lines |
| `internal/config/config_normalization.go` | one line in `SanitizeOpenAICompatibility` | 6.1 |
| `internal/watcher/synthesizer/config.go` | one line at each of the two places an entry's credentials are made | 6.1 |
| `internal/api/handlers/management/config_lists.go` | two lines in `PatchOpenAICompat` | 6.1 |
| `internal/watcher/diff/openai_compat.go` | two detail lines in `describeOpenAICompatibilityUpdate` | 6.1 |

**Edits to layer files** (`sdk/cliproxy/live_model_lists.go`, the fetcher).

| Lines | Edit |
|---|---|
| `:596-647` `refreshLiveModelLists` | the loop takes logins as today and keys by `keyModelListKind` |
| `:698-702`, `:750-800` `fetchLiveModelListWithin` | the executor is looked up by the key's own provider (an `openai-compatibility` key's provider is its entry's name); key kinds go to `fetchKeyModelList` |
| `:649-679` `fetchAndRegisterLiveModelList`, `:634-645` | key kinds never call `provimuxUpdateLearnedModels`; the log lines of 7.1 |
| `applyLiveModelList` `:391-410` | unchanged: it filters, and stays for logins only |

`internal/api/handlers/management/provimux_model_lists.go` gains the `keys` array and the second source. `liveModelListIdentity` (`:121-146`) already gives a key a stable identity: provider, base URL and a hash of the key.

**Cost at an upstream sync.** About fifteen added lines in six upstream files: three in `service_models.go`, five struct fields, and the seven of 6.1. None changes an existing line. A rebase stops only if upstream rewrites the lines beside them; `service_models.go` changed 46 lines between 8.1.1 and 8.1.2 [ran: `git diff --stat 003d1f273 e6655b15e`]. If upstream adds its own `excluded-models` to `openai-compatibility`, theirs replaces ours.

## 9. Fit with the parked branches

| Branch | Same code? | Verdict |
|---|---|---|
| `combined-names` (2 commits) | No. It touches `internal/client/codex/models/`, one test file in `management/` and the README | independent; any order |
| `providers-dev-lists` (4 commits, 2,622 lines, 24 files) | Yes, six files: `live_model_lists.go`, `service_models.go`, `provimux_model_lists.go` (handler), `provimux_model_list_status.go`, `pui13/openapi.yaml`, `config.example.yaml` [ran: `git diff --stat 003d1f273 providers-dev-lists`] | different hunks, different meaning; land it first |

**What `providers-dev-lists` changes in those files** [read: its diff]: the first line of `applyLiveModelList`, `openLiveModelListCache`, two lines of `filterByLiveModelList`, `fillLiveModel`, the refresh hook in `runLiveModelListRefresh`, one line at `service_models.go:91`, the `published` block of the handler, and the per-field sources in the status code.

**What this design changes there:** the fetch loop, the fetch switch, the register step, three other lines of `service_models.go`, a new array in the handler. No function is edited by both in the same lines, except the handler's response struct and the OpenAPI file, where both add fields.

**The two do not overlap in meaning.** providers.dev fills fields of models learned from a **login's** list, found through a map from login type to listing. A key's learned models are not in that map and get nothing from providers.dev in this version.

**Order: `providers-dev-lists` first, then this, built on top of it.**

1. It is finished and tested; this is not built. The built branch should not be rebased over new hunks in the same functions.
2. Its composition step for a learned model (`provimuxComposeLearned`) is where providers.dev's values for a key's learned models would later go: a map from a key's host to a listing (`openrouter.ai` to `openrouter`, `api.x.ai` to `xai`). Not part of this design.
3. If this had to ship without it: possible, with about six small conflicts for whoever lands second.

Both parked branches sit on `003d1f273`; `main` is the same stack on 8.1.2, so each goes over with `git rebase --onto main 003d1f273 <branch>`. The worker's branch starts from `main` once `providers-dev-lists` is on it. This design's own branch, `key-model-lists`, is on `e6655b15e` and holds only this file.

## 10. The test plan

Stand-in servers only (`httptest`), fixtures in `sdk/cliproxy/testdata/provimux-keys/` in each service's documented shape. No test contacts a real service or port 8317. Clocks are set, never slept (the repository's rule).

**The merge (pure functions, table tests).**

| Case | Must hold |
|---|---|
| Rows and a list | 3.4's sentence: the builder's result for rows plus one bare row per learned id |
| A row names a listed id; with another case; with a thinking suffix | not learned a second time |
| A listed id equals a row's alias | not learned; the alias still resolves to the row's model |
| A row the list lacks | still offered |
| `excluded-models`, exact and wildcard, on a row and on a learned id | both hidden; on `openai-compatibility` too |
| `models` empty: `openai-compatibility`; a Claude key | the whole list; the catalog, unchanged |
| An id that reads as a thinking suffix; an empty id; a duplicate id | skipped; skipped; once |
| An OpenRouter entry | a learned model has OpenRouter's window and efforts, as a bare row has |
| The registration cache's shared slice | two keys of one entry with different lists do not see each other's models |

**`excluded-models` on `openai-compatibility` entries (6.1).**

| Case | Must hold |
|---|---|
| An exact id, a wildcard (`x-ai/*`, `*-preview`), another case, spaces around a pattern | the same models hidden that the same list hides on a Claude key with the same rows |
| A pattern matching a row's alias; a row's `name` when it has no alias; a learned id | hidden in each case |
| A model taken out of `models` while the service still lists it | offered again after the refresh; hidden once it is in `excluded-models` |
| `fixed-models: true`; no list held; `live-lists: false` | the list still hides rows |
| Both registration paths: the entry (`:270`) and the registration cache (`:296`) | the same result; a second key of the entry is not affected by the first one's filtering |
| A list that excludes everything | the key offers nothing (or its plugin models) and is not left registered with stale models |
| An entry with a `prefix` | the pattern is matched without the prefix |
| Load, `PUT` and `PATCH` with `[" X-AI/* ", "x-ai/*"]` | stored as `["x-ai/*"]`; `GET` returns it; a `PATCH` that does not name the field keeps it |
| The list changed in a running instance | applies at the reload: the credential's `excluded_models_hash` changed and the key was registered again |
| An entry without the field, before and after this change | byte-identical registration: the rows, as today |

**The fetch, per key type, against a stand-in.**

| Case | Must hold |
|---|---|
| The URL and headers for each row of section 4's table, with and without a trailing `/v1` or `/models` | the stand-in sees exactly that path and credential; the entry's headers are sent |
| `openai-compatibility`: `401` then open | one retry without key and headers; `500` or `404` gets no retry |
| Shapes: `data`, `models`, a bare array, strings, objects with `name` | the same ids the console's `normalizeModelList` gives |
| `404`, `500`, invalid JSON, an empty list, 9 MB, no answer within the deadline | a failed fetch with the right category; the held list stays; with none held, the rows |
| Claude `has_more`; Gemini three pages, and a page that never ends | incomplete is a failure |
| A key's proxy | the request goes through it |

**The lifecycle.**

| Case | Must hold |
|---|---|
| Refresh, then the service drops a learned model | gone after the next accepted answer; a row with that model stays |
| Restart with the cache and no network | the last run's models are offered before any request |
| 7 days without a refresh | the rows alone, one warning |
| `fixed-models: true`; `live-lists: false`; home mode | the stand-in receives no request; the key offers the config |
| `fixed-models` switched in a running instance through a config reload | applies without a restart, both ways |
| A config save through the management API after a refresh | the written YAML has no learned row (compare the file before and after) |
| A request for a learned model | reaches the stand-in with the service's id; a request for a row's alias with the row's name |
| A Claude key's list on a gateway names an unknown id | `LookupStaticModelInfoByChannel("…", "claude")` does not find it: keys do not feed learned models |
| A slow stand-in during registration and during a request | neither waits |

**Status.**

| Case | Must hold |
|---|---|
| A key with a held list; with a failure; fixed; unreachable type | `keys` row as 7.1, every key always present |
| A Claude key behind a gateway | absent from `logins`, with and without `?view=live` |
| `?auth_index=` of a key | learned and row models with `source`, `learned`, `listed` |
| The example file | validates against the OpenAPI |

**Not regressed.** Every existing `TestProvimux*` test, and the sync check's verdict (`check.sh`): no test fails that passes on the tag. Upstream's tests build configs without `live-lists`, which is off in a test binary, so no upstream test sees a fetch.

## 11. The build plan

On `main` after `providers-dev-lists` has landed (section 9).

| Step | Does | Files | Size |
|---|---|---|---|
| K1 | The two config fields; `excluded-models` on `openai-compatibility` entries through every place of 6.1; which keys are asked; the request and parser; the merge; the three hooks; the fetcher's four edits; cache and expiry through what exists; the log lines | `config_types.go`, `config_normalization.go`, `synthesizer/config.go`, `management/config_lists.go`, `diff/openai_compat.go`, `provimux_key_lists.go` (new), `service_models.go`, `live_model_lists.go`, tests and fixtures | about 480 lines of code, 950 of tests; the merge, `excluded-models` and fetch tables of section 10, and the lifecycle table |
| K2 | The `keys` array, the OpenAPI and its example, `config.example.yaml`, the README's live-lists section | `provimux_key_list_status.go` (new), `management/provimux_model_lists.go`, `pui13/openapi.yaml`, `pui13/examples/model-lists-keys.json`, `.github/README.md` | about 250 lines of code, 300 of tests; the status table |
| K3 | The console (7.2), in `provimux-ui`, with the excluded-models field for `openai-compatibility` entries: read, edited and saved as for the other key types | the key settings sheet (`keySettings.ts`, `KeySettingsFields.tsx`), the entry's serializer and reader (`src/services/api/providers.ts`, `transformers.ts`), the Model lists page, the API types, seven locale files | about 450 lines; its own tests against the example file, and a save that round-trips the field |
| K4 | A critical review of K1 and K2 against sections 3, 5 and 8, then of K3 | | findings to the chief |
| K5 | Notes in this file of what was built and what did not hold, as `model-lists.md` section 13 does; the brain page `live-model-lists.md` | | |

`excluded-models` for `openai-compatibility` is built inside K1, not after it: a release in which these lists refresh without it would leave the owner no way to keep one model out. K1 and K2 are one worker in that order; K3 can start from the example file as soon as K2's contract is merged. Before release the owner's own Provimux runs the branch once: section 13 lists what only that shows.

## 12. Decisions that are the owner's

| # | Decision | Recommendation | The other way costs |
|---|---|---|---|
| 1 | **On the day of the upgrade every existing key with rows starts offering its service's whole list.** Where a learned id is one a subscription also serves (an OpenAI key listing `gpt-5.5` beside a Codex login; a Claude key beside a Claude login), requests for it are shared between the two at equal `priority`, and the key's share is paid per token. Today the owner's rows decide what a key serves | **Keep on by default, as decided, and say it in the release notes** with the three controls: `priority`, `excluded-models`, `fixed-models: true`. The Model routes page already shows every account serving a model | Off by default for keys that already have rows would need a marker written to the config at upgrade, which a refresh must not do. A rule "never learn a model another account serves" depends on the order accounts register and would make a model appear and vanish as logins come and go |
| 2 | A row the service no longer lists | **Keep it, and mark it in the console** (3.3) | Dropping it means writing the config, and removes deliberate rows |
| 3 | `excluded-models` on `openai-compatibility` entries | **Decided by the chief (2026-11-24): added**, with the built-in keys' meaning, applied after the refresh (6.1) | |
| 4 | An `openai-compatibility` entry with no rows offers its service's whole list | **Yes**: it follows from the rule, and makes "add the key, done" work | Requiring one row first is a special case with no reason behind it |
| 5 | A built-in key with no rows keeps the catalog | **Leave it**: outside the owner's question, and a live list there would filter the catalog and could drop models | Asking those keys too is one line once K1 exists, and needs its own decision on filtering |
| 6 | No limit on how many models a list may add | **None in this version**; the log line and the status row show the count | A cap would need a number nobody has evidence for; `excluded-models` and `fixed-models` are the controls |

## 13. What I ran, and what I did not verify

| Check | Result |
|---|---|
| The registration path for every key type (`service_models.go:34-365`) and the conductor's name resolution (`conductor_models.go`) | sections 2.1 and 1: an unnamed model passes through |
| The console's discovery code in `provimux-ui` at `fd1346d7` | section 2.2 |
| `git diff --stat` of both parked branches against `003d1f273`, and the diff of `providers-dev-lists` in the six shared files | section 9 |
| `git ls-remote provimux main` before the commit | `e6655b15e`, unchanged |

**Not verified.**

| Point | Who settles it |
|---|---|
| Any service's real `/models` answer: the fixtures are documented shapes | the owner's Provimux running the branch once, before release; the status rows show each key's result |
| That OpenRouter's `/models` answer stays under the 8 MB limit (upstream's own OpenRouter fetch allows 32 MB) | the same run; if not, the limit for key kinds is raised |
| That a config reload registers keys again when only `fixed-models` changed (`excluded-models` changes the credential's hash, 6.1) | K1's test; if not, `fixed-models` is written to the credential's attributes as well |
| That nothing else reads a credential's `excluded_models` attribute in a way that misbehaves for an `openai-compatibility` credential (read: `service_models.go:80-83`, `service_executors.go:740`, `model_registry.go:124`, which skips API keys) | K1; the test "an entry without the field registers as today" and upstream's own tests |
| That each built-in executor's `HttpRequest` sends an API key's credential on a list path as it does on a model path (read for `openai-compatibility` and Claude; the others by their `PrepareRequest`) | K1's fetch tests |
| How many models the owner's own keys would add, and which of them a subscription also serves (decision 1) | the same run, read from the `keys` rows and the Model routes page |
| Whether groundtruth's discovery tolerates the new top-level `keys` | it reads `logins` only [infer]; checked when K2 is reviewed |

## 14. As built (K1, K2)

Built on branch `key-model-lists-build`, on `main` with `providers-dev-lists` below it. What differs from sections 1 to 13, and what a review found.

**Upstream lines, additions only.** 29 lines in 9 files, against the 15 in 6 of section 8:

| File | Lines | Why more than planned |
|---|---|---|
| `sdk/cliproxy/service_models.go` | 3 | as planned |
| `internal/config/config_types.go` | 9 | 5 fields; 4 blank lines keep `gofmt` from realigning existing fields |
| `internal/config/vertex_compat.go` | 2 | `fixed-models` on Vertex keys: below |
| `internal/config/config_normalization.go` | 1 | as planned |
| `internal/config/config_v8.go` | 1 | `fixed-models` is a shared group field of the `api-keys:` layout: below |
| `internal/watcher/synthesizer/config.go` | 5 | the credential carries `fixed_models` too: below |
| `internal/api/handlers/management/config_lists.go` | 4 | `gofmt` writes the `if` on three lines |
| `internal/api/handlers/management/config_auth_index.go` | 3 | `GET /openai-compatibility` names its fields: below |
| `internal/watcher/diff/openai_compat.go` | 1 | one call to a new file |

**What did not hold.**

- *A reload sees `fixed-models` by itself* (section 5). It does not: the watcher dispatches a config key only when its synthesized credential changes. The credential therefore carries `fixed_models: "true"` (the fallback section 13 named). Status readers without the config read the same attribute.
- *Both fields pass `PUT` with no handler edit* (section 6). True for `PUT`, but `GET /openai-compatibility` builds its answer field by field, so a console that saved what it read would have erased both fields. It now returns them.
- *The `api-keys:` layout.* A group of built-in keys accepts only the shared fields and fails the whole load on any other, so `fixed-models` on a group stopped a start. It is a shared field now: a group sets it for its keys, a key may override it, a save writes it on the group.
- *Vertex keys have no `fixed-models`* (section 6). A config with it on a Vertex key loaded (the field ignored) but then every write through the v8 config endpoint was refused, because the whole file is validated strictly. Vertex keys accept the field; it has no effect, since they are never asked (`key.fixed` reports it, `key.not_asked` stays `no_list_endpoint`).
- *Legacy `PATCH` and `fixed-models`.* `PATCH /openai-compatibility`, `/claude-api-key` and the other per-entry `PATCH` handlers name their fields one by one and do not take `fixed-models`: a `PATCH` naming it answers 200 and leaves the field as it was. Making them apply it needs about four upstream lines per handler (six handlers); left as it is. `PUT` of the list and the v8 endpoints (`PUT /config/api-keys/<family>`, the console's own save path) write it. `PATCH /openai-compatibility` takes `excluded-models` (section 6.1).
- *A fixed Gemini key on Google's host without rows* kept `live.supported: true` in `logins` although it is never asked. Its row now says `supported: false` and the catalog, as the older live-lists endpoint does.

**Beyond the design.**

- The `keys` row's `key` object also has `name`, `learned` (the ids the key offers that no row names, after `excluded-models`) and `unlisted` (the rows the held list does not name), so a console can show what a refresh added without one request per key.
- The request without key and headers (the 401/403 retry of section 4) is a fourth argument of `fetchKeyModelList`; `KeyModelList` takes the config the handler holds.

**Which entry is an `openai-compatibility` key's (v8.1.7-pmx.2).** The key's settings (`excluded-models`, `fixed-models`, its rows, whether it is asked, its status row) come from the entry upstream's registration finds for it, through one function, `keyCompatEntry`: the credential's config index only while the entry there still has its provider name and base URL (`openAICompatMatchesAuth`), else the first enabled entry with that name and base URL, or, for an older credential without a base URL, that name. Up to v8.1.7-pmx.1 it trusted the index alone. A credential keeps its old index until the watcher hands out the refreshed one, and the watcher's service callback registers every key before that, so after another endpoint was inserted ahead of a key's own, the key took that endpoint's `excluded-models` and `fixed-models` until a restart: it offered a model its own entry excluded, dropped one it kept, and a fixed key was asked for its list. The built-in key types already go through upstream's resolvers, which check key and base URL the same way. Tests: `provimux_key_endpoint_test.go`.

**Redirects (accepted as is).** The list request goes through the key type's executor, which follows redirects as it does for model requests. When a key's own host redirects `/models` to another host, Go's client drops `Authorization` there, but forwards a Gemini key's `x-goog-api-key` and an entry's custom headers. Only the host the owner configured for the key can cause this, and a model request to the same host already behaves the same way.

**Tests.** The tables of section 10, against stand-ins (`sdk/cliproxy/provimux_key_lists_test.go`, `provimux_key_list_status_test.go`, `internal/api/handlers/management/provimux_key_*_test.go`), including the console's save path (`GET /config`, `PUT /config/api-keys/<family>` for every key family) and a request through the conductor for a learned model and for a row's alias.
