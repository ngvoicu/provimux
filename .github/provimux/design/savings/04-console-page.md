# The Savings page of the console: its purpose

For the worker who builds the page in provimux-ui. This says who the page is for, what it must let them see and do, and what it must never claim. It does not say how the page looks: layout, cards, charts and colour are the builder's, within the console's own conventions (provimux-ui `AGENTS.md`). The data is `GET /v8/management/provimux/savings` (`03-endpoint.md`, `openapi.yaml`).

## 1. Who opens it, and why

The person who runs Provimux for themselves or a few people, signed in with the management key. Two reasons:

1. **To know whether it pays.** Provimux's promise is "every AI request costs as little as it can, and you always know why". This page is where that is checked: how much caching saved, how much went through subscriptions instead of paid keys, what the paid keys really cost.
2. **To show it.** The owner copies one line, or takes a screenshot, and posts it. So every number on the page must survive a stranger's "how do you know?", from the page itself.

The second reason sets the tone: a number without its basis is worse than no number. The page says "estimate" where it estimates, names the price list, and shows what it could not value.

## 2. What they must be able to see

In this order; it is the owner's. The first is the headline.

| # | What | From the answer | The sentence that must be with it |
|---|---|---|---|
| 1 | **Money saved by caching** in the period | `totals.estimate.saved_by_caching_usd` | It is an estimate at list prices, net of what writing the cache cost. Its two parts are shown with it: the part on API-key requests (money that would have been paid) and the part on subscription requests (on a subscription, caching saves quota, not dollars; this is its value at list price). Both are in the `group=kind` rows |
| 2 | **Subscription against API key**: how many requests each served | `requests` of the `oauth` and `apikey` rows of `group=kind` | These are counts of what happened. The page does not say Provimux preferred one: which is tried first is a priority the owner sets. A row of kind `unknown`, or a kind the page does not know, is shown as "other" |
| 3 | **What the subscription requests would have cost** on an API key | the `oauth` row's `estimate.cost_usd` | An estimate at API list price. It is not what the subscription saved: Provimux does not know what the subscription costs |
| 4 | **What the API keys really cost** | the `apikey` row's `reported_cost.usd`, and `reported_cost.requests` of its `requests` | A charge the provider reported, not an estimate. The page names who reported it (`reported_cost_hosts`; `openrouter.ai` is OpenRouter) and for how many of the API-key requests. For API-key requests without a reported cost it shows the estimate, labelled as one |
| 5 | **Cache hit rate** | `totals.cache_hit_rate` | Cached input tokens read, out of all input tokens. "No input yet" when null, never 0% |
| 6 | **Per client key** | the rows of `group=key` | A key is shown by its last 4 characters (`key.tail`), or by the first 4 characters of `key.id` when it has no tail, or as "no key" when `key.id` is `""`, or "other keys" for `~other`. Never more of a key; the answer has no more. `key.name` is null today; when a later server fills it, the name is shown instead |

Beside those, always visible without a click:

- **The word "estimate"** on every amount that comes from `estimate`.
- **The price source**: OpenRouter's public list and its date (`prices.openrouter.fetched_at`), and Provimux's own table (`prices.provimux.updated`) when a model in the range uses it.
- **The period and the zone**: the dates the numbers cover (`range.from`, `range.to`), the server's zone (`timezone`, or the offset when the name is `Local`), and since when Provimux has been recording (`recording.since`) when that is later than `range.from`.
- **That requests are attempts**: one line, wherever request counts are explained. A request retried on another account counts twice.

Reachable in one step (a table, a disclosure; the builder's choice):

- **Per model**: provider, model, requests, tokens, and the price it is valued at: the price id, the source, how it was matched (`models[].price.id`, `.source`, `.match`) and the five per-million prices. This is the page's answer to "how do you know?".
- **Per day**: the saving and the requests of each day of the period (`group=day`), so the owner sees when it was earned. Days without requests are missing in the answer and are zero.
- **Per provider and host**: `group=kind,provider,host`, so OpenRouter stands apart from a provider's own API.
- **The token counts** behind the hit rate: uncached, cache read, cache write (with the 5 minute and 1 hour parts when either is above 0), output, reasoning.

## 3. What they must be able to do

| Action | Behaviour |
|---|---|
| **Pick the period** | Today, 7 days, 30 days, this month, last month, this year, all time: the endpoint's `period` values, sent as they are. Default this month. The page never computes a date range itself: "today" is the server's. Changing the period loads at once and an answer for the earlier choice is dropped. The choice lasts for the session |
| **Copy the line** | One button puts one line of plain text on the clipboard, section 5 |
| **Refresh** | The page's button and the header's, as on the other Provimux pages |
| **Go from a model without a price to the reason** | The unpriced list of section 4 |

Not in this version: naming a key, exporting, filtering by key or model (the endpoint has the filters; the page may use them later), choosing a custom date range.

## 4. States the page must handle

Each is a state of the answer, not an error.

| State | How it is known | What the page says |
|---|---|---|
| **Nothing recorded yet** | `recording.since` null | That Provimux records from the moment this version started, that totals appear with the first request, and that nothing from before can be shown. No zeros dressed as results, no copy button |
| **Nothing in this period** | `totals.requests` 0, `recording.since` set | "No requests in this period", and since when there is data |
| **Models without a price** | `totals.estimate.unpriced_requests` above 0 | Near the headline, not in a footnote: how many requests and tokens are not in the amounts, and which models (`models[]` with `price` null, most tokens first). They are counted everywhere else. The headline is then a lower bound, and the page says so |
| **Valued with today's prices** | `estimate.floating_requests` above 0 | That part of the period was recorded without a price and is valued with today's list, so its amounts can still move |
| **No price list** | `prices.openrouter.fetched_at` null | That there are no prices yet, so nothing can be valued; with `remote` false, that this Provimux runs with `--local-model` and does not download the list. Requests, tokens and the hit rate are shown as usual |
| **Old price list** | `fetched_at` more than a day before `observed_at` | The list's date, plainly |
| **Caching cost more than it saved** | `saved_by_caching_usd` below 0 | The negative amount as it is, and why: the cache was written and not read enough. No copy button |
| **Not being saved** | `recording.persistent` false | A warning that totals are in memory only and end with the next restart: the local store is off (`server.kv-store.disabled`) or a write to it failed |
| **A day reached the row limit** | `recording.folded_days` above 0 | That some models and keys of those days are summed under "other" |
| **The store could not be read** | HTTP 503 | The error and a retry; the page stays |
| **Not a Provimux with this endpoint** | HTTP 404 | No nav item; the route goes to the dashboard (`ProvimuxPageGate`) |

Amounts under one cent are shown as "under $0.01", not as $0.00. Token counts are shortened for reading (1.2M) with the exact number available.

## 5. The copy line

Purpose: a sentence the owner can paste anywhere and stand behind.

- **The amount** is the headline: `totals.estimate.saved_by_caching_usd` of the period on screen. Whole dollars from $100, cents below; **rounded down**, so the line never says more than the page.
- **The text**, English, in `src/provimux/locales/en.json` with the period as a variable:
  `Provimux saved me $1,429 this month with prompt caching (estimate at OpenRouter list prices).`
  The owner's words are "Provimux saved me $X this month"; the tail is what makes the line true when it stands alone. Whether the tail stays, and whether X is the caching saving alone, is open question 1 in `README.md`.
- **Period words**: today, in the last 7 days, in the last 30 days, this month, last month, this year, so far.
- **When there is no line to copy**: the button is disabled, with the reason beside it, when the amount is under $0.01 or negative, or when nothing in the period has a price.
- It says "OpenRouter list prices" only when every priced model in the range has `price.source` `openrouter`; otherwise "list prices".
- The clipboard goes through `copyToClipboard` (`src/utils/clipboard.ts`), and success or failure is shown as the console shows it elsewhere (`src/features/authFiles/AuthFilesPage.tsx:511-518`).
- The line contains no key, no host, no model and no account.

## 6. What the console relies on

Everything in `../pui13/05-console-contract.md` applies: the management key through `apiClient`, 404 as absence, 401 as logout, responses that only grow, unknown fields and enum values ignored.

| Point | For Savings |
|---|---|
| Presence | `ProvimuxEndpoint` gains `savings`; it is probed with the others, once per connection (`src/provimux/presence.ts`). The probe is `GET /provimux/savings?period=today`: totals only, small |
| The page's request | `GET /provimux/savings?period=<p>&group=kind&group=key&group=day&group=kind,provider,host`, one call for the whole page. `group` is repeated, not `group[]` (as `modelQuery` does for `model`, `src/provimux/api/provimuxApi.ts:45-46`) |
| Refreshing | on open, on a refresh, on a change of period, and every 60 s while the tab is visible (`useProvimuxResource` with a fetcher that changes with the period). Never while hidden |
| Reading the answer | `readSavings` in `src/provimux/api/normalize.ts`: a missing number is 0 where the contract says "0 when", else null; a missing array is empty; an unknown `kind`, `price.source` or `price.match` is kept as a string and shown as "other" |
| 503 | shown with a retry, as any error that is not 404 |
| Strings | `provimux.savings.*`, `provimux.nav.savings`, `provimux.nav_meta.savings` in `src/provimux/locales/en.json`; no upstream locale file changes (`src/provimux/i18n.ts`) |
| Money and numbers | formatted with `Intl.NumberFormat` in the page's language; the copy line is always English |

### Types

For `src/provimux/api/types.ts`. `Timestamp` is the existing type.

```ts
/** A calendar day of the server's zone, YYYY-MM-DD. */
type ProvimuxDay = string;

interface ProvimuxSavingsMetrics {
  requests: number;
  failures: number;
  tokens: {
    input: {
      uncached: number; cache_read: number; cache_write: number;
      cache_write_5m: number; cache_write_1h: number; total: number;
    };
    output: { total: number; reasoning: number };
    unclassified: number;
  };
  /** 0..1; null when there are no input tokens. */
  cache_hit_rate: number | null;
  /** Estimates at list prices. Amounts can be negative. */
  estimate: {
    cost_usd: number;
    cost_without_cache_usd: number;
    saved_by_caching_usd: number;
    priced_requests: number;
    unpriced_requests: number;
    unpriced_tokens: number;
    /** Priced with today's list because their day recorded no price. */
    floating_requests: number;
  };
  /** Charges an upstream reported; not an estimate. */
  reported_cost: { usd: number; byok_upstream_usd: number; requests: number };
}

interface ProvimuxSavingsKey {
  /** 16 hex characters; '' = no client key; '~other' = folded rows. */
  id: string;
  /** The key's last 4 characters, only for keys of 16 characters or more. */
  tail: string | null;
  /** Reserved; null in this version. */
  name: string | null;
}

type ProvimuxSavingsDimension = 'day' | 'month' | 'kind' | 'provider' | 'host' | 'model' | 'key';

/** The metrics of one combination; only the dimensions named in `by` are present. */
interface ProvimuxSavingsRow extends ProvimuxSavingsMetrics {
  day?: ProvimuxDay;
  /** YYYY-MM */
  month?: string;
  kind?: 'oauth' | 'apikey' | 'unknown' | (string & {});
  provider?: string;
  /** '' for a login and for a key on its provider's own address. */
  host?: string;
  /** '~other' for folded rows. */
  model?: string;
  key?: ProvimuxSavingsKey;
}

interface ProvimuxSavingsBreakdown {
  by: ProvimuxSavingsDimension[];
  rows: ProvimuxSavingsRow[];
  truncated: boolean;
}

interface ProvimuxSavingsPrice {
  source: 'openrouter' | 'provimux' | (string & {});
  /** The price list entry, e.g. anthropic/claude-opus-5.5. */
  id: string;
  match: 'exact' | 'variant' | 'normalized' | 'table' | (string & {});
  basis: 'recorded' | 'current' | 'mixed' | (string & {});
  /** US dollars per million tokens. */
  input_per_mtok: number;
  output_per_mtok: number;
  cache_read_per_mtok: number | null;
  cache_write_per_mtok: number | null;
  cache_write_1h_per_mtok: number | null;
}

interface ProvimuxSavingsModel {
  provider: string;
  model: string;
  requests: number;
  tokens: number;
  /** null = unpriced. */
  price: ProvimuxSavingsPrice | null;
}

interface ProvimuxSavingsResponse {
  observed_at: Timestamp;
  timezone: string;
  utc_offset_seconds: number;
  today: ProvimuxDay;
  range: { period: string | null; from: ProvimuxDay; to: ProvimuxDay };
  recording: {
    since: ProvimuxDay | null;
    persistent: boolean;
    saved_at: Timestamp | null;
    folded_days: number;
  };
  prices: {
    currency: 'USD';
    openrouter: { url: string; fetched_at: Timestamp | null; models: number; remote: boolean };
    provimux: { updated: ProvimuxDay; models: number };
  };
  totals: ProvimuxSavingsMetrics;
  /** In the order of the request's `group` values. */
  breakdowns: ProvimuxSavingsBreakdown[];
  models: ProvimuxSavingsModel[];
  models_truncated: boolean;
  reported_cost_hosts: string[];
}
```

The page finds a breakdown by its `by` (`['kind']`, `['key']`, `['day']`, `['kind','provider','host']`), not by its position.
