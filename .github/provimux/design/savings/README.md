# Savings: daily usage totals, list prices, one management endpoint, one console page

Design for task T-101, 2026-11-20. This folder is the design only: no code exists yet. A backend worker builds the server side from it, a console worker the page, and a reviewer checks both (`05-build-tests-review.md`).

Read from `main` at `ff02809c` (provimux) and `49c94b56` (provimux-ui). Every `file:line` is in those commits. The code study it started from is research 01 of T-98; what that study and the brief assumed and the code does not bear out is under "Premises corrected".

Tags: **[read]** in code, **[ran]** executed, **[infer]** reasoned and not verified.

## What is designed

The owner's goal is "every AI request costs as little as it can, and you always know why". Savings is the proof: what caching saved, how much ran on subscriptions instead of paid keys, what the paid keys really cost.

| Part | What it is | Document |
|---|---|---|
| Recording | a usage plugin that adds every usage record to the totals of its day, in memory, and writes each changed day to the local store as one value | `01-recording.md` |
| Prices | OpenRouter's public list, kept from the download Provimux already has code for, and a small table of Provimux's own; how a model finds its price; the formulas | `02-prices.md` |
| Endpoint | `GET /v8/management/provimux/savings`: a range of days, totals, breakdowns, the prices used | `03-endpoint.md`, `openapi.yaml` |
| Console page | who it is for, what it must show and never claim | `04-console-page.md` |
| Build | files, the code's frame, commits, tests, the reviewer's list | `05-build-tests-review.md` |

## Files in this folder

| File | Fixes |
|---|---|
| `01-recording.md` | what is taken from a usage record, the token rules, the store's keys and growth, restart and crash behaviour, the day and its zone, the client key |
| `02-prices.md` | the two price sources, the one line that keeps OpenRouter's prices, the table's format, the matching of model ids, prices kept per day, the formulas, what the estimate does not know |
| `03-endpoint.md` | parameters, how the answer is built, errors |
| `04-console-page.md` | the page's purpose, its states, the copy line, the TypeScript types |
| `05-build-tests-review.md` | files and seams, the frame, commits, the tests of both builds, the reviewer's checks, acceptance |
| `openapi.yaml` | the contract a build is checked against (OpenAPI 3.1). Its own file: `../pui13/openapi.yaml` is left as it is, under the same rules |
| `schemas/day.schema.json` | the value stored per day |
| `schemas/prices-table.schema.json` | Provimux's own price table |
| `examples/days.json` | three stored days |
| `examples/savings-month.json`, `examples/savings-empty.json` | complete answers; the first is what `days.json` gives |
| `examples/prices-table.json` | the table's format, with a model that does not exist |

## Decisions taken in this design

| # | Decision | Why |
|---|---|---|
| D1 | A new package `internal/provimux/savings`, fed by a named usage plugin, started when the management routes are registered | No guarded path changes. Usage records already carry everything and reach every plugin whatever `usage-statistics-enabled` says. Starting with the routes records exactly when a management key exists, and keeps other packages' tests free of it |
| D2 | The plugin adds to a map under a mutex and returns; a separate goroutine does all store I/O and never holds the mutex across it | Records are already delivered off the request path, by one goroutine for all plugins (`sdk/cliproxy/usage/manager.go:728-816`). Savings must not stall that goroutine either |
| D3 | One store value per day, holding absolute totals, written whole | The store cannot list keys, and every write is an fsync of about 9 ms whatever its size [ran]. A whole value is atomic and idempotent: no torn day, no double count |
| D4 | Written every 10 s while it changes, and once more on a clean stop | A crash loses at most 10 s of records; a clean stop loses none. One added line flushes before the store closes |
| D5 | A day is the server's local calendar day of the request's start; the answer names the zone and the server's today | "Today" and "this month" then mean what the owner's clock says. Open question 2 |
| D6 | A row is day, account kind, provider, **host**, model, client key. Host is added to the owner's list | Through OpenRouter's Anthropic-format endpoint the provider is `claude`; only the host says OpenRouter, and the owner's fourth number is OpenRouter's real cost. It is the host of an API key's base URL, as model-routes shows it; empty for logins |
| D7 | A client key is stored as an HMAC with a secret of the installation (16 hex characters) and its last 4 characters, the latter only for keys of 16 characters or more | The page is made to be shown. A plain hash of a short key beside 4 of its characters is found by trying keys. Open question 5 |
| D8 | Token counts come from the normalised `token_breakdown` only; the 5 minute and 1 hour parts of cache writes from the detail's own fields | Providers disagree on whether input includes cached tokens; the breakdown is already exclusive. Claude's `CachedTokens` falls back to cache writes and must not be read |
| D9 | OpenRouter's prices are kept by one added line in the owner's OpenRouter patch file plus a new file; the saver asks the catalog to refresh under the catalog's own rules; the last list is saved in the store | No second download, timer or policy. Open questions 3 and 4 |
| D10 | Each day keeps the prices it was first valued with; a row recorded without a price is valued with today's list and marked | A past month's saving must not move when a price changes; a model priced later still gets a value |
| D11 | Only tokens and reported costs are stored. Every estimate is computed when asked | The formulas can be corrected without touching the data. Reported cost is stored in billionths of a dollar, as integers |
| D12 | Saved by caching = all input at the input price, minus what the cached mix costs. It is net of the cache-write premium, can be negative, and is zero where no cache-read price is published | The honest number; a cache that is written and not read costs money |
| D13 | A request is one usage record: one attempt at one upstream | That is what exists (`usage_helpers.go:637-645`). Counting client requests would need a second source and a way to join them |
| D14 | One call answers totals and every breakdown the page needs; `group` names them. Responses only grow | One consistent snapshot per page load; the compatibility rule of PUI-13 (D9 there) |
| D15 | Only the local bbolt store is written, never Home's. With the store off the totals live in memory and the answer says so | In home mode every management route is 404; Home's store is not Savings' to fill |
| D16 | A day holds at most 2000 rows; beyond that, new combinations fold into model and key `~other` | Bounds a day at about 430 KB against a client that invents model names or keys; totals stay right |
| D17 | Recording starts only when a management key is configured; once started it continues while management is off, only the endpoint is gated (T-116) | Without one nobody can read the page; it is also the rule of upstream's usage queue (`internal/api/server.go:285`) |

## Premises corrected

| The brief or the study said | The code says | Consequence |
|---|---|---|
| "CPA already downloads OpenRouter's `/api/v1/models`" | Only when something looks a model up, which today is an OpenRouter entry; never under `--local-model` or home mode (`internal/registry/openrouter_models.go:58-68`, `:103-123`) | A Provimux without an OpenRouter entry has no list. D9 makes the saver ask for it; that is a new outbound request for those installations (open question 3) |
| persist with `KVIncrBy`/`KVMSet` | `KVClient` has no way to list keys (`internal/kvstore/types.go:26-37`), and `KVIncrBy` is one fsynced transaction per counter: 8.8 ms each [ran] | One value per day (D3) |
| "every request yields a `coreusage.Record`" | every upstream attempt does; a retried request yields several, a side model one more | D13, and the page says "attempts" |
| the store can simply be written at any time | on shutdown the store is closed before the usage manager stops (`sdk/cliproxy/service_lifecycle.go:396`, `:404`) | the one added line in `sdk/cliproxy/kv_store.go` (D4) |
| a client key hash "plus its last 4 characters" | client keys can be any string, including short ones | D7's floor |
| OpenRouter publishes input, output, cache read, cache write | it also publishes a 1 hour cache-write price (36 models) and higher prices above a prompt size (88 models) [ran] | the first is used; the second cannot be applied to daily totals (`02-prices.md` section 7) |

## Open questions for the chief and the owner

Each has a default that the design is written for; none blocks the build.

1. **The copy line.** Decided by the owner (T-107): headline "Worth of prompt caching", line "Prompt caching through Provimux was worth $X this month at API list prices."; `04-console-page.md` section 5. The question as it was asked: the owner's words were "Provimux saved me $X this month". The design takes X as the money saved by caching (the headline) and appends "with prompt caching (estimate at OpenRouter list prices)", so the line is true when it stands alone. Alternatives: the bare line; or X = caching plus the list-price value of subscription requests, which is a much larger number and not a saving, because the subscription's fee is unknown. I recommend the caching number with the tail.
2. **Whose day.** Server local time (the design) or UTC. Local makes "today" the owner's day; Docker users get Shanghai days unless they set `TZ`, the image's default (`.github/provimux/Dockerfile:49`). UTC is the same everywhere and turns the day at 02:00 or 03:00 in Bucharest. **This cannot be changed after data exists.** I recommend local.
3. **A new outbound request.** With Savings, a Provimux without an OpenRouter entry downloads the public model list from openrouter.ai, at most every 3 hours and only while requests are recorded (after a failed download, the catalog's own retry: no sooner than a minute later); never under `--local-model`. No key and nothing about the installation is sent. Without it those installations have no prices. I recommend it, and one line about it in the README's Savings section.
4. **One line in the OpenRouter patch file.** `provimuxKeepOpenRouterPrices(data)` in `internal/registry/openrouter_models.go` is a layer line inside a file of the owner's patch, as the failure note in `live_model_lists.go` was in PUI-13 (question 1 there, option B). The alternative is to put the price parsing into the patch itself, below the layer, which rewrites the stack. I recommend the line.
5. **The tail of short keys.** The design shows the last 4 characters only for keys of 16 characters or more, and the first 4 of the id otherwise. If the owner's own keys are shorter and he wants to recognise them by their ending, lower the floor; below 12 I would not.

Not asked, but worth knowing: recorded days are kept forever and nothing prunes them (the owner's decision; 2.4 MB a year for one person, `01-recording.md` section 5).

## What I ran

| Check | Result |
|---|---|
| `kvstore.OpenBoltStore` in a temporary directory, Apple M4 Pro: `KVSet` of a day of 30, 180 and 2000 rows; `KVIncrBy`; `KVMGet` of 365 days and decoding them | 9.1, 8.3 and 9.3 ms; 8.8 ms; 1.9 ms and 16 ms for 65,700 rows. Values: 6,484 B, 38,434 B, 427,094 B |
| `GET https://openrouter.ai/api/v1/models`, 2026-11-20 | 477 models; which `pricing` fields exist and on how many (`02-prices.md` section 2); 469 have a usable price; the saved copy would be 28,294 B |
| A prototype of the model matching (`02-prices.md` section 4) against that list | resolves 130 of the 182 ids of `internal/registry/models/models.json` and 39 of the 52 of `devin_models.json`; every resolved pair read and found to join the same model name |
| A script applying `03-endpoint.md` section 3 and the formulas to three days of rows | wrote `examples/days.json`, `savings-month.json` and `savings-empty.json`; the worked example of `02-prices.md` section 6 agrees with it |
| `openapi.yaml` through `openapi-spec-validator`; both answers against its schemas with unknown properties forbidden; the days and the table against their JSON Schemas (`jsonschema`, draft 2020-12) | valid OpenAPI 3.1.0; 0 errors everywhere; an answer with four planted faults (a missing key, a bad day, a wrong type, an extra key) gave four errors; a table with three planted faults gave three |
| `go list -deps` of `sdk/cliproxy/usage`, `internal/kvstore`, `internal/registry`, `internal/logging` | none reaches `internal/api`, the management handlers or `sdk/cliproxy`: the new package can be imported by all three |
| A usage plugin with `usage-statistics-enabled` off | not run again: `../pui13/README.md`, "What I ran" |

The prototype, the scripts and the benchmark were scratch files and are in no commit. To repeat the contract check: `pip install openapi-spec-validator jsonschema pyyaml`, validate `openapi.yaml`, then validate an answer against `#/components/schemas/SavingsResponse` with `additionalProperties: false` added to every object schema (merge the `allOf` of `BreakdownRow` first).

The owner's server on 127.0.0.1:8317 was not contacted.

## What the build changed (T-107)

The backend was built from this folder (commits "keep OpenRouter's list prices" to "savings endpoint for the console"). The chief decided the five open questions as their defaults: the copy line's X is the caching amount, and the owner then worded the headline "Worth of prompt caching" and the line "Prompt caching through Provimux was worth $X this month at API list prices." (it includes subscription requests at list price, so it is not money saved; the formulas and the field `saved_by_caching_usd` are unchanged); days are the server's local days (the README's Docker part says to set `TZ` first); the new outbound request is taken and documented; the one line in `openrouter_models.go` and the one in `kv_store.go` are as designed; tails only for keys of 16 characters or more. Where the build had to depart from or complete the design:

| Point | Design | Built | Why |
|---|---|---|---|
| Prices at `Start` | `ProvimuxOpenRouterPrices()` once at `Start` (`02` section 2) | not at `Start`; only in rounds with a day to write | Reviewer check 19 says the list is downloaded only while requests are recorded; the call at `Start` made every server with a management key, and every test binary that builds the API server, contact openrouter.ai with nothing recorded. So that the answer still knows `remote` and a list downloaded after the saver's last round, the registry file has one more function, `ProvimuxOpenRouterPricesKept()`: the same three values, without asking for a refresh; `Query` reads it |
| Price id of a table `price` entry | not specified | `provimux:<provider>/<model>` | must be unique in a day's `prices` map beside OpenRouter ids; now in `02` section 3 |
| "no `/api/v1` in the body" (`05` section 4, carries no secret) | absolute | holds except `prices.openrouter.url`, which the contract fixes as `https://openrouter.ai/api/v1/models` | the test removes that one URL before looking for `/api/v1` |
| `costn` | 1 when `Cost` was present (`01` section 4) | 1 when `Cost` is present and not negative | a negative cost is ignored, so it is not counted as a reported cost either |
| A local store that cannot be opened | saver: memory only (`01` section 5); 503 only for a read error (`03` section 3) | saver as designed; the endpoint answers 503 | it is "on and could not be read" in `openapi.yaml`'s words; the console keeps the page and shows the error |
| Query before the saver reached the store | reads `meta` (`01` section 7) | reads `meta` and the saved OpenRouter list in one `KVMGet`, never writes | the answer then reports the saved list's `fetched_at` from the first request on |
| `recording.persistent` | the last write, before it the last read | the same, and a failed read of the saver also makes it false until a write succeeds | |
| Row limit at record time | new combinations beyond 2000 fold | `HandleUsage` cannot know key ids of pending rows, so it folds by the count of stored plus pending rows, and for a day already read it looks the row up by id first; the write applies the limit exactly | keeps `HandleUsage` free of I/O |
| Cross-package tests | records published through the usage manager | `sdk/cliproxy`'s test hands its record to `savings.RecordForTest` | `kv_store_test.go` calls `Service.Shutdown`, which stops that binary's default usage manager; the end-to-end test in `test/provimux` still goes through `coreusage.PublishRecord` |
| `prices.json` | entries where a public source exists | empty (`"models": []`, valid) | no entry was added without a source the reviewer can open; the candidates of `02` section 3 stay unpriced and are listed in the answer |

Unchanged and checked by the tests: `examples/savings-month.json` and `savings-empty.json` are what the code answers for `examples/days.json` (except `observed_at`, `recording.saved_at` and `prices`), and every row of the matching table in `02` section 4 resolves as written against OpenRouter's list of 2026-11-20 (`internal/provimux/savings/testdata/openrouter-models.json`).
