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

`HandleUsage` runs on the usage manager's goroutine. It reduces the record to the fields below, takes the package's mutex, adds the numbers to a map, and returns. It does no I/O and never waits for the disk: the saver never holds that mutex across a store call. The same shape measured 84 ns per record with no allocation in PUI-13 (`../pui13/README.md`, "What I ran").

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
| `provimux:savings:v1:meta` | `{"v":1,"created_at":"<RFC 3339>","first_day":"<YYYY-MM-DD>","key_secret":"<base64 of 32 random bytes>"}` | once; again only when a record lands on a day before `first_day` |
| `provimux:savings:v1:prices:openrouter` | the last OpenRouter price list that was downloaded, `02-prices.md` section 2 | when a downloaded list differs from the saved one |

No key has a TTL (`KVSet` without `EX`/`PX` stores no expiry, `bolt_store.go:296-367`), so the sweeper never removes them. The `v1` in the key is the layout's version; a counter added later goes into the same value (readers ignore what they do not know, absent means 0).

Dates in a range are computed, so reading a range is one `KVMGet` of its day keys; `meta.first_day` tells `period=all` where to start.

**Only the local store.** The saver takes `kvstore.CurrentKVClient()` (`controller.go:102-126`) and uses it only when it is a `*kvstore.BoltStore`. In home mode that function returns Home's client (`:108-110`); Savings never writes there, and in home mode every management route is 404 anyway (`server_management.go:219-222`). It does not use the `KV...Required`/`BestEffort` helpers, which go wherever `CurrentKVClient` points.

### Absolute totals, written whole

In memory, per day:

| Part | Holds |
|---|---|
| `stored` | the day's rows as they are in the store, keyed by (kind, provider, host, model, key id) |
| `loaded` | whether `stored` has been read from the store in this process |
| `pending` | what records added since, keyed by (kind, provider, host, model, **the client key itself**) |
| `dirty` | `stored` differs from the store |

`HandleUsage` only adds to `pending`. Every 10 seconds, and on `FlushNow`, the saver first makes sure it has read `meta` (or written it, the first time on a store), then does, for each day with `pending` or `dirty`:

1. If not `loaded`: read the day's key (outside the mutex), then under the mutex put its rows into `stored` and set `loaded`. A read error leaves the day for the next round.
2. Under the mutex: turn `pending` into rows keyed by key id (section 7), add them to `stored`, give rows without a price one if there is one (`02-prices.md` section 5), empty `pending`, set `dirty`, and take a copy of `stored`.
3. Outside the mutex: encode the copy and `KVSet` it. On success clear `dirty` unless the day changed meanwhile; on failure it stays `dirty`.

Consequences:

- **A write is one transaction of one value**: it happens entirely or not at all. There is no half-written day and no counter that is ahead of another.
- **Nothing is counted twice.** The value is the total, not a difference; writing it again changes nothing. A day is read from the store once per process, before its first write.
- **A crash or `kill -9` loses at most what was recorded since the last write**: up to 10 seconds of records. The page does not show that loss; it is the price of not touching the disk per request.
- **A clean stop loses nothing that reached the recorder.** `Service.Shutdown` stops the API server (`sdk/cliproxy/service_lifecycle.go:370`), then closes the store (`:396`), then stops the usage manager (`:404`). One added line calls `FlushNow` just before the store closes (`05`, section 2). A record still in the usage manager's queue at that instant is lost; the server has stopped accepting requests by then.
- **Write volume**: at most one write of the changed days every 10 s while there is traffic, none when idle. bbolt reuses the pages of the replaced value, so the file grows with the days kept, not with the writes.

Days more than 2 days old with nothing pending and nothing dirty leave memory; a late record for one starts a new `pending` and the day is read again before its write.

### When the store is off or failing

| Situation | Behaviour |
|---|---|
| `server.kv-store.disabled: true` (`internal/config/config_types.go:483-490`), or home mode | records stay in `pending`, in memory; the endpoint answers from memory with `recording.persistent: false`; at most 400 days are kept, the oldest dropped first |
| the store cannot be opened (`CurrentKVClient` returns an error) | the same, retried every round; when it opens, `pending` is merged into what the store holds and written |
| a write fails (disk full) | the day stays `dirty` and is retried every round; `recording.persistent` is false until a write succeeds |
| the store is switched off at run time (config reload, `sdk/cliproxy/kv_store.go:18-22`) | as the first row from then on; days already in memory keep their totals |
| `server.kv-store.path` changes at run time (config reload) | before the reload changes the store, `applyKVStoreConfig` flushes to the store in use (one line, `provimuxFlushSavings()`). The in-memory state is bound to the store it describes: on the first use of the new store, what memory held as stored is dropped, meta, the key secret and the saved list are read again from the new store, and each day is read from it before its next write, so the new store's history is extended, never replaced (T-116) |
| a day's value cannot be decoded | it is not overwritten: the day is skipped by the saver and by reads, an error is logged once per day with the key, no value. `pending` for it is kept in memory |

### Growth

A row is about 215 bytes of JSON [ran: 6,484 B for 30 rows, 38,434 B for 180]. A day has one row per combination that actually occurred.

| Use | Rows a day | A day | A year |
|---|---|---|---|
| one person: 3 client keys, 8 models, logins and one API key | 30 | 6.5 KB | 2.4 MB |
| a small team: 10 keys, 12 models | 180 | 38 KB | 14 MB |
| the limit, every day | 2000 | 427 KB | 156 MB |

Nothing is ever deleted (the owner's decision). The price list copy adds about 30 KB once.

**The row limit.** A day holds at most 2000 rows (`stored` and `pending` together). Once reached, a record whose combination is new is added to the row with its kind, provider and host, model `~other` and key `~other`, and the day is marked `folded`. Those `~other` rows come on top of the 2000, one per kind, provider and host, which the configuration bounds. This bounds the value against a client that sends many model names or keys; totals stay right, only their split by model and key is lost for the overflow.

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

- **Id**: the first 16 hex characters of HMAC-SHA-256(`key_secret`, key). `key_secret` is 32 random bytes kept in `provimux:savings:v1:meta`. The process makes a random secret at `Start`; when it first reaches the store it takes the secret of `meta` if there is one, and otherwise writes its own as `meta` (`KVSetNX`, then `KVGet` for the value that won). The same key gives the same id for the life of the store; another installation gives another id.
- **Tail**: the key's last 4 characters when the key has 16 or more, else none. It is kept per day in the value's `keys` map (id to tail).
- **No key**: a request without a client key has id `""`.
- **Names**: none in this version. The response's key object has `name: null`; a later version keeps id to name under `provimux:savings:v1:key-names` and fills it, without touching the days.

Why a secret and not a plain SHA-256: the page is the proof the owner will show, so its screenshots travel. A plain hash of a short key is found by trying keys; with 4 of its characters beside it, in seconds. With the secret, an id tells nothing to anyone who does not hold `kv.db`. Why the 16 character floor: the last 4 of an 8 character key are half of it. The holder of the management key can read every client key in the configuration anyway (the console shows them, provimux-ui `src/features/config/components/blocks/ApiKeysCardEditor.tsx:137`); the floor is for what leaves the screen.

Why `pending` is keyed by the key itself: an id can only be made with the stored secret, and the store may not be reachable when the first record arrives. Keeping the key in memory until the write (at most 10 s with the store on) means every id that is ever stored was made with the stored secret. A query that comes before the saver's first round reads `meta` itself (it never writes it), so the ids it shows for `pending` rows are already the stored ones. Without a store the process's own secret is used for the ids it shows.

If the store's file is replaced at run time by one with another secret (the path is changed in the configuration), days already loaded are written with the ids they have; the same key can then appear under two ids for those days. Not handled further.

## 8. Corrections from the review (T-116)

| Finding of T-112 | Now |
|---|---|
| 1. A switch of the store path overwrote the new store's days with the old store's rows | section 5, "When the store is off or failing": flush before the reload, then the state is bound to the active store |
| 5. A query whose read straddled the first write of a day counted its records twice | a write generation moves with every successful day write and every store switch; a query whose copy of memory and read of the store straddle one reads again, the third time holding the saver's lock |
| 6. With the store off, prices moved with the list but were labelled `recorded` | a price given while reading is not the day's: rows without a recorded price are valued with today's list and counted in `floating_requests`, `basis` `current`, until the saver writes the day |
| 7. A list whose first download completed after the last write was never saved | every round, even one with no day to write, takes the kept list (`ProvimuxOpenRouterPricesKept`, no download) and saves it when it is newer than the saved copy; a clean stop runs such a round |
| 8. At exactly 2000 combinations a repeat took a slot and the next new one folded | a repeat of a stored row takes no slot |
| 9. A Kimi login through its Claude Messages route counted under `claude` | the account's provider, section 2 |
