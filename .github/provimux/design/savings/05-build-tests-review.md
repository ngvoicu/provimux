# Building it: files, seams, commits, tests, and what the reviewer checks

Two builds: the backend in provimux, then the page in provimux-ui. The page can be built against `examples/savings-month.json` before the backend ships; it appears only against a Provimux that has the endpoint.

## 1. The rules they must fit

Those of `../pui13/04-layer-commits-tests.md` section 1: every commit carries the trailer `Provimux-Layer: branding`; no commit changes `internal/runtime/executor/`, `internal/auth/`, `internal/misc/`, `internal/translator/` or `sdk/cliproxy/session/` (`.github/provimux/guard.sh:28`); the layer is new files plus single lines in existing ones. Importing a guarded package is not a change to it.

Repository conventions that apply (provimux `AGENTS.md`): JSON through `github.com/MoeForge/moego/json`, never `encoding/json`; no standard `regexp`; `gofmt`; shadowed errors named `errXxx`; no panics in handlers; no secrets in logs; no wall-clock `time.Sleep` in tests about time (inject the clock); after changes `go build -o test-output ./cmd/server && rm test-output`.

## 2. Backend: files

| File | New | Package | Holds |
|---|---|---|---|
| `internal/provimux/savings/savings.go` | yes | `savings` | `Start`, `FlushNow`, `Query`, the default instance, the options tests inject |
| `internal/provimux/savings/record.go` | yes | `savings` | the usage plugin: a record's fields (`01` section 2 to 4) |
| `internal/provimux/savings/day.go` | yes | `savings` | the day value: types, encode, decode, the row limit (`schemas/day.schema.json`) |
| `internal/provimux/savings/saver.go` | yes | `savings` | the saver's round: read the store in use, add the deltas, write, forget them; `meta`; the price list copy (`01` section 5) |
| `internal/provimux/savings/clientkey.go` | yes | `savings` | key id and tail (`01` section 7) |
| `internal/provimux/savings/prices.go` | yes | `savings` | the prices in use: OpenRouter's list, its saved copy, the table (`02` sections 1 to 3) |
| `internal/provimux/savings/prices.json` | yes | | Provimux's table, embedded (`schemas/prices-table.schema.json`) |
| `internal/provimux/savings/modelid.go` | yes | `savings` | from a recorded model to a price (`02` section 4) |
| `internal/provimux/savings/estimate.go` | yes | `savings` | the formulas (`02` section 6) |
| `internal/provimux/savings/query.go` | yes | `savings` | parameters, range, groups, filters, the response types (`03`) |
| `internal/registry/provimux_openrouter_prices.go` | yes | `registry` | the prices of the downloaded catalog (`02` section 2) |
| `internal/api/handlers/management/provimux_savings.go` | yes | `management` | `(*Handler).ProvimuxSavings` |
| `sdk/cliproxy/provimux_savings.go` | yes | `cliproxy` | `provimuxFlushSavings()`: calls `savings.FlushNow()` |
| `internal/api/server_provimux.go` | the layer's own, **+2 lines** and an import | `api` | the route, and `savings.Start()` |
| `internal/registry/openrouter_models.go` | **1 line added** | `registry` | `provimuxKeepOpenRouterPrices(data)` at `:198` (the owner's patch file; open question 4) |
| `sdk/cliproxy/kv_store.go` | **2 lines added** | `cliproxy` | `provimuxFlushSavings()` in `shutdownKVStore`, before `kvstore.ShutdownLocal()` (`:27-33`), and in `applyKVStoreConfig` before a reload changes the store (T-116; kept by T-122, not needed for correctness) |

Imports are free of cycles [ran: `go list -deps`]: `savings` needs `sdk/cliproxy/usage`, `internal/kvstore`, `internal/registry` and `internal/logging`; none of them reaches `internal/api`, `internal/api/handlers/management` or `sdk/cliproxy`, which import `savings`.

### The frame

```go
// Package savings records daily usage totals and values them at list prices, for the console's
// Savings page. Design: .github/provimux/design/savings/.
package savings

// Start registers the usage plugin "provimux-savings" and starts the saver. Idempotent.
// Called when the management routes are registered; before it, nothing is recorded.
func Start()

// FlushNow writes every changed day to the local store and returns when that is done, or after
// 2 seconds. Safe to call at any time and more than once; a no-op before Start.
func FlushNow()

// ErrStoreUnavailable: the local store is on and could not be read.
var ErrStoreUnavailable = errors.New("savings: store unavailable")

// ParseRequest reads the endpoint's query. Its error text is the 400 answer's message.
func ParseRequest(values url.Values) (Request, error)

// Query answers a request from memory and the store. It writes nothing and starts no download.
func Query(ctx context.Context, request Request) (*Response, error)

// Request is a parsed query: a period or a from/to pair, the group values, the filters.
// A nil filter is absent; a pointer to "" is present and empty.
type Request struct {
	Period   string
	From, To string
	Groups   [][]string
	Kind, Provider, Host, Model, Key *string
}

// Response is the endpoint's answer: the properties of SavingsResponse in openapi.yaml, with those
// names as JSON tags. Slices are never nil ("breakdowns": [], not null).
type Response struct{ /* as openapi.yaml */ }
```

Inside the package, the parts the tests name:

```go
// localStore is the part of *kvstore.BoltStore Savings uses. Tests wrap a real one to block or fail.
type localStore interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSet(ctx context.Context, key string, value []byte, opts kvstore.KVSetOptions) (bool, error)
	KVSetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
	KVMGet(ctx context.Context, keys ...string) ([][]byte, []bool, error)
}

// currentLocalStore returns kvstore.CurrentKVClient() when it is a *kvstore.BoltStore; nil when the
// local store is off or the client is Home's; an error when the local store cannot be opened.
func currentLocalStore() (localStore, error)

// options are what tests replace: the clock, the zone, the store, the prices, the interval.
type options struct {
	now      func() time.Time
	location *time.Location
	store    func() (localStore, error) // currentLocalStore
	// openRouter is registry.ProvimuxOpenRouterPrices.
	openRouter func() (map[string]registry.ProvimuxOpenRouterPrice, time.Time, bool)
	interval time.Duration // 10 s
}

// HandleUsage implements coreusage.Plugin. No I/O; one mutex; never held across a store call.
func (r *recorder) HandleUsage(ctx context.Context, record coreusage.Record)

// sampleOf reduces a record to what is stored. Pure.
func sampleOf(ctx context.Context, record coreusage.Record, location *time.Location, now time.Time) sample

// saveRound is one round of the saver: 01-recording.md section 5, steps 1 to 3, for every day
// with pending or dirty rows; then the price list copy.
func (r *recorder) saveRound(ctx context.Context)

// keyID is the first 16 hex characters of HMAC-SHA-256(secret, key); "" for an empty key.
func keyID(secret []byte, key string) string

// keyTail is the key's last 4 characters when it has 16 or more, else "".
func keyTail(key string) string

// resolvePrice is 02-prices.md section 4.
func (b *priceBook) resolvePrice(provider, model string) (ref priceRef, price price, ok bool)

// amounts is 02-prices.md section 6: the cost and the cost without the cache, in dollars.
func amounts(c counters, p price) (cost, withoutCache float64)
```

### The three lines in files the layer does not own

```go
// internal/api/server_provimux.go (the layer's own file)
func (s *Server) registerProvimuxRoutes(v8 *gin.RouterGroup) {
	savings.Start() // <- added
	v8.GET("/provimux/model-routes", s.mgmt.ProvimuxModelRoutes)
	v8.GET("/provimux/live-lists", s.mgmt.ProvimuxLiveLists)
	v8.GET("/provimux/savings", s.mgmt.ProvimuxSavings) // <- added
}
```

```go
// internal/registry/openrouter_models.go, fetchOpenRouterModels (:198)
	provimuxKeepOpenRouterPrices(data) // <- added
	return parseOpenRouterModels(data)
```

```go
// sdk/cliproxy/kv_store.go, shutdownKVStore (:27-33)
	s.kvStoreManaged.Store(false)
	provimuxFlushSavings() // <- added: write the day totals while the store is still open
	return kvstore.ShutdownLocal()
```

`Start` lives in the route registration because that is the one place the layer already owns that runs exactly when the page can exist: when a management key is configured (`internal/api/server.go:285-287`). It adds no line to an upstream file, and test binaries that never build the API server record nothing.

## 3. Backend: commits

Five layer commits, in this order, each with the trailer, each building and passing its tests alone.

| # | Subject | Adds | Edits |
|---|---|---|---|
| 1 | `feat(provimux): keep OpenRouter's list prices` | `provimux_openrouter_prices.go`, its test | `openrouter_models.go` +1 |
| 2 | `feat(provimux): prices and estimates for Savings` | `prices.go`, `prices.json`, `modelid.go`, `estimate.go`, their tests | |
| 3 | `feat(provimux): record daily usage totals` | `savings.go`, `record.go`, `day.go`, `saver.go`, `clientkey.go`, `sdk/cliproxy/provimux_savings.go`, their tests | `kv_store.go` +1, `server_provimux.go` +1 |
| 4 | `feat(provimux): savings endpoint for the console` | `query.go`, `provimux_savings.go`, their tests, `test/provimux/savings_test.go` | `server_provimux.go` +1 |
| 5 | `docs(provimux): Savings` | | `.github/README.md`: a short section; the files of this folder where the build made the design wrong |

They ship in one Provimux release, by the existing automatic flow; the release notes name the path.

## 4. Backend: tests

All in new `provimux_*_test.go` files or in the new package, so no upstream test file changes. Stores are real: `kvstore.OpenBoltStore` in `t.TempDir()`. Time is the injected clock; rounds of the saver are called directly, never waited for.

### `internal/registry/provimux_openrouter_prices_test.go`

| Test | Asserts |
|---|---|
| parses the five prices | strings in dollars per token become dollars per million; a missing cache field is nil; `overrides` is ignored |
| keeps what has a price | a negative `prompt` or `completion` drops the model; zero is kept; a model without limits or efforts, which `parseOpenRouterModels` drops (`:231-233`), is kept |
| a bad body changes nothing | not JSON, no `data`, or no priced model: the previous prices and time stay |
| the download keeps prices | with `openRouterModelsURL` on a test server: after `fetchOpenRouterModels` the prices are there, and its own result equals the result without the added line |
| asks by the catalog's rules | policy says no (`--local-model`): no fetch, `remote` false. Fresh catalog: no fetch. Stale catalog: exactly one fetch, in the background, and the call returned before it finished |
| registers no interest | after calls, a changed catalog notifies no provider |
| prices do not re-register models | two catalogs that differ only in a price are the same for `sameOpenRouterModels` |

### `internal/provimux/savings/modelid_test.go`, `prices_test.go`, `estimate_test.go`

| Test | Asserts |
|---|---|
| every row of the table in `02` section 4 | the id, the source and the `match` named there, against a fixed list of OpenRouter ids in `testdata/` |
| the steps in order | a table entry wins over an exact id; a provider entry wins over `*`; `same_as` to an id the list lacks is unpriced and tries no later step |
| what is not rewritten | `-fast`, `-highspeed`, `-preview`, `-latest`, `-1m` are not removed; at most two effort suffixes are; an unknown prefix is unpriced; a negative price is not used |
| `dotted` | the five examples of `02` section 4 |
| no `regexp` | the package's source files do not import it |
| the table file | `prices.json` satisfies `schemas/prices-table.schema.json`'s rules: one of `price` and `same_as`, an `https` `source`, lower case, no pair twice, `updated` a day |
| the worked example | 145.32, 646.84, 501.52 and 0.9531 from the counters of `02` section 6 |
| missing prices | no cache-read price: reads save nothing. No cache-write price: no premium. No 1 hour price: valued as cache write |
| a negative saving | writes without reads give a negative `saved_by_caching` |
| the split is only a part | `cw5` changes no amount; `cw1h` moves only the 1 hour part |
| rounding at the end | 1,000 rows of 0.0000004 dollars sum to 0.0004, not 0 |

### `internal/provimux/savings/record_test.go`

| Test | Asserts |
|---|---|
| kind | `oauth`, `apikey`; empty and any other value are `unknown` |
| host | only for `apikey`; a base URL with user info, a port, a path and a query gives the host alone, lower case; a login with a base URL has none |
| provider and model | trimmed, lower case, cut at 64 and 128 bytes; empty is `unknown` |
| tokens from the breakdown | a valid breakdown is copied bucket by bucket; an invalid one is made by `EnsureTokenBreakdownForProvider`; an unknown provider's tokens are `unc` only |
| the Claude quirk | a detail with `CachedTokens` 5000, `CacheReadTokens` 0, `CacheCreationTokens` 5000: `cr` 0, `cw` 5000 |
| the lifetime split | nil is 0; `cw5 + cw1h > cw` records both as 0 |
| failure | `Failed`; or status 500 in the context with `Failed` false; 200 is not a failure. A failed record's tokens are counted |
| cost | 0.0123 becomes 12,300,000; a negative cost is ignored; `ucost` only with `IsBYOK` true; `costn` |
| the day | 23:59:59 and 00:00:00 local fall on two days; a fixed zone is used, not the machine's; the two daylight-saving days; a zero `RequestedAt` uses the clock |
| nothing else is read | a record whose `Source`, `AuthID`, `Fail.Body`, `ResponseHeaders` and `APIKey` hold marker strings: after a write, none of the markers is in the store's file |
| no I/O | with a store whose calls block, 1,000 `HandleUsage` calls return while a round is stuck in a write (holding `mu`) |
| before `Start` | a published record is not recorded |

`BenchmarkHandleUsage`, `BenchmarkHandleUsageDuringSave` (a round blocked in a write) and `BenchmarkHandleUsageWithSaves` (rounds back to back on a real file) are reported in the commit message (time per record, allocations); no test asserts a time.

### `internal/provimux/savings/saver_test.go`

| Test | Asserts |
|---|---|
| totals are absolute | 3 records, a round: the value holds their sums; 2 more, a round: the sums of 5. The same totals encode identical bytes; after a write memory holds nothing for the day |
| idle | a round with no deltas makes no store call |
| restart | a second instance on the same file, 1 record, a round: old totals plus 1. The day is read once per round that writes it |
| crash | an instance dropped without `FlushNow`: a new one sees exactly the last written totals |
| clean stop | `FlushNow` writes the deltas; with a store that blocks it returns within its bound |
| store off | memory only, `persistent` false, answers still come; at most 400 days are kept |
| store appears later | the deltas are added to the totals the file already has for that day, and written |
| write fails | the deltas stay in memory and are written by a later round; `persistent` false, then true |
| a value that does not decode | is not overwritten; reads skip the day; one log line, without the value |
| only the local store | `currentLocalStore` returns nil for a `KVClient` that is not a `*kvstore.BoltStore` (home mode), and no call reaches that client |
| row limit | 2,001 models: 2,000 rows and `~other`, `folded` true, and the sums equal the sums of all records |
| written days leave memory | after a write no day is kept, whatever its age; a late record for an old day is added to the file, not written over it |
| `meta` | made once; `first_day` only moves earlier; the secret and every key id are the same after a restart |
| key id and tail | 16 hex characters; another store gives another id; a tail only from 16 characters; `""` for no key |
| the key never reaches the disk | after a write the store's file does not contain the client key |
| ids use the stored secret | records taken before the store was reachable are written with ids made from the secret already in the file; on a new file the process's secret becomes the file's; a query before the first round shows the ids the round then writes, and writes nothing |
| a day keeps its prices | a row gets `pm` on its first write with a price; a later price change leaves the day's `prices` alone; a row first written without a price gets one on a later write of that day |
| the price list copy | written when the list differs, not when it is the same; read at start; the later `fetched_at` wins |
| the zone changes | a second instance in another zone files new records by its zone and leaves an existing day's `tz` |
| concurrent | under `-race`: 8 goroutines of records, rounds and queries together; the totals are exact |
| `Start` twice | one plugin, one saver |

### `internal/provimux/savings/deltas_test.go` (T-122)

| Test | Asserts |
|---|---|
| failed flush before a switch loses nothing (T-121 finding 1) | A's write and the reload's flush fail, the path moves to B: A is byte-identical, B has its own rows plus the waiting delta, 5 recorded = 5 stored, memory empty |
| a query and a save do not interleave (T-121 finding 5) | a round paused right after Bolt committed: a query waits and then counts the request once; a query paused at its read: the round waits; both orders give 1 |
| `recorded` only from disk (T-121 finding 6) | after a failed write the row is `current`, floating, and moves with the list; once written, `recorded` at the price of the write; a query during a write in flight waits and then says `recorded` |
| a full store does not fold in the next (T-121 finding 8) | A at 2000 rows, switch to an empty B: B's first request is a regular row, priced; back on A a repeat merges and a new combination folds |
| failed write kept, then written | three rounds of a failing write: one log line, the deltas wait, `persistent` false; a failing read keeps them too; the next good round writes all, memory empty, `persistent` true; a new streak logs again |
| a query during saves counts once | under `-race`: records, back-to-back rounds on a slow store and queries; every answer lies between the records done before it started and after it ended, cache reads match |
| a switch lands the deltas in the new store | A gets no store call after the switch and keeps its bytes; B holds the waiting deltas, with ids of B's secret |
| store off, then on | off: no write, `persistent` false, floating, the process's ids; on: the answer adds the deltas to the file with the file's ids; the round writes them and forgets them |

### `internal/provimux/savings/query_test.go`

| Test | Asserts |
|---|---|
| periods | for a fixed today, each `period` gives the range of `03` section 2: `last_month` in January, 29 February, a month's first day, the two daylight-saving days; default `month` |
| `all` | starts at `first_day`; at an earlier day held only in memory; at today when nothing is recorded |
| every 400 | each case listed in `openapi.yaml`, with a message naming the parameter, and no store call before it |
| **the examples** | the three values of `examples/days.json` in a store, today 2026-11-20, zone Europe/Bucharest, the request of `03` section 4: the answer equals `examples/savings-month.json` except `observed_at`, `recording.saved_at` and `prices`. An empty store and `period=today`: `examples/savings-empty.json`, with the same exceptions |
| sums agree | for each single dimension, the rows of its breakdown add up to `totals` in every counter and amount; `priced_requests + unpriced_requests = requests` in every row |
| deltas are included | a record not yet written is in the answer, and the answer equals the one after a round (a new row is floating until written) |
| order | the same on repeated calls; time first, then requests, then the dimension values |
| truncation | 2,001 combinations: 2,000 rows, `truncated`, the ones with the most requests |
| filters | each alone and two together; `host=` and `key=` select rows without one; an absent parameter does not filter; filters narrow `models`, `reported_cost_hosts` and `folded_days` |
| the model list | most tokens first; `basis` `recorded`, `current` and `mixed`; `price` null for an unpriced model and for `~other`; 501 pairs set `models_truncated` |
| floating | a day recorded without prices is valued once a list exists, counted in `floating_requests`, and the query wrote nothing |
| no prices | every amount 0, `unpriced_requests` = `requests`, `fetched_at` null |
| a key's tail | from the latest day that has the id |
| reads do not write | the store's file is byte-identical before and after 100 queries |

### `internal/api/handlers/management/provimux_savings_test.go`, `internal/api/server_provimux_test.go`

| Test | Asserts |
|---|---|
| 200 | the body has every required property of `SavingsResponse`; `breakdowns` and `models` are `[]`, not `null` |
| 400 and 503 | a wrong parameter is 400 with `{"error": ...}`; a store read error is 503, not 404 |
| carries no secret | with a client key, a base URL `https://user:secret-pass@OpenRouter.ai:443/api/v1?token=secret-token`, an account email as `Source` and an auth ID on the records: none of them and no `/api/v1` is in the body; `host` is `openrouter.ai` |
| access control | the path joins the table of `TestProvimuxManagementRoutesShareAccessControl` (`server_provimux_test.go:66`): 200, 401 without a key, 404 with management off, 404 in home mode |
| recording starts with the routes | after the server registered its routes, a published record is recorded |

### `sdk/cliproxy/provimux_savings_test.go`, `test/provimux/savings_test.go`

| Test | Asserts |
|---|---|
| a clean stop writes | a service with a store, one record, `Shutdown`: the file opened again holds the day |
| end to end, statistics off | with `usage-statistics-enabled` off, records published through `coreusage.PublishRecord` reach the endpoint's totals. The test waits for the dispatcher with a plugin of its own registered after Savings', not with a sleep |
| the real parsers | usage payloads of Claude (cache creation without a read; a read; a 5 minute and 1 hour split), an OpenAI-compatible answer with `prompt_tokens_details.cached_tokens` and OpenRouter's `cost`, Codex, and Gemini with `cachedContentTokenCount`, each through its parser in `internal/runtime/executor/helps` and then the recorder: `in + cr + cw` equals the upstream's input total as that provider counts it, `cr` equals its cached count, and for Claude `CachedTokens` is 5000 while `cr` is 0 |

### What the repository's own checks do with this

`provimux-guard.yml`: the trailer on all five commits, no guarded path. `provimux-check.yml`: `go test ./...` against upstream's; the new tests are additions and a new failure fails it. The sync check rebuilds the stack after each upstream release; the two places this leans on unexported names (`openRouterModels.lookup` and `fetchOpenRouterModels` in `internal/registry`, `shutdownKVStore` in `sdk/cliproxy`) fail at compile time there if they are renamed.

## 5. Console: files and tests

In provimux-ui; read its `AGENTS.md`. Purpose and contract: `04-console-page.md`.

| File | New | Holds |
|---|---|---|
| `src/provimux/pages/savings/SavingsPage.tsx`, `SavingsPage.module.scss` | yes | the page |
| `src/provimux/pages/savings/logic.ts` | yes | everything the page derives, as pure functions: the headline and its two parts, the key label, the states of `04` section 4, the copy line, days filled with zeros, the unpriced list |
| `src/provimux/api/types.ts` | edit | the types of `04` section 6 |
| `src/provimux/api/normalize.ts` | edit | `readSavings` |
| `src/provimux/api/provimuxApi.ts` | edit | `PROVIMUX_PATHS.savings`, `savings(client, period)`, `probeSavings(client)` |
| `src/provimux/presence.ts` | edit | `savings` in `ProvimuxEndpoint`, the state and `PROBES` |
| `src/provimux/nav.tsx`, `routes.tsx`, `components/ProvimuxIcons.tsx` | edit | the nav item, the gated route, an icon |
| `src/provimux/locales/en.json` | edit | `provimux.savings.*`, `provimux.nav.savings`, `provimux.nav_meta.savings` |
| `tests/provimux/fixtures/savings-month.json`, `savings-empty.json` | yes | copies of this folder's examples, byte for byte |
| `tests/provimux/savings.test.ts` | yes | below |
| `tests/provimux/api.test.ts`, `presence.test.ts`, `i18n.test.ts` | edit | below |

No file outside `src/provimux/` and `tests/provimux/` changes; the layer commits carry the console's trailer and pass its guard.

| Test | Asserts |
|---|---|
| reads both fixtures | `readSavings` keeps every number; a missing property is null or 0 as the contract says; an unknown `kind`, `source` or `match` survives as a string |
| the request | `group` is repeated (`group=kind&group=key`), never `group[]`; the period is sent as given; the probe is `?period=today` |
| presence | 404 hides the page and its nav item; 503 and a network error keep it, with the error; a later 200 clears it |
| the headline | the amount and its API-key and subscription parts from the `kind` rows; found by `by`, not by position; a missing `kind` breakdown shows the total alone |
| key labels | the tail; the first 4 of the id without a tail; "no key"; "other keys"; a name when the server sends one. The markup holds no more than 4 characters of any fixture id |
| each state of `04` section 4 | detected from a fixture changed for it: nothing recorded, nothing in the period, unpriced, floating, no price list (with and without `remote`), an old list, a negative saving, not persistent, folded days |
| the copy line | rounded down (1429.74 gives `$1,429`; 42.189 gives `$42.18`); the period words; "OpenRouter list prices" only when every source is `openrouter`; no line and a reason when the amount is under a cent, negative, or nothing is priced |
| the words that keep it honest | in the static markup of the month fixture: "estimate" is with every estimated amount; the price list and its date are there; the unpriced model `swe-2` is named; the headline items come in the owner's order |
| the empty fixture | no amount, no copy button, the explanation of when recording starts |
| days | missing days are zero; the range is the answer's `from` and `to`, never the browser's date |
| strings | every key the page uses exists in `en.json` |

`bun run verify` before handoff. The route is opened in a browser against a Provimux that has the endpoint, in light and dark, with screenshots; if none is available the handoff says so.

## 6. What the reviewer checks

The claims a critical reader should try to break, each with where to look.

**That it cannot slow or break a request**

1. `HandleUsage` makes no store call and takes only `inMu`, which guards the incoming map and is never held across anything else; `mu`, which rounds and queries hold across their store calls, is never taken by `HandleUsage` (read `record.go` and `saver.go` for every `Lock`). Before the mutex it reads the account's provider from the model registry (`registry.ProvimuxClientProvider`, a read lock, no I/O; T-116).
2. A stuck disk stalls only the saver: the "no I/O" test, and the usage manager's queue does not grow because of Savings.
3. A panic in the plugin is recovered by the manager (`manager.go:818-825`); the saver's goroutine recovers and logs its own.

**That the totals are right**

4. No request is lost or counted twice across a restart, a failed read or write, a late record, a store switch or a query during a save (the saver and deltas tests). Memory holds only deltas since the last successful write: `grep` `saver.go` for any state describing a store; there should be none but `listSavedAt`, which only saves a store call. Kill the process between requests and compare with what was sent.
5. Token buckets come from `TokenBreakdown` only. `git grep` the package for `CachedTokens`, `InputTokens`, `OutputTokens`: none outside tests.
6. Breakdown rows add up to the totals; priced plus unpriced is all.
7. The examples are reproduced by the code, not pasted into a test's expected value by hand.

**That the money is honest**

8. Every amount under `estimate` comes from `amounts()`; nothing else multiplies a price. The saving subtracts the write premium and can be negative.
9. A model without a published cache-read price claims no saving. An unpriced model is in no amount and is listed.
10. Each entry of `prices.json` has a `source` that says what the entry says. Open them.
11. Each `normalized` match in a real answer names the same model as the recorded id. A wrong match is a wrong price shown as a fact.
12. A recorded day does not change when the price list does; a floating one says it can.
13. The page says "estimate", names the price list, and does not call the subscription figure a saving. The copy line never rounds up.

**That nothing leaks**

14. The store's file, the logs and the answers contain no client key, provider key, token, email, auth ID or base URL path (the marker tests; then `strings kv.db | grep` for a real key on a test installation).
15. A key under 16 characters has no tail. The id cannot be recomputed without `kv.db` (it is an HMAC, not a hash).
16. The page shows at most 4 characters of an id.

**That it stays a layer**

17. `git diff <base of the layer>..HEAD --stat`: new files; `server_provimux.go` +2 and an import; one line in `openrouter_models.go` and two in `kv_store.go` (the flush in `shutdownKVStore`, and since T-116 the flush in `applyKVStoreConfig` before a reload changes the store; T-122 keeps it, see `01` section 8); nothing under a guarded path. The guard passes.
18. Stock CLIProxyAPI still answers 404 on the path. A server started without management records nothing and starts no goroutine. Once started, recording continues while management is off (the chief's decision in T-116: the totals go to the user's own local store); only the endpoint is gated, and answers 404 then. A server started without management and given a key later starts recording then.
19. A Provimux without an OpenRouter entry downloads the list from openrouter.ai at most once per 3 hours (after a failure, no sooner than a minute later), only while requests are recorded, and never under `--local-model` (count the requests against a test server).
20. Nothing is written to Home's store.

**That the contract holds**

21. Real answers validate against `openapi.yaml` with unknown properties forbidden (how: `README.md`, "What I ran"). Any property the build added is in the file.
22. The owner's live server on 127.0.0.1:8317 was not used for any of this: tests and checks run against a build started on another port with its own auth directory.

## 7. Acceptance

1. The path answers as `openapi.yaml` says; `examples/*.json` are reproduced by the tests.
2. After a restart the page shows the same totals as before it.
3. `go test ./...` has no new failure; `go build ./cmd/server` passes; the guard passes.
4. On a running build: a request through a subscription login and one through an OpenRouter key appear within a second in `?period=today`, the first under `oauth` with an estimate, the second under `apikey` with a reported cost close to its estimate.
5. With `--local-model` and a fresh auth directory: requests and tokens are recorded, amounts are 0, and the answer says there is no price list.
6. The console page shows the six numbers in the owner's order with their sentences, hides on a stock server, and its copy line matches the headline.
