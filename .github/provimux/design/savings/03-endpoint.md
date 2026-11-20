# The endpoint: `GET /v8/management/provimux/savings`

The contract is `openapi.yaml`; `examples/savings-month.json` and `examples/savings-empty.json` are complete answers that validate against it. This file says what the handler does to produce them.

## 1. Where it sits

One line in `registerProvimuxRoutes` (`internal/api/server_provimux.go`), the layer's own file, next to the two existing routes:

```go
	v8.GET("/provimux/savings", s.mgmt.ProvimuxSavings)
```

It is in the authenticated v8 group, so the availability check, the management key and the version headers are those of every other route (`internal/api/server_management_v8.go:17-21`; `../pui13/05-console-contract.md` section 1). Read-only: it never writes to the store, never starts a download, and changes no routing state. Absent (404) on stock CLIProxyAPI and on a Provimux older than it. Every estimate is at base list prices, never the higher rates some models publish above a prompt size: the answer says so in `prices.rates` (`base`). Having prices means every installation downloads OpenRouter's public list, at most every 3 hours while requests are recorded and never under `--local-model` (chief's decision on open question 3; `../../../README.md`, Savings). In home mode every management route is 404 (`internal/api/server_management.go:219-222`).

The handler is a method of `*management.Handler` in a new file `provimux_savings.go`, as the other two are (`../pui13/README.md`, D1). It parses the query, calls `savings.Query`, and writes the result; all logic is in the `savings` package, where it is tested without HTTP.

## 2. Parameters

| Parameter | Values | Default |
|---|---|---|
| `period` | `today`, `yesterday`, `7d`, `30d`, `month`, `last_month`, `year`, `all` | `month`, when `from` and `to` are absent too |
| `from`, `to` | `YYYY-MM-DD`, both or neither, inclusive | |
| `group` | repeatable; each value 1 to 4 of `day`, `month`, `kind`, `provider`, `host`, `model`, `key`, joined by commas | none: totals only |
| `kind`, `provider`, `host`, `model`, `key` | one exact value each | no filter |

### The range

All days are calendar days of the server's zone (`01-recording.md` section 6), and "today" is the server's: `provimuxNow().In(loc)`.

| `period` | From | To |
|---|---|---|
| `today` | today | today |
| `yesterday` | today - 1 | today - 1 |
| `7d` | today - 6 | today |
| `30d` | today - 29 | today |
| `month` | the 1st of today's month | today |
| `last_month` | the 1st of the previous month | its last day |
| `year` | 1 January of today's year | today |
| `all` | `first_day` of the store's `meta`, or the earliest day in memory when that is earlier or there is no store; today when nothing is recorded | today |

- Day arithmetic is on calendar dates (`time.Date` with the day changed), never by adding 24 hours: a day of 23 or 25 hours must not shift a range.
- `from` and `to` may lie before the first recorded day or after today; days without totals are simply not there.
- A range is at most 3660 days. A year of a small team reads in about 2 ms and decodes in about 16 ms [ran, `01-recording.md` section 5].

### Groups

Each `group` value asks for one breakdown: the rows of the range that pass the filters, summed by the named dimensions. The answer's `breakdowns` has one entry per value, in the order asked, each with the `by` it was asked with.

| Dimension | A row's property | From |
|---|---|---|
| `day` | `day`: `YYYY-MM-DD` | the day's key |
| `month` | `month`: `YYYY-MM` | the day's first 7 characters |
| `kind` | `kind` | row `k` |
| `provider` | `provider` | row `p` |
| `host` | `host` | row `h`, `""` when absent |
| `model` | `model` | row `m` |
| `key` | `key`: `{id, tail, name}` | row `c`; `tail` from the `keys` map of the latest day in the range that has the id, null when it is `""` there; `name` null |

- Only combinations with at least one request appear; the console fills missing days with zeros.
- Order: with `day` or `month`, oldest first, then most requests first; otherwise most requests first. Ties by the dimension values, compared as strings in `by` order (`key` by its id), so the order is the same on every call.
- At most 2000 rows per breakdown: the 2000 with the most requests are kept, then ordered as above, and `truncated` is true.
- What the console's page asks in one call: `?period=month&group=kind&group=key&group=day&group=kind,provider,host`.

### Filters

Present with a value: only rows whose field equals it (`provider`, `host` and `model` are lower-cased first). Present and empty: for `host` and `key`, rows without one; for the others, no row. Absent: no filter. The handler must therefore tell "absent" from "empty" (`url.Values` has the key or not). Filters narrow everything in the answer: `totals`, every breakdown, `models`, `reported_cost_hosts`, `recording.folded_days`. They leave `recording.since` and `prices` as they are.

## 3. How the answer is built

`savings.Query(ctx, q)` does, in this order:

1. Take `mu`, the lock every save round holds for its whole length (`01-recording.md` section 5), and move the incoming deltas in. A query and a round never overlap, so each request is counted once.
2. One `KVMGet` of `meta` and the list copy, one of the keys of the days in range. A read error is `ErrStoreUnavailable` (the handler answers 503). A value that does not decode is skipped and logged once (`01-recording.md` section 5).
3. Per day: the rows of the store plus the deltas turned into rows keyed by key id (the stored secret). No price is given while reading: a row only in the deltas is valued with the current list and is floating. The page is current to the last record without forcing a write; the query writes nothing.
4. Per row: apply the filters; find its price (`02-prices.md` section 5: the day's recorded price, else today's, else none); compute the three amounts (`02-prices.md` section 6); add counters and amounts into the totals, into each breakdown's bucket, and into the model list.
5. Round money to 6 decimals and the hit rate to 4, at the end only.

### Where each part of the answer comes from

| Property | Source |
|---|---|
| `observed_at` | the clock, UTC, formatted as the other Provimux endpoints do (`provimuxTime`) |
| `timezone`, `utc_offset_seconds`, `today` | `01-recording.md` section 6 |
| `range` | the resolved range; `period` null when `from`/`to` were given |
| `recording.since` | the earlier of `meta.first_day` and the earliest day in memory; null when neither exists |
| `recording.persistent` | the local store is on and this process's last write to it succeeded (before the first write: its last read) |
| `recording.saved_at` | the last successful write of a day by this process |
| `recording.folded_days` | days in the range (after filters, any row left) whose value has `folded` |
| `prices.openrouter` | `fetched_at` and the number of models of the list in use (`02-prices.md` section 2); `remote` from `ProvimuxOpenRouterPrices` |
| `prices.rates` | `base`: the estimates use each model's base list rate (`02-prices.md` section 7) |
| `prices.provimux` | `updated` and the number of entries of the embedded table |
| `totals`, `breakdowns[].rows[]` | the same `Metrics`, from the same function |
| `models` | one entry per (provider, model), most tokens first, at most 500 (`models_truncated`); `tokens` = `in + cr + cw + out + unc`. `price` is that of the latest day in the range that priced the pair; `basis` is `recorded` when every priced row used its day's price, `current` when none did, `mixed` otherwise. Model `~other` has `price` null |
| `reported_cost_hosts` | the distinct `h` of rows with `costn` above 0, sorted |

`Metrics`, field by field, from summed counters:

| Field | From |
|---|---|
| `requests`, `failures` | `req`, `fail` |
| `tokens.input.uncached`, `.cache_read`, `.cache_write`, `.cache_write_5m`, `.cache_write_1h` | `in`, `cr`, `cw`, `cw5`, `cw1h` |
| `tokens.input.total` | `in + cr + cw` |
| `tokens.output.total`, `.reasoning` | `out`, `rsn` |
| `tokens.unclassified` | `unc` |
| `cache_hit_rate` | `cr / (in + cr + cw)`; null when that sum is 0 |
| `estimate.cost_usd`, `.cost_without_cache_usd`, `.saved_by_caching_usd` | sums of the per-row amounts of priced rows |
| `estimate.priced_requests` | `req` of priced rows |
| `estimate.floating_requests` | `req` of rows priced with today's list |
| `estimate.unpriced_requests`, `.unpriced_tokens` | `req` and `in + cr + cw + out + unc` of rows without a price |
| `reported_cost.usd` | `(cost + ucost)` in dollars |
| `reported_cost.byok_upstream_usd` | `ucost` in dollars |
| `reported_cost.requests` | `costn` |

`priced_requests + unpriced_requests = requests` always.

## 4. Example

```
GET /v8/management/provimux/savings?period=month&group=kind&group=key&group=kind,provider,host
Authorization: Bearer <management key>
```

The full answer is `examples/savings-month.json`. It is what the three day values in `examples/days.json` give on 2026-11-20 in Europe/Bucharest: a Claude and a Codex login, an OpenRouter key, a direct Claude key, and a Devin model without a price. Its totals:

```json
{
  "requests": 2245,
  "failures": 12,
  "tokens": {
    "input": { "uncached": 16177500, "cache_read": 470700000, "cache_write": 16425000,
               "cache_write_5m": 2700000, "cache_write_1h": 13725000, "total": 503302500 },
    "output": { "total": 12870000, "reasoning": 3420000 },
    "unclassified": 0
  },
  "cache_hit_rate": 0.9352,
  "estimate": {
    "cost_usd": 429.1155,
    "cost_without_cache_usd": 1858.8555,
    "saved_by_caching_usd": 1429.74,
    "priced_requests": 2155,
    "unpriced_requests": 90,
    "unpriced_tokens": 877500,
    "floating_requests": 0
  },
  "reported_cost": { "usd": 22.145526, "byok_upstream_usd": 0, "requests": 208 }
}
```

and its `apikey` row of the `kind` breakdown, where the estimate (22.27, of which the direct Claude key is 0.04) can be read beside the charge OpenRouter reported (22.15):

```json
{ "kind": "apikey", "requests": 239, "estimate": { "cost_usd": 22.2705, "saved_by_caching_usd": 40.545 },
  "reported_cost": { "usd": 22.145526, "byok_upstream_usd": 0, "requests": 208 } }
```

(shortened; a real row has every `Metrics` property). The numbers of both example files were produced by a script that applies section 3 and `02-prices.md` section 6 to the rows, with prices taken from the live list. [ran]

## 5. Errors

| Status | Body | When |
|---|---|---|
| 400 | `{"error": "<what is wrong>"}` | a parameter is wrong, see the list in `openapi.yaml`. The message names the parameter. Checked before any state is read |
| 401, 403 | the middleware's | the management key, as every route |
| 404 | empty | management off, home mode, or no such route |
| 503 | `{"error": "savings store unavailable"}` | the local store is on and reading it failed. Not 404: the console must not take it for absence, the rule of live-lists (`provimux_live_lists.go:92-96`) |

Not errors, each a 200:

| Situation | Answer |
|---|---|
| nothing recorded yet | zeros, `cache_hit_rate` null, `recording.since` null: `examples/savings-empty.json` |
| the store is off | what is in memory, `recording.persistent` false |
| no price list | every amount 0, `estimate.unpriced_requests` = `requests`, `prices.openrouter.fetched_at` null |

No error body and no log line of this endpoint contains a client key, a base URL or a store value.

## 6. Not in it, on purpose

- Per-request rows, sessions, client addresses, user agents: only daily totals exist.
- Which login served a request: the row has the kind and the provider, not the account.
- A name for a key: reserved (`key.name`).
- A verdict on the routing ("Provimux sent this to the subscription first"): the endpoint counts what happened.
- Dollars a subscription saved: the fee is not known to Provimux.
