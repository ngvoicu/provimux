# Recording: from a usage record to a day's totals

For the worker who builds the backend. Prices and formulas are in `02-prices.md`, the endpoint in `03-endpoint.md`, files and tests in `05-build-tests-review.md`. The stored value is fixed by `schemas/day.schema.json`; `examples/days.json` holds three.

## 1. What arrives

Every attempt at an upstream ends in one `coreusage.Record` (`sdk/cliproxy/usage/manager.go:26-83`), built by `buildRecordForModel` (`internal/runtime/executor/helps/usage_helpers.go:741-785`) and published once per attempt (`:637-645`, `:666-673`). [read]

- **One record is one attempt, not one client request.** A request that fails on one account and succeeds on the next gives two records, the first with `Failed`. A side model (an image tool) gives one more record without a failure (`PublishAdditionalModel`, `:409-415`). "Requests" in Savings therefore means usage records; the contract says so.
- **Delivery is already off the request path.** `Publish` appends to a queue under a mutex and returns (`manager.go:728-782`); one goroutine takes records off it and calls every plugin in turn (`:784-816`). A slow plugin delays the other plugins and lets the queue grow (it has no bound: `NewManager` ignores its argument, `:655-659`), but no request waits for it.
- **Plugins get records whatever `usage-statistics-enabled` says.** That switch is checked inside the queue plugin (`internal/redisqueue/plugin.go:31`), not by the manager. [ran, in `../pui13/README.md`, "What I ran"]
- **Counting tokens is not recorded.** None of the 13 `CountTokens` functions of the executors creates a usage reporter. [ran: a scan of `internal/runtime/executor/`]
- The token breakdown is attached before the record leaves the executor (`usage_helpers.go:641`, `:647-649`), by the parser of each protocol.

## 2. The recorder

A new package `internal/provimux/savings` holds everything. It registers one usage plugin by name, `coreusage.RegisterNamedPlugin("provimux-savings", ...)` (`manager.go:836`), from `savings.Start()`. `Start` is called by `registerProvimuxRoutes` (`internal/api/server_provimux.go`, the layer's own file), so:

- recording begins when the management routes exist, which is when a management key is configured (`internal/api/server.go:285-287`, `server_reload.go:149`, `:158`). Without a key nobody could read the page; this is also the rule of upstream's usage queue (`server.go:285`);
- test binaries of other packages, which never build the API server, record nothing.

`Start` is idempotent and starts one goroutine, the saver (section 5). Recording then lasts as long as the process: removing the management key later stops the endpoint (404), not the recording, whose totals stay in the user's own local store (the chief's decision in T-116; `05` check 18).

`HandleUsage` runs on the usage manager's goroutine. It reduces the record to the fields below, takes a small lock of its own (`inMu`, which guards only the incoming deltas and is never held across anything else), adds the numbers to a map, and returns. It does no I/O and never waits for the disk, not even while a save round is stuck in a write (section 5). It measures about 220 ns per record, one allocation of 16 bytes, the same with a save round blocked in a write and about 236 ns with rounds running back to back on a real bbolt file (T-122, `BenchmarkHandleUsage*`).

### What is taken from a record

| Stored as | From | Rule |
|---|---|---|
| day | `RequestedAt` (`time.Now()` when zero) | calendar day in the server's zone, section 6 |
| kind `k` | `AuthType` | `oauth` or `apikey` as `Auth.AuthKind()` gives them (`sdk/cliproxy/auth/classification.go:30-50`, set at `usage_helpers.go:150`); anything else, and empty, is `unknown` |
| provider `p` | the account's provider: what the model registry has for the record's `AuthID` (`registry.ProvimuxClientProvider`); `Provider` (the executor's identifier) when the registry has none | trimmed, lower case, at most 64 bytes; empty is `unknown`: `claude`, `codex`, `gemini`, `vertex`, `aistudio`, `antigravity`, `xai`, `kimi`, `meta`, `devin`, or the name of an OpenAI-compatible entry. The account's, not the executor's (T-116): a Kimi login served through its Claude Messages route runs the embedded Claude executor (`kimi_executor.go:114`), whose identifier is `claude`. The executor's `Provider` still chooses the token semantics (section 3) |
| host `h` | `BaseURL` | only for kind `apikey`: lower-cased hostname of the URL (`url.Parse(...).Hostname()`), else `""`. The rule of `provimuxBaseURLHost` (`internal/api/handlers/management/provimux_model_routes.go:289-298`). Never the path, the port or user info |
| model `m` | `Model` | trimmed, lower case, at most 128 bytes; empty is `unknown`. It is the upstream model: the conductor resolves aliases and removes a credential's prefix before the executor runs (`sdk/cliproxy/auth/conductor_execution.go:998`, `conductor_models.go:770-783`), and executors remove the thinking suffix (`thinking.ParseSuffix(req.Model).ModelName`, e.g. `openai_compat_executor.go:130-132`, `claude_executor_execute.go:42`) |
| client key | `APIKey` | the key the client sent (`manager.go:42`, `usage_helpers.go:912-932`), turned into an id and a tail, section 7. Never `Source`, which can be the provider's key (`usage_helpers.go:964-1001`) |
| `req` | | 1 |
| `fail` | `Failed`, and the response status in the context | 1 when `Failed`, or when `internallogging.GetResponseStatus(ctx)` is 400 or more: the rule of the usage queue (`redisqueue/plugin.go:140-143`, `:334-340`) |
| `in`, `cr`, `cw`, `out`, `rsn`, `unc` | `Detail.TokenBreakdown` | section 3 |
| `cw5`, `cw1h` | `Detail.CacheCreation5mTokens`, `CacheCreation1hTokens` | section 3 |
| `cost`, `ucost`, `costn` | `Detail.CostDetails` | section 4 |

Nothing else is read. `AuthID` is only looked up for the provider and never kept. In particular not `Alias` (the client's name for the model), `Source`, `AuthIndex`, `SessionID`, `ResponseHeaders`, `Fail.Body`.

Tokens of a failed record are counted: a truncated stream was still billed (`PublishFailureWithDetail`, `usage_helpers.go:624-626`).

## 3. Tokens: only the normalised breakdown

Providers disagree on what "input tokens" means, so no raw field of `Detail` is summed. The recorder calls `coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)` (`sdk/cliproxy/usage/accounting.go:253`; a no-op when the breakdown is already valid, which is the normal case) and reads `TokenBreakdown` (`accounting.go:27-49`):

| Counter | Field |
|---|---|
| `in` | `Input.UncachedTokens` |
| `cr` | `Input.CacheReadTokens` |
| `cw` | `Input.CacheWriteTokens` |
| `out` | `Output.TotalTokens` (reasoning included) |
| `rsn` | `Output.ReasoningTokens` |
| `unc` | `UnclassifiedTokens` |

The three input buckets are mutually exclusive and add up to the input total (`Valid`, `accounting.go:52-81`), whatever the provider:

| Provider | What the upstream reports | Made exclusive by |
|---|---|---|
| Claude | `input_tokens` without cache reads and cache writes | `NewIndependentTokenBreakdown` (`usage_helpers.go:2855-2862`) |
| OpenAI-compatible (OpenRouter too), Codex, Meta, xAI, Kimi | input including cached tokens, reasoning inside output | `NewSubsetTokenBreakdown` (`usage_helpers.go:2083`, reached by the Codex parser at `:1748`; `accounting.go:347-361`) |
| Gemini, Vertex, AI Studio, Antigravity, Devin | input including cached tokens, reasoning beside output | `NewSeparateReasoningTokenBreakdown` (`usage_helpers.go:3346`, `:3745`) |
| unknown provider, or numbers that contradict each other | a total only | `UnclassifiedTokens` (`accounting.go:228-243`, `:363`, `:366-380`) |

**The Claude `CachedTokens` quirk.** For Claude, `Detail.CachedTokens` is the cache read count, or the cache creation count when nothing was read (`usage_helpers.go:2849-2851`). Summing it would count cache writes as hits. `TokenBreakdown.Input.CacheReadTokens` does not have the quirk; a test pins it (`05`, section 4).

**The one field outside the breakdown.** The breakdown has no lifetime split, so `cw5` and `cw1h` come from `Detail.CacheCreation5mTokens` and `CacheCreation1hTokens` (nil counts as 0; filled for Claude by `applyCacheDetailsToUsageDetail`, `usage_helpers.go:2963-2978`). They are parts of `cw`, never added to it. If `cw5 + cw1h > cw` for a record, both are recorded as 0 for that record. The 30 minute and 12 hour buckets (`:1726`, `:1730`) are not kept: they are inside `cw` and no price list prices them apart.

`unc` tokens are counted and shown, and never priced: nobody knows whether they were input or output.

## 4. The cost an upstream reported

`Detail.CostDetails` (`sdk/cliproxy/usage/detail.go:98-105`) is filled from `usage.cost`, `usage.is_byok` and `usage.cost_details` by both the Claude-format and the OpenAI-format parser (`usage_helpers.go:3215-3250`, used at `:2163-2192`), so it arrives for OpenRouter whichever endpoint of it an entry uses.

| Counter | Rule |
|---|---|
| `cost` | `Cost`, when present and not negative, rounded to billionths of a dollar |
| `ucost` | `UpstreamInferenceCost`, only when `IsBYOK` is true |
| `costn` | 1 when `Cost` was present |

Integers, so sums are exact; an `int64` of billionths holds 9.2 billion dollars. `CostInUSDTicks` (xAI) is not kept in this version.

[infer] For a bring-your-own-key request OpenRouter's `cost` is its own fee and `upstream_inference_cost` is what the provider charged on the owner's key; the two are stored apart so that reading them differently later needs no new data.

The host is what tells the page whose bill it is: the row's `h` is `openrouter.ai` for OpenRouter. This is why `host` is part of a row (decision D6 in `README.md`): with the Anthropic-format endpoint of OpenRouter the provider is `claude`, and only the host says OpenRouter.

## 5. Where the totals live

### Why not one key per counter

`internal/kvstore` (`types.go:26-37`) offers get, set, delete, increment and multi-get. It has **no listing**: a layout of one key per row could never be read back without an index. And every write is its own bbolt transaction with an fsync (`bolt_store.go:194-201`).

[ran] On this machine (Apple M4 Pro, temporary directory, `kvstore.OpenBoltStore`):

| Call | Time |
|---|---|
| `KVIncrBy`, one counter | 8.8 ms |
| `KVSet` of a day of 30 rows (6.5 KB) | 9.1 ms |
| `KVSet` of a day of 180 rows (38 KB) | 8.3 ms |
| `KVSet` of a day of 2000 rows (427 KB) | 9.3 ms |
| `KVMGet` of 365 days of 180 rows | 1.9 ms, plus 16 ms to decode their 65,700 rows |

Twelve increments per record would be about 100 ms of disk per request on the usage manager's goroutine. One value per day costs the same 9 ms whatever its size, once per interval.

### Keys

| Key | Value | Written |
|---|---|---|
| `provimux:savings:v1:day:<YYYY-MM-DD>` | the day's totals, JSON, `schemas/day.schema.json` | while the day changes, every 10 s at most |
| `provimux:savings:v1:meta` | `{"v":1,"created_at":"<RFC 3339>","first_day":"<YYYY-MM-DD>","key_secret":"<base64 of 32 random bytes>"}` | once; again only when a day before `first_day` is about to be written, **before** that day (T-125) |
| `provimux:savings:v1:prices:openrouter` | the last OpenRouter price list that was downloaded, `02-prices.md` section 2 | when a downloaded list differs from the saved one |

No key has a TTL (`KVSet` without `EX`/`PX` stores no expiry, `bolt_store.go:296-367`), so the sweeper never removes them. The `v1` in the key is the layout's version; a counter added later goes into the same value (readers ignore what they do not know, absent means 0).

Dates in a range are computed, so reading a range is one `KVMGet` of its day keys; `meta.first_day` tells `period=all` where to start.

**Only the local store.** The saver takes `kvstore.CurrentKVClient()` (`controller.go:102-126`) and uses it only when it is a `*kvstore.BoltStore`. In home mode that function returns Home's client (`:108-110`); Savings never writes there, and in home mode every management route is 404 anyway (`server_management.go:219-222`). It does not use the `KV...Required`/`BestEffort` helpers, which go wherever `CurrentKVClient` points.

### Deltas since the last save

Memory holds only what was recorded since the last successful write of each day: per day, counters per combination (kind, provider, host, model and **the client key itself**, section 7). Never a day's totals, never a copy of what the store holds; there is nothing in memory that describes a store, so nothing to bind to one or to invalidate when it changes (T-122).

| Lock | Guards | Held |
|---|---|---|
| `inMu` | `incoming`: the deltas `HandleUsage` adds | for a map update only |
| `mu` | the deltas, every save round, every query | for the whole round or query, **store calls included**; `HandleUsage` never takes it |

A round or a query first moves `incoming` into the deltas (under `mu`, taking `inMu` for the swap). Every 10 seconds, and on `FlushNow`, a round then does, under `mu`:

1. Read `meta` and the list copy of the store **in use now** (one `KVMGet`); on a store without `meta`, write it (`KVSetNX`, then `KVGet` for the value that won; section 7).
2. Read every day that has deltas (one `KVMGet`). If the earliest of them is before `meta.first_day`, write `meta` with that day first; if that write fails, no day is written this round and every delta waits. The index may be too early (harmless: `period=all` starts at an empty day) but never later than a stored day, which `period=all` would miss (T-124 finding 14).
3. Per day: add the deltas to the rows read (by key id, with the secret of that store's `meta`), apply the row limit to the merged rows (a repeat of a row takes no slot), give rows without a price the one in use now (`02-prices.md` section 5), encode and `KVSet` the whole value.
4. **Only after the write succeeded**, forget that day's deltas. A failed read or write keeps all of them for the next round; nothing is dropped. The error is logged once per streak of failures, and once more when writes succeed again.

A query does the same reads under the same `mu`, adds the deltas to what it read and writes nothing (`03-endpoint.md` section 3). Because a round holds `mu` from its first read to the moment it forgets the deltas, a query sees either the store before the write plus the deltas, or the store after it without them: each request is counted exactly once, with no generation counter and no second read.

Consequences:

- **A write is one transaction of one value**: it happens entirely or not at all. There is no half-written day and no counter that is ahead of another.
- **Nothing is counted twice.** The value written is the stored total plus deltas that are forgotten in the same critical section; writing it again cannot happen, because the deltas are gone once written. Each round reads the day again, so another writer of the file (a second Provimux on it, not supported) would lose nothing either.
- **A crash or `kill -9` loses at most what was recorded since the last write**: up to 10 seconds of records, more while the store is failing. The page does not show that loss; it is the price of not touching the disk per request.
- **A clean stop loses nothing that reached the recorder.** `Service.Shutdown` stops the API server (`sdk/cliproxy/service_lifecycle.go:370`), then closes the store (`:396`), then stops the usage manager (`:404`). One added line calls `FlushNow` just before the store closes (`05`, section 2). A record still in the usage manager's queue at that instant is lost; the server has stopped accepting requests by then.
- **The price list copy** follows the store in use: every round, idle ones and the clean flush included, compares the downloaded list with that store's copy and writes it when the store has none or an older, different one; there is no memory of which store was last given it (T-124 finding 7). An idle round makes this one read only once a list was downloaded.
- **Write volume**: at most one write of the days with deltas every 10 s while there is traffic, none when idle. bbolt reuses the pages of the replaced value, so the file grows with the days kept, not with the writes. Each such round also reads those days and `meta` (about 2 ms for a year of days, section "Why not one key per counter").
- **A query waits for a round in progress**, and a round for a query: at most one store write (about 9 ms) or one range read. `HandleUsage` waits for neither.
- **Prices.** A row read from the store keeps the price it was written with (`basis` `recorded`). A row that exists only in the deltas, or was stored without a price, is valued with the current list and counted in `floating_requests` (`basis` `current`) until a write records its price. A row whose write is in flight or failed is therefore never `recorded`.

### When the store is off or failing

| Situation | Behaviour |
|---|---|
| `server.kv-store.disabled: true` (`internal/config/config_types.go:483-490`), or home mode | the deltas accumulate in memory, bounded per day by the row limit (beyond it new combinations fold into `~other`) and to 400 days (the oldest dropped first, logged once). The endpoint answers from them with `recording.persistent: false`, every amount `current`. **They are lost on restart** |
| the store cannot be opened (`CurrentKVClient` returns an error) | the same, retried every round; when it opens, the deltas are added to what the store holds and written |
| a read or a write fails (disk full) | the deltas stay in memory and are retried every round; `recording.persistent` is false until a round writes every day it has; logged once per failure streak |
| only the price list copy cannot be read (an idle round) or written | the copy is retried every round; `recording.persistent` is not touched, since it says only whether the days reach the store; logged once per streak of its own, and once when it succeeds again (T-127 7a) |
| the store is switched off at run time (config reload, `sdk/cliproxy/kv_store.go:18-22`) | the reload flushes first (`provimuxFlushSavings()`); from then on as the first row |
| `server.kv-store.path` changes at run time (config reload) | nothing special. Before the reload changes the store, `applyKVStoreConfig` flushes to the store in use (`provimuxFlushSavings()`), so what was recorded before the switch normally lands in the old store. Whatever is not written by then (that flush failed or timed out, or a record arrived between the flush and the switch) is written to the store in use at the next round: **up to one save interval of usage made just before a switch can land in the new store**. Nothing is lost and nothing is counted twice; the old store is not touched again. Each store's days are read from that store, so its history is extended, never replaced, and its row limit is its own |
| a day's value cannot be decoded | it is not overwritten: the day is skipped by the saver and by reads, an error is logged once per day with the key, no value. Its deltas are kept in memory (bounded as above) |

### Growth

A row is about 215 bytes of JSON [ran: 6,484 B for 30 rows, 38,434 B for 180]. A day has one row per combination that actually occurred.

| Use | Rows a day | A day | A year |
|---|---|---|---|
| one person: 3 client keys, 8 models, logins and one API key | 30 | 6.5 KB | 2.4 MB |
| a small team: 10 keys, 12 models | 180 | 38 KB | 14 MB |
| the limit, every day | 2000 | 427 KB | 156 MB |

Nothing is ever deleted (the owner's decision). The price list copy adds about 30 KB once.

**The row limit.** A day holds at most 2000 rows. It is applied when the deltas are added to the rows read from the store: a repeat of a stored row takes no slot; once 2000 are reached, a combination that is new is added to the row with its kind, provider and host, model `~other` and key `~other`, and the day is marked `folded`. To bound memory, the deltas of one day also hold at most 2000 combinations; a new one beyond that is kept folded. So a record is folded before it meets the store only when one day sees more than 2000 distinct combinations between two successful writes (or, with the store off, at all). Those `~other` rows come on top of the 2000, one per kind, provider and host, which the configuration bounds. This bounds the value against a client that sends many model names or keys; totals stay right, only their split by model and key is lost for the overflow.

## 6. Which day

A record belongs to the calendar day of its `RequestedAt` (the start of the attempt) in the server's local time zone, `time.Local`. The response names the zone and gives the server's `today`; the console never computes a day from the browser clock.

| Point | Rule |
|---|---|
| zone name | `$TZ` when it is set and `time.LoadLocation` accepts it, else `time.Local.String()` (`Local` or `UTC`); the offset is always given |
| a request that starts before midnight and ends after | belongs to the day it started |
| daylight saving | a day is 23 or 25 hours long twice a year; totals are unaffected |
| the zone changes (a laptop travels, `TZ` is edited, then a restart) | later records use the new zone. A few hours of records land in a neighbouring day; nothing is lost or counted twice. Each day's value keeps the zone it was first written in (`tz`, `utc_offset`) |
| the clock is set back | records go to the earlier day's value, which is read and extended as any day |
| Docker | the image sets `TZ=Asia/Shanghai` (`.github/provimux/Dockerfile:49`, upstream's default); days are Shanghai days unless the container sets `TZ` |

The alternative is UTC days: the same everywhere and simpler to explain, but for the owner in Bucharest "today" would turn at 02:00 or 03:00 and "this month" would start that late. Days cannot be re-cut later, because only daily totals are kept; this is open question 2 in `README.md`.

The clock and the zone are injected (`now func() time.Time`, `loc *time.Location`), as `provimuxNow` is for the other endpoints, so tests set both.

## 7. The client key

The key is never written to the store, a log or a response.

- **Id**: the first 16 hex characters of HMAC-SHA-256(`key_secret`, key). `key_secret` is 32 random bytes kept in `provimux:savings:v1:meta`. The process makes a random secret at `Start`. Every round that writes reads `meta` from the store in use and makes the ids with its secret; a store without `meta` gets the process's secret (`KVSetNX`, then `KVGet` for the value that won). The process's secret is never replaced by a store's, so a new store gets the process's own secret, not that of the store used before (but two new stores made by one process share it). The same key gives the same id for the life of the store; another installation gives another id.
- **Tail**: the key's last 4 characters when the key has 16 or more, else none. It is kept per day in the value's `keys` map (id to tail), only for a row that refers to the id: a key whose record folds into `~other` leaves neither id nor tail (T-124 finding 15).
- **No key**: a request without a client key has id `""`.
- **Names**: none in this version. The response's key object has `name: null`; a later version keeps id to name under `provimux:savings:v1:key-names` and fills it, without touching the days.

Why a secret and not a plain SHA-256: the page is the proof the owner will show, so its screenshots travel. A plain hash of a short key is found by trying keys; with 4 of its characters beside it, in seconds. With the secret, an id tells nothing to anyone who does not hold `kv.db`. Why the 16 character floor: the last 4 of an 8 character key are half of it. The holder of the management key can read every client key in the configuration anyway (the console shows them, provimux-ui `src/features/config/components/blocks/ApiKeysCardEditor.tsx:137`); the floor is for what leaves the screen.

Why the deltas are keyed by the key itself: an id can only be made with the secret of the store it is written to, and that store may not be reachable when the record arrives, or may change before the write. Keeping the key in memory until the write (at most 10 s with the store on) means every id that is ever stored was made with that store's secret, also after a switch of `server.kv-store.path`. A query reads `meta` itself (it never writes it), so the ids it shows for deltas are the ones the round then writes. Without a store the process's own secret is used for the ids it shows.

## 8. Corrections from the reviews

### T-130 (after the fourth review, T-127)

| Note of T-127 | Now |
|---|---|
| 7a. One failed idle read of the list copy (or a rejected write of it) left `recording.persistent` false until the next day write, though the days were safely stored | the list copy's errors are tracked apart (`listFailed`); `persistent` follows only the reads and writes of the days |

### T-125 (after the third review, T-124)

| Finding of T-124 | Now |
|---|---|
| 7. After a switch to a populated B without a list copy, the idle round and the flush skipped B because A had been given the list | every round compares the downloaded list with the copy of the store in use; `listSavedAt` is removed |
| 14. A late earlier day was committed, then the `first_day` update failed: `period=all` missed that day for good | `first_day` is moved back before the day is written; a failure keeps the day's deltas |
| 15. Folded keys still added their id and tail to the day | the id and tail are kept only after the fold decision, for a regular row |

Unchanged, as decided: above 2,000 distinct combinations between two successful writes a repeat of a stored row can fold (section 5, "The row limit").

### T-122: deltas instead of totals (after the second review, T-121)

Two reviews in a row found blockers in the in-memory totals, the saver's generation counter and the binding of memory to a store. They are replaced, not hardened: section 5, "Deltas since the last save".

| Finding of T-121 | Now |
|---|---|
| 1. A failed flush before a store switch dropped the unsaved usage | deltas are forgotten only after their write succeeded; what the old store could not take goes to the store in use at the next good round (the old store is left as it was) |
| 5. A query between Bolt's commit and the generation bump counted a request twice | a query and a round hold the same `mu` for their whole length; there is no generation |
| 6. A row was `recorded` while its write was in flight or after it failed | prices are given only when a round writes; a query never gives one, so a row not on disk is `current`; a query waits for a write in flight |
| 8. After a switch from a full A to an empty B, B's first request was folded | the row limit is applied against the rows read from the store being written, at write time |

Removed: the in-memory day state (`stored`, `loaded`, `dirty`, versions, `broken`), `bindLocked` and the bound store, the write generation and the query's re-read loop, the saver-only lock, the cached `meta` and list copy, the eviction of old days. The one-line flush in `applyKVStoreConfig` stays: it is no longer needed for correctness, but it puts usage made before a reload into the store it was made under and writes it before a reload closes the store.

### T-116 (after the first review, T-112)

The T-116 rows of findings 1 and 5 are superseded by T-122 above; 6's rule (a price given while reading is not the day's) holds, now also for a write in flight or failed.

| Finding of T-112 | Now |
|---|---|
| 1. A switch of the store path overwrote the new store's days with the old store's rows | section 5, "When the store is off or failing": flush before the reload, then the state is bound to the active store |
| 5. A query whose read straddled the first write of a day counted its records twice | a write generation moves with every successful day write and every store switch; a query whose copy of memory and read of the store straddle one reads again, the third time holding the saver's lock |
| 6. With the store off, prices moved with the list but were labelled `recorded` | a price given while reading is not the day's: rows without a recorded price are valued with today's list and counted in `floating_requests`, `basis` `current`, until the saver writes the day |
| 7. A list whose first download completed after the last write was never saved | every round, even one with no day to write, takes the kept list (`ProvimuxOpenRouterPricesKept`, no download) and saves it when it is newer than the saved copy; a clean stop runs such a round |
| 8. At exactly 2000 combinations a repeat took a slot and the next new one folded | a repeat of a stored row takes no slot |
| 9. A Kimi login through its Claude Messages route counted under `claude` | the account's provider, section 2 |
