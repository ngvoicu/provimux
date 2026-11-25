# Only my names: the gateway serves the names the owner set, and nothing else

Design, 2026-11-25. Written before the code (built since: sections 14, 15, 16.11 and 17). The status endpoint's contract is added to `pui13/openapi.yaml` with it (path `/provimux/names`).

**Amended the same day by section 16**, which moves the rule from where accounts register to where names are read. Where sections 1 to 15 and section 16 differ, section 16 holds; 16.9 lists the places. **Section 17** (after the third review) narrows a name the owner set to what the config says, compared exactly; **section 18** (after the fourth) makes the request's answer a function of one rule value and the name. Where they differ from the sections before them, the later section holds.

**The owner's words (2026-11-25).** "In the Models page can we have a setting where only 'names' I set are available through Provimux? The rest are like excluded?"

**Read from.**

| Project | Commit |
|---|---|
| Provimux | GitHub `main` at `3a819b531` (CLIProxyAPI `v8.1.3` + the layer; last layer tag `v8.1.2-pmx.2`) |
| provimux-ui | GitHub `main` at `b26c647c` |

Every `file:line` of Provimux below is in that commit; console lines are in `b26c647c`.

**Tags.** **[read]** in code. **[ran]** executed by me. **[infer]** reasoned, not verified; each names the test that settles it. I sent no request to any service and never contacted port 8317 or 8787.

## 1. The design in one page

**The rule.** (As first designed; sections 16 and 17 replace the mechanism: nothing registered changes, names are read through a gate.) With `models.only-my-names.enabled: true`, an account's model is registered only under a name the owner set; every other name is treated on that account exactly as an excluded model is today: it is not listed to clients and a request for it is refused as an unknown model, with a message that names the setting.

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
| R4 | (corrected by section 17) names are compared trimmed and exactly: ` gpt-5.5 ` on `keep` keeps `gpt-5.5`, not `GPT-5.5`; `Opus` in the config does not make `opus` his. The status never shows an account for a spelling the door refuses |
| R5 | (corrected by section 17) an alias in a login's own file is not his unless `keep` lists it; it is under `others` |
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

## 16. Amendment: the rule is applied where names are read, not where accounts register

2026-11-25, after two reviews of the build ended "not ready" on the same part. This section replaces the mechanism of sections 3.1, 8 and 15 and the parts of 3.2, 3.3, 4, 9, 11 and 14 named in 16.9. What the owner sees (sections 1, 2, 5, 6, 10) stays, with one change of meaning (16.3, the alias of a login's own file).

**Read from.** Provimux branch `only-my-names` at `e337b95c9`; every `file:line` below is in that commit. The console at `074f924d` (provimux-ui, branch `only-my-names`). Tags as in the head of this document.

### 16.1 The decision

**The switch no longer changes what is registered.** Accounts register exactly as with the switch off. The rule is one immutable value, built from a config and put in force with one atomic store when that config is committed. It is applied in four places where a name is read: the two loops that build every model list, the one function every handler passes before it executes a request, the function that asks plugin routers, and the alpha-search route. The status endpoint is computed from the same value and the same registry when it is asked.

Why this shape and not the built one: every finding of both reviews came from the rule living in registration. Registration is per account, asynchronous and fenced, so the rule took effect account by account some time after the config; a second structure (the record) had to remember what the registry no longer held; and the status tried to describe that process. With the rule at the reads there is no process: the rule is in force for the next list and the next request, a late or rejected registration cannot matter because registration does not consult the rule, there is nothing to remember (what is not served is what is registered and not his), and nothing to report as "applying".

**The gate is by name, never by account.** The chief's question was whether the gate can be closed if it sits where the registry is read. Per account it cannot; by name it can.

| Found | Evidence |
|---|---|
| The registry's read methods are called from 42 places in 28 of upstream's files (outside the registry's own file, tests and the layer) | [ran] a search for the eleven read methods |
| The scheduler keeps its own copy of each account's model set and rebuilds it only when that account registers again | `sdk/cliproxy/auth/scheduler.go:1097-1105`, `:1205-1225`, `:1329-1338` [read]. A per-account gate inside a registry read is frozen into that copy at the last registration, which brings back the lag; a gate outside it is not consulted on the fast path (`conductor_selection.go:2788`, `:3129`) |
| Every handler execution passes one function before any account is chosen | `providersForExecution`, `sdk/api/handlers/handlers_routing.go:129`, called at `handlers_execution.go:70`, `:159`, `handlers_stream.go:45`, `:375`; the only calls of the manager's `Execute`, `ExecuteCount` and `ExecuteStream` outside `sdk/cliproxy/auth` are behind them (`handlers_execution.go:123`, `:203`, `handlers_stream.go:435`, `:648`) [ran: a search; read] |
| The plugin-executor branch is taken before that function, from one source of decisions | `applyModelRouter`, `handlers_routing.go:369`; its five callers (`handlers_execution.go:62`, `:155`, `handlers_stream.go:40`, `:363`, `handlers_context.go:177`) [read] |
| One client route chooses a login for a named model by itself | alpha search: `internal/api/server_routes.go:403-418` (`SelectAuthWithCredentialPolicy`) [read] |
| Every model list a client receives is built by two loops | `buildAvailableModelsLocked` (`internal/registry/model_registry.go:1734`) behind `GetAvailableModels`: the OpenAI, Responses, Claude and Gemini lists, the Codex client catalog, `/v1/models/<id>`, `/v1beta/models/<id>`, and `auto` (`GetFirstAvailableModel`, `:2222`). `GetAvailableModelInfos` (`:1715`): Grok's list and Muse Code's [read] |

So a by-name gate needs the door (one function), the routers (one function), alpha search (one line of its own) and two loops. Everything else that reads the registry either runs for a request that already passed the door, or serves no client's model name (16.7, test K3).

**The two other shapes, and why not.**

| Shape | Why not |
|---|---|
| A gate inside each registry read, honouring a login's own alias per account | Not closable: the scheduler's copy above. Honouring an alias per account at selection needs the copy to carry the alias flag, which is an edit of upstream's scheduler, not an addition |
| The fallback: keep the rule in registration, drop the record and `applied` | Keeps the window between the config and the registrations, and the gap the second review showed in it (a registration computed under the old rule publishes after the new one: the fence at `sdk/cliproxy/model_registry.go:110` does not know the rule). And it loses `others` while the switch is on, which is the Models page's "Not served" section and its **Keep** |

**What it costs.**

1. **An alias in a login's own file is by name, like every other name he set** (16.3). As built it was scoped to its login. The difference shows only when another account registers a model under the same spelling: that account now serves the name too, as it does with the switch off. Decision 2 of section 11 already says this for every other kind of name.
2. **More inserted lines in upstream's files, in fewer files**: 24 lines in 7 files, all pure additions, against 14 lines in 9 files as built (one of them a changed line). Eight of the 24 are in `internal/registry/model_registry.go`, which the layer did not touch before: one field on `ModelInfo` and two three-line checks.
3. **What reads the registry outside the gate now sees every registered name while the switch is on**: upstream's management views (the models of an auth file, which the console's Routing page shows per credential; `registered_models` of `GET /provimux/model-lists`), the scheduler, cooldown state. For the gateway this is a gain: a model that is not served keeps its state, and a path that uses a login without naming a model (Muse Code's relay picks a Meta login by its registered models, `internal/client/musecode/relay.go:359-420`) no longer loses the login when all its names are refused. For the console it is a wording matter: those views say what an account offers, not what is served.
4. **With the switch on**, building a list asks, for each registered name that is not literally his, whether some account registers it as an alias or as a prefixed copy: one map lookup per account. 20 accounts with 500 such names is 10,000 lookups per uncached list. With the switch off the cost is one atomic load per list and per request (16.7).

### 16.2 What is removed from the build

After the change these five upstream files are byte-identical to `9ffc2a244` (the commit before the build): a reviewer checks it with `git diff 9ffc2a244 -- <file>`.

| File | Removed |
|---|---|
| `sdk/cliproxy/service_executors.go` | the line `normalizedModels = provimuxOnlyMyNames(a, normalizedModels)` (`:572`) |
| `sdk/cliproxy/antigravity_models.go` | the line `models = provimuxOnlyMyNames(current, models)` (`:888`) |
| `internal/pluginhost/adapters_executors.go` | the line `registration.models = registry.ProvimuxOnlyMyNames(...)` (`:276`) |
| `internal/pluginhost/model_router.go` | the three lines `if provimuxRouteHeld(nextReq) { continue }` (`:68-70`) |
| `internal/watcher/config_reload.go` | the clause `\|\| config.ProvimuxOnlyMyNamesChanged(oldConfig, newConfig)` (`:171`): nothing has to register again when the rule changes |

In upstream's `sdk/api/handlers/handlers_routing.go`: the line `body = provimuxNotServedBody(body, baseModel)` (`:216`).

In the layer's own files:

| File | Removed |
|---|---|
| `internal/registry/provimux_only_my_names.go` | the records and everything that serves them: `provimuxRecord`, `provimuxOffered`, the `records`, `gcAt`, `applied`, `appliedGen` fields and the one mutex over rule and records; `ProvimuxOnlyMyNames`, `provimuxStaleLocked`, `provimuxSweepLocked`, `provimuxHeldLocked`, `provimuxRegisteredLocked`, `ProvimuxRouteHeld`, `provimuxAppliedLocked`, `ProvimuxNamesApplied`, `ProvimuxNamesState`, `ProvimuxNamesRecords`, `ProvimuxApplied`, `(*ProvimuxNamesRule).registers` |
| `internal/config/provimux_only_my_names.go` | `ProvimuxOnlyMyNamesChanged`, `provimuxNameKeys`, `sameNameSet`. `ProvimuxNamesRule` and `provimuxAliasNames` stay |
| `sdk/cliproxy/provimux_only_my_names.go` | `provimuxOnlyMyNames`. `provimuxSetNamesRule` stays |
| `sdk/api/handlers/provimux_only_my_names.go` | `provimuxNotServedBody` |
| `internal/pluginhost/provimux_only_my_names.go` and its test | the whole files |
| `internal/api/handlers/management/provimux_model_routes.go` | the `Applied` field of the response, its assignment (`:91`) and `provimuxNamesAppliedNow` |
| `sdk/cliproxy/provimux_only_my_names_writers_test.go` | replaced by test K5 (16.7): the writers must not mention the rule at all |

The Savings change of round 2 (requests by requested name) is not touched.

### 16.3 The gate

**The value.** `ProvimuxNamesRule`, immutable once set: `enabled`, `home`, `source` (the config's SHA-256, the config API's ETag), `generation` (a counter given when it is set), `names` (the lower-cased names of the config: rows 1, 2 and 4 of section 2.2, each with its spelling and `set_by`), the ignored `keep` entries. It is held in one `atomic.Pointer`. It **applies** when it is not nil, `enabled` is true and `home` is false. Nothing else is state, except the refusal counter of section 5 (at most 256 names, its own small lock, touched only when a request is refused or the status is read).

**Three facts per registered model**, all already on the model as its account registered it:

| Fact | Where it comes from |
|---|---|
| `ID` | the registered name |
| `OAuthAlias` | upstream sets it on every alias of an `oauth-model-alias` row and of a login's own file, and keeps it on the copy under the account's prefix (`sdk/cliproxy/service_models.go:1445-1446`) |
| `PrefixedFrom` (new) | the id a prefixed copy copies: `opus` on `team-a/opus`. Set where upstream makes the copy (16.4). Empty on every other model. It is a fact about the model, written with the switch on or off; no name is added or removed by it |

**A name `n` is his** when one of these holds:

1. `n`, trimmed and lower-cased, is in the value's names;
2. some account registers a model with `ID` `n` whose `OAuthAlias` is not empty;
3. some account registers a model with `ID` `n` whose `PrefixedFrom`, lower-cased, is in the value's names.

For 2 and 3 the registry is asked for `n` as given and, when `n` has upper-case letters and nothing is registered under it, for its lower-case form, as upstream's own lookup does (`internal/util/provider.go:65-70`). Case therefore decides nothing new: whether a request's spelling reaches a registered model stays upstream's (round 2, K).

**A name is served** when the rule does not apply, or the name is his. That is the whole gate: one function of the value and the registry, called by the lists, the door, the routers, alpha search, `model-routes` and the status. No caller has its own variant.

| Case | What the gate does |
|---|---|
| Switch off, absent, or Home mode | returns "served" after one atomic load; reads nothing else |
| A thinking suffix, `opus(high)` | the door checks the name without the suffix (`thinking.ParseSuffix`), then, if that is not his, the name as sent (an exact route registered with its suffix) |
| `auto` | not a name he set. With the rule applying, the door replaces `auto` (its suffix kept) by the first available of his names: `GetFirstAvailableModel("")`, which reads the gated list. If none is available the request is refused as upstream refuses an `auto` it cannot resolve. A router is not asked about `auto` |
| An account's `prefix` | `team-a/opus` is his when `opus` is, through `PrefixedFrom`; with `force-model-prefix` too, since the copy carries the fact whether or not `opus` itself is registered |
| The `devin/…` and `copilot/…` namespaces, OpenRouter's `vendor/model` | part of the id, not a prefix: his only when an alias names it or `keep` lists it in full (R7) |
| An alias in a login's own file | his, by fact 2, **for every account that registers the spelling**. This is the change of cost 1 |
| A plugin executor's own models | registered as with the switch off; his only by literal name |
| A name nobody registers | not his unless the config names it. A request for it is answered as upstream answers an unknown model; it is not counted as refused |

**Where it is applied.**

| # | Place | With the rule applying |
|---|---|---|
| G1 | the two list loops of the registry | a registered name that is not his is skipped. `auto` and every list follow |
| G2 | the door: the first statement of `providersForExecution` | the requested name, and the model a router's decision names if it names one, must be his; otherwise HTTP 400 with upstream's unknown-model body (`type: invalid_request_error`, `code: model_not_found`, `param: model`). The message is section 5's when an account registers the name, and the refusal is then counted under the name without its suffix; otherwise upstream's "unknown provider for model …". This covers the registry path, a forced provider and a router's provider decision alike, so decision 6 of section 11 is gone: all three get the setting's message |
| G3 | `applyModelRouter`, after `var decision` | for a requested name that is not his it returns the empty decision: no router is asked, no plugin executor is chosen, and the request goes on to G2 |
| G4 | alpha search, after the router's answer | the requested model and the selection model must be his; otherwise 400 in that route's error shape, with the same message rule and counting (as built this route answered 503 "no auth available") |

**Lock rules.** The value is read with one atomic load. G1 runs under the registry's lock, which its caller holds, and takes no other. The other callers take the registry's read lock only for a name that is not literally in the value, and release it before touching the refusal counter. Nothing calls the auth manager, so there is no order between its lock and the registry's to get wrong.

**Setting the value.** `SetProvimuxNamesRule` takes the registry's lock, stores the pointer, clears the registry's list cache (`invalidateAvailableModelsCacheLocked`, which the layer's file may call: same package) and releases the lock. A list is built and cached under that lock and G1 reads the pointer inside it, so no list built with the old value exists once the call returns, and no list mixes two values. The refusal counter starts again when the rule starts applying and is dropped when it stops. Warnings (an ignored `keep` entry, zero names) are logged only when the rule applies; with the switch off a reload logs nothing about the setting.

### 16.4 What is added

**Upstream's files: every line inserted.** No line is changed or removed in any of them beyond 16.2.

| File | Place | Inserted |
|---|---|---|
As of section 18 (head after the fifth round): **22 lines in 6 files**. The `PrefixedFrom` rows of 16.4 are gone
(18.1); the table below is the current one.

| File | Place | Inserted | Why |
|---|---|---|---|
| `internal/registry/model_registry.go` | `GetAvailableModelInfos`, after the `if !available …` block (`:1725`) | `if r.provimuxHeld(registration.Info, "") { continue }` (3 lines) | the list gate (G1) of the info list |
| `internal/registry/model_registry.go` | `buildAvailableModelsLocked`, after the `if !available …` block (`:1745`) | `if r.provimuxHeld(registration.Info, handlerType) { continue }` (3 lines) | the list gate of every format's list (the Gemini format checks the name it shows, 18.4) |
| `sdk/api/handlers/handlers_routing.go` | `providersForExecution`, first statement (`:130`) | `if errMsg := h.provimuxDoor(&modelName, originalRequestedModel, routeDecision); errMsg != nil { return nil, "", errMsg }` (3 lines) | the door (G2): every handler execution passes it before an account is chosen |
| `sdk/api/handlers/handlers_routing.go` | in `modelRouteDecision`, after `Model string` (`:334`) | `provimuxRule provimuxRuleValue // Provimux: only-my-names` (1 line; the type is an alias in `provimux_only_my_names.go`, so no import) | carries the rule value the router check (G3) judged under to the door (G2), directly or in the context of a prepared stream route, so one request is judged under one value (18.2). Without it a commit between the two let G3 skip the router under one rule and the door serve under the other: 449 of 2000 requests served in `TestProvimuxOnlyMyNamesRouterUnderAlternatingRules`. A sync must keep the field: our file reads and writes it, so a sync that drops it does not build, and K3's source checks require the door to start from it |
| `sdk/api/handlers/handlers_routing.go` | `applyModelRouter`, after `var decision modelRouteDecision` (`:374`) | `if provimuxNoRouter(&decision, modelName) { return decision }` (3 lines) | the router check (G3): no router is asked about a name the rule does not serve; stores its rule value on the decision |
| `internal/api/server_routes.go` | `codexAlphaSearch`, after the `if errRoute != nil` block (`:404-408`) | `if s.provimuxRefused(c, routing.Model, selectionModel) { return }` (3 lines) |
| `sdk/cliproxy/service_config.go` | `:181` | stays: `provimuxSetNamesRule(newCfg)` |
| `sdk/cliproxy/builder.go` | `:318` | stays: `provimuxSetNamesRule(b.cfg)` |
| `internal/registry/catalog_config.go` | `:25-28` | stays: the `OnlyMyNames` field |

Each inserted statement ends with the comment `// Provimux: only-my-names`, as the built ones do.

**The layer's files.**

| File | Holds |
|---|---|
| `internal/registry/provimux_only_my_names.go` (rewritten) | the value and its pointer; `SetProvimuxNamesRule`; the gate; the refusal counter and message; the status view. About a third of its 598 lines remain |
| `sdk/api/handlers/provimux_only_my_names.go` (rewritten) | `provimuxDoor`, `provimuxNoRouter` |
| `internal/api/server_provimux.go` | `provimuxRefused` for alpha search, beside the route and `ProvimuxRoutes` |
| `internal/api/handlers/management/provimux_names.go` | the answer of 16.6, from the status view |
| `internal/api/handlers/management/provimux_model_routes.go` | skips an id the gate does not serve; with the rule not applying the code path is the one of `9ffc2a244` after one atomic load |
| `config.example.yaml`, `.github/README.md` (`:397-470`, `:499`) | the words of 16.1, 16.3 and 16.8 in place of "an account's model is registered only under a name you set", the "as applied" status and the per-login alias |

Signatures the build is checked against:

```go
// internal/registry

// SetProvimuxNamesRule puts rule in force: under the registry's lock, one atomic store and the list cache cleared.
func SetProvimuxNamesRule(rule *ProvimuxNamesRule)

// ProvimuxServes reports whether name may be listed and asked for: true when the rule does not apply
// or name is the owner's (only-my-names.md 16.3). name has no thinking suffix.
func ProvimuxServes(name string) bool

// ProvimuxRefuse reports whether a request for name is refused. offered: an account registers the
// name; the refusal was then counted and the caller answers with ProvimuxNotServedMessage(name).
// Otherwise the caller answers as for a model nobody offers.
func ProvimuxRefuse(name string) (refused, offered bool)

func ProvimuxNotServedMessage(name string) string

// provimuxHeld is the list gate. The caller holds the registry's lock.
func (r *ModelRegistry) provimuxHeld(info *ModelInfo) bool

// ProvimuxRegistered is one model one client registers, with the facts the rule reads.
type ProvimuxRegistered struct{ ClientID, ID, OAuthAlias, PrefixedFrom string }

// ProvimuxNamesView is the rule in force and, from one pass under one read lock and without cloning
// model metadata, every model the given clients and every plugin executor register.
func ProvimuxNamesView(clientIDs []string) (rule *ProvimuxNamesRule, registered []ProvimuxRegistered)

// sdk/api/handlers

// provimuxDoor refuses a request whose name, or whose router target, is not the owner's. With the
// rule applying it replaces "auto" in *modelName by the first available of his names.
func (h *BaseAPIHandler) provimuxDoor(modelName *string, originalRequestedModel string, decision modelRouteDecision) *interfaces.ErrorMessage

// provimuxNoRouter reports whether no plugin router may be asked about modelName.
func provimuxNoRouter(modelName string) bool
```

### 16.5 The second review's findings under it

| Finding | Becomes |
|---|---|
| 1. The status reports the new rule as settled while a late-registering login still lists and serves a refused model | Gone by construction. Registration does not consult the rule, so when a login registers cannot matter: its model is skipped by G1 and refused by G2 from the store of the pointer. The status reads that pointer |
| 1. Two writes without a status read report the rule of two generations ago | Gone: there is no "applied" rule kept beside the current one, and nothing advances on a read |
| 1. A rejected registration leaves `applying: true` for ever | Gone: nothing compares the registry with an expectation. `applying` is the constant `false` |
| 4. A plugin with no registered models executes a name that only a login-file alias serves | Changes meaning, and stays to be decided with cost 1. The guard is no longer "does any provider register the spelling" but "is the name his" (G3): for a name that is not his, no router is asked at all. A name that is an alias in a login's file is his, by name, so a router may act on it exactly as with the switch off: the router is the owner's plugin, acting on a name the owner set. If the chief wants the alias scoped to its login instead, see the first row of "the two other shapes" in 16.1 |
| 5. A rejected writer replaces a live offer with an obsolete one; 250 add, reject and remove cycles leave 250 records | Gone: no record exists. The layer keeps nothing per account. What accounts offer is read from the registry, which upstream already cleans on removal |
| 6. After a stale Antigravity refresh is rejected, a served name is also in `others` | Gone: `names` and `others` are one pass over the registry with one predicate, so a registered name is in exactly one of them (test K7) |
| 13. A prefixed copy that is itself on `keep` disappears from its base name's `copies` | Gone: `copies` of a name are the registered ids whose `PrefixedFrom` is that name, whatever else is true of the id. A copy that is also kept in full is in `copies` and has its own row |
| The cost: the `model-routes` probe at 3.2 ms and 7.6 MB with the switch off | Gone: the marker computation is removed from that endpoint; with the switch off it does what it did at `9ffc2a244` after one atomic load (test O2) |
| 7 (noted there): the status comparison folds case and breaks a case-only alias edit | Gone with the comparison |
| 10 (the route inventory skips any path containing `/management`) | Not changed by this section, and the closure rests on it: test K1 states the fix |
| 14 (Savings counts some requests under the resolved name) | Not changed by this section |

### 16.6 The contract after it

`pui13/openapi.yaml` is updated in the commit that adds this section. The shape of `GET /provimux/names` is unchanged, so the console at `074f924d` needs no code change to keep working; two meanings change and one field leaves `model-routes`.

| Field | Stays | Meaning after |
|---|---|---|
| `only_my_names.enabled`, `applies`, `not_applied` | yes | of the rule in force |
| `applied.config_etag` | yes | the ETag of the config whose rule is in force: the value every list and request is checked against as the answer is made. The console's wait (`namesApplied.ts`) ends at the first read where it equals the write's ETag |
| `applied.applying` | yes | always `false`. Kept so the console needs no change |
| `applied.generation` | yes | counts the rules put in force since the start |
| `names[].name`, `set_by`, `accounts` | yes | unchanged, except `login-file`: an alias a login registers from its own file, served by every account that registers the spelling |
| `names[].copies` | yes | the registered ids that copy the name under an account's prefix; a copy kept in full is here and has its own row |
| `keep_ignored` | yes | unchanged |
| `others` | yes | every name an enabled account or a plugin executor registers now that is not his. Read from the registry, not from a record. A spelling that is a login-file alias on one account is no longer here for another account |
| `refused` | yes | counted only when an account registers the refused name; a router's target can be the name |
| `applied` of `GET /provimux/model-routes` | **removed** | the console reads it and does not use it (`normalize.ts:139`; the page waits on the one of `/provimux/names`, `ModelsPage.tsx:193`, `:220`) [read] |
| `models` of `GET /provimux/model-routes` | yes | served names only, by the gate, as the answer is made |
| `registered_models` of `GET /provimux/model-lists` | yes | what the account registers, the same with the switch on or off (test S5 now asserts that nothing moves) |

A write of the config answers before its config is committed (the reload runs in its own goroutine, `internal/api/handlers/management/handler.go:304-320`) [read]. Until the commit the old rule is in force and the status says so: `applied.config_etag` is still the old one. The status is never ahead of what is served, and never behind it.

### 16.7 The tests

Tests R1 to R12, C1 to C5, D1 to D5 and S1 to S6 of section 9 stay, reworded from "registered" to "listed and served" where they say it; R12 and C4 are replaced by K5 and K6, and S5 asserts the opposite of before. New or changed:

**The gate is closed.**

| | Proves |
|---|---|
| K1 | The route inventory (`TestProvimuxOnlyMyNamesDoorRouteInventory`): every route the server registers is in D1's table (lists models), D2's (takes a model), the table of routes that name none, or under the management API. A route is management only when its path starts with the management base path, not when it contains the word (second review, 10) |
| K2 | D2 for every route of its table with a name that is not his: refused, the stand-in vendor receives nothing, and the answer carries the setting's message. Added rows: a forced provider, a router's provider decision with a target that is not his, a router's decision for its own executor, alpha search (400) |
| K3 | A source test, as R12 was for the writers: every call of the registry's eleven read methods (`GetAvailableModels`, `GetAvailableModelInfos`, `GetAvailableModelsByProvider`, `GetModelCount`, `GetModelProviders`, `GetModelInfo`, `GetFirstAvailableModel`, `GetModelsAndEpochForClient`, `GetModelsForClient`, `ClientSupportsModel`, `LookupClientModelRoute`) outside tests and the registry's own file is in a table in the test, with its class: **list** (reads a gated loop), **after the door** (runs for a request `providersForExecution` let through, or decides a capability and executes nothing), **no client name** (management, state and cooldown reconciliation, the scheduler's copy, Muse Code's relay, verification, the translators' metadata lookups). `GetAvailableModelsByProvider` is not gated and is in the third class: its callers are the Antigravity translators' metadata lookups, the registry's static-definition helper and a management handler. A call that is not in the table fails the test and names this section, and so does an exported method of `ModelRegistry` the test does not know, so an upstream release that adds a reader or a read method fails the sync check |
| K4 | A source test: `buildAvailableModelsLocked` and `GetAvailableModelInfos` each contain the gate line; `provimuxDoor` is the first statement of `providersForExecution`; `applyModelRouter` and `codexAlphaSearch` contain theirs |
| K5 | The switch never changes what is registered: for the same accounts every client's registered models are deep-equal with the switch off and on, and across off, on, off with no registration in between; the source of the three writers does not contain "Provimux" |
| K6 | No lag. `SetProvimuxNamesRule` alone, with registration paused throughout: the next list and the next request follow, and a list cached before is gone. Then the second review's schedules as regressions: a login whose registration was computed before the rule and publishes after it; two writes with no status read between them; a registration the fence rejects; 250 add, reject and remove cycles (the layer's state is the pointer and at most 256 counted names, before and after); a stale Antigravity refresh; a case-only alias edit |
| K7 | A property test over generated registries and rules: every registered id is in exactly one of `names` (as a name or a copy) and `others`; the ids under `names` are exactly the ids the lists show and the door lets through; the ids under `others` are exactly the ones skipped and refused. The status and the gate cannot disagree because the test fails if they call different predicates |
| K8 | After a write through the config API, the first status read whose `applied.config_etag` is the write's ETag is followed at once, with no wait and with registration paused, by a list and a request that obey the new rule. 100 rounds |

**Off is today.**

| | Proves |
|---|---|
| O1 | With the key absent, and with `enabled: false` and a filled `keep`: the three list shapes, a chat by alias, by a vendor name and by an unknown name, and every client's registered models are equal to a golden file produced by running the same test at `9ffc2a244` |
| O2 | Benchmarks, each run on `9ffc2a244` and on the new head with the same file, switch off, 20 accounts of 500 names: the `model-routes?model=` probe (the review measured 1.36 µs and 2,688 bytes before the build), the registration of 100 models (418 allocations at the base), `GetAvailableModelInfos`. Required: allocations and bytes per operation equal to the base; time within 10%. And `provimuxDoor` alone: no allocation |
| O3 | With the switch off the gate reads nothing but the pointer: a registry whose lock is held by the test does not block `ProvimuxServes`, `provimuxNoRouter` or the door |
| O4 | With the switch off a reload logs nothing about the setting |
| O5 | The five files of 16.2 are byte-identical to `9ffc2a244`; upstream's tests are unchanged and the sync check is no worse than upstream |

**With the switch on**, one bound: an uncached list over 20 accounts of 500 names not his, under 1 ms; the door for one of his names allocates nothing.

### 16.8 What stays documented as not covered

- **The realtime and live routes** (`/v1/realtime*`, `/v1/live`): they choose a Codex login without naming a registered model (`internal/client/codex/live/live.go:525`).
- **A plugin executor reached through a router, for one of his names**: the plugin decides what it runs. For a name that is not his no router is asked.
- **A plugin that rewrites the final model list** (`sdk/api/handlers/handlers_interceptors.go`): what it adds is shown; a request for it is still refused.
- **The Responses websocket** may close without the message: the refusal now reaches it as an execution error, and what the client then sees is upstream's; D2 records it [infer].
- **Media names and Claude's generated ids** are resolved by their handlers before the rule sees a name (round 2, I and J).
- **An exchange under way when the rule changes** (a stream, a websocket session's follow-up on its pinned login) finishes under the rule it started with [infer; K6 names what it observes].
- **Between a write's answer and its commit** the old rule is in force; the status says which.
- **Home mode**: the rule does not apply.
- **Upstream's management views** show what accounts register, which with the switch on includes names that are refused (cost 3).

### 16.9 What this replaces in sections 1 to 15

| Section | Now |
|---|---|
| 1, the table's first row; 3.1 | The rule is applied at the reads (16.3), not in the three writers. There is no record |
| 3.2 | The rows that say "no provider registers the name" read "the door refuses the name"; the forced-provider and router row gets the setting's message |
| 3.3 | A learned model, a plugin's model: "not served unless named" instead of "held back"; `registered_models` does not move |
| 4, the reload row | The rule is in force at the commit; no account registers again for it; the clause in `config_reload.go` is gone |
| 2.2 row 3; R5 | An alias in a login's own file is by name |
| 8 | 16.2 and 16.4 |
| 9 | 16.7 |
| 11, decision 6 | Gone: every refusal carries the message |
| 14 | The bullets on the record, the reload clause and the plugin executor's record no longer hold |
| 15 | The bullets on the status as applied, the record, `others` and the plugin router are replaced by 16.3, 16.5 and 16.6 |

### 16.10 What I ran, and what I did not verify

**Ran.** The searches named above (the readers, the callers of the manager's three entry points, the callers of `applyModelPrefixes`). `go test ./internal/api/handlers/management -run Provimux` after editing `pui13/openapi.yaml`: ok (the contract tests read that file; the built handler still passes because the shape did not change). No request to any service; nothing on port 8317 or 8787.

**Read**, with the lines given: the scheduler's copy and when it is rebuilt; the four handler entry points and the two functions before them; alpha search; the two list loops, `GetFirstAvailableModel` and the list cache; `applyModelPrefixes`; the config commit and the management write's reload; the console's use of `applied`.

**Not verified, each with its test:**

- that `?include=accounts` annotates only listed ids (`openai_handlers.go:86-92` suggests so): D1;
- that the Codex client catalog and `/v1beta/models/<id>` are built from the gated list: D1;
- that no handler outside `sdk/api/handlers` reaches a vendor for a named model without `providersForExecution`, apart from alpha search: K1 with K2;
- that upstream's caches keyed on the registry's generation (the Codex spawn-agent model list) follow the cache clear at a rule change: K6;
- that adding a field to `ModelInfo` breaks none of upstream's tests (the ones I read compare single fields, `sdk/cliproxy/service_oauth_model_alias_test.go:295-330`): O5;
- what a Responses websocket client receives for a refused name: D2.

### 16.11 As built

Section 16 is built. The upstream lines are the 24 of 16.4, in its 7 files, all inserted; the five files of 16.2 are
byte-identical to `9ffc2a244`. Where the build differs from 16.1 to 16.7, and why:

- **The facts are read from an index, not one lookup per account.** Cost 4 of 16.1 counted 10,000 lookups; a name is
  looked up once per account, so 20 accounts of 500 names is 200,000, and the uncached list took 3.4 ms against the
  bound of 1 ms. The registry's side of the predicate now reads an index of the ids some live client registers as an
  alias or as a prefixed copy of a name of the rule, built in one pass and kept for one registry generation and one
  rule (every change of what a client registers moves the generation, and so does putting a rule in force). The
  uncached list with the switch on is 0.57 ms; the predicate is the same function for the gate and the status.
- **`ProvimuxRefuseSent(base, sent)`** sits beside `ProvimuxRefuse`: a request is refused when neither the name
  without its suffix nor the name as sent is his, and counted under the name without the suffix.
- **`ProvimuxNamesView()` takes no client list**: it returns every registration, so the status decides what is his
  over the same registrations the gate reads; the endpoint shows the enabled accounts and the plugin executors among
  them. `ProvimuxIndex` and `(*ProvimuxNamesRule).His` give the endpoint the gate's predicate.
- **Names the door lets pass**: an empty name (it names no model). A refused name no account registers gets upstream's
  unknown-model words and is not counted. `auto` with none of his names available stays `auto` and is refused as
  upstream refuses it.
- **Alpha search**: the router is asked before the check (G4 sits after its answer, as 16.4 places it), and an empty
  model passes.
- **The status's names**: a login-file name comes from the enabled logins' files, and from a registered alias the
  config does not name; an id that is his but neither a name nor a copy of one (registered by a client the status does
  not show) gets its own row, so every registered id is in exactly one of names and others. Counts and `others` group
  spellings that differ only in case, as before; K7 generates lower-case ids.
- **A refusal racing a rule change** is counted only when the rule that refused it is still in force.
- **Tests**: K1, K2 (D2 and the handler paths), K6 and K8 at the service and the handlers; K3, K4 and the source half
  of K5 in `sdk/cliproxy/provimux_only_my_names_source_test.go`; K7 at the endpoint; O1's golden file is
  `sdk/cliproxy/testdata/provimux_only_my_names_off.golden.json`, written by the same test file at `9ffc2a244`; the O2
  benchmarks are in files that use upstream's API only. O3 holds the registry's lock in the registry's tests (the
  door, off, calls only the atomic load they test). O5 is checked with `git diff 9ffc2a244 -- <file>`, not by a test.
- **The Responses WebSocket**, asked for a name that is not his, still closes with 1006 and no event (D2 records it).

## 17. Amendment after the third review: a name is his only if the config says so, compared exactly

2026-11-25, after the review of the gate (`640d87e08`) ended "not ready". Decided by the chief; where this section
differs from the sections before it, it holds.

### 17.1 The two conveniences dropped

**A. The registry's alias flag no longer makes a name his.** A name is the owner's when the committed config names it:
the alias of a key's models row (differing from its name), the alias of an `oauth-model-alias` row, or an entry of
`keep`. `OAuthAlias` on a registered model is no longer read: it marks global aliases and login-file aliases alike,
so a commit that deleted a global alias kept its name served until the login registered again (third review, 1), and
an unavailable login-file alias appeared in both `names` and `others` (3, first case). An alias that exists only in a
login's own file is now an ordinary name: under `others`, refused while the switch applies, his once `keep` lists it.
The rule is a pure function of the committed config. `set_by: login-file` is no longer produced; the value stays in
the contract's enum so a console written for it needs no change (the smaller change).

The one fact still read from the registry is `PrefixedFrom`: `team-a/opus` is served when `opus` is his, because an
account registers it as a copy of `opus`. That is not ownership of a spelling but the account's prefix (3.3); whether
`opus` is his still comes from the config alone.

**B. Names are compared exactly: trimmed, nothing else, as upstream matches a request.** No case folding in the
rule, the gate or the status. `keep: [mixed-case]` does not own `Mixed-Case`; `names[].accounts` counts the accounts
that register the name exactly; `others` keeps spellings that differ only in case apart. A request is judged as
sent: upstream's lower-case fallback (a request `MINE` reaching a registered `mine`) is not honoured by the door,
which refuses `MINE` unless the config names it (with upstream's unknown-model words when nothing registers `MINE`).
R4 and R5 of section 9 are corrected accordingly.

### 17.2 The fixes

**C. One predicate, the door's, everywhere.** A name `n` is served when the rule does not apply, or `n` is his, or `n`
without a thinking suffix is his (a request for `opus(high)` runs `opus`). His: the config names it exactly, or some
account registers it as a prefixed copy of a config name. `auto` is never his. The two list loops, the door, the
router check, alpha search, `model-routes` and the status all call this predicate (`serves`); a registered id such as
`local-base(high)` is therefore listed, served and shown under `names` (among the base's `copies`) when `local-base`
is kept. `copies` now holds every registered id served through a name that is not the name itself: its prefixed
copies and its spellings with a thinking suffix.

**D. One rule per answer.** `GET /provimux/names` reads the rule, its refusal counter and the registrations under one
read lock of the registry (a rule is put in force under the write lock), and builds the whole answer from that
reading. `GET /provimux/model-routes` loads the rule once and decides every id under it (`ProvimuxServesUnder`).

**E. The Gemini format.** It names a model by `Name`. The list gate takes the handler type (the argument of the line
already inserted in `buildAvailableModelsLocked`; `""` in `GetAvailableModelInfos`): in the Gemini list a model whose
`Name` (without `models/`) differs from its id is listed only when that name is served too. A plugin executor's model
kept by its public id and registered with a different provider-native name is therefore left out of the Gemini list,
and the Gemini detail (which reads that list) answers 404 for both spellings; the other formats list the kept id.
Documented in the README's "What the switch does not cover". No upstream line was added: the two inserted lines changed
their argument.

**F. Small.** `auto` on `keep` is ignored like `*`, with the same warning, under `keep_ignored` with the new reason
`reserved`. The index of facts is dropped when the rule stops applying, and a commit with the rule not applying before
or after no longer clears the list cache. K3 also catches a read method taken as a method value (two existing ones
are classified: the Codex client catalog's providers lookup and Devin's name check, both on ids of the gated list).
The reload tests compare registrations without `Created`.

**G, H, I.** Documented: a Responses WebSocket prewarm (`generate: false`) for a name that is not his is acknowledged
by upstream without a provider call; alpha search asks its router before the check. The cost of a commit with the
setting absent (about 10 µs) is accepted.

### 17.3 Tests

The third review's reproductions are regressions in `sdk/cliproxy/provimux_only_my_names_review_test.go` (the deleted
global alias with registration paused, refused at the first status read that shows the write's ETag; the unavailable
login-file alias; the exact keep; the case-only save; the suffixed id) and
`internal/pluginhost/provimux_only_my_names_test.go` (the Gemini native name). K7 now generates spellings that differ
only in case, spellings with a thinking suffix, prefixed copies, `auto` and alias flags, and checks the account counts
too. The registry's tests cover exact names, the alias flag, suffixed spellings, the Gemini names, `auto` and the
index's release; the endpoint's, one rule per answer.

## 18. Amendment after the fourth review: one rule value and the name, nothing else

2026-11-25, after the review of `940ac40cb` ended "not ready". Every case it left had one cause: a request, or a status
answer, put together two things read at two moments (the rule and the live registrations, or the rule twice). Decided
by the chief; where this section differs from the sections before it, it holds.

### 18.1 The rule

The rule is one immutable value built at the config commit from the config alone: the names the config sets (17.1),
and the model prefixes the config declares on its key and provider entries (the `prefix` field of every Gemini,
Interactions, Claude, Codex, xAI, Meta, Vertex-compat and OpenAI-compatibility entry, trimmed as the synthesizer
trims it). A name is his when the config names it exactly, or it is `p/n` for a declared prefix `p` and one of his
names `n`. A name is served when it is his or its name without a thinking suffix is. `auto` never is.

A prefix that exists only in a login's own file is not the config's (no login type takes a prefix from the config):
a copy under it is his only when its full name is on `keep`, exactly as an alias in a login's own file (it shows
under `others`). The rule is by name: `crew/mine` is his when `crew` is a declared prefix and `mine` his, whichever
account registers that spelling.

The rule lives in its own package, `internal/provimux/onlymynames`, which imports only the standard library: it
cannot read a registration. The live fact index of 16.11 and `ModelInfo.PrefixedFrom` are gone (and with them the
review's finding 8, an index republished after the switch went off). The upstream lines that set and declared
`PrefixedFrom` are removed: `sdk/cliproxy/service_models.go` is again identical to `9ffc2a244`. With the field of
18.2, the upstream lines are 22 in 6 files.

**Upstream's copies equal the rule's.** A generated-config test registers 300 configs (Claude, Codex and
OpenAI-compatibility entries with and without prefixes, alias and plain rows, `force-model-prefix` on and off, `keep`)
twice, with and without their prefixes; the ids only the prefixed registration has are upstream's copies. For every
account the config declares, the copies of his names upstream registers are exactly the copies the rule computes,
and every copy is served exactly when what it copies is (1,196 copies checked). No case was found where they differ.

### 18.2 The door

The door loads the rule once and judges every name of the request under that value: the submitted name, then a
router's target. Its judgement (`provimuxDoorJudge`) calls nothing but the rule and the standard library. So the
answer to a name is the rule value and the name: a name nobody registers is refused like any other, with the
setting's message, and counted. Resolving `auto` reads what is available (the newest available model the request's
rule value serves); that is a choice of name, not a judgement, and the chosen name is then judged like any other.

Alpha search loads the rule once for both its names; `model-routes` once per answer; a list once per build (under
the registry's lock, under which a rule is put in force).

**The router check and the door share the value** (the chief's decision (a)). The router check (G3, in
`applyModelRouter`) and the door (G2, in `providersForExecution`) are two places in upstream's code. Each loaded the
rule, so a commit between them could let G3 skip the router under rule B and the door serve the submitted name under
rule A, which with A alone would have been routed to a target A refuses: a test alternating two such rules served
449 of 2000 requests. Now `provimuxNoRouter` stores the value it loaded on the decision, in one inserted field of
`modelRouteDecision` (16.4's table), every return of `applyModelRouter` carries it, and the door judges under it,
loading the rule only when a decision carries none (one not made by `applyModelRouter`: then the door is the only
judgement of the request and loads once). A prepared stream route (`PrepareStreamModelRoute`) stores the decision in
the context, the value with it. Tests: `TestProvimuxOnlyMyNamesRouterUnderAlternatingRules` (0 of 2000, under
`-race` 20 times; 100 to 213 with the door ignoring the value), `TestProvimuxOnlyMyNamesPreparedStreamCarriesItsRule`,
and K3's source checks (the door starts from the decision's value; `provimuxNoRouter` stores it; `applyModelRouter`
never replaces its decision and returns only it).

Every other judgement of a name loads the rule once for its whole answer and asks no router through
`applyModelRouter`: alpha search (its own router call, then both names under one value), each list build,
`model-routes`, the status. A plugin executor chosen by a router is reached only for a name G3 served under the
value it stored; what the plugin runs is under "What the switch does not cover".

### 18.3 The status

`GET /provimux/names` reads the rule, its counter and the registrations under one read lock, then the accounts.
An account enters the auth manager before its models enter the registry, so every registration read is matched to
its account and a count is right at the first answer carrying a rule's ETag (the review's finding 3). An id is
placed by the door's predicate under the same value. A name kept with its thinking suffix has its own row and stays
among its base's `copies`, as a prefixed copy kept in full does.

`accounts` counts exact registrations. With `MINE` kept and only `mine` registered, `MINE` shows no account, while a
request for `MINE` reaches `mine` through upstream's lower-case lookup; this is left as it is.

### 18.4 The Gemini format

The Gemini list shows a model under its `Name`, or its id when `Name` is empty, and a Gemini client generates it
under that name without `models/`. The list gate now applies the same fallback: an alias spelled
`models/gemini-2.5-pro` is left out of the Gemini list (its detail answers 404) unless `gemini-2.5-pro` is served,
so list, detail and generation agree.

### 18.5 Usage by requested name keeps the case

Found by the chief running gateway and console together on `940ac40cb`: the requested-name tally (`requestedName`
in `internal/provimux/savings/requested.go`) went through `cut`, which lower-cases. A tool that sent `B-Coder` showed
as `b-coder` under "used in the last 30 days", "Keep all used" kept `b-coder`, and `B-Coder` was then refused under
exact names (17). The tally now stores the name trimmed and cut to 128 bytes in its own case (`cutKeepingCase`);
`cut` and the rows it feeds are unchanged. Names recorded before this change stay lower case in the stored days;
nothing rewrites them. Test: `TestRequestedModelsKeepCase` (`B-Coder` sent, `B-Coder` reported, beside a separate
`b-coder`, in memory and from the store).

### 18.6 Left

K3 does not see a registry method taken as a method value at package level (the review's note 7): left.

