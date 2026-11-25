# Prices and cache lifetimes from providers.dev: first whether, then how

Design, 2026-11-25. This is the design only: no code exists yet.

**The owner's question.** "Should we get from providers.dev also the cache and prices and advertise them? This computer has pi-cache-keeper, which already does that, but other computers might not."

**The answer.** Yes, in two parts that can ship apart, and smaller than "Provimux hands out prices and lifetimes":

1. **Provimux says, per account behind a model, which providers.dev entry it is** (`provider` and `model`, providers.dev's own ids). It needs no request to anyone. It is what pi-cache-keeper lacks today for every name the owner sets himself.
2. **The console shows providers.dev's figures on each account's line** of a name's card: the price per token, and the cache lifetimes with who measured them and when. For that Provimux asks providers.dev for entries, in the round that already asks it for model lists.

Provimux does not put the figures into `/v1/models`, acts on none of them, and does not make pi-provimux write a cache lifetime into Pi. Section 3 says why.

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

## 1. Four findings that shaped it

1. **Prices are already on every machine that could use them.** Provimux values requests with OpenRouter's public list for the Savings page (`internal/provimux/savings/prices.go`, `internal/registry/provimux_openrouter_prices.go`) [read]. pi-provimux takes a price per model id from models.dev (`extensions/lib.ts:641-643`, `:996`) [read]. pi-cache-keeper fills a price from providers.dev only where Pi has none (`model-data.ts:401-424`) [read]. A computer without pi-cache-keeper lacks no price. It lacks the cache lifetime, and on such a computer nothing acts on one.
2. **providers.dev's "safe" lifetime is not the lifetime of a route through Provimux.** On a login the safe value follows the vendor's own client, then a direct request, then a proxy (`SafeLifetime.measured_s` in the OpenAPI) [read]. Of the 34 entries that have a measurement made through Provimux, 17 have one that differs from the safe value, 16 of them logins [ran]:

   | Entry | Safe value | Measured through Provimux |
   |---|---|---|
   | `openai-codex/gpt-6.2-sol` | 150 s, Codex CLI, 2026-11-24 | 0 s, plain requests, 2026-11-17 (3 of 10 checks hit) |
   | `openai-codex/gpt-6-astra` | 300 s, Codex CLI, 2026-11-23 | 7,200 s with a coding agent, 2026-11-11; 0 s with plain requests, 2026-11-17 |
   | `devin/kimi-k3` | 3,000 s, Devin CLI, 2026-11-23 | 0 s, plain requests, 2026-11-04 |
   | `anthropic/claude-opus-5-5`, long | 3,600 s, direct, 2026-11-17 | 600 s, plain requests asking `ttl: 1h`, 2026-11-04 |

   The lifetime through Provimux depends on the vendor, on what Provimux does to the request, and on what the client in front of it sends. So Provimux cannot state one number as "this account's cache lifetime". It can relay dated measurements, each with the client that made it.
3. **providers.dev has entries for the latest models only** (its editorial rules: `GET /api/v1/meta`, `exclusions`). Of the models its listings name, 4 of 15 on the Claude login, 18 of 60 on Devin and 8 of 11 on the Codex login have an entry [ran]. Every other model has no figure, and must show none.
4. **The map from an account to providers.dev exists twice, and the client's copy cannot know the model.** Provimux maps a login type to a listing (`internal/provimux/modellists/published.go:48-60`). pi-provimux maps an account to a provider in two tables of its own (`extensions/upstreams.ts:73-109`) and takes the model id from the name the client sees (`upstreamOf`, `:158-166`) [read]. For a name the owner set (`opus` for `claude-opus-5-5`), that id is the name, providers.dev has no such model, and pi-cache-keeper gets no data. With "serve only my names" his own names become the normal case. Only the gateway knows the model behind a name.

## 2. Who holds what today

| Who | Prices | Cache lifetimes | From |
|---|---|---|---|
| Provimux, Savings | list price per model, a day keeps the prices it saw | none | OpenRouter's list, at most 3 h old; its own table (empty) |
| Provimux, model lists (P3) | none | none | providers.dev listings: names, windows, output limits, modalities, gaps only |
| Console | Savings only | none | the gateway |
| pi-provimux | one per model id | none; it passes the route to others (`model-upstreams` events) | models.dev |
| pi-cache-keeper | Pi's own; providers.dev's where Pi has none | providers.dev's safe value, per route, and it acts on it (countdown, keep-warm, compaction) | providers.dev `/meta` and `/entries?provider=…`, at session start, at most daily |
| Any other client (Claude Code, Codex CLI, an editor) | its own | its own | not Provimux |

## 3. Whether, and for whom

| Reader | What it would do with the figures | Verdict |
|---|---|---|
| The console's user | Order the accounts in a pool: a subscription first, the cheaper key next, the route whose cache holds longer. See what a key charges before keeping a name. Works whatever client is used | **Yes.** The main reader |
| pi-cache-keeper, on a machine that has it | It needs no figure from Provimux: it reads providers.dev itself. It needs the **entry** of each route, which it gets wrong for the owner's own names (finding 4) | **Yes, the reference only** |
| pi-provimux, on a machine without pi-cache-keeper | It could write a lifetime into Pi's model (`promptCache`), and Pi's own warmer would then spend tokens on it. On a pooled name it does not know which account will answer; pi-cache-keeper handles that with the answered route and its checks, and pi-provimux would have to copy them | **No.** The answer for such a machine is to install pi-cache-keeper; pi-provimux's README says so |
| Other clients | Nothing: they ignore unknown fields | Console only |
| Provimux itself | Nothing. It never refreshes a cache and never picks a route by price | No |

So the honest reach is: **the console, and one small field for clients**. The field is worth building even on its own.

**pi-cache-keeper and the console can show different numbers, and that is allowed.** They read the same dataset at different times (Provimux every 6 hours, pi-cache-keeper daily), and the console prints measurements through Provimux where pi-cache-keeper uses the safe value. Both name providers.dev and the date. In Pi, pi-cache-keeper's value is the one in force, because it is the only one that acts; nothing of Provimux's overrides it. They cannot disagree on **which entry** a route is, because that comes from Provimux alone (section 5).

## 4. Which figures

All of one entry, relayed as providers.dev states them. Provimux selects and never computes: no lifetime is derived, no price converted, no two values merged.

| Figure | From the entry summary (`GET /api/v1/entries`) | Shown as |
|---|---|---|
| Input and output price | `price_in`, `price_out` | US dollars per million tokens |
| Cache read price | `pricing.rules`: `meter` `text.cache_read`, `operation` `set`, no `when` | the same |
| Cache write price | the rule for `text.cache_write` without `when`; and one per lifetime where `when` is `cache.retention_s eq N` | "cache write $5 (5 min), $8 (1 h)": the long-cache price is the row of the longer lifetime |
| Whose price, and its date | `safe.price.source`, `.date`, `.agreement` | "models.dev, 2026-11-24"; `differ` gets a warning mark |
| Whether it is charged | `pricing.billing`, `price_from` | `per_use`: the account's price. `reference`: "not billed per token; API list price for comparison", with the entry it is taken from. Otherwise no price |
| Other rates exist | any rule with another `when` (size tiers, fast mode, region); `pricing.coverage` | "base rates"; the entry's page has the rest |
| Safe lifetime, per profile | `cache_lifetime.short` and `.long`: `safe_s`, `basis`, `source`, `date` | with its basis: "measured with the Codex CLI, 2026-11-24", "documented" |
| Who measured the safe value | the items of `cache_lifetime.lifetimes` with `chosen: true`: `client_label`, `client_kind` | in the same line |
| Measured through Provimux | the items with `basis: measured` whose `client` is `cliproxyapi` ("Requests sent through Provimux") or `agent+cliproxyapi` ("A coding agent, through Provimux"): `lifetime_s`, `date`, `checks`, `hits`, `first_miss_s`, `parameter`, `value` | one line each, with label and date |
| Documented lifetime | `documented_s`, `documented_status` | only worded "documented"; never beside a measurement that contradicts it without saying so |
| Caching at all, and whether a lifetime can be asked for | `cache`, `cache_retention` | "unknown" is not "no" |

The two profiles are providers.dev's: **short** is what a request gets without asking, **long** what it gets when it asks for the longer cache.

**Which cache figure is the true one for a route through Provimux.** In this order, and the line always says which it is:

1. A measurement through Provimux, where one exists. Two can exist and differ (a coding agent, plain requests); both are shown, neither is picked.
2. Else the entry's safe value, with the client that gave it. On an API key that is a direct request, which is what Provimux forwards unless the request is translated. On a login it is the vendor's own client: shown as that, with "not measured through Provimux".
3. Else the documented lifetime, as documented.
4. Else nothing.

The owner's rules send new login measurements through the vendor's own client first and through his gateway last, and API keys never through a proxy. So the measurements of row 1 will age. Their dates are always shown, and nothing hides an old one behind a newer value of another kind.

**Never shown as fact.**

| Never | Because |
|---|---|
| A documented lifetime without the word, or one a measurement contradicts as if it stood | providers.dev measured otherwise |
| A reference price as what a subscription account costs | the entry is not paid per token (`price_from`) |
| The safe value of a login as the lifetime through Provimux | finding 2 |
| A lifetime of 0 as "does not cache" | 0 means no age at which every check hit; the hits are beside it |
| One price or one lifetime for a name several accounts serve | section 5 |
| A figure without its date and "providers.dev" | a price can be stale; the reader must be able to tell |
| A figure for a model matched by rewriting its id | Savings does that for an estimate and says so; a card line is read as a statement about the account |

Not included: the measured input window. The registry already holds windows, P3 fills their gaps, and a second window beside them on the card needs its own rule.

## 5. Per account, not per name

### 5.1 The reference

Every account that serves a model gets one optional field: the providers.dev entry it would be.

```json
{ "provider": "claude", "kind": "oauth", "priority": 5,
  "providers_dev": { "provider": "claude-subscription", "model": "claude-opus-5-5" } }
```

- `provider` comes from the table of 5.2: a constant of Provimux, applied to the account's provider, kind and host.
- `model` is the id the account sends upstream for this name: after aliases, namespaces and prefixes are resolved, and without a thinking suffix or a reasoning variant. Where the held listing of that provider names the id as an alias or a variant of another id, it is that id.
- It is `null` where the table has no row. It says "this is where the model would be", not "an entry exists": the latest-models rule of providers.dev decides that, and a reader that finds no entry has no data. It needs no request and is the same with the setting off or the network down.

A pooled name shows **nothing merged**. Its card already has one line per account in the order they are tried; each line gets its own figures. The name's header gets no price and no lifetime while more than one account serves it. With "which account answered" (`X-CPA-AUTH-KIND`, `-PROVIDER`, `-HOST`; `sdk/api/handlers/handlers_answered.go:52-56`), a client finds the account in the list and so its entry; no new header is needed.

### 5.2 The map

| Account | providers.dev provider | |
|---|---|---|
| Claude login | `claude-subscription` | exact |
| Claude key, no base URL or `api.anthropic.com` | `anthropic` | exact |
| Codex login | `openai-codex` | exact |
| Codex key, no base URL or `api.openai.com` | `openai` | exact |
| Copilot | `github-copilot` | exact; Copilot's ids are the entry's |
| Antigravity | `antigravity` | exact |
| Devin | `devin` | exact; the model is Devin's own id, without `devin/` and without the effort variant |
| Meta login | `meta-subscription` | exact |
| xAI key, `api.x.ai` | `xai` | exact |
| Gemini key, no base URL or `generativelanguage.googleapis.com` | `google` | exact |
| Vertex | `google-vertex` | exact |
| Any key whose base URL host is `openrouter.ai` | `openrouter` | exact; the model is OpenRouter's id without a routing suffix (`:nitro`, `:floor`) |
| A key whose host is `api.deepseek.com`, `api.moonshot.ai`, `api.mistral.ai`, `api.groq.com`, `api.cerebras.ai`, `api.fireworks.ai`, `api.together.ai` | the provider of that host | exact for the reference; no figures until the provider is in the request (6.1) |
| **Kimi login** | **empty** | Provimux's default login is on `kimi.com` (`internal/auth/kimi/kimi.go:31`), which providers.dev names `kimi-code-plan-cn`; `kimi.ai` is `kimi-code-plan-global`. P3 maps Kimi to `-global` by inference. Empty until the host of the login decides it |
| **xAI login** | **empty** | providers.dev's `xai` is the paid API; it has no entry for the login |
| **Meta key** (`meta-api-key`) | **empty** | on `api.meta.ai` it is either the paid API (`meta`) or the key a Muse Code subscription applies to (`meta-subscription`, by providers.dev's own description); the account does not say which [infer] |
| **AI Studio** | **empty** | requests go through a browser, not the paid API |
| A key on a host not in this table; a plugin's own models; a runtime-only credential | **empty** | not known, and never guessed from the model's name |

The model id is matched to an entry **exactly**: the id, or an id the provider's listing gives as its alias or variant. No dots for dashes, no dates removed.

One map then serves three readers: the console, pi-provimux and through it pi-cache-keeper. pi-provimux keeps its tables only for a gateway that does not send the field.

## 6. The request

### 6.1 What providers.dev offers today

`GET /api/v1/entries` returns entry summaries, filtered by `provider` (comma-separated), at most 200 a page. A summary already carries everything section 4 reads: `price_in`, `price_out`, `price_from`, `pricing`, `safe`, `cache_lifetime`, `cache`, `cache_retention` [read: OpenAPI 1.6.2, `EntrySummary`]. **The site's API needs no addition.**

Measured today [ran]:

| Request | Entries | On the wire (gzip) | Decoded |
|---|---|---|---|
| twelve providers: the fixed set of 6.2 without `openrouter`, with `kimi-code-plan-global` | 190, one page | 51 KB | 1.53 MB |
| `openrouter` | 400, two pages | 68 KB | 2.09 MB |
| the same request with `If-None-Match` | | `304`, no body | |

Each answer has a weak `ETag`, `X-Dataset-Version` and `X-Dataset-Generated-At`, and `cache-control: public, max-age=300`. What Provimux keeps of 190 entries is 149 KB as JSON [ran: a script that builds section 7.2's records from the answer].

### 6.2 What Provimux sends

```
GET https://providers.dev/api/v1/entries?provider=<the fixed set>&limit=200&offset=<0, 200, …>
User-Agent: provimux/<version>
If-None-Match: <the page's ETag, when one is held>
```

- **The fixed set** is a constant: `anthropic`, `antigravity`, `claude-subscription`, `devin`, `github-copilot`, `google`, `google-vertex`, `meta-subscription`, `openai`, `openai-codex`, `openrouter`, `xai`. It does not depend on the accounts a user has, so the request is the same for every user and tells the server nothing about him (the rule of `model-lists.md` section 7). About 580 entries today, three pages, about 120 KB on the wire on a day the dataset changed.
- **When:** in the round that fetches the listings (`sdk/cliproxy/provimux_published_lists.go:37-44`): at start in the background, every 6 hours, on `POST /v8/management/models/refresh`; after a failure 10 minutes, then 1 hour. Pages are asked one after another until `next_offset` is null, at most 10.
- **A fetched document replaces the held one only if** every page is a `200` or a `304`, well-formed, at most 8 MB, all pages name one `dataset_version`, the pages together hold `total` entries, and `generated_at` is not before the held document's. Anything else is a failed fetch: the held document stays, and the failure is recorded as a code. An entry Provimux cannot read is left out alone.
- **Disk:** `model-lists/entries.json` beside `published.json`: the records of section 7.2 with the dataset version, the dates and each page's `ETag`. Mode `0600`, temporary file and rename, as `published.json` is written (`internal/provimux/modellists/published.go:387`). A damaged or foreign file is ignored with one warning and replaced by the next accepted document.
- **Start:** never waits. The disk copy is read when the model lists' folder is opened; a start without network shows the figures of the last run, with their dates.
- **Unreachable:** the held document stays, however old. With nothing held there are no figures; the references of 5.1 are unchanged. No list, no route and no request to a vendor depends on any of it.
- **Off:** `models.published-lists: false` already means "Provimux never contacts providers.dev" (`internal/registry/catalog_config.go:21-24`). It turns this request off too, and the figures endpoint answers `off`. The references stay: they come from no request. Off in a test binary and in Home mode, as P3 is.
- **Through the configured proxy,** within 30 seconds a page, like the listings request.

Requests to AI vendors are not touched: no executor, translator or auth file changes.

## 7. The contracts

Both grow existing answers or add a path. Nothing is removed or redefined. Contract file: `pui13/openapi.yaml`, extended in step E1 and E2.

### 7.1 The reference, on both account lists

`GET /v1/models?include=accounts` (`sdk/api/handlers/openai/openai_models_accounts.go:15-23`) and `GET /v8/management/provimux/model-routes` (`internal/api/handlers/management/provimux_model_routes.go:71-81`): each account or route gains

```json
"providers_dev": { "provider": "openai-codex", "model": "gpt-6.2-sol" }
```

or `"providers_dev": null`. On `/v1/models` the key is left out when null, as `host` is. `/v1/models` without `include=accounts` stays byte for byte what it is.

What it adds for a holder of a client key: the model behind a name. Today the list gives the provider, the kind and the host of each account, and nothing that identifies one; the id behind an alias is new. Decision 6.

### 7.2 The figures: `GET /v8/management/provimux/published-entries`

Read-only, behind the management key, registered in `internal/api/server_provimux.go`. It reports what is held and predicts nothing. It returns the entries that a route of an enabled account refers to; `?all=true` returns every held entry.

```json
{
  "observed_at": "2026-11-25T12:04:11Z",
  "options": { "published_lists": true },
  "document": {
    "state": "held",
    "dataset_version": "89105175cb0af1aa",
    "generated_at": "2026-11-25T09:49:05Z",
    "fetched_at": "2026-11-25T12:00:03Z",
    "from_cache": false,
    "last_failure": null,
    "entries": 584
  },
  "entries": [
    {
      "provider": "openai-codex",
      "model": "gpt-6.2-sol",
      "url": "https://providers.dev/entries/openai-codex/gpt-6.2-sol/",
      "access": "subscription",
      "verified": "2026-11-24",
      "price": {
        "billing": "reference",
        "reference": { "provider": "openai", "model": "gpt-6.2-sol" },
        "input": 2, "output": 10, "cache_read": 0.1,
        "cache_write": null,
        "cache_write_by_lifetime": [ { "lifetime_s": 1800, "price": 2.5 }, { "lifetime_s": 43200, "price": 5 } ],
        "other_rates": true,
        "source": "models.dev", "date": "2026-11-24", "agreement": "single"
      },
      "cache": {
        "documented": "yes",
        "retention": "unknown",
        "short": {
          "safe_s": 150, "basis": "measured", "source": "providers.dev", "date": "2026-11-24",
          "measured_with": [ { "client": "codex-cli", "label": "OpenAI's Codex CLI", "kind": "own_client" } ],
          "documented_s": null, "documented_status": null,
          "through_provimux": [
            { "client": "cliproxyapi", "label": "Requests sent through Provimux", "lifetime_s": 0,
              "date": "2026-11-17", "checks": 10, "hits": 3, "first_miss_s": 150, "asked": null }
          ]
        },
        "long": {
          "safe_s": null, "basis": "none", "source": null, "date": null, "measured_with": [],
          "documented_s": null, "documented_status": null, "through_provimux": []
        }
      }
    }
  ]
}
```

The figures in the example are today's answer for that entry [ran].

| Field | Meaning |
|---|---|
| `document.state` | `held`, `none` (never fetched and nothing on disk) or `off`. Clients treat another value as "no figures" |
| `document.last_failure` | `{at, category, http_status}`: codes only, as `model-lists` reports them |
| `entries[].provider`, `.model` | the key a route's `providers_dev` is joined on |
| `price.billing` | `per_use`, `reference`, or another value: then every price is `null` |
| `price.*` | US dollars per million tokens; `null` = providers.dev states none |
| `price.other_rates` | rates this record does not carry exist (tiers by size, fast mode, region) |
| `cache.short`, `.long` | section 4, as providers.dev states them; `asked` is `{parameter, value}` of the request field that asked for the lifetime, or `null` |
| every key | always present: `null` for unknown, `[]` for none |

An older Provimux answers `404`: the console shows no figures. An older console ignores the new field of `model-routes`.

## 8. Trust and wording

The figures are providers.dev's, dated, and none is Provimux's own claim.

**In the console, on an account's line of a name's card** (the look is the designer's):

| Case | Words |
|---|---|
| A key | "$4 in, $20 out per million tokens · cache read $0.20 · cache write $5 (5 min), $8 (1 h)" and under it "providers.dev, from models.dev, 2026-11-24 · base rates" |
| A subscription | "Not billed per token. API list price for comparison: $2 in, $10 out (openai/gpt-6.2-sol)" |
| Measured through Provimux | "Cache through Provimux: 1 h (measured 2026-11-17, plain requests)". A second line for a coding agent where one exists |
| Only the safe value | "Cache: 2 min 30 s with OpenAI's Codex CLI (measured 2026-11-24). Not measured through Provimux" |
| Measured 0 | "No cache hit held through Provimux (3 of 10 checks hit, 2026-11-17)" |
| Documented only | "Cache: 5 min, documented, not measured" |
| No entry | "providers.dev has no entry for this model" (once per card, small), or nothing |
| Not mapped | nothing |
| Old | any date more than 90 days back (providers.dev's own stale threshold today) is marked "old" |
| Sources disagree | a warning mark and "sources disagree on this price", with the link |

Every line links to the entry's page on providers.dev. A line at the top of the page's settings says "Prices and cache lifetimes: providers.dev dataset of 2026-11-25, fetched 2 hours ago", or "providers.dev was not reached since …; figures are of …", or that they are off. Savings keeps its own line ("OpenRouter's public list of …"): the two pages state different things (what tokens would have cost at list price; what an account charges) and each names its source.

**In the log:** `prices and cache lifetimes from providers.dev: dataset 89105175… of 2026-11-25, 584 entries`, and the failure line of the listings with this name.

**"Advertise", at most.** One sentence in Provimux's README and on its page of the site: "Per account behind a model name, the console shows providers.dev's list price and the cache lifetime providers.dev measured, with dates." And for clients: "`/v1/models?include=accounts` names each account's entry on providers.dev." Never: that Provimux knows what a request costs, that it guarantees a cache lifetime, or a figure in a place where a client would take it for the gateway's own (a `pricing` or a cache field on a model of `/v1/models`).

## 9. The smallest build

### 9.1 Provimux

Layer commits only (trailer `Provimux-Layer: branding`). No protected path (`guard.sh:33`).

**New files (no conflict possible).**

| File | Holds |
|---|---|
| `internal/provimux/modellists/entries.go` | the request of 6.2, the acceptance rule, the record of 7.2 as a Go type, `entries.json` |
| `internal/provimux/modellists/entry_ref.go` | the map of 5.2 |
| `sdk/cliproxy/provimux_entry_refs.go` | the model an account sends upstream for a registered name; the reference of 5.1 |
| `sdk/cliproxy/provimux_published_entries.go` | the held document, its place in the listings round, the state source for the endpoint |
| `internal/api/handlers/management/provimux_published_entries.go` | the endpoint of 7.2 |
| their `_test.go` files; `pui13/examples/published-entries.json` | section 9.4 |

Signatures the steps build to:

```go
// internal/provimux/modellists

// EntryRef names an entry of providers.dev: a provider and a model id at it.
type EntryRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// EntryProvider returns the providers.dev provider of an account by its provider, kind ("oauth" or
// "apikey") and the host of its base URL, or "" where the map of prices-and-cache.md 5.2 has no row.
func EntryProvider(provider, kind, host string) string

// EntryProviders returns the fixed set the entries request asks for, sorted.
func EntryProviders() []string

// EntriesRequestURL is one page of the request Provimux sends, built on base.
func EntriesRequestURL(base string, offset int) string

// Entry is what Provimux keeps of one entry summary: prices-and-cache.md 7.2, entries[].
type Entry struct { /* Provider, Model, Access, Verified, Price, Cache */ }

// EntriesDocument is an accepted answer, as held in memory and written to entries.json.
type EntriesDocument struct { /* Schema, ETags, DatasetVersion, GeneratedAt, FetchedAt, Entries */ }

// FetchEntries asks every page. A result with NotModified keeps the held document.
func FetchEntries(ctx context.Context, client *http.Client, base string, held *EntriesDocument, userAgent string, now time.Time) (EntriesResult, error)

func (s *Store) ReadEntries() (*EntriesDocument, error)
func (s *Store) WriteEntries(doc *EntriesDocument) error

// sdk/cliproxy

// provimuxEntryRefOf returns the providers.dev entry an account's registered model would be, or nil.
func provimuxEntryRefOf(auth *coreauth.Auth, model *registry.ModelInfo) *modellists.EntryRef
```

Each body is a worker's. `provimuxEntryRefOf` is the one function with a question in it: the upstream id of a registered name sits in different fields by route (`SourceModelID`, `MetadataModelID`, `OAuthAliasSource` of `registry.ModelInfo`, `internal/registry/model_registry.go:43-51`, and the `name` of a key's `models` row) [read; which field per route is not verified]. Its test is the proof: for every route of a fixture config, the function's model equals the `Model` of the usage record a request through that route produces, which is the id Savings already prices by (`internal/provimux/savings/record.go:26-31`).

**Edits to existing files: additions only.**

| File | Edit | Lines |
|---|---|---|
| `sdk/api/handlers/openai/openai_models_accounts.go` | the field on `modelAccount`, filled in `modelAccounts` through a function variable `sdk/cliproxy` sets (this package cannot import it) | +6 |
| `internal/api/handlers/management/provimux_model_routes.go` | the field on `provimuxRoute`, filled the same way | +5 |
| `sdk/cliproxy/provimux_published_lists.go` | one call in `refreshPublishedLists`, one in `loadCache` | +2 |
| `internal/api/server_provimux.go` | the route | +1 |
| `.github/provimux/design/pui13/openapi.yaml` | the field, the path, the schemas, the example | text |
| README (the part "the requests Provimux sends"), `config.example.yaml` | the second request; what `published-lists: false` now also turns off | text |

None of the four code files is upstream's: three are the layer's own, and `openai_models_accounts.go` is the owner's patch file, edited by a layer commit as `live_model_lists.go` was.

### 9.2 The console (`provimux-ui`)

| File | Change |
|---|---|
| `src/provimux/api/types.ts`, `api/normalize.ts`, `api/provimuxApi.ts` | `providers_dev` on `ProvimuxModelRoute`; `ProvimuxPublishedEntriesResponse`; one read, on page load and on refresh, not in the 30-second poll |
| `pages/models/catalog.ts` | `RouteLine` gains the joined entry |
| `pages/models/NameCard.tsx` | the two lines of section 8 on a route's line, and the details |
| `pages/models/ModelsSettings.tsx` | the status line; it also puts the unused published-lists strings (`en.json:613-618`) back to use |
| `locales/*.json` | the strings, in all seven languages |
| `tests/provimux/` | the join, each case of section 8's table, a gateway without the path, a document `off`, `none` and stale |

### 9.3 pi-provimux and pi-cache-keeper

| Repo | Change |
|---|---|
| pi-provimux | `parseAccounts` (`extensions/sources.ts:30-42`) keeps `providers_dev` when both parts are strings. `upstreamOf` (`extensions/upstreams.ts:158-166`) then gives the upstream an `entry: {provider, model}` and takes its `model` from it. Without the field (an older gateway, plain CLIProxyAPI) everything is as today. README: one paragraph, "for cache lifetimes that Pi acts on, install pi-cache-keeper" |
| pi-cache-keeper | an upstream with `entry` is looked up under exactly that provider and model, before its own mapping (`lookupsFor`, `model-data.ts:321-338`); `neededProviders` asks for that provider. Without `entry`, as today |

The event stays `model-upstreams`; `entry` is one optional key on an upstream, ignored by a reader that does not know it.

### 9.4 Tests that must hold

| Step | Must hold afterwards |
|---|---|
| E1 | every row of 5.2's table, each empty row included; the usage-record test of 9.1 for a login, a key, an alias, a prefixed account, a Devin variant, an OpenRouter id with a routing suffix; `/v1/models` without `include` is byte-identical to `e8b6b74d3`; both endpoints give the same reference for the same account (`test/provimux/model_routes_parity_test.go` grows) |
| E2 | a test server: `200` on three pages, `304`, `500`, a cut-off body, pages of two dataset versions, an older document, an entry with no rules, an entry with a rule the reader does not know; the request carries no parameter beyond `provider`, `limit`, `offset` and no header beyond the two; nothing is fetched with the option off or in a test binary; a start with the server down serves the disk copy; the example validates against the OpenAPI |
| C1 | the console's own tests of 9.2; `bun run verify`; the page checked against a stand-in gateway |
| X1 | pi-provimux: an account with the field, without it, with a malformed one; the event's `entry`. pi-cache-keeper: an upstream with `entry` finds the entry under a name that is not a model id |

## 10. Fit with what is being built

**"Only my names"** (branch `only-my-names`: N1 and N2 built, N3 the console in work).

- No rule of it changes. It filters in front of the registry; the references and the figures are read from the registry, so a name that is not served has no route and no line.
- It makes this design matter more: once only his names are served, every name Pi sees is an alias, and without the reference pi-cache-keeper has data for none of them (finding 4).
- Shared lines: `internal/api/server_provimux.go` (each adds one route), `pui13/openapi.yaml` (each adds a path), the console's `NameCard.tsx`, `catalog.ts` and the seven locale files. So this lands **after** N3, on top of it. `catalog_config.go` is not touched here.
- Later, not in this build: the accounts of `/provimux/names`' `others` could carry `providers_dev` too, so the Keep preview can say "paid per token, $2 in". It is growth of that answer.

**Savings** stays on OpenRouter's list. A day keeps the prices it was valued with, and its question is what the tokens would have cost at list price, for every model, also ones providers.dev does not list. The card's price is the entry's, for the account's own provider. Both name their source.

**P3 (listings)** is unchanged. Its Kimi row is the one open point this design found (5.2).

## 11. The steps

S: up to a day of one worker. M: one to two days.

| Step | Repo | Size | Holds | Depends on | Ships alone |
|---|---|---|---|---|---|
| E1 | Provimux | M | the map, the upstream id, `providers_dev` on both account lists, the contract. No request | | yes: clients and the console's link gain at once |
| X1 | pi-provimux, pi-cache-keeper | S each | section 9.3 | E1's contract; can be built against a fixture | yes, with any gateway |
| E2 | Provimux | M | the entries request, the disk copy, the endpoint, the README part | E1 | yes: nothing shows until the console reads it |
| C1 | provimux-ui | M | section 9.2 | E2's contract and a stand-in gateway; N3 merged | with E2 released |
| Review | each | S | one reviewer for E1 and E2 (a figure for a wrong account; the request's privacy; start without network; the sync check no worse than upstream), one for C1 (every line of section 8's "never" table) | | |
| Release | Provimux, the console, the two extensions | | E1 with the next `-pmx` release; X1 after it; E2 and C1 together | | |
| W1 | the site, the READMEs | S | the sentences of section 8, after the owner's word on them | C1 released | |

**Recommendation: do E1 now, right after "only my names"; E2 and C1 next.** The first step is E1. If only one thing is built, it is E1 with X1: it costs no request, and it repairs data pi-cache-keeper already shows.

## 12. Decisions that are the owner's or the chief's

| # | Decision | Recommendation | The other way costs |
|---|---|---|---|
| 1 | Build it, and in which shape | **The reference for clients, the figures in the console.** No figure on `/v1/models`, nothing that acts | Figures on `/v1/models` have one possible reader, pi-provimux, which should not act on them (section 3); they can be added later as growth |
| 2 | Which cache figure a route's line leads with | **A measurement through Provimux where one exists, the safe value named by its client beside it** | The safe value alone is one number and simpler, and for 16 logins today it is not what a request through Provimux gets |
| 3 | The switch | **`models.published-lists` turns both requests off** | A second key lets a user keep lists and refuse figures; one more setting to explain for one host |
| 4 | OpenRouter in the request | **Yes**: 400 entries, 85 of them with a measured lifetime, two more pages a day | Without it the paid route most users have shows a price only, from the list Provimux already holds, under a second label |
| 5 | Kimi, the xAI login, AI Studio, a Meta key | **Empty**, and P3's Kimi row is checked against the login's host | A guess shows another plan's figures as this account's |
| 6 | The model behind a name becomes visible to a holder of a client key, with `include=accounts` | **Accept**: it is asked for explicitly, and the list already gives provider, kind and host | A setting to hide it; then Pi gets no data for his own names, which is the point of E1 |
| 7 | pi-provimux writes no cache lifetime into Pi; the README points to pi-cache-keeper | **Yes** | A second, simpler keeper inside pi-provimux that warms a cache on the wrong account's lifetime in a pool |
| 8 | Savings' prices | **Stay OpenRouter's** | Moving them to providers.dev leaves every model it does not list unpriced |
| 9 | The public sentences of section 8 | the owner's words; the draft is there | |

Not asked, worth knowing: pi-cache-keeper uses the safe value on a route through Provimux, and says so in its README. Finding 2 puts numbers on it for Codex and Devin logins, in both directions. Whether it should prefer a measurement made by a coding agent through Provimux is a question for that extension, not for this design.

## 13. What I ran, and what I did not verify

| Check | Result |
|---|---|
| `GET /api/v1/openapi.json` | version 1.6.2; `EntrySummary` carries prices, `pricing`, `safe` and `cache_lifetime`; `/entries` takes `provider` and at most 200 a page; the listings carry no price or cache field |
| `GET /api/v1/entries?provider=<twelve providers>&limit=200`, plain and with `If-None-Match` | 190 entries, one page, 51 KB gzip, 1.53 MB decoded; `304` on the second. `https://providers.dev/entries/<provider>/<model>/` is the entry's page (200) |
| `GET /api/v1/entries?provider=openrouter`, two pages | 400 entries, 68 KB gzip; 290 with a price; 85 short lifetimes measured directly, 9 items through Provimux |
| A script over the 190 summaries | 164 have a price, 137 a cache read price, 55 a cache write price, 119 have other rates, 8 have sources that disagree on the price; 72 have a safe lifetime in a profile, 34 a measurement through Provimux, 17 of those differ from the safe value (16 logins); `price_in` equals the base input rule on all 190; no profile is marked contradicted today; the kept records are 149 KB |
| `GET /api/v1/listings?view=full&limit=40&provider=<the same twelve>` | 10 listings; how many rows have an `entry` (finding 3) |
| `GET /api/v1/meta` | the editorial rules that leave older models out; `stale_after_days` 90 |
| Read: Provimux's account list, model routes, the listings client and its round, Savings' prices; "only my names" as built; pi-provimux's tables and `upstreamOf`; pi-cache-keeper's reader | sections 1, 2, 9, 10 |

**Not verified.**

| Point | Who settles it |
|---|---|
| Which field of `registry.ModelInfo` holds the upstream id on each kind of route | E1's usage-record test |
| That a page's `ETag` changes whenever any entry on it changes | E2: the document is also checked by `dataset_version` on every page |
| That the two client ids stay the only ones that mean "through Provimux" | E2 keeps them as a constant; a new id on providers.dev's side is one line here. If a third appears, a marker on the client in providers.dev's API is the growth to ask for |
| Which host a Kimi login of the owner uses; whether a Meta key can be told from a Muse Code subscription's key | decision 5 |
| Nothing was built or run against a gateway | every step's tests |
