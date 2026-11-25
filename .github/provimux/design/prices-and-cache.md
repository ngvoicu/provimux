# Prices and cache lifetimes from providers.dev, per account: held, shown, and given to tools

Design, 2026-11-25. This is the design only: no code exists yet. The contract a build is checked against is `prices-and-cache/openapi.yaml`, with two example answers beside it.

**The owner's decision (2026-11-25).** Provimux uses providers.dev's data: in the console, and advertised to clients, "to help developers develop tools, or alternatively use pi-cache-keeper". The shape:

1. Provimux **holds** providers.dev's figures per account: prices, cache prices, the cache lifetime to rely on, the window, each with its date and source. Filled from providers.dev, never blocking, with a switch.
2. The **console** shows them on a name's card per account, and on the Savings page per account.
3. **Clients** get them per account in the model list (`/v1/models`), as a small, documented, versioned contract for tools. With the headers that already say which account answered, a tool knows what that account costs and how long its cache lives without knowing any provider.
4. **pi-provimux** uses it; **pi-cache-keeper**, behind Provimux, has one source and not two.
5. Provimux **acting** on the data itself is a later phase (section 11).

This document designs that shape to be built, and section 2 says where the shape is wrong as stated and what the design does instead.

**Read from.**

| Project | Commit |
|---|---|
| Provimux | GitHub `main` at `e8b6b74d3` (v8.1.3-pmx.1 plus two workflow commits); branch `only-my-names` at `81e203ead` |
| provimux-ui | `main` at `d678044d`; branch `only-my-names` at `06936825` |
| pi-provimux | `574091a` (105.0.7) |
| pi-cache-keeper | `c75d182` (0.10.7) |
| providers.dev | the live API, version 1.6.2, dataset of 2026-11-25 |

Every `file:line` of Provimux below is in `e8b6b74d3`.

**Tags.** **[read]** in code. **[ran]** executed by me on 2026-11-25: plain GETs to `https://providers.dev/api/v1`, and scripts over their answers. **[infer]** reasoned, not verified. I sent no request with a login or a key and never contacted the owner's gateway.

## 1. The design in one page

| Piece | Where | What it does |
|---|---|---|
| The entry of an account | Provimux, a constant map and the model behind a name | says which providers.dev entry (`provider/model`) an account's model is. No request |
| The figures | Provimux, one more providers.dev request in the round that fetches the listings; a disk copy | holds, per entry, the price, the cache lifetimes, the window and the output limit, with dates and sources |
| The contract for tools | `GET /v1/models?include=figures` | per model its accounts, per account its entry, and once per answer the figures of every entry named. Contract 1, `prices-and-cache/openapi.yaml` |
| The console | `GET /v8/management/provimux/figures`, the same records | the figures on each account's line of a name's card; Savings by account, valued at the account's own list price |
| pi-provimux | reads the contract | prices a model by the account tried first, shows the price of the account that answered, and hands each route's figures on |
| pi-cache-keeper | reads what pi-provimux hands on | for a model behind Provimux, Provimux's figures and nothing else; for every other model, providers.dev as today |
| Later | Provimux's selector | a conversation that spilled to a paid key goes back once that key's cache is cold (section 11) |

Five findings shaped it.

1. **Prices are already on every machine; the lifetime is what is missing.** Savings values requests with OpenRouter's list (`internal/provimux/savings/prices.go`). pi-provimux takes a price per model id from models.dev (`extensions/lib.ts:641-643`, `:996`). pi-cache-keeper fills a price from providers.dev only where Pi has none (`model-data.ts:401-424`) [read].
2. **providers.dev's "safe" lifetime is not the lifetime of a route through Provimux.** On a login the safe value follows the vendor's own client, then a direct request, then a proxy (`SafeLifetime.measured_s` in the OpenAPI) [read]. Of the 34 entries with a measurement made through Provimux, 17 have one that differs from the safe value, 16 of them logins [ran]:

   | Entry | Safe value | Measured through Provimux |
   |---|---|---|
   | `openai-codex/gpt-6.2-sol` | 150 s, Codex CLI, 2026-11-24 | 0 s, plain requests, 2026-11-17 (3 of 10 checks hit) |
   | `openai-codex/gpt-6-astra` | 300 s, Codex CLI, 2026-11-23 | 7,200 s with a coding agent, 2026-11-11; 0 s with plain requests, 2026-11-17 |
   | `devin/kimi-k3` | 3,000 s, Devin CLI, 2026-11-23 | 0 s, plain requests, 2026-11-04 |
   | `anthropic/claude-opus-5-5`, long | 3,600 s, direct, 2026-11-17 | 600 s, plain requests asking `ttl: 1h`, 2026-11-04 |

3. **providers.dev has entries for the latest models only** (its editorial rules: `GET /api/v1/meta`, `exclusions`). Of the models its listings name, 4 of 15 on the Claude login, 18 of 60 on Devin and 8 of 11 on the Codex login have an entry [ran]. Every other model has no figure.
4. **Only the gateway knows the model behind a name.** pi-provimux maps an account to a provider in two tables of its own (`extensions/upstreams.ts:73-109`) and takes the model id from the name the client sees (`upstreamOf`, `:158-166`) [read]. For a name the owner set (`opus` for `claude-opus-5-5`) providers.dev has no such model, and pi-cache-keeper gets no data. With "serve only my names" that is every name.
5. **No client acts on a figure it is merely given** (section 9.1). The contract makes a tool possible; it replaces no tool. Pi with pi-cache-keeper is the one client that acts today.

## 2. Where the decided shape is wrong, and what the design does

| The shape says | What is true | The design |
|---|---|---|
| "The cache lifetime to rely on", one per account | providers.dev's safe value is, on a login, the vendor's own client's. Through Provimux it can be longer or shorter (finding 2) | `rely_on_s` is Provimux's selection by a written rule (4.2): a measurement through Provimux first, else the safe value. The safe value and every measurement through Provimux stay beside it, dated. On the Codex login this gives 0 today, which is what was measured |
| Figures "per account" | providers.dev knows a provider, an access method and a model, not an account. Two Claude logins have the same figures | Figures hang on the **entry**; an account names its entry. The contract states each entry once |
| Every account has figures | Entries exist for the latest models only (finding 3), and some accounts cannot be mapped (6.1) | No entry and no figures are normal answers, with their own states; nothing is filled in by guessing |
| "True money per account" on Savings | providers.dev states list prices, not charges. A subscription is not billed per token. The one real charge Provimux sees is what OpenRouter reports | Savings by account, valued at the account's own list price and called an estimate; a subscription's rows say "at API list price"; the reported charge stays the only real one (section 7) |
| "The window" as a figure | Provimux already serves a window per model, from the vendor's list and the catalog. providers.dev's safe input window can differ | The figure is given with its source and changes nothing Provimux registers; the gap-filling rule of the model lists stays the only way a window reaches the registry |
| pi-provimux shows "the right cost for the route that answered" | Pi prices a message itself, from the model's one price, before the answer names an account (pi-provimux computes no cost [read]) | The model is priced by the account tried first; the footer names the account that answered with its price. A pooled name whose accounts differ in price cannot be exact in Pi's own total (8.1) |
| Clients learn nothing new | With the entry, a holder of a client key learns the model behind a name | Decision 5 |

## 3. Who reads what

One source for each reader, so that no two can disagree about the same model.

| Reader | Reads | From |
|---|---|---|
| Provimux | entry summaries | providers.dev, `GET /api/v1/entries`, the only request (section 6) |
| Console | the figures and each route's entry | Provimux's management API |
| A tool | the contract of section 5 | Provimux, `GET /v1/models?include=figures`, and the three answered-by headers |
| pi-provimux | the same contract | Provimux. models.dev stays only for a gateway without figures and for the fast-mode price |
| pi-cache-keeper, a model behind Provimux | the figures pi-provimux hands on with each route | Provimux, through pi-provimux. It looks nothing up for such a model |
| pi-cache-keeper, any other model | providers.dev itself | as today |
| Savings | the figures Provimux holds, then OpenRouter's list | Provimux |

Before this design, pi-cache-keeper behind Provimux read providers.dev's safe value while the gateway would state another; with it, the value for a model behind Provimux has one author.

## 4. The figures

### 4.1 What is held, and where each comes from

All of one entry summary of `GET /api/v1/entries`.

| Figure | From the summary | Unit |
|---|---|---|
| Input, output price | `price_in`, `price_out` | US dollars per million tokens |
| Cache read price | `pricing.rules`: `meter` `text.cache_read`, `operation` `set`, no `when` | the same |
| Cache write prices | the rule for `text.cache_write` without `when` (lifetime not stated), and one per lifetime where `when` is `cache.retention_s eq N`. The long-cache price is the row of the longer lifetime | the same |
| Prices above a prompt size | `tiers` of the source in `safe.price.sources` that `safe.price.source` names | the same, per tier |
| Other rates exist | any other rule with a `when` (fast mode, region, time) | a flag; the entry's page has them |
| Whose price, its date, whether sources disagree | `safe.price.source`, `.date`, `.agreement` | |
| Charged or not | `pricing.billing`, `price_from` | `per_use`; `reference` = not billed per token, the API price for comparison; else unknown |
| Safe lifetime per profile, its basis | `cache_lifetime.short`, `.long`: `safe_s`, `basis`, `source`, `date`, `documented_s`, `documented_status` | seconds |
| Who measured the safe value | the items of `cache_lifetime.lifetimes` with `chosen: true` | client id, label, kind |
| Measured through Provimux | the measured items whose `client` is `cliproxyapi` or `agent+cliproxyapi` | seconds, date, checks, hits |
| Caching documented, a lifetime can be asked for | `cache`, `cache_retention` | |
| Input window, output limit | `safe.input_window`, `safe.max_output`: `value`, `at_least`, `source`, `date` | tokens |

The two profiles are providers.dev's: **short** is what a request gets without asking, **long** what it gets when it asks for the longer cache.

### 4.2 The lifetime to rely on

Per entry and profile, Provimux gives one value, `rely_on_s`, and says what it rests on:

| Order | Taken when | `basis` |
|---|---|---|
| 1 | providers.dev has measurements of the entry made through Provimux: the **lowest** of them | `measured_through_provimux` |
| 2 | else the entry's safe value is measured | `measured` |
| 3 | else the entry's safe value is documented | `documented` |
| 4 | else | `none`, value `null` |

- The lowest in row 1 is providers.dev's own rule for several clients of the kind that counts; the design applies it with "through Provimux" as that kind.
- 0 is a value: measured, and no age held on every check. It is not "does not cache".
- The rule is Provimux's and is part of the contract. The values are providers.dev's and change with its dataset.
- What the rule gives today, from the examples: the Codex login's `gpt-6.2-sol`, short: 0 (through Provimux, 2026-11-17), where the safe value is 150 s with the Codex CLI. An Anthropic key's `claude-opus-5-5`, long: 600 s (through Provimux, 2026-11-04), where the safe value is 3,600 s measured directly on 2026-11-17. The Claude login's `claude-opus-5-5`: 3,600 s by both. The rule errs low, which costs a tool a warm-up too many and never a missed cache; the alternative for decision 1 is to apply row 1 to logins only.
- The owner's rules send new login measurements through the vendor's own client first and through his gateway last, and API keys never through a proxy. So row 1's measurements will age; the date is in the answer, and a reader can see a newer safe value beside an older measurement.

### 4.3 Never stated as fact

| Never | Because |
|---|---|
| A documented lifetime without the word | it was not measured |
| A reference price as what a subscription account costs | the entry is not paid per token |
| A lifetime of 0 as "does not cache" | 4.2 |
| One price or lifetime for a name several accounts serve | each account has its own; the name has none |
| A figure without its date and source | a price can be stale |
| A figure for a model matched by rewriting its id | Savings does that for an estimate and says so; here an entry is matched exactly or not at all |

## 5. The contract for tools

`prices-and-cache/openapi.yaml`, with `examples/models-figures.json`. In short:

```
GET /v1/models?include=figures          (a client key, as for every /v1 request)
```

```json
{
  "object": "list",
  "data": [
    { "id": "opus", "object": "model", "owned_by": "anthropic",
      "accounts": [
        { "provider": "claude", "kind": "oauth",  "priority": 5, "entry": "claude-subscription/claude-opus-5-5" },
        { "provider": "claude", "kind": "apikey", "priority": 1, "host": "api.anthropic.com",
          "entry": "anthropic/claude-opus-5-5" }
      ] }
  ],
  "figures": {
    "contract": 1,
    "source": "https://providers.dev",
    "state": "held",
    "dataset_version": "89105175cb0af1aa",
    "generated_at": "2026-11-25T09:49:05Z",
    "fetched_at": "2026-11-25T12:00:03Z",
    "entries": { "anthropic/claude-opus-5-5": { "…": "price, cache, input_window, max_output" } }
  }
}
```

**How a tool uses it.**

1. Once, and again when it likes (the answer is cheap): read the list. Per model, `accounts` are in the order they are tried.
2. Per answer: read `X-CPA-AUTH-KIND`, `X-CPA-AUTH-PROVIDER` and, for a key with a base URL, `X-CPA-AUTH-HOST` (`sdk/api/handlers/handlers_answered.go:52-56`; on a websocket the fields `cpa_auth_kind`, `cpa_auth_provider`, `cpa_auth_host` of `response.created`). The account of the model with the same kind, provider and host is the one that answered. Accounts that agree in all three have the same entry.
3. `figures.entries[account.entry]` is what that account costs and how long its cache lives. An account without `entry`, or an entry not in `entries`, has no figures.

**The three includes.**

| Request | The answer |
|---|---|
| `GET /v1/models` | upstream's, byte for byte as today |
| `?include=accounts` | as today, and each account gains `entry` where the map has a row |
| `?include=figures` | accounts as above, and the `figures` object. `include=accounts,figures` is the same |

**What we promise to keep, for contract 1.**

| Promise | |
|---|---|
| Keys | every key the OpenAPI lists as required is present. None is removed, and none changes its type, unit or meaning |
| Unknown | a value not known is `null`, never 0 and never left out |
| Units | prices in US dollars per million tokens; lifetimes in seconds; windows in tokens; dates as `YYYY-MM-DD`, times as RFC 3339 in UTC |
| The join | an account's `entry` is the key of `figures.entries`; an account is found from the three headers by kind, provider and host |
| The rule of `rely_on_s` | section 4.2 |
| Growth | keys and enum values may be added; a tool ignores what it does not know and treats an unknown enum value as unknown |
| A change that breaks any of this | is served as `"contract": 2` under a new include value, with contract 1 still answered for at least two Provimux releases that say so in their notes |

**What we do not promise.** The values: they are providers.dev's, dated, and change when its dataset does. That an entry exists for an account. That the account listed first is the one that answers. Which measurement clients providers.dev has. That `figures.state` is `held`: with the switch off or nothing fetched yet, `entries` is empty and the references are still there.

A client that sends no `include` gets nothing new, and no client breaks: pi-provimux keeps only the account fields it knows (`extensions/sources.ts:30-42`) [read].

## 6. Holding the figures

### 6.1 Which entry an account is

`entry` is `<providers.dev provider>/<model id>`, providers.dev's own form of a reference (the provider never contains a slash).

- The provider comes from the map below, by the account's provider, kind and the host of its base URL.
- The model is the id the account sends upstream for the name: after aliases, namespaces and prefixes are resolved, without a thinking suffix or a reasoning variant. Where the held listing of the provider names that id as an alias or a variant of another id, it is that id.
- Exact or nothing: no dots for dashes, no dates removed.
- It needs no request and is the same with the switch off or the network down. It says where the model would be, not that an entry exists.

| Account | providers.dev provider | |
|---|---|---|
| Claude login | `claude-subscription` | exact |
| Claude key, no base URL or `api.anthropic.com` | `anthropic` | exact |
| Codex login | `openai-codex` | exact |
| Codex key, no base URL or `api.openai.com` | `openai` | exact |
| Copilot | `github-copilot` | exact |
| Antigravity | `antigravity` | exact |
| Devin | `devin` | exact; Devin's own id, without `devin/` and the effort variant |
| Meta login | `meta-subscription` | exact |
| xAI key, `api.x.ai` | `xai` | exact |
| Gemini key, no base URL or `generativelanguage.googleapis.com` | `google` | exact |
| Vertex | `google-vertex` | exact |
| Any key whose base URL host is `openrouter.ai` | `openrouter` | exact; OpenRouter's id without a routing suffix (`:nitro`, `:floor`) |
| A key whose host is `api.deepseek.com`, `api.moonshot.ai`, `api.mistral.ai`, `api.groq.com`, `api.cerebras.ai`, `api.fireworks.ai`, `api.together.ai` | the provider of that host | a reference; no figures until the provider joins the request |
| **Kimi login** | **none** | Provimux's default login is on `kimi.com` (`internal/auth/kimi/kimi.go:31`), which providers.dev names `kimi-code-plan-cn`; `kimi.ai` is `kimi-code-plan-global`. The model lists map Kimi to `-global` by inference. None until the login's host decides it |
| **xAI login** | **none** | providers.dev's `xai` is the paid API; it has no entry for the login |
| **Meta key** (`meta-api-key`) | **none** | on `api.meta.ai` it is either the paid API (`meta`) or the key a Muse Code subscription applies to (`meta-subscription`); the account does not say which [infer] |
| **AI Studio** | **none** | requests go through a browser, not the paid API |
| A key on a host not in this table; a plugin's own models; a runtime-only credential | **none** | not known, and never guessed from the model's name |

### 6.2 The request

`GET /api/v1/entries` returns entry summaries, filtered by `provider`, at most 200 a page; a summary carries everything 4.1 reads [read: OpenAPI 1.6.2, `EntrySummary`]. **The site's API needs no addition.**

```
GET https://providers.dev/api/v1/entries?provider=<the fixed set>&limit=200&offset=<0, 200, …>
User-Agent: provimux/<version>
If-None-Match: <the page's ETag, when one is held>
```

- **The fixed set** is a constant: `anthropic`, `antigravity`, `claude-subscription`, `devin`, `github-copilot`, `google`, `google-vertex`, `meta-subscription`, `openai`, `openai-codex`, `openrouter`, `xai`. It does not depend on the accounts a user has, so the request is the same for every user and tells the server nothing about him (the rule of `model-lists.md` section 7).
- **Size** [ran]: the set without `openrouter` and with one Kimi provider is 190 entries in one page, 51 KB gzip, 1.53 MB decoded; `openrouter` is 400 entries in two pages, 68 KB gzip. So about 580 entries, three pages, about 120 KB on a day the dataset changed; with `If-None-Match` a `304` with no body. What Provimux keeps of 190 entries is about 150 KB as JSON.
- **When:** in the round that fetches the listings (`sdk/cliproxy/provimux_published_lists.go:37-44`): at start in the background, every 6 hours, on `POST /v8/management/models/refresh`; after a failure 10 minutes, then 1 hour. Pages one after another until `next_offset` is null, at most 10.
- **A fetched document replaces the held one only if** every page is a `200` or a `304`, well-formed, at most 8 MB, all pages name one `dataset_version`, together they hold `total` entries, and `generated_at` is not before the held document's. Anything else is a failed fetch: the held document stays, and the failure is recorded as a code. An entry Provimux cannot read is left out alone.
- **Disk:** `model-lists/figures.json` beside `published.json`: the kept records, the dataset version, the dates and each page's `ETag`. Mode `0600`, temporary file and rename (`internal/provimux/modellists/published.go:387`). A damaged or foreign file is ignored with one warning and replaced by the next accepted document.
- **Never blocking:** no request is awaited at start or on any path that serves a client. The disk copy is read when the model lists' folder is opened; a start without network gives the figures of the last run, with their dates.
- **Unreachable:** the held document stays, however old; its dates say so. With nothing held, `state` is `none` and there are no figures.
- **The switch:** `models.published-lists: false` already means "Provimux never contacts providers.dev" (`internal/registry/catalog_config.go:21-24`). It turns this request off too: `state` is `off`, no figure is given, the disk copy is not read. Off in a test binary and in Home mode, as the listings are. Decision 3.
- **Through the configured proxy,** within 30 seconds a page.

No model list, no route and no request to a vendor depends on any of it. No executor, translator or auth file changes.

### 6.3 Status for the operator

`GET /v8/management/provimux/figures`, read-only, behind the management key: the same `figures` object as the tools get, with `last_failure` (`{at, category, http_status}`, codes only), `from_cache` and the count of held entries; `entries` holds those a route of an enabled account refers to, every held entry with `?all=true`. `GET /v8/management/provimux/model-routes` gains `entry` on each route (`internal/api/handlers/management/provimux_model_routes.go:71-81`). An older Provimux answers `404` on the new path: the console shows no figures.

Log: `prices and cache lifetimes from providers.dev: dataset 89105175… of 2026-11-25, 584 entries`, and the listings' failure line under this name.

## 7. The console

**On a name's card, per account** (`pages/models/NameCard.tsx`, the line of a route; the look is the designer's):

| Case | Words |
|---|---|
| A key | "$4 in, $20 out per million tokens · cache read $0.20 · cache write $5 (5 min), $8 (1 h)" and under it "providers.dev, from models.dev, 2026-11-24 · base rates" |
| A subscription | "Not billed per token. API list price for comparison: $2 in, $10 out" |
| Measured through Provimux | "Cache through Provimux: 1 h (measured 2026-11-17)" |
| From the safe value | "Cache: 5 min, measured straight at the provider, 2026-11-17. Not measured through Provimux" |
| Both, and they differ | both lines, the measurement through Provimux first |
| Measured 0 | "No cache lifetime held through Provimux (3 of 10 checks hit, 2026-11-17)" |
| Documented only | "Cache: 5 min, documented, not measured" |
| Window differs from the registered one | "providers.dev: input up to 999,500 tokens (measured 2026-11-17)" in the card's details |
| No entry | "providers.dev has no entry for this model", small, once per card |
| No reference | nothing |
| Old | a date more than 90 days back (providers.dev's stale threshold today) is marked "old" |
| Sources disagree on the price | a warning mark, and the link |

Every line links to the entry's page (`https://providers.dev/entries/<provider>/<model>/`). The name's header gets no price and no lifetime while more than one account serves it. A line in the page's settings says "Prices and cache lifetimes: providers.dev dataset of 2026-11-25, fetched 2 hours ago", or since when providers.dev was not reached, or that they are off; it puts the unused published-list strings (`en.json:613-618`) back to use. One read on page load and on refresh, not in the 30-second poll.

**On Savings, per account.** What exists: a day's rows are kept by account kind, provider, host, model and client key, and the record's account is deliberately not read (`internal/provimux/savings/record.go:24-25`); tokens are valued at OpenRouter's list price for the maker's model; a day keeps the prices it was first valued with [read]. What is added:

| | |
|---|---|
| The account in a row | a row is also keyed by the account's index (`auth_index`, as `model-routes` gives it). Days recorded before have none and show as "before accounts were recorded" |
| A grouping | `group=account`: per account its provider, kind, host, the login's name as `GET /credentials` names it today (null when the account is gone), its requests, tokens, estimate and reported charge |
| The account's own price | a new first step in the price search (`savings/02-prices.md` section 4): the entry of the row's account, when the figures hold a `per_use` or `reference` price for it. Source `providers.dev`, price id `providers.dev:<entry>`, match `entry`. Then the steps of today. The entry is `EntryProvider(provider, kind, host)` and the recorded model, which is already the upstream id |
| What stays | a day keeps its prices, so no past number moves. Prices above a prompt size are still not applied: a day's totals hold no request sizes, and the answer keeps saying `base` |
| Words | "at this account's list price (providers.dev, 2026-11-24)"; a subscription's rows "would have cost at API list price"; the only real charge on the page stays the reported one |

The savings contract files (`savings/openapi.yaml`, `schemas/day.schema.json`, `02-prices.md`, `03-endpoint.md`, `04-console-page.md`) are amended as the first task of that phase; this section is their brief. Not verified: that an account's index is the same after a restart and after a token refresh.

## 8. The two Pi extensions

### 8.1 pi-provimux

| | |
|---|---|
| Reads | `GET /v1/models?include=figures` in place of `?include=accounts` (`extensions/lib.ts:1112`, `extensions/upstreams.ts:233-256`). A gateway without figures answers as before, and everything is as today |
| A model's price | the price of the account tried first, with its size tiers, where `figures` has one (`per_use` or `reference`); when several accounts share the first place and their prices differ, the highest, and the name says so. Without figures for it: models.dev, as today. Fast mode: models.dev, as today |
| The footer | "answered: Anthropic api · $4 in / $20 out" (or "· not billed per token"), from the entry of the account that answered |
| What it cannot do | Pi totals a message from the model's one price, so on a pooled name a message answered by another account than the first is counted at the first's price. The footer shows the right one. Whether an extension can correct a message's recorded cost is not verified |
| Writes no cache lifetime into Pi | Pi's own warmer never checks a warm-up; choosing the value for a pooled name is pi-cache-keeper's work |
| Hands on | each upstream of its `model-upstreams` events gains `entry` and `figures` (the entry's record as Provimux gave it, or `null` when Provimux held figures and has none for it). The upstream's `model` is taken from the entry |

### 8.2 pi-cache-keeper

| | |
|---|---|
| An upstream with `figures` | its window, output limit, lifetimes (`rely_on_s` per profile) and price are taken from it. Nothing is looked up for it, and its provider is not asked of providers.dev (`lookupsFor`, `model-data.ts:321-338`; `neededProviders`, `index.ts:1195-1207`) |
| An upstream with `figures: null` | no data, as for an entry providers.dev lacks |
| An upstream without the key | as today: its own lookup at providers.dev (an older pi-provimux or gateway, or figures off) |
| Everything after that | unchanged: several upstreams are merged as today, the answered route swaps in as today, `models.json` is written under today's rules |
| Its status line | names the source: "providers.dev through Provimux, dataset of 2026-11-25" |

## 9. Advertising it, and what stays true

### 9.1 What a client does with it, with no code of its own

| Client | A price | A cache lifetime |
|---|---|---|
| Pi with pi-provimux | the model's price and the footer (8.1) | nothing without pi-cache-keeper |
| Pi with pi-cache-keeper too | as above | acted on, from Provimux's figures (8.2) |
| opencode, Claude Code, Codex CLI | nothing | nothing |

For the last row this is from what each is known to do, not from reading their code today [infer]. So the public words say what a tool **can** do, never that Provimux does it for a client.

### 9.2 The words

For Provimux's README, its page on the site and the notes of the release (the owner's word on public text first):

- "Per account behind a model name, Provimux gives providers.dev's list price, cache prices, cache lifetime and window, each with its date and source: in the console, and to tools in `/v1/models?include=figures`."
- "With the `X-CPA-AUTH-*` headers a tool knows what the account that served a request costs and how long its cache lives, without knowing any provider. pi-cache-keeper is such a tool for Pi."
- A README section "Figures for tools": the three steps of section 5, the promises, and the example.

Never: that Provimux knows what a request cost, that it guarantees a lifetime, or a price or cache field on a model itself, where a client would take it for the gateway's own.

## 10. The build

### 10.1 Provimux

Layer commits only (trailer `Provimux-Layer: branding`). No protected path (`guard.sh:33`).

**New files.**

| File | Holds |
|---|---|
| `internal/provimux/modellists/figures.go` | the request of 6.2, the acceptance rule, the kept record as Go types, `figures.json`, the rule of 4.2 |
| `internal/provimux/modellists/figures_map.go` | the map of 6.1 |
| `sdk/cliproxy/provimux_entry_refs.go` | the model an account sends upstream for a registered name; `entry` |
| `sdk/cliproxy/provimux_figures.go` | the held document, its place in the listings round, the source the handlers read |
| `internal/api/handlers/management/provimux_figures.go` | the endpoint of 6.3 |
| `sdk/api/handlers/openai/openai_models_figures.go` | `include=figures` |
| their `_test.go` files | 10.4 |

```go
// internal/provimux/modellists

// EntryProvider returns the providers.dev provider of an account by its provider, kind ("oauth" or
// "apikey") and the host of its base URL, or "" where the map of prices-and-cache.md 6.1 has no row.
func EntryProvider(provider, kind, host string) string

// EntryRef joins a providers.dev provider and a model id as providers.dev writes a reference.
func EntryRef(provider, model string) string

// FigureProviders returns the fixed set the figures request asks for, sorted.
func FigureProviders() []string

// FiguresRequestURL is one page of the request Provimux sends, built on base.
func FiguresRequestURL(base string, offset int) string

// Figures is what Provimux keeps of one entry: openapi.yaml, components/schemas/Entry.
type Figures struct { /* Provider, Model, URL, Access, Price, Cache, InputWindow, MaxOutput */ }

// FiguresDocument is an accepted answer, as held in memory and written to figures.json.
type FiguresDocument struct { /* Schema, ETags, DatasetVersion, GeneratedAt, FetchedAt, Entries by ref */ }

// FetchFigures asks every page. A result with NotModified keeps the held document.
func FetchFigures(ctx context.Context, client *http.Client, base string, held *FiguresDocument, userAgent string, now time.Time) (FiguresResult, error)

// RelyOn applies prices-and-cache.md 4.2 to one profile of an entry.
func RelyOn(safeSeconds *int, safeBasis string, throughProvimux []MeasuredLifetime) (seconds *int, basis string, date string)

func (s *Store) ReadFigures() (*FiguresDocument, error)
func (s *Store) WriteFigures(doc *FiguresDocument) error

// sdk/cliproxy

// provimuxEntryOf returns the providers.dev entry an account's registered model would be, or "".
func provimuxEntryOf(auth *coreauth.Auth, model *registry.ModelInfo) string
```

Each body is a worker's. `provimuxEntryOf` holds the one open question of the build: the upstream id of a registered name sits in different fields by route (`SourceModelID`, `MetadataModelID`, `OAuthAliasSource` of `registry.ModelInfo`, `internal/registry/model_registry.go:43-51`, and the `name` of a key's `models` row) [read; which field per route is not verified]. Its test is the proof: for every route of a fixture config, the function's model equals the `Model` of the usage record a request through that route produces (`internal/provimux/savings/record.go:26-31`).

**Edits to existing files: additions only.**

| File | Edit | Lines |
|---|---|---|
| `sdk/api/handlers/openai/openai_models_accounts.go` | `entry` on `modelAccount` (`:15-23`), filled through a function variable `sdk/cliproxy` sets; `include=figures` recognised beside `accounts` | +8 |
| `sdk/api/handlers/openai/openai_handlers.go` | where the list is written (`:85-128`): the `figures` object when asked | +5 |
| `internal/api/handlers/management/provimux_model_routes.go` | `entry` on `provimuxRoute` | +5 |
| `sdk/cliproxy/provimux_published_lists.go` | one call in `refreshPublishedLists` (`:208`), one in `loadCache` (`:112`) | +2 |
| `internal/api/server_provimux.go` | the route | +1 |
| README, `config.example.yaml` | the second request; what the switch also turns off; "Figures for tools" | text |

`openai_handlers.go` is upstream's file with the owner's patch in it (the `accounts` lines); the five added lines sit beside those. The others are the layer's own or the owner's patch file.

### 10.2 The console

| File | Change |
|---|---|
| `src/provimux/api/types.ts`, `api/normalize.ts`, `api/provimuxApi.ts` | `entry` on `ProvimuxModelRoute`; `ProvimuxFiguresResponse`; one read |
| `pages/models/catalog.ts`, `NameCard.tsx`, `ModelsSettings.tsx` | the join, the lines of section 7, the status line |
| `pages/savings/` | the account grouping and its words |
| `locales/*.json` | the strings, in all seven languages |
| `tests/provimux/` | each row of section 7's table; a gateway without the path; `off`, `none`, an old date |

### 10.3 The extensions

Section 8. pi-provimux: `sources.ts` (`entry`), `lib.ts` (the price), `upstreams.ts` (the event), `answered-footer.ts`. pi-cache-keeper: `model-data.ts` (an upstream's figures before its own lookup), `index.ts` (`neededProviders`, the status line).

### 10.4 What must hold

| Phase | Must hold afterwards |
|---|---|
| F1 | every row of 6.1's table, each "none" row included; the usage-record test for a login, a key, an alias, a prefixed account, a Devin variant, an OpenRouter id with a routing suffix; `/v1/models` without `include` is byte-identical to `e8b6b74d3`; both account lists give the same `entry` for the same account (`test/provimux/model_routes_parity_test.go` grows) |
| F2 | a test server: `200` on three pages, `304`, `500`, a cut-off body, pages of two dataset versions, an older document, an entry with no rules, a rule the reader does not know; the request carries no parameter beyond `provider`, `limit`, `offset` and no header beyond the two; nothing is fetched with the switch off or in a test binary; a start with the server down serves the disk copy; no path that answers a client waits for the fetch; 4.2's rule row by row, with the four entries of finding 2 as fixtures |
| F3 | both examples validate against `prices-and-cache/openapi.yaml`, and so does the handler's answer for a fixture; every `entry` of an account whose figures are held is a key of `entries`; `include=accounts` alone carries no `figures`; with the switch off `state` is `off` and `entries` is empty |
| F4, F6 | the console's own tests; `bun run verify`; the pages checked against a stand-in gateway; Savings' examples regenerated and its past days unchanged |
| F5 | pi-provimux: a gateway with figures, without, with a malformed entry; the footer; the event. pi-cache-keeper: an upstream with figures is never looked up and its provider never fetched; `figures: null` gives no data; without the key, today's tests pass unchanged |

## 11. The later phase: Provimux acting on the data

Not part of phases F1 to F7 and not in their way. Compaction stays the client's: only the client knows its own context.

**A. Back to the preferred account once the cache is cold. Build it, as the first acting feature.**

| | |
|---|---|
| What upstream does | With `routing.session-affinity: true` a conversation is bound to the account that served it. Every request refreshes the binding; it ends after `session-affinity-ttl` without a request, one hour unless set, the same for every account (`sdk/cliproxy/auth/session_cache.go:122-144`). The binding outranks priority: "once a session is bound, that credential is kept even if a higher-priority credential recovers" (`config.example.yaml:229-232`; `Pick`, `sdk/cliproxy/auth/selector.go:1738-1745`) [read]. The session is found from the headers of Claude Code, Codex, OpenCode and pi, from `X-Session-ID`, a cache key, or the shared start of the conversation (`config.example.yaml:221-228`). So "stay on the account that holds the cache" exists, for every client, and the owner has it on |
| What is missing | The way back. A conversation that failed over from a subscription to a paid key stays on the key while it keeps talking, and for an hour after |
| The rule | Opt-in (`routing.session-affinity-return: "cold"`). A binding to an account below the highest available priority is not honoured once the conversation has been quiet for longer than that account's cache lifetime for the model. The request is then routed as a conversation without a binding |
| Which lifetime | what the bound account's last answer in this conversation reported for its cache write, where the vendor reports one (Claude's format does; `internal/provimux/savings/record.go:60`); else the largest lifetime the entry's figures state, in either profile. The largest, because returning late costs nothing new and returning early gives up a warm cache. Unknown: the binding holds as today |
| Cost | No request is added and none is changed. A lifetime known too short: one cache write on the preferred account where a read on the key would have done |
| Risks | The vendor sees the same requests from the same accounts, so nothing about subscription terms changes. The risk is the code: an inserted block in upstream's selector, a file upstream changed 13 times between v8.0.60 (2026-11-08) and v8.1.3 (2026-11-25) [ran: `git log`] |
| Why the data fits | the account a conversation spills to is almost always an API key, where providers.dev's lifetime is a direct measurement or the documented one |
| The build | new files `sdk/cliproxy/auth/provimux_affinity_return.go` (the rule; a read of a binding's account and last use that does not refresh it) and `sdk/cliproxy/provimux_cache_lifetimes.go`; inserted lines in `Pick` before the binding is read (`selector.go:1738`) and wherever else a binding is honoured (the fallback key `:1797`, the shared-start matcher `:1851`; not read in full); the setting beside the affinity settings (`sdk/cliproxy/service_config.go:60-63`, `internal/config/config_types.go:620`). With the setting off, upstream's selector tests pass unchanged. Size L |

**B. A keep-warm request sent by the gateway. Do not build it.**

| | |
|---|---|
| What upstream does | Nothing: no code re-sends a request to keep a cache alive [ran: a search; the matches are a client's own websocket pre-warm and a latency probe] |
| What it takes | each conversation's last request held in memory, re-sent with one output token before the lifetime ends, to the same account, and a client's own warm-ups told apart so nothing is warmed twice |
| Cost | a cache read of the whole prompt each time: for 300,000 tokens at Claude Opus 5.5's cache-read price ($0.20 per million) $0.06, twelve times an hour on a 5-minute cache, for a conversation nobody may come back to. On a subscription, quota and requests against the plan's limits |
| Risks | the gateway would send a user's content to a vendor in a request no client asked for; on a login that is traffic the vendor's own client would never send, under that client's name. A re-sent request can have effects (a stored response, a turn in Devin's session). A wrong lifetime turns every warm-up into a full cache write |
| Instead | a tool on the contract of section 5, which knows whether its user is still there; pi-cache-keeper is one. Asking the vendor for the longer lifetime on an API key, per key and opt-in, is the gateway-side alternative: it changes a request to a vendor, so it is an owner patch and its own decision, and the one measurement of it through Provimux held 10 minutes, not an hour (finding 2, last row) |

## 12. Fit with what is being built

**"Only my names"** (branch `only-my-names`; its console step in work). No rule of it changes: it filters in front of the registry, and entries and figures are read from the registry, so a name that is not served has no account line. It makes this design matter more (finding 4). Shared lines: `internal/api/server_provimux.go` (each adds one route), the console's `NameCard.tsx`, `catalog.ts` and the locale files. So F1 lands after its console step is merged. `pui13/openapi.yaml` gains `entry` on a route in F1; the new paths are in this design's own contract file, so there is no second edit there.

**Savings** keeps OpenRouter's list as the source for every model without an entry price, and every stored day as it is.

**The model lists (P3)** are unchanged. Their Kimi row is the one open point this design found (6.1).

## 13. The phases

Each ships alone. S: up to a day of one worker. M: one to two days. L: three to five.

| Phase | Repo | Size | Holds | Depends on | What a user has after it |
|---|---|---|---|---|---|
| F1 | Provimux | M | 6.1: the map, the model behind a name, `entry` on both account lists | "only my names" merged | each account names its providers.dev entry; no request yet |
| F2 | Provimux | M | 6.2, 6.3: the request, the disk copy, the switch, the rule of 4.2, `GET /provimux/figures`, the log | F1 | the gateway holds the figures |
| F3 | Provimux | S | section 5: `include=figures`, the contract test, the README section | F2 | tools can read them; a release note names contract 1 |
| F4 | provimux-ui | M | section 7, the name's card and the status line | F2 released | figures per account on the Models page |
| F5 | pi-provimux, pi-cache-keeper | M each | section 8 | F3 released; built against the examples before that | Pi prices by account; pi-cache-keeper has one source behind Provimux |
| F6 | Provimux, provimux-ui | M each | section 7, Savings by account; its contract files amended first | F2 | Savings by account, at the account's list price |
| F7 | the site, the READMEs | S | section 9.2, after the owner's word | F3 and F4 released | it is advertised |
| Review | each phase | S | F1 to F3: a figure or an entry for a wrong account, the request's privacy, a start without network, the sync check no worse than upstream. F4, F6: every row of 4.3. F5: no model with two sources | | |
| Later | Provimux | L | section 11 A | F2 | conversations return from a paid key |

Order: F1, F2, F3; then F4 and F5 side by side; then F6; then F7. A release of Provimux can carry F1 alone, F1 to F3 together, or each in turn.

## 14. Decisions

| # | Decision | Recommendation | The other way costs |
|---|---|---|---|
| 1 | What "the lifetime to rely on" is | **The rule of 4.2**: a measurement through Provimux first, else providers.dev's safe value, both always shown | The safe value alone is one field of providers.dev and no rule of ours, and for 16 logins today it is not what a request through Provimux gets. Row 1 for logins only keeps a key's direct measurement (3,600 s on the Anthropic key's long cache, not the 600 s once measured through the gateway) |
| 2 | The words on Savings | **"At this account's list price", an estimate**; only a reported charge is called a charge | "True money" for rows no vendor reported a charge for is a claim the data cannot carry |
| 3 | The switch | **`models.published-lists` turns both requests off** | A second key lets a user keep lists and refuse figures; one more setting for one host |
| 4 | OpenRouter in the request | **Yes**: 400 entries, 85 with a measured lifetime, two more pages a day | Without it the paid route most users have has no figures |
| 5 | The model behind a name becomes visible to a holder of a client key who asks with `include` | **Accept**: it is asked for explicitly, and the list already gives provider, kind and host | A setting to hide it; then no tool, and no pi-cache-keeper, has data for his own names |
| 6 | Kimi, the xAI login, AI Studio, a Meta key | **No entry**, and the model lists' Kimi row is checked against the login's host | A guess gives another plan's figures as this account's |
| 7 | How long contract 1 is kept after a contract 2 | **Two Provimux releases that say so** | Longer costs two shapes in one handler for longer; shorter breaks a tool between two of its own releases |
| 8 | pi-provimux writes no cache lifetime into Pi | **Yes**; "or use pi-cache-keeper" is the owner's own alternative | Pi's warmer would act on it and never checks a warm-up |
| 9 | The later phase | **A, not B** (section 11) | |
| 10 | The public sentences of 9.2 | the owner's words; the draft is there | |

## 15. What I ran, and what I did not verify

| Check | Result |
|---|---|
| `GET /api/v1/openapi.json` | version 1.6.2; `EntrySummary` carries prices, `pricing`, `safe` and `cache_lifetime`; `/entries` takes `provider`, at most 200 a page; the listings carry no price or cache field |
| `GET /api/v1/entries?provider=<twelve providers>&limit=200`, plain and with `If-None-Match` | 190 entries, one page, 51 KB gzip, 1.53 MB decoded; `304` on the second; `https://providers.dev/entries/<provider>/<model>/` is the entry's page |
| `GET /api/v1/entries?provider=openrouter`, two pages | 400 entries, 68 KB gzip; 290 with a price; 85 short lifetimes measured directly, 9 items through Provimux |
| A script over the 190 summaries | 164 have a price, 137 a cache read price, 55 a cache write price, 119 have other rates, 8 have sources that disagree on the price; 72 have a safe lifetime in a profile, 34 a measurement through Provimux, 17 of those differ from the safe value (16 logins); `price_in` equals the base input rule on all 190 |
| `GET /api/v1/listings?view=full&limit=40&provider=<the same twelve>`; `GET /api/v1/meta` | finding 3; the editorial rules; `stale_after_days` 90 |
| `prices-and-cache/openapi.yaml` through `openapi-spec-validator`; both examples against its schemas with unknown properties forbidden | valid OpenAPI 3.1.0; 0 errors in each; an example with five planted faults (a missing key, a wrong type, an unknown enum value, an extra key, another contract number) gave five errors. The examples' figures are today's answer for those entries, built by a script from the fetched summaries by the rules of 4.1 and 4.2 |
| Read: Provimux's account list, model routes, the listings client and its round, Savings' recording and prices, the affinity selector's `Pick` and the session cache; "only my names" as built; pi-provimux's tables, `upstreamOf` and where a price is set; pi-cache-keeper's reader | sections 1, 6 to 8, 10, 11 |
| `git log v8.0.60..v8.1.3` on `selector.go` and `session_cache.go`; a search of the tree for a warm-up | 13 commits; no request is re-sent to keep a cache alive |

**Not verified.**

| Point | Who settles it |
|---|---|
| Which field of `registry.ModelInfo` holds the upstream id on each kind of route | F1's usage-record test |
| That a page's `ETag` changes whenever an entry on it changes | F2: the document is also checked by `dataset_version` on every page |
| That the two client ids stay the only ones that mean "through Provimux" | F2 keeps them as a constant. If a third appears, a marker on the client in providers.dev's API is the growth to ask for |
| That an account's index survives a restart and a token refresh | F6's first task |
| Whether a Pi extension can correct a message's recorded cost | F5; without it 8.1's limit stands |
| Which host a Kimi login of the owner uses; whether a Meta key can be told from a Muse Code subscription's key | decision 6 |
| Every place the selector honours a binding beyond `Pick`'s cache-hit branch | the later phase's first task, as a test |
| What opencode, Claude Code and Codex CLI do with the answer | not needed: they send no `include` |
| Nothing was built or run against a gateway | every phase's tests |
