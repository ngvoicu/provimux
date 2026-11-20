# Prices: where they come from, which model gets which, and the formulas

For the worker who builds the backend. A price is five numbers in US dollars per million tokens: input, output, cache read, cache write, 1 hour cache write; the last three may be missing. Nothing in CLIProxyAPI or the layer holds a price today (`internal/registry/model_registry.go:40-85`, `internal/api/handlers/management/provimux_model_routes.go:38-39`). [read]

## 1. Two sources

| Order | Source | Covers |
|---|---|---|
| 1 | Provimux's table, `internal/provimux/savings/prices.json`, in the binary | models OpenRouter does not list, and names OpenRouter lists under another id |
| 2 | OpenRouter's public list, `https://openrouter.ai/api/v1/models` | everything else |

The table is asked first so that it can correct a match; it stays small.

## 2. OpenRouter's list

### What exists

`internal/registry/openrouter_models.go` downloads the list (`:24`, `:174-199`) and keeps the context window, the completion limit and the reasoning efforts (`:29-36`, `:201-235`); `pricing` is read past. The file is the owner's OpenRouter patch (`2dcfc828`, `a7205981`, `053444f0`): below the layer, not upstream's, not guarded. [read]

**It is not downloaded on every installation.** The download starts only when something looks a model up (`lookup`, `:103-123`), which today is an OpenRouter entry registering its models; it is repeated when the copy is older than 3 hours (`internal/registry/model_updater.go:17`), retried no sooner than a minute after a failure (`:114`), and never happens under `--local-model` or home mode (`remoteCatalogPolicy`, `:58-68`). A Provimux with no OpenRouter entry never asks openrouter.ai. The brief's "the fetch Provimux already does" holds only for installations that have such an entry.

### What the list carries

[ran] The live list, 2026-11-20: 477 models. `pricing` values are decimal strings in dollars per token.

| Field | Models that have it | Kept as |
|---|---|---|
| `pricing.prompt`, `pricing.completion` | 477 | input, output |
| `pricing.input_cache_read` | 341 | cache read |
| `pricing.input_cache_write` | 117 | cache write |
| `pricing.input_cache_write_1h` | 36 | 1 hour cache write |
| `pricing.overrides` (other prices above a prompt size) | 88 | not kept, section 7 |

8 entries have a negative price (routers such as `openrouter/auto`). Variants are separate ids: 92 end in `:batch`, 12 in `:free`.

A model is kept when `prompt` and `completion` both parse and neither is negative (469 of the 477); zero is a price (`:free`). Each of the three cache fields is kept when it parses and is not negative, else it is missing. Values are multiplied by 1,000,000.

### The seam: one line, one new file

```go
// internal/registry/openrouter_models.go, fetchOpenRouterModels, before its last line (:198)
	provimuxKeepOpenRouterPrices(data) // <- the one added line
	return parseOpenRouterModels(data)
```

```go
// internal/registry/provimux_openrouter_prices.go (new file, package registry)

// ProvimuxOpenRouterPrice is one model's list price at OpenRouter, in US dollars per million tokens.
// CacheRead, CacheWrite and CacheWrite1h are nil when the list publishes none.
type ProvimuxOpenRouterPrice struct {
	Input, Output                       float64
	CacheRead, CacheWrite, CacheWrite1h *float64
}

// provimuxKeepOpenRouterPrices keeps the prices of a downloaded catalog and the time. Called by
// fetchOpenRouterModels with the bytes it read. A body that is not the catalog, or that prices no
// model, leaves the previous prices in place.
func provimuxKeepOpenRouterPrices(data []byte)

// ProvimuxOpenRouterPrices returns the prices of the last downloaded catalog, keyed by lower-case
// model id, and when it was downloaded; nil and the zero time when none was in this process.
// remote is false when the catalog may not be downloaded (--local-model, home mode).
// It asks the catalog for a refresh under the catalog's own rules: in the background, when its
// copy is older than ModelsRefreshInterval, never when remote is false. It never blocks.
func ProvimuxOpenRouterPrices() (prices map[string]ProvimuxOpenRouterPrice, fetchedAt time.Time, remote bool)

// ProvimuxSetOpenRouterPricesForTest installs prices for a test and returns the function that
// restores the previous ones. It starts no download.
func ProvimuxSetOpenRouterPricesForTest(prices map[string]ProvimuxOpenRouterPrice, fetchedAt time.Time) func()
```

- The refresh is asked for with `openRouterModels.lookup("", "")`: an empty provider registers no interest (`:106-111`) and an empty id finds nothing, so the call has no effect beyond starting the catalog's own background refresh when it is due (`:113-120`). No second download, no second timer, no second policy.
- The prices are parsed from the same bytes, in the new file. `OpenRouterModel`, `parseOpenRouterModels` and `sameOpenRouterModels` do not change, so a price change does not make OpenRouter entries register their models again (`:137-151`).
- `parseOpenRouterModels` drops a model that has neither limits nor efforts (`:231-233`); the price parser has its own loop and keeps it.
- The alternative without a line in the patch file is a second download inside the savings package: the same megabyte fetched twice, with a copy of the policy to keep in step. Not taken. Who owns the one line is open question 4 in `README.md`.

### When Provimux asks for it

The saver calls `ProvimuxOpenRouterPrices()` in every round in which it has a day to write (not at `Start`: the build dropped that call, see `README.md`, "What the build changed"). So the list is downloaded at most every 3 hours, and only while requests are being recorded; after a failed download the catalog tries again no sooner than a minute later (`:114`). This makes a Provimux without an OpenRouter entry contact openrouter.ai, which it does not do today: a public list, no key, nothing about the installation in the request. It is what the owner's decision asks for, and it is new behaviour; open question 3 in `README.md`.

### The saved copy

When the downloaded list differs from the saved one, the saver writes it to `provimux:savings:v1:prices:openrouter`:

```json
{ "v": 1, "fetched_at": "2026-11-20T07:02:44.130Z",
  "models": { "anthropic/claude-opus-5.5": { "in": 4, "out": 20, "cr": 0.2, "cw": 5, "cw1h": 8 } } }
```

About 30 KB for 469 models [ran: 28,294 B], rewritten only on a change of a price or of the set of models. It is read when the saver first reaches the store. The list in use is the one with the later `fetched_at` of the downloaded and the saved; the response reports it (`prices.openrouter.fetched_at`, `.models`).

### Without the list

| Situation | Prices in use | The response |
|---|---|---|
| normal | downloaded list, at most 3 hours old | `fetched_at` recent, `remote: true` |
| the download fails (offline, DNS) | the saved copy, however old | `fetched_at` old, `remote: true`; the page says from when the prices are |
| `--local-model` after earlier normal runs | the saved copy | `fetched_at` old, `remote: false` |
| `--local-model` from the first run, or the store off and no download yet | Provimux's table only | `fetched_at: null`, `models: 0`; every model that is not a `price` entry of the table is unpriced |

Unpriced is not lost: requests and tokens are recorded all the same, and a day's rows that got no price are valued with the list of the day they are read (section 5).

## 3. Provimux's table

`internal/provimux/savings/prices.json`, embedded with `go:embed`, checked against `schemas/prices-table.schema.json` by a test. `examples/prices-table.json` shows the format with a model that does not exist.

```json
{ "v": 1, "updated": "2026-11-20",
  "models": [
    { "provider": "devin", "model": "<id as recorded>", "price": { "in": 1.5, "out": 6, "cr": 0.15 }, "source": "https://..." },
    { "provider": "*", "model": "<id as recorded>", "same_as": "<an OpenRouter id>", "source": "https://..." }
  ] }
```

- An entry has a `price` or a `same_as`, never both. `price`: `in` and `out` required, `cr`, `cw`, `cw1h` optional. `same_as`: the model is valued at that OpenRouter entry's price, whatever it is on the day.
- `provider` is a provider as recorded, or `*`. An entry naming the provider wins over `*`. A provider and model pair appears once.
- **`source` is required**: the public page that states the price, or that states the model is the one in `same_as`. No source, no entry: a model nobody publishes a price for stays unpriced, and the page says so.
- A change of the table ships with a release. `updated` is the day it was last checked.
- The price id of an entry with a `price` (a row's `pm`, `models[].price.id`) is `provimux:<provider>/<model>` of the entry, e.g. `provimux:devin/swe-2`; OpenRouter ids never start with `provimux:`. An entry with `same_as` has the OpenRouter id as its price id.

**What goes in at first.** No price is invented in this design. The worker fills entries only where a public source exists, and the reviewer opens each `source`. [ran] Against the live list, these ids of the static catalogs find no price by section 4 and are the candidates:

| Provider | Ids |
|---|---|
| devin (`devin_models.json`: 39 of its 52 ids resolve) | `swe-2`, `swe-2-flash`, `swe-2-lightning`, `swe-1-7`, `swe-1-6`, and Devin's names for other makers' models that section 4 does not resolve: `claude-5-fable`, `claude-opus-4-6-1m`, `claude-sonnet-4-6-1m`, `glm-5-2-1m`, `kimi-k2-7`, `muse-wonder-1-0`, `nemotron-3-5-super`, `nemotron-3-ultra` |
| codex | `gpt-6-chat-thinking`, `codex-auto-review`, `gpt-reserve` |
| claude | `claude-opus-4-20250514`, `claude-sonnet-4-20250514`, `claude-3-7-sonnet-20250219`, `claude-3-5-haiku-20241022` (no longer in OpenRouter's list) |
| gemini, vertex, aistudio, antigravity | `gemini-3-pro-preview`, `gemini-3-pro`, `gemini-3-flash`, `gemini-3.1-pro`, `gemini-pro-latest`, `gemini-flash-latest`, `gemini-flash-lite-latest`, `gemini-3.8-flash-tiered`, the `imagen-*` models |
| kimi | `kimi-k2.8`, `kimi-k2.8-code`, `kimi-k3-256k`, `kimi-k2.7-code-highspeed`, `kimi-k3.1-pro-highspeed` |
| xai | `grok-4.7-build-fast`, `grok-build-0.1`, `grok-4.20-0309-reasoning`, `grok-4.20-0309-non-reasoning`, `grok-4.20-multi-agent-0309`, `grok-3-mini`, `grok-3-mini-fast`, `grok-composer-2.5-fast`, the `grok-imagine-*` models |
| meta | `muse-ember-1.0`, `muse-wonder-1.0`, `muse-wonder-full-1.0` and their `-contributor` forms |

An empty `models` list is a valid first table.

## 4. From a recorded model to a price

Input: the row's provider and model, both lower case (`01-recording.md` section 2). The model is the upstream id, after aliases and prefixes were resolved and without a thinking suffix: `claude-opus-5-5`, `gpt-6.2-sol`, `anthropic/claude-sonnet-5.5`, `gemini-3.8-flash-high`. The client's own name for it (`Record.Alias`) plays no part. Output: a price, its source, the id it came from and how it was matched; or nothing (unpriced). The first step that answers ends the search.

| Step | Rule | `match` |
|---|---|---|
| 1 | The table has an entry for (provider, model), else for (`*`, model). A `price` is the answer. A `same_as` is answered with that id's price from OpenRouter's list; if the list lacks the id the model is unpriced, and no later step is tried | `table` |
| 2 | OpenRouter's list has the model as an id | `exact` |
| 3 | The model contains `:` and the list has the part before the last `:` (a routing suffix such as `:nitro` or `:floor`; `:free` and `:batch` are ids of their own and match in step 2) | `variant` |
| 4 | Rewriting, below | `normalized` |
| 5 | Unpriced | |

**Step 4.** Take the model without its `:` suffix, and of that the part after the last `/`. Call it `n`.

1. Remove a date at the end: `-` and 8 digits, or `-YYYY-MM-DD`.
2. Find the maker by the first prefix of `n` in this table; none means unpriced.

   | `n` starts with | Maker |
   |---|---|
   | `claude-` | `anthropic` |
   | `gpt-`, `o1`, `o3`, `o4`, `codex-`, `chatgpt-` | `openai` |
   | `gemini-`, `gemma-` | `google` |
   | `grok-` | `x-ai` |
   | `kimi-` | `moonshotai` |
   | `glm-` | `z-ai` |
   | `deepseek-` | `deepseek` |
   | `qwen` | `qwen` |
   | `muse-` | `meta` |

3. Candidates, in order: `n`; `n` without one effort suffix at its end (`-thinking`, `-xhigh`, `-high`, `-medium`, `-low`, `-minimal`, `-none`); that without one more.
4. For each candidate `c`, try `<maker>/c`, then `<maker>/dotted(c)`. `dotted` turns a `-` into a `.` where a digit stands before it and one or two digits follow that end the name or are followed by `-`: `claude-opus-4-5` becomes `claude-opus-4.5`, `claude-3-7-sonnet` `claude-3.7-sonnet`, `gpt-5-6-sol` `gpt-5.6-sol`, `kimi-k3-1-pro` `kimi-k3.1-pro`; `grok-4.20-0309-reasoning` is unchanged.

Only effort suffixes are removed. `-fast`, `-highspeed`, `-preview`, `-latest`, `-1m` name other products or other prices, so such a model is unpriced until the table says what it is. The repository forbids the standard `regexp` (AGENTS.md); these are plain string scans.

| Provider | Recorded model | Price id | `match` |
|---|---|---|---|
| openrouter | `anthropic/claude-sonnet-5.5` | `anthropic/claude-sonnet-5.5` | `exact` |
| openrouter | `openai/gpt-5.6-sol:floor` | `openai/gpt-5.6-sol` | `variant` |
| claude | `claude-opus-5-5` | `anthropic/claude-opus-5.5` | `normalized` |
| claude | `claude-opus-4-5-20251101` | `anthropic/claude-opus-4.5` | `normalized` |
| codex | `gpt-6.2-sol` | `openai/gpt-6.2-sol` | `normalized` |
| antigravity | `claude-opus-5-5-high` | `anthropic/claude-opus-5.5` | `normalized` |
| antigravity | `gemini-4-hydrogen-xhigh` | `google/gemini-4-hydrogen` | `normalized` |
| devin | `gpt-5-6-sol` | `openai/gpt-5.6-sol` | `normalized` |
| xai | `grok-4.7-build-fast` | none | unpriced |
| openrouter | `openrouter/auto` | none (negative price) | unpriced |
| devin | `swe-2` | none | unpriced, until the table has it |

[ran] A prototype of these steps against the live list resolves 130 of the 182 ids in `internal/registry/models/models.json`; I read all 130 pairs and each joins the same model name on both sides. The 52 it leaves are the candidates of section 3.

A model served by a subscription of another company (Claude through Devin or Antigravity) is valued at its maker's list price: the question the page answers is what these tokens would have cost on an API key.

A match by rewriting can be wrong if a maker reuses a name. The response therefore gives, per model, the price id and the kind of match (`models[].price.id`, `.match`), and the page shows them.

## 5. A day keeps the prices it was valued with

Only tokens are stored, so the dollars are computed when the page asks. If they were computed with today's list, last March's total would move whenever a price changes, and the owner's "saved $X in March" would not be a number that stays.

So each day's value carries a `prices` map and each row a `pm` (the price id) and `pmm` (the match). When the saver writes a day (`01-recording.md` section 5, step 2), every row without `pm` is resolved with the prices then in use; on a hit the row gets `pm` and `pmm`, and the day's `prices` gets the price under that id unless it already has one. Once set, neither is changed: a day is valued with the first price it saw for an id.

When a range is read:

| Row | Valued with | Counted as |
|---|---|---|
| has `pm` | the day's `prices[pm]` | priced; `models[].price.basis` `recorded` |
| has no `pm`, and resolves today | today's price | priced, and in `estimate.floating_requests`; basis `current` |
| has no `pm`, and does not resolve | nothing | `estimate.unpriced_requests`, `unpriced_tokens`; `models[].price` null |

So a model the table learns later is valued back to its first day, marked as floating, and a day recorded offline gets a value as soon as there is a list. Reads never write.

## 6. Formulas

Per row of a day, with the counters `U` = `in`, `R` = `cr`, `W` = `cw`, `W1` = `cw1h`, `O` = `out`, and the price `pi` input, `po` output, `pr` cache read, `pw` cache write, `pw1` 1 hour cache write, in dollars per million tokens:

```
pr'  = pr  when published, else pi      (no saving is claimed without a published cache-read price)
pw'  = pw  when published, else pi      (no premium is assumed without a published cache-write price)
pw1' = pw1 when published, else pw'

cost               = ( U*pi + R*pr' + (W - W1)*pw' + W1*pw1' + O*po ) / 1,000,000
cost_without_cache = ( (U + R + W)*pi + O*po ) / 1,000,000
saved_by_caching   = cost_without_cache - cost
                   = ( R*(pi - pr') - (W - W1)*(pw' - pi) - W1*(pw1' - pi) ) / 1,000,000
```

- **Saved by caching is net of the cache-write premium.** Writing a cache costs more than plain input (1.25 times for 5 minutes, 2 times for 1 hour at Anthropic's list prices [ran, in the list]); that premium is subtracted. A row that wrote and never read has a negative saving, and the page shows it as it is.
- `O` includes reasoning tokens; they are billed as output. `unc` tokens are in no amount.
- `cw5` is not in the formula: 5 minute writes and writes without a reported lifetime are both `W - W1`, at `pw'`.
- Amounts are computed per row and day in `float64`, summed over the range, and rounded to 6 decimals when written to the response. No estimate is ever stored.

Over any set of rows (the totals, a breakdown row):

```
cache_hit_rate = sum(R) / sum(U + R + W)        null when the denominator is 0; 4 decimals
reported_cost  = ( sum(cost) + sum(ucost) ) / 1,000,000,000   dollars
```

Worked example, the first row of the second day in `examples/days.json` (Claude Opus 5.5 at 4 / 20 / 0.2 / 5 / 8): `U` 710,000, `R` 138,400,000, `W` = `W1` 6,100,000, `O` 3,300,000.
cost = 2.84 + 27.68 + 0 + 48.80 + 66.00 = **145.32**; without the cache = 580.84 + 66.00 = **646.84**; saved = **501.52**; hit rate = 138.4 / 145.21 = **0.9531**. [ran]

### The page's numbers, in the owner's order

| # | The owner asked for | Field |
|---|---|---|
| 1 | money saved by caching | `totals.estimate.saved_by_caching_usd` |
| 2 | subscription against API key | `group=kind`: `requests` of the `oauth` and the `apikey` row |
| 3 | estimated cost of subscription requests at API list price | the `oauth` row's `estimate.cost_usd` |
| 4 | real API-key cost through OpenRouter | the `apikey` row's `reported_cost.usd`, with `reported_cost_hosts` |
| 5 | cache hit rate | `totals.cache_hit_rate` |
| 6 | per client key | `group=key` |

Two of them need a sentence on the page, because the number alone would claim too much (`04-console-page.md`):

- **1** covers all requests at list price. On an API key that is money; on a subscription caching saves quota, not dollars. The `group=kind` rows give both parts.
- **3** is what the subscription requests would have cost on an API key. It is not what the subscription saved: Provimux does not know what the subscription costs.

## 7. What the estimate does not know

| Not modelled | Effect |
|---|---|
| Prices that rise above a prompt size (`pricing.overrides`, 88 models in the list) | a daily total has no request sizes; long prompts are valued at the lower price, so cost and saving are understated |
| Fast mode and priority tiers (the record has `ResponseServiceTier`, `Detail.Speed`; `usage_helpers.go:2842-2848`) | valued at the standard price: understated |
| Web search, image, audio and per-request fees | not valued |
| Discounts, credits, a negotiated price | list price is used |
| Which account kind is tried first | recorded as it happened. Subscription before API key is a priority the owner sets, not a default; the page reports counts and does not say Provimux chose |

Where a real charge exists (`reported_cost` on OpenRouter rows), the page can put the estimate of the same rows beside it; that is the reader's check on the estimate.
