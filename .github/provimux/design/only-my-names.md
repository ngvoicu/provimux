# Only my names: the gateway serves the names the owner set, and nothing else

Design, 2026-11-25. This is the design only: no code exists yet. The status endpoint's contract is added to `pui13/openapi.yaml` with it (path `/provimux/names`).

**The owner's words (2026-11-25).** "In the Models page can we have a setting where only 'names' I set are available through Provimux? The rest are like excluded?"

**Read from.**

| Project | Commit |
|---|---|
| Provimux | GitHub `main` at `3a819b531` (CLIProxyAPI `v8.1.3` + the layer; last layer tag `v8.1.2-pmx.2`) |
| provimux-ui | GitHub `main` at `b26c647c` |

Every `file:line` of Provimux below is in that commit; console lines are in `b26c647c`.

**Tags.** **[read]** in code. **[ran]** executed by me. **[infer]** reasoned, not verified; each names the test that settles it. I sent no request to any service and never contacted port 8317 or 8787.

## 1. The design in one page

**The rule.** With `models.only-my-names.enabled: true`, an account's model is registered only under a name the owner set; every other name is treated on that account exactly as an excluded model is today: it is not listed to clients and a request for it is refused as an unknown model, with a message that names the setting.

**A name the owner set** is one of four things, compared trimmed and without case (section 2):

1. the `alias` of a `models` row of an API key or `openai-compatibility` entry, when it differs from the row's `name`;
2. the `alias` of an `oauth-model-alias` row;
3. an alias written in a login's own file, on that login;
4. a name on the setting's own `keep` list: a model kept under the vendor's name.

**The field.**

```yaml
models:
  only-my-names:
    enabled: true
    keep:
      - claude-opus-5-5
      - gpt-5.5
```

Absent means off. Nothing else in the file is read differently or rewritten; `enabled: false` (or removing the key) brings every name back at the next reload.

| Piece | Where | What it does |
|---|---|---|
| The rule | one new function, called where every account's list is handed to the registry (3 inserted lines in upstream files) | drops the names that are not his, after aliases, namespaces and prefixes are applied, and remembers what it dropped |
| The refusal | 3 inserted lines where an unknown model is refused today | same status and code as today, a message that says why, and a count per name |
| Status | new `GET /v8/management/provimux/names` | his names, every other name with the accounts behind it, and what was asked for and refused |
| Console | the Models page | the switch, a preview before it goes on, one-click **Keep**, a "Not served" section, a "refused" strip |

Four findings shaped it.

- **Upstream has no allow-list of any kind** [ran: a search of the tree and of `config.example.yaml` for allow, whitelist and their spellings]. Not per client key, not per route, not global. There is nothing to build on except the exclusion seam.
- **"Add it to my names as it is" cannot be an alias row.** Upstream deletes a row whose alias equals its name when the config loads (`internal/config/config_normalization.go:160-162`) and skips it again at registration (`sdk/cliproxy/service_models.go:1381-1383`) [read]. The console calls such a row "ignored" (`same-as-name`). So a model kept under the vendor's name needs its own place: the `keep` list, inside the one setting.
- **The list cannot be a plain list field of `models:`.** Upstream's own test compares the `models:` struct with `!=` (`internal/config/model_catalogs_test.go:31`, `:48`, `:59`); a list inside it stops that test from compiling [read]. One pointer to a small struct compiles and stays `nil` in upstream's tests.
- **Filtering where accounts register gets every surface for free.** Every model list and every request path reads the registry (section 3). A filter in front of the registry needs three inserted lines; a filter at each list and each request path needs an edit per surface and leaks at the next surface upstream adds.

## 2. What "a name I set" is

### 2.1 The console today

The Models page puts a name under **Your names** when an alias row gives it: `own: aliasKeys.has(key)` (`src/provimux/pages/models/catalog.ts:539`), where `aliasKeys` holds the trimmed, lower-cased `alias` of every row that has one (`:508-510`), and a row has one when:

| Store | A row has an alias when | Console line |
|---|---|---|
| API key `models` rows (Gemini, Interactions, Claude, Codex, xAI, Meta, Vertex, `openai-compatibility`) | `alias` is not empty and differs from the row's `name` | `src/provimux/pages/aliases/rows.ts:156` |
| `oauth-model-alias` rows | `alias` is not empty (the proxy has already dropped rows whose alias equals the name) | `rows.ts:176` |

A name with an edit not saved also sits there while the edit lasts (`catalog.ts:669-670`); that is the draft, not the config.

### 2.2 The gateway's rule

| # | A name he set | Read from | Scope |
|---|---|---|---|
| 1 | `alias` of an API key's `models` row, not empty, different from the row's `name` | every key family and every `openai-compatibility` entry of the loaded config, disabled ones included | the whole gateway |
| 2 | `alias` of an `oauth-model-alias` row | the loaded config (already cleaned by `SanitizeOAuthModelAlias`) | the whole gateway |
| 3 | an alias in a login's own file | the model itself: upstream marks every login alias `OAuthAlias` when it applies it (`service_models.go:1445-1446`) | that login only |
| 4 | a name on `models.only-my-names.keep` | the loaded config | the whole gateway |

Rows 1 and 2 are the console's rule of 2.1, stated on the same config. Rows 3 and 4 are additions, and the console takes both from the gateway so the two cannot disagree: `keep` is in the same `GET /config` the page already reads, and the status endpoint lists every name with who set it (`set_by`, section 6), which is the only way the page learns of row 3 (it never reads login files).

**Not a name he set:** a key's row without an alias (rows are also what "Get the list from …" once wrote in bulk); a `display-name`; a model a login's or a key's list offers; a learned model; a plugin's model; anything `excluded-models` says.

**The rule is by name, not by route.** A name is his or it is not. When it is, every account that answers that name serves it, exactly as now: the alias row's account, and any other account that offers a model under the same name. That is what the name's card on the Models page already shows (all routes under the name, the ones without a row marked "built in"), so the page and the gateway show one thing. Decision 2 in section 11 is the alternative.

### 2.3 Keeping a model under the vendor's name

One click on the Models page (**Keep**) adds the name, as it is served, to `models.only-my-names.keep`. Nothing else is written. The name then stands under Your names, marked "kept under its own name", and **Stop keeping** removes it from the list.

- `keep` holds exact names, as a client sends them: `claude-opus-5-5`, `devin/claude-opus-4.6`, `team-a/gpt-5.5`. Case does not matter.
- No wildcards. An entry containing `*` is ignored with one warning in the log and listed by the status endpoint (`keep_ignored`). A pattern would let a model the service adds tomorrow in without his word, which is what the setting exists to stop (decision 4).
- `keep` may be filled while the switch is off; that is how the preview works (section 5). It has no effect until the switch is on.
- A kept name that no account offers now is harmless and is served from the moment an account offers it.

## 3. Where it is enforced

### 3.1 One place: in front of the registry

Every account's models reach the registry through three writers, and the rule is applied in each, after exclusions, aliases, the `devin/` and `copilot/` namespaces and the account's prefix have been applied [read]:

| Writer | Who goes through it | Line |
|---|---|---|
| `registerResolvedModelsForAuthFenced` | every login and key (six call sites in `service_models.go:278-357`), and a login whose models a plugin supplies (`service_executors.go:766`) | `sdk/cliproxy/service_executors.go:551-580` |
| the Antigravity catalog replacement | Antigravity logins | `sdk/cliproxy/antigravity_models.go:888` |
| a plugin executor's own models | plugins that bring a provider | `internal/pluginhost/adapters_executors.go:276` |

A model `m` of an account stays when one of these holds, and is held back otherwise:

1. `m` is a login alias (`OAuthAlias` is set): rules 2 and 3 of 2.2, and their prefixed copies;
2. `m`'s name is in the names of rules 1, 2 and 4;
3. the account has a prefix `p`, `m` is `p/r`, and `r` is in those names.

With the switch off the same function runs and holds nothing back, but still records what it would: that record is the preview (section 5).

**The record.** Per account, the names the last computation held back (or would have). It is written when the list is computed, so an account whose every name is held back, and which therefore registers nothing, is still recorded. A computation the fence then rejects leaves the record one step behind; upstream follows every rejected publication with a completion pass for the same account (`service_models.go:24-29`), which writes it again [read; test C4]. Nothing hooks unregistration: the status endpoint reads the records of the accounts that exist and are enabled, and drops the rest.

**Why this is safe under reload.** The function runs inside the fenced computation: a result computed under an older config is rejected at publication when a newer registration of the same account got there first (`sdk/cliproxy/model_registry.go:110-114`, the registry epoch) [read].

### 3.2 What follows without further code

| Surface | Why it follows | Evidence |
|---|---|---|
| `GET /v1/models` (OpenAI shape), Claude shape, `GET /v1/models/<id>` | built from the registry's available models | `internal/api/server_routes.go:72-76`, `:654-684` [read] |
| `GET /v1/models?include=accounts` | per account, `GetModelsForClient` | `sdk/api/handlers/openai/openai_models_accounts.go:45-64` [read] |
| `GET /v1beta/models`, `GET /v1beta/models/<id>` | the Gemini handler reads the registry | `server_routes.go:152-155` [read in part: the routes, not the handler line by line]; test D1 |
| Codex's client catalog (`/v1/models?client_version=…`), Grok's, Muse Code's (`/muse-code/models`) | each formats registry models | `server_routes.go:654-728`, `internal/client/musecode/models.go:236` [read in part]; test D1 |
| Chat completions, completions, Responses (HTTP and websocket), Messages, count_tokens, Gemini generate and stream, Interactions, images | all resolve the name in `getRequestDetailsWithOptions`; no provider registers the name, so the request is refused before any account is chosen | `sdk/api/handlers/handlers_routing.go:166-226`; its callers `handlers_execution.go:70`, `:159`, `handlers_stream.go:45`, `:375`; the websocket executes through `ExecuteStreamWithAuthManager` (`openai/openai_responses_websocket.go:1101-1103`) [read] |
| A request a plugin's model router sent to a provider, or one with a forced provider | provider resolution is skipped, but choosing an account still needs the account to have the name registered | `sdk/cliproxy/auth/conductor_selection.go:1476-1493` [read]. The refusal there is upstream's "no auth available", without the setting's message |
| `auto` | resolves to the first available model: one of his names | `handlers_routing.go:168-186` [read] |
| A thinking suffix, `opus(high)` | the suffix is parsed off before the lookup | `handlers_routing.go:188-202` [read] |

**Paths that name no model** (`/v1/alpha/search`, the realtime and `/v1/live` routes, `/muse-code/config|search|browser_open`, video retrieval by id): they use a login directly and serve no name, so the rule does not touch them [infer; not traced line by line]. Test D2 walks every route of `server_routes.go:68-156` and records what each does with a held-back name; a route that names a model and still reaches a stand-in is a defect of the build, not an accepted gap.

### 3.3 Aliases, prefixes, pools, learned models, plugins, Home

| | With the switch on |
|---|---|
| An alias and what it routes to | unchanged. The alias is the registered name; the model behind it is resolved from the config at execution, as today when an alias row has no `fork` (the model's own name is then already unregistered) |
| `fork: true` | the alias stays; the model's own name is held back unless it is itself one of his names |
| The `devin/…` and `copilot/…` namespaces | part of the name. `devin/claude-opus-4.6` is served when an alias renames it or `keep` lists it in full |
| An account's `prefix` | belongs to the account, not to the name: `team-a/opus` is served when `opus` is his. `force-model-prefix` is unchanged |
| A name several accounts serve (a pool) | all of them serve it, in the same order, with the same cooldowns |
| A learned model (a login's live list, a key's self-refreshing list) | held back like any other name he did not set; listed by the status endpoint, so he can keep it |
| A plugin's models | held back unless named. Models a plugin appends to a provider take the account's path; a plugin executor's own models are matched by exact name |
| The catalog, live lists, published lists, the disk cache | unchanged: they decide what an account offers; this rule decides which of it is served |
| Home mode | the rule does not apply: model lists and routing are Home's (`server_routes.go:664-675`, `handlers_routing.go:198-200`) [read]. The status endpoint says `applies: false`, `not_applied: "home"`, and the console disables the switch with that reason |
| The management API and the console | unaffected. They read and write the config and the layer's status endpoints, never a client model name. This is the way back if he locks his tools out |
| `GET /provimux/model-routes` | unchanged code; it reads the registry (`provimux_model_routes.go:120-140`), so it lists served names only. An older console therefore shows less, never something false |
| `GET /provimux/model-lists` | its lists are unchanged. `registered_models` keeps meaning "registered now", so it falls to the served count [infer; test S5 pins which fields move]. `?view=live` is the vendor's list as it arrived and must not move (test S5) |

## 4. The setting

```yaml
models:
  # Serve only the names I set: the aliases on my keys and logins, plus the names under keep (models kept
  # under their vendor's name). Every other name is treated as excluded: not listed, and refused.
  # Off unless enabled is true. Applied on reload. Nothing else in this file changes.
  only-my-names:
    enabled: false
    keep: []
```

| | |
|---|---|
| Where | `registry.CatalogSources` (`internal/registry/catalog_config.go:13-25`), beside `live-lists` and `published-lists`, as `OnlyMyNames *OnlyMyNames` with `yaml:"only-my-names,omitempty"` |
| Default | absent, which is off. `enabled: false` with a filled `keep` is also off |
| Reload | every config commit re-registers every account (`sdk/cliproxy/service_config.go:239-247`, `forceReplaceAuths`) [read]. So the switch, a `keep` entry and an alias row all take effect at the reload that carries them, with no restart; an alias added on one key also opens the same name on another account at that reload. `configChangeForcesAuthRefresh` (`internal/watcher/config_reload.go:164-172`) gains the setting, as it already carries `live-lists` |
| Written by | the config API the console already uses: `PUT /v8/management/config/models/only-my-names` with the whole object and `If-Match` (the path writer and its reload are the ones `live-lists` uses; `provimux_live_lists_config_test.go`). That the writer accepts an object at a new path is [infer]; test C2 |
| Off again | `enabled: false`. The next reload registers every name. `keep` stays in the file for the next time |
| Zero names | the rule applies as written: nothing is served. The log warns at every reload ("only-my-names is on and no name is set in the config: only aliases in a login's own file are served"), the status endpoint shows an empty `names`, and the console does not offer the switch while he has no names (decision 3) |

Validation at load: `keep` entries are trimmed; an empty entry or one with `*` is ignored, never an error, so a typo cannot stop a start.

## 5. Not locking himself out

His own tools ask by name (`claude-opus-5-5`, …). Three things stand between him and a silent break.

**Before: the preview.** The status endpoint (section 6) answers the same whether the switch is on or off: `names` (his) and `others` (every other name an account offers, with the accounts behind it). With the switch off, `others` is exactly what would stop being served. The console joins it with the usage it already reads (`GET /provimux/savings?period=30d&group=model`, the source of the page's "used in the last 30 days") and shows the used ones first, each with **Keep**. Keeping writes `keep` while the switch is still off, so the list shrinks as he goes and the switch goes on last.

**The moment after: a clear error.** Today an unknown model gets HTTP 400 with

```json
{"error":{"message":"unknown provider for model claude-opus-5-5","type":"invalid_request_error","code":"model_not_found","param":"model"}}
```

(`handlers_routing.go:204-221`). A held-back name gets the same status, type, code and param, and this message:

```
model claude-opus-5-5 is not served: this Provimux serves only the names its owner set (models.only-my-names). Keep it on the console's Models page, or ask for a served name (GET /v1/models).
```

A name no account offers at all keeps upstream's message. The client format's own error wrapping (Claude, Gemini, the Responses stream) is upstream's and is not touched.

**After: what was refused.** Each such refusal is counted in memory per name (at most 256 names, since the switch went on or the process started). The status endpoint returns the counts, and the Models page shows them at the top: "Asked for and refused since 10:04: `claude-opus-5-5`, 14 times, last 2 minutes ago", with **Keep**. This catches what the 30-day preview cannot: a tool he had not used in a month, a name with a prefix he forgot.

**And the way back.** The console and the management API never depend on the switch (3.3).

## 6. The status endpoint

`GET /v8/management/provimux/names`, read-only, behind the management key, registered in `internal/api/server_provimux.go` like the other four. Contract: `pui13/openapi.yaml`, path `/provimux/names` (added with this design). It reports recorded state and predicts nothing.

```json
{
  "observed_at": "2026-11-25T10:04:11Z",
  "only_my_names": { "enabled": false, "applies": true, "not_applied": null },
  "names": [
    { "name": "opus", "set_by": ["alias"], "accounts": 2 },
    { "name": "claude-opus-5-5", "set_by": ["keep"], "accounts": 3 },
    { "name": "work-fast", "set_by": ["login-file"], "accounts": 1 }
  ],
  "keep_ignored": [ { "value": "claude-*", "reason": "pattern" } ],
  "others": [
    {
      "name": "gpt-5.5",
      "accounts": [
        { "auth_index": "a8d167b7d82c8c15", "provider": "codex", "kind": "oauth", "priority": 0, "host": null,
          "login": { "name": "codex-office.json", "email": "office@example.com", "label": "office@example.com" } }
      ]
    }
  ],
  "refused": {
    "since": "2026-11-25T10:04:00Z",
    "names": [ { "name": "claude-opus-5-5", "requests": 14, "last_at": "2026-11-25T10:31:52Z" } ],
    "truncated": false
  }
}
```

| Field | Meaning |
|---|---|
| `only_my_names.enabled` | the loaded config's value |
| `only_my_names.applies`, `not_applied` | `false` with `"home"` in Home mode; otherwise `true` and `null`. Clients treat another reason as "does not apply" |
| `names` | every name he set, as the gateway reads the config and the logins: `set_by` holds `alias`, `keep`, `login-file` (one or more); `accounts` is how many enabled accounts register it now, `0` when nothing answers it. Sorted by name |
| `keep_ignored` | `keep` entries the gateway ignores: `pattern` (contains `*`) or `empty` |
| `others` | every other name an enabled account offers, with those accounts, named as `model-routes` names them. Held back when `enabled`, served when not. Sorted by name |
| `refused` | requests refused because the name is held back: per name the count and the last time; `since` is when counting began; `truncated` when more than 256 names were refused. `null` while the switch is off |

Usage is not in the answer: the console already holds it, per name, from the savings endpoint. Rules kept from the other endpoints: `auth_index` identifies an account; names follow `GET /credentials`; the response only grows; no key, token or error text.

An older Provimux answers 404: the console shows no switch. A gateway with the switch set by hand and an older console still works: `model-routes` then lists served names only.

## 7. Fit with what shipped in v8.1.2-pmx.2

**Self-refreshing key lists.** A learned model is never his name unless he names it: it is not an alias (rule 1 needs a row with an alias) and not on `keep`. It appears in `others` with its key as the account, so the Models page can show it under "Not served" and offer **Keep**. Nothing about fetching, the cache, `fixed-models` or `excluded-models` changes; `excluded-models` is applied before this rule, so an excluded model is not even in `others` for that key.

**The "also paid per token" warning** (`paidOverlap`, `catalog.ts:552-562`: a model a key learned that a subscription also serves).

| Name | With the switch on |
|---|---|
| Not served | no warning is needed and none appears: the name has no routes in `model-routes`, which is what the warning is computed from |
| His name, served by a subscription, and a key's learned model has the same name | the warning is still needed, unchanged. The rule is by name, so the key's model is served under his name too and takes its share at equal priority. This is the upgrade-day case of `key-model-lists.md` section 12, narrowed from several hundred names to his own |

So the switch removes the warning from every name he does not use and leaves it exactly where it still matters. One addition follows from it: before **Keep** writes, the page says who will serve the name ("3 accounts: Codex subscription, OpenAI API key (paid per token), OpenRouter (paid per token)"), from the `accounts` of `others`.

**Live lists and the catalog** are untouched; they feed `others`.

## 8. The smallest build

### 8.1 New files (no conflict possible)

| File | Holds |
|---|---|
| `internal/registry/provimux_only_my_names.go` | the rule as the process holds it (enabled, the names with who set each, ignored `keep` entries); the filter; the record of what each account had held back; the refusal message and its counter. In `internal/registry` because all three writers and the request handlers already import it |
| `sdk/cliproxy/provimux_only_my_names.go` | builds the rule from a config (the alias rows of every key family and of `oauth-model-alias`, and `keep`); the two service-side calls |
| `internal/api/handlers/management/provimux_names.go` | the endpoint of section 6 |
| their `_test.go` files, and `test/provimux_only_my_names_test.go` | section 9 |

Signatures the steps build to:

```go
// internal/registry
type OnlyMyNames struct {
	Enabled bool     `yaml:"enabled" json:"enabled"`
	Keep    []string `yaml:"keep,omitempty" json:"keep,omitempty"`
}

type ProvimuxNamesRule struct { /* enabled; names: key -> spelling and set-by; ignored keep entries */ }

func SetProvimuxNamesRule(rule *ProvimuxNamesRule)
// ProvimuxOnlyMyNames returns the models of clientID that are served under the current rule and records
// the rest for the status endpoint. prefix is the account's, "" for a plugin executor's own models.
func ProvimuxOnlyMyNames(clientID, prefix string, models []*ModelInfo) []*ModelInfo
// ProvimuxNotServedMessage is "" unless the rule is on and an account has name held back; it counts the refusal.
func ProvimuxNotServedMessage(name string) string
// ProvimuxNamesState is the rule and the records of the given clients; records of other clients are dropped.
func ProvimuxNamesState(clientIDs []string) ProvimuxNamesSnapshot

// sdk/cliproxy
func provimuxNamesRuleOf(cfg *config.Config) *registry.ProvimuxNamesRule
```

### 8.2 Edits to upstream's files: additions only

| File | Edit | Lines |
|---|---|---|
| `sdk/cliproxy/service_executors.go` | before the empty check at `:572`: `normalizedModels = registry.ProvimuxOnlyMyNames(a.ID, a.Prefix, normalizedModels)` | +1 |
| `sdk/cliproxy/antigravity_models.go` | before `ReplaceClientModels` at `:888`: the same call with `current` | +1 |
| `internal/pluginhost/adapters_executors.go` | before `RegisterClient` at `:276`: the same call with an empty prefix | +1 |
| `sdk/cliproxy/service_config.go` | after `:180`, where the committed config is handed to the catalogs: `registry.SetProvimuxNamesRule(provimuxNamesRuleOf(newCfg))` | +1 |
| `sdk/api/handlers/handlers_routing.go` | in the unknown-model branch, after the body is built (`:213-216`): if `ProvimuxNotServedMessage(baseModel)` is not empty, set it as `error.message` | +3 |
| `internal/registry/catalog_config.go` | the `OnlyMyNames` field (a file the layer already edits) | +4 |
| `internal/watcher/config_reload.go` | one clause on `:171` (a line the layer already edits) | 1 changed |
| `internal/api/server_provimux.go` | the route (a layer file) | +1 |
| `config.example.yaml`, the README's Provimux part | the setting, in the words of section 4 | text |

No protected path is touched (`guard.sh`: `internal/runtime/executor`, `internal/auth`, `internal/misc`, `internal/translator`, `sdk/cliproxy/session`). `internal/registry/model_registry.go` is not edited. Every commit carries `Provimux-Layer: branding`.

**The rule is set at the commit, not at first use**, so the first registration of a start and a plugin executor's models both see it. That the first config of a start passes `commitConfigUpdate` is [infer]; test R9 starts a service with the switch on in its first config and fails if any held-back name is ever registered.

**A new writer cannot slip in unseen.** A layer test lists every call of `RegisterClient`, `RegisterClientIfEpochCurrent` and `ReplaceClientModels` outside tests and the registry itself and fails when the list differs from the three of 3.1, naming this section. An upstream release that adds a fourth way into the registry then fails the sync check instead of serving held-back names.

**Upstream's tests** build their configs without the key, so the rule is off and the filter returns its input: no upstream test changes. The sync check ("no worse than upstream") covers it.

## 9. The test plan

All against stand-ins: `httptest` servers for vendors, temporary config files and auth directories, the in-process registry. No test contacts port 8317, port 8787 or any real service.

**The rule (unit, `internal/registry` and `sdk/cliproxy`).**

| | Case |
|---|---|
| R1 | switch off: every model registered; the record says what would be held back |
| R2 | a key row with an alias: the alias is served, the model's own name is not; a row whose alias equals its name, or without one, gives no name |
| R3 | an `oauth-model-alias` row without `fork`: alias served. With `fork`: alias served, the model's name held back; then `keep` the name: both served |
| R4 | case and spaces decide whether a name is his: `Opus` in the config makes a model registered as `opus` his; ` gpt-5.5 ` on `keep` keeps `GPT-5.5`. Matching a request's name to a registered model stays upstream's and exact (round 2, decision K) |
| R5 | an alias in a login's own file is served on that login and makes no other account's model served |
| R6 | by name: an alias `gpt-5.5` on a Codex login also leaves a key's own `gpt-5.5` served; a pool keeps every member and its order |
| R7 | prefixes: `prefix: team-a` serves `team-a/opus` (and `opus` unless `force-model-prefix`); `devin/x` is held back, served when `keep` has `devin/x`, not when it has `x` |
| R8 | `keep` with `claude-*` and with an empty entry: ignored, reported, nothing served by it |
| R9 | the first registration of a start already applies the rule; a plugin executor's models too |
| R10 | zero names with the switch on: nothing registered, the warning logged once per reload |
| R11 | a learned model of a key and of a login: held back; served once kept; excluded models never appear in the record |
| R12 | the registry-writers test of 8.2 |

**Reload (service level, a temporary config file).**

| | Case |
|---|---|
| C1 | off to on to off through `PUT /v8/management/config/models/only-my-names`: the registry follows each time without a restart, and the last state equals the first |
| C2 | the config API writes the object at a path the file lacked; the rest of the file is byte-identical apart from that key |
| C3 | adding an alias on key A, in one reload, makes account B's same-named model served; removing it holds it back again |
| C4 | a registration computed under the old config and published after the new one is rejected (the fence), and after the completion pass the account's record matches what it has registered |
| C5 | upstream's `internal/config` tests compile and pass with the new field |

**The door (integration, `test/`, a full server over stand-in vendors).**

| | Case |
|---|---|
| D1 | every list lists exactly his names: `/v1/models` in the OpenAI and Claude shapes, `?include=accounts`, `/v1/models/<id>` (404 for a held-back id), `/v1beta/models` and `/v1beta/models/<id>`, the Codex client catalog, Grok's, `/muse-code/models` |
| D2 | every route of `server_routes.go:68-156` that takes a model, asked for a held-back name: refused, and the stand-in vendor received no request. Chat, completions, Responses over HTTP and over the websocket, Messages, count_tokens, Gemini generate, stream and count, Interactions, images, videos, speech. Routes that name no model are listed with what they do |
| D3 | the refusal: HTTP 400, `code: model_not_found`, the message of section 5, in each client format's wrapping; a name no account offers keeps upstream's message |
| D4 | his names work as before: an alias, a pool across two stand-ins, a prefixed name, a name with a thinking suffix, `auto` |
| D5 | a forced provider and a plugin model router cannot reach a held-back name |

**Status (`internal/api/handlers/management`).**

| | Case |
|---|---|
| S1 | the answer validates against `pui13/openapi.yaml`; every required key present with the switch on and off |
| S2 | `names` equals the console's rule on a fixture config shared with provimux-ui's tests (2.1), plus `keep` and login-file names with their `set_by` |
| S3 | `others` plus the served names is every name any enabled account offers; a disabled account contributes nothing |
| S4 | `refused` counts per name, keeps the last time, stops at 256 with `truncated`, and restarts when the switch goes on |
| S5 | `model-lists` with the switch on: `?view=live` is byte-identical to the switch off; the fields that move are listed in the test |
| S6 | Home mode: `applies: false`, `not_applied: "home"` |

**Console (provimux-ui, `bun test`, a stand-in gateway).** Your names equals the status endpoint's `names` on the shared fixture; the preview orders used names first and counts them; **Keep** writes the whole object with `If-Match` and is refused while name edits are unsaved; a 404 on `/provimux/names` hides the switch; zero names disables it.

## 10. The console, in words

The look is the frontend designer's. What the Models page needs:

1. **The switch**, with the other list settings below the names: "Serve only my names", one line under it ("Tools can ask only for the names under Your names. Every other model is refused."), and its state ("On: 14 names served, 547 not served"). Not shown on a gateway without the endpoint; disabled with the reason in Home mode and while he has no names.
2. **The preview**, opened by turning the switch on, before anything is written: "547 names will stop being served. 6 were used in the last 30 days." Those six first, most used first, each with its requests, the accounts that serve it, and **Keep**; then the never-used rest, folded, with a search. A **Keep all 6 used** action. The confirming button says what will happen: "Serve only my names (14)", or, while used names are not kept, "Turn on: 3 used names will be refused". If the usage read failed or is truncated, one line says the order by use is not known.
3. **Keep, in one click**, wherever a not-served name appears: writes `keep` at once (its own request with `If-Match`, the way the page writes an exclusion; refused with a line while name edits are unsaved, because the write changes the tag those edits are saved against). Before writing, one line names who will serve it and which of them is paid per token.
4. **With the switch on**, the list has two parts: Your names (as now; a kept name says "kept under its own name" and has **Stop keeping**) and **Not served** (folded; used ones first; each row **Keep**). Removing a name's last alias says in the save summary that the name will no longer be served.
5. **The refused strip**, on top while `refused.names` is not empty: the names tools asked for and were refused, with count and last time, each with **Keep**, and "since" so he knows how far back it looks.
6. **Turning it off** asks nothing: one click, everything is served again.

## 11. Decisions

| # | Decision | Recommendation | The alternative costs |
|---|---|---|---|
| 1 | **A kept vendor name lives on a `keep` list inside the setting**, not as an alias row. The chief's lean was "no second list" | Take the list. An alias row equal to its name is deleted by upstream at load and skipped at registration; keeping such rows alive means changing upstream's sanitizer and its alias code (not additions), per login type, with nothing to hold a plugin's model. The list is inside the one setting, so there is still one field | Two behaviour changes in upstream files that every sync must carry, and the console's "ignored row" mark reversed |
| 2 | **By name, not by route**: a name he set is served by every account that answers it | By name. It is what the page shows, what "routes behind his names work as now" means, and it needs no account on `keep` | By route (only the rows he wrote serve) would end the paid-key overlap entirely, but `keep` would need an account per entry, a pool would need a row per member forever, and a name's card would show routes that are silently off |
| 3 | **Zero names with the switch on serves nothing** | Apply the rule as written, warn in the log, and have the console refuse to switch on with zero names | Failing open (ignoring the switch when no name is set) makes the setting untrue in the one case where he would most want to know |
| 4 | **`keep` takes exact names only** | Exact. A pattern re-admits tomorrow's models without his word | Patterns are one function away (`excluded-models` has them) and can be added later without changing the field |
| 5 | **Refusals are counted in memory only** | Memory. It is a safety net for the hours after the switch, not a record | Persisting needs a file and a retention rule |
| 6 | **Requests that reach a held-back name through a plugin router or a forced provider** are refused with upstream's "no auth available", not the setting's message | Accept. The owner's tools do not use those paths; the request is still refused | One more inserted block in `sdk/cliproxy/auth`, a file every upstream release edits |

## 12. The build plan

| Step | Repo | Size | Holds | Depends on |
|---|---|---|---|---|
| N1 | Provimux | M | the field; the rule and its record; the three registry calls; the commit call; the reload clause. Tests R1-R12, C1-C5 | |
| N2 | Provimux | M | the refusal message and counter; `GET /provimux/names`; `config.example.yaml` and README. Tests D1-D5, S1-S6 | N1 |
| N3 | provimux-ui | M | section 10, against the contract in `pui13/openapi.yaml` and a stand-in gateway; can start with N1 | the designer's look; the contract |
| Review | both | S | one reviewer for N1 and N2 together (a path that still serves a held-back name; reload; the sync check no worse than upstream), one for N3 | |
| Release | Provimux | | with the next `-pmx` release; the notes say the setting exists, is off, and what the error looks like | review |

## 13. What I ran, and what I did not verify

**Ran.** A search of `3a819b531` for an existing allow-list (none). `git log` for which of the edited files the layer already touches. `go test ./internal/api/handlers/management -run Provimux` after adding `/provimux/names` and its schemas to `pui13/openapi.yaml`: ok (the layer's contract tests read that file). Nothing else: this design adds no code.

**Read, with the lines given above:** the three registry writers and the six call sites of the first; the unknown-model refusal and its four callers; the websocket's use of the same path; the account-selection gate; the alias and prefix code; the config sanitizer; the commit and re-registration on reload; `?include=accounts`; `model-routes`; the console's `own` rule.

**Not verified, each with its test:**

- that every route naming a model passes the refusal of `handlers_routing.go` or the selection gate (videos, speech, the xAI image path, `/v1/live`): D2;
- that the config API writes an object at a new path: C2;
- that the first config of a start passes `commitConfigUpdate`: R9;
- which `model-lists` fields read the registry: S5;
- that the savings endpoint's per-model name is the name the client sent, prefix included (`internal/provimux/savings/record.go:30`): the console step checks it against its fixture, since the Models page already relies on it for "used in the last 30 days";
- that the owner uses no login-file aliases, plugins or Home mode: the design holds either way.

## 14. As built (N1 and N2)

Steps N1 and N2 are built; N3 (the console) is not. The six decisions of section 11 were taken as recommended. Where
the build differs from sections 1 to 9, and why:

- **The first config of a start is never committed** (`commitConfigUpdate` runs on reloads and Home commits only; `Run`
  starts from the config `Build` stored). So the rule is also set in `Build` (`sdk/cliproxy/builder.go`, one more
  inserted line). Test R9 builds a service with the switch on and fails if a held-back name is ever registered.
- **The config's names are read in `internal/config`** (`ProvimuxNamesRule`, `ProvimuxOnlyMyNamesChanged`), because the
  watcher's reload clause needs them and `internal/registry` cannot import the config; `sdk/cliproxy` keeps the two
  service-side calls.
- **The record holds what each account offered**, before the rule, and the rule is applied to it when it is read. The
  preview (`others`) and the refusal message therefore follow a change of `keep` or of an alias at once, whether or
  not the change re-registers the account.
- **The reload clause** forces every account through registration when the switch turns, or, with the switch on,
  when the set of names changes; with the switch off on both sides it never fires, so a reload is what it was. In the
  tests every reload re-registered every account anyway (C1 and C3 pass without the clause, a login in the auth folder
  included); the clause is kept as the design asked.
- **The refusal is one inserted line**, a call to a helper in a new file, not three.
- **A plugin executor's own models** appear in `others` with `auth_index: null`, `kind: null` and the plugin's
  provider; their record is kept until the process ends, even after the plugin is gone.
- **`names[].accounts`** counts the enabled accounts that register the name exactly; a prefixed copy (`team-a/opus`)
  is not counted under `opus`.
- **The message names the base model** (after `auto` and without a thinking suffix), and refusals are counted under
  the name as the client sent it, without the suffix.
- **Routes that map a client's name to a fixed one** apply the rule to the fixed name: `/v1/audio/speech` with
  `tts-1` is refused as `grok-tts`, `/openai/v1/videos` with `sora-2` as `grok-imagine-video`, and video retrieval by
  id (which names no model) is refused while `grok-imagine-video` is held back.
- **The Responses websocket**: upstream answers a name it cannot route (held back or unknown alike) by closing the
  socket without an error event, so the setting's message cannot reach a websocket client; nothing reaches a vendor.
- **The realtime and live routes** (`/v1/realtime*`, `/v1/live`) name a model but choose a Codex login without
  consulting the registry, and no account registers their names (`gpt-realtime`, `gpt-live-1-codex`): those names are
  neither the owner's nor held back, and with a Codex login such a request is served whatever the switch says. Not
  changed: it needs a decision and an insertion in `internal/client/codex/live`.
- **C2**: the config API rewrites a file of the older layout into the v8 layout at its first write, whatever the key;
  the file stays byte-identical apart from the new key only when it is already in the v8 layout (the console's files
  are, after any save). C2 is tested from a v8 file.
- **Test placement**: D1 to D4 and the reload tests run in `sdk/cliproxy` (they drive the real Service, watcher and
  API server, which `test/` cannot reach); D5 runs at the handlers; S5 runs beside the model-lists source.
- **The shared fixture of S2** is `pui13/examples/names-fixture.yaml`; `pui13/examples/names.json` is an example
  answer checked against the contract.

## 15. Round 2 (review and console run, 2026-11-25)

Taken from the review of N1 and N2 and from a run of the gateway with the console.

- **The status is the rule as applied** (finding A). `GET /provimux/names` describes what the registry serves now:
  after a save it keeps describing the rule before it until every account has registered again under the new one.
  A block `applied` (`generation`, `config_etag`: the config API's ETag of the applied rule's config, `applying`) tells
  the console when its write has taken effect; `GET /provimux/model-routes` carries the same block. The registry is
  compared with what the current rule says each account registers; the applied rule moves only when they all match.
  The config writer is upstream's: it still answers before the accounts have registered again (usually tens of
  milliseconds); the console waits on `applied`.
- **The record** (finding B) is one map under one lock, written by the writers and read by the status and the refusal;
  a read never drops a record. A record carries the registry's epoch for its account just before the writer published;
  every registration or removal moves that epoch by one, so an epoch past it plus one means the account was removed
  or emptied by a path that does not pass a writer (removal, disabling, every model excluded, a plugin unloaded): the
  record is stale, ignored at once and dropped by a sweep that runs when the records double (bounded by twice the
  accounts). An account that offers nothing leaves no record.
- **A served name is never "not served"** (finding C): `others` holds only names that no account serves under the rule
  (a login's own alias counts on its login) and, with the rule applying, that no account registers.
- **Prefixed copies** (finding D): each name lists `copies`, the ids it is served under an account's prefix.
- **Usage by requested name** (finding E): Savings keeps, apart from its rows, the requests per name the client sent
  (`Record.Alias`, without a thinking suffix), given with `include=requested_models`; rows and every existing field are
  unchanged; days recorded before have none (`requested_models.from`).
- **A plugin's own models** (finding F): a model router's decision to run a held-back name on a plugin executor is
  dropped (three inserted lines in `internal/pluginhost/model_router.go`), and the request is refused with the
  setting's message. A router that rewrites to a provider or another model, and a plugin that rewrites the final model
  list, are not covered (the latter is upstream's `handlers_interceptors.go`); the README says so.
- **Test D2** compares its table with the routes the server registers (`TestProvimuxOnlyMyNamesDoorRouteInventory`);
  D1 checks the Claude shape by id: each listed id resolves to the model behind one of his names.
- **Decided, not changed**: the Claude shape's generated ids (I), the fixed speech and video names (J; the contract
  now says the refusal counter names the name as the gateway resolved it), upper and lower case (K; R4 corrected).
- **Warnings**: an ignored `keep` entry is logged at every reload; the zero-names warning says that a login's own
  aliases are still served.
