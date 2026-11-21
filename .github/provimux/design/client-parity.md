# Request parity: a login through its vendor's own client, and through pi and Provimux

Written on 2026-11-22 for spec item CP-02. The owner's rule: where a login's own client and the pi coding agent
through Provimux measure different cache lifetimes, we find out why, and fix Provimux or pi-provimux where they ask
differently.

Nothing was sent for this document: no request to a provider, a login or a proxy, and no client was run. No
credential file and none of the owner's configuration was read, so what the owner's own `config.yaml` and credential
files set is not known here; where it matters, the text says so.

**Where each statement comes from.**

| Mark | Source |
|---|---|
| *(src)* | Provimux at `ecd046439` (`provimux/main`, on CLIProxyAPI v8.1.0), path and line |
| *(codex)* | `openai/codex` at tag `rust-v0.170.0`, paths under `codex-rs/` |
| *(pi)* | pi 1.6.0 (`@earendil-works/pi-ai`, `pi-coding-agent`) and pi-provimux at `cef8aee` |
| *(bin)* | Strings of the installed binary: Devin CLI 3000.14.30821, Muse Code 1.4.25, Claude Code 2.1.330, Antigravity CLI. A string shows that a name exists in the client, not when it is sent |
| *(doc)* | The vendor's published documentation or terms, read on 2026-11-22 |
| *(run)* | groundtruth's raw result files and `observed.json` |
| *(inferred)* | A conclusion from the above, not read directly |
| *(open)* | Not established; section 9 names what settles it |

## 1. The answer in short

### 1.1 The differences, row by row

`all_warm_through_s` per client. "Checks" are warm checks of all checks, over the ages both runs have.

| Login | Model | Own client | pi through the proxy | Checks, own / pi | Verdict |
|---|---|---|---|---|---|
| codex | gpt-6.1-terra | 1800 | 900 | 13 of 30 / 10 of 30 | **Chance.** One check apart at 30 min, 1 h and 2 h |
| codex | gpt-6.1-sol | 300 | 1800 | 18 of 30 / 20 of 30 | **Chance.** The sign is the opposite of Terra's |
| devin | claude-opus-5-5 | 300 | 300 | 2 of 14 / 2 of 14 | **No difference.** The plain probe's 2400 is a misreading (3.2) |
| devin | glm-5-3 | 7200 | 900, 300 | 10 of 10 / 6 of 10, then 15 of 25 | **Not established.** pi's misses are whole ages, not old ages |
| devin | gpt-6-1-terra | none | 1800 | 0 of 12 / 6 of 12 | **The instrument.** The Devin CLI reported no cached token even at 30 s |
| devin | grok-4-7 | 900 | none | 4 of 10 / 5 of 10 | **Chance** |
| devin | nemotron-3-5-super | 7200 / none | none / 120 | 10 of 10 / 0 of 10 | **Explained: two different models.** `-none` against `-medium` (3.2) |
| meta | muse-wonder-1.0 | none | 7200, 14400 | 0 of 21 / 21 of 21 | **Real, and the other way round.** Muse Code cached no conversation; the 14400 is the owner's 24 h rule (3.3) |
| codex | gpt-6.1-terra, the plain probe in the own client's place | cold in 2.5 to 5 min | 15 to 30 min | | **Not established.** Same executor; the callers' requests differ (3.1) |

Two limits of the data apply to every row:

- An age has two or three checks, and the samples of one age are primed within seconds of each other and checked
  within seconds of each other, so they are not independent. A row of ten checks is closer to five observations.
- The paired runs of 7 and 8 November started within a minute of each other, on the same login. A row from two
  different days (GLM's second pi run, the plain probe against pi) compares days as well as clients.

### 1.2 Where Provimux asks differently from the own client

Only what bears on a cache. Identity is section 5.

| Login | The own client asks | Provimux asks, for pi | Bears on the measured lifetime |
|---|---|---|---|
| codex | A cache key that is the session id, also sent as the `session-id` header; no lifetime | The same: pi's session id as key and as `Session-Id`; no lifetime | No |
| devin | Nothing a user can set; a model variant per reasoning level | The same wire fields. A reasoning level the model lacks is moved to one it has | Yes, on Nemotron |
| meta | A cache key, no retention | pi's cache key; `prompt_cache_retention: "24h"` where the owner's payload rule adds it | Yes: 2 h without the rule, 4 h with it |
| claude | 1 hour inside the plan's included usage, 5 minutes in overage | 1 hour always | Only in overage (predicted, not measured) |
| antigravity | Not compared: Google's terms end the question (section 5) | | |

### 1.3 What follows

- **No change to Provimux's code is established by these measurements.** Where a row holds a real difference, the
  cause is a setting (Devin's reasoning variant, the owner's Meta rule), the own client's instrument (Devin on
  GPT-6.1 Terra) or the own client itself (Muse Code).
- **One change is proposed for a predicted difference** (P-1: a Claude login in overage), and it is the owner's
  choice.
- **One check is proposed in pi-provimux** (X-1: a thinking level the model lacks).
- **Most of the work is in the measurement** (section 6 and 7): the same model variant on both sides, the Devin
  CLI's process model and keepalive recorded, and samples of one age spread in time.

## 2. The path of a request

```
pi  ──(1)──>  Provimux  ──(2)──>  the vendor
own client  ──────────────────>  the vendor
```

Hop (1) is the same for every login but Claude's *(pi)*:

- pi-provimux registers one pi API for everything that is not a Claude model, `cliproxyapi-codex-responses`: pi's own
  `openai-codex-responses` client, patched (`extensions/codex-stream.ts:26`, `extensions/index.ts:536`). GPT, Devin,
  Muse and Gemini models all leave pi in the Codex Responses format, to `<proxy>/backend-api/codex/responses`.
- Transport `auto`: a WebSocket to that path first, SSE as the fallback (`codex-stream.ts:1029`; pi-ai
  `openai-codex-responses.js:488-498`).
- Claude models that pi's catalog knows go as `anthropic-messages` to `<proxy>/v1/messages` (`extensions/lib.ts:635`,
  `:1124`, `:1162`).

What pi puts in a Codex-format request that bears on a cache (pi-ai `openai-codex-responses.js`):

| Field | Value | Line |
|---|---|---|
| `prompt_cache_key` | pi's session id, cut to 64 characters; absent only with `cacheRetention: "none"` | 181-182, 422 |
| `session-id`, `x-client-request-id` headers | the same session id | 1326-1331, 1341-1343 |
| `originator` header | `pi` | 190 |
| `store` | `false` | 417 |
| `include` | `["reasoning.encrypted_content"]` | 421 |
| Retention | none: this client has no retention field and ignores `PI_CACHE_RETENTION` | whole file |
| `previous_response_id` | only on a WebSocket reused inside one pi process | 1186, 1203 |

pi's session id is a UUIDv7 per session, or the id given with `--session-id`; a resumed session keeps it
(`pi-coding-agent/dist/core/session-manager.js:13`, `:705`, `:731`). So the cache key of a resumed pi session is the
key of its first turn. pi's cache warmer sends nothing for these models: it stops when a model declares no cache
lifetime, and the extension declares none (`core/cache-warmer.js:67-71`) *(inferred)*.

Provimux takes both routes with the handler type `openai-response`
(`sdk/api/handlers/openai/openai_responses_handlers.go:884`), so the executors below see pi's `prompt_cache_key` as
the caller's.

## 3. Per login

### 3.1 `codex-subscription`: Codex CLI and pi

**What Codex CLI 0.170.0 sends** *(codex)*.

| What | Value | Where |
|---|---|---|
| Endpoint | `https://chatgpt.com/backend-api/codex/responses` | `model-provider-info/src/lib.rs:87` |
| `prompt_cache_key` | The session id (for a new thread, the thread id). The same after `codex exec resume`, after compaction and in sub-agents; new after a normal fork | `core/src/client.rs:712-733`, `core/src/session/session.rs:996-1064` |
| `session-id` header | The same value as the cache key. The source says: "ChatGPT derives cache affinity from the Responses session-id header" | `core/src/client.rs:733-742`, `codex-api/src/requests/headers.rs:25-34` |
| `thread-id`, `x-client-request-id` headers | The thread id | `headers.rs:31`, `codex-api/src/endpoint/responses.rs:87-88` |
| Lifetime | `prompt_cache_options.ttl` exists (`"30m"`, `"12h"`), but the CLI never sets it: the app-server that `codex exec` and the TUI go through passes `prompt_cache_ttl: None` | `protocol/src/turn_input.rs:223-228`, `app-server/src/request_processors/turn_processor.rs:729` |
| `store`, `stream` | `false`, `true` | `core/src/client.rs:1299-1304` |
| Base prompt | A `developer` message at `input[0]`, not `instructions` | `core/src/client.rs:1192-1224` |
| `client_metadata` | `x-codex-installation-id`, `session_id`, `thread_id`, `x-codex-window-id`, turn metadata | `core/src/responses_metadata.rs:337-380` |
| Identity | `originator: codex_exec`, `User-Agent: codex_exec/0.170.0 (…)`, `version`, `chatgpt-account-id` | `exec/src/lib.rs:279`, `login/src/auth/default_client.rs:159-175` |
| Transport | A WebSocket by default, also in `codex exec`; kept between the turns of one process; only new items with `previous_response_id` while the socket lives | `model-provider-info/src/lib.rs:611`, `core/src/client.rs:1336-1343`, `:1679-1708` |
| Warm-up | A request with `generate: false` at every session start, carrying the base prompt and tools only | `core/src/session/session.rs:2172`, `core/src/session/startup_prewarm.rs:401` |
| A resumed session | The same ids; the full history rebuilt from the rollout file and sent on a new socket | `core/src/session/session.rs:996-1048` |
| Keepalive | None | |

**What Provimux sends for pi** *(src)*.

| What | Value | Where |
|---|---|---|
| Endpoint | The same | `codex_executor_stream.go:50-52`, `:131` |
| `prompt_cache_key` | The caller's, unchanged: pi's session id | `codex_executor_request.go:168-172`, `:190-192` |
| `Session-Id` header | The same value | `codex_executor_request.go:230-232` |
| `X-Client-Request-Id` | pi's, forwarded: the session id | `codex_executor_request.go:597` |
| `Thread-Id`, `Version`, `X-Codex-Window-Id`, `X-Codex-Turn-Metadata` | Forwarded when the caller sends them; pi sends none | `codex_executor_request.go:594-601` |
| Lifetime | Removed for a login, whatever the caller sent: `prompt_cache_retention` and `prompt_cache_options` | `internal/translator/codex/openai/responses/codex_openai-responses_request.go:66`, `codex_executor_stream.go:86`, `codex_executor_request.go:482-487` |
| `store`, `stream` | `false`, `true` | `codex_openai-responses_request.go:32` |
| `previous_response_id`, `generate` | Removed on the HTTP path | `codex_executor_stream.go:84-85` |
| `client_metadata` | None added | |
| `X-Codex-Routing-Hint` | `model=<slug>`, as the CLI | `codex_executor_request.go:636-672` |
| Identity | `Originator: codex-tui` and `User-Agent: codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)`, forced unless `disable-codex-cloaking` is set; pi's `originator: pi` is overwritten | `codex_executor_request.go:32-33`, `:706-712` |
| Transport to OpenAI | HTTP with SSE, unless pi came by WebSocket **and** the credential has `websockets: true`; then a WebSocket upstream too | `codex_websockets_executor.go:42-47`, `:83`, `:96`, `:147-172` |

Whether the owner's Codex credential has `websockets: true` is not known here.

**The differences, and what they mean for a paused session.**

| Difference | Bears on the cache of a paused session |
|---|---|
| Cache key and `session-id` | None: both are a session id that survives a resume, sent as key and as header |
| Lifetime | None: neither asks |
| Warm-up at session start | No. It warms the base prompt and tools, not the conversation. It shows in the first request: a new Codex session read 13,184 of 20,964 tokens from cache on its first request, pi 0 of 4,794 *(run: H3, H4 baselines; the link to the warm-up is inferred)* |
| WebSocket with `previous_response_id` | No: a resumed session is a new process and a new socket on both sides |
| `thread-id`, `client_metadata`, `version`, originator, User-Agent | Identity, not cache fields; whether OpenAI's backend treats clients differently by them is *(open)* and not something this document proposes to test by imitation (section 5) |
| Size | Codex CLI's own prompt is about 21,000 tokens, pi's about 4,800 *(run)* |

**What the measurements say** *(run: `harness-2026-11-07`, started 12:16 to 12:18 UTC)*.

| Age | Terra, Codex CLI (H3) | Terra, pi (H4) | Sol, Codex CLI (H1) | Sol, pi (H2) |
|---|---|---|---|---|
| 5 min | 3/3 | 3/3 | 3/3 | 3/3 |
| 15 min | 3/3 | 3/3 | 2/3 | 3/3 |
| 30 min | 3/3 | 2/3 | 3/3 | 3/3 |
| 1 h | 2/3 | 1/3 | 2/3 | 2/3 |
| 2 h | 2/3 | 1/3 | 3/3 | 2/3 |
| 4 h | 0/3 | 0/3 | 1/3 | 3/3 |
| 6 h | 0/3 | 0/3 | 1/3 | 2/3 |
| 8 h | 0/3 | 0/3 | 2/3 | 1/3 |
| 10 h | 0/3 | 0/3 | 1/3 | 1/3 |
| 12 h | 0/3 | 0/3 | 0/3 | 0/3 |
| All | 13/30 | 10/30 | 18/30 | 20/30 |

`all_warm_through_s` is the first age with a miss, so one miss moves it by a whole step: Terra's "1800 against 900"
is the single miss at 30 minutes, Sol's "300 against 1800" the single miss at 15 minutes. Fisher's exact test on the
totals gives p = 0.60 and p = 0.79, and it flatters a difference because the checks are not independent. Both
clients show the same picture: misses from 15 or 30 minutes on, no hit past 2 hours on Terra, hits to 10 hours on
Sol. **Chance.**

**The plain probe against pi.** Both go through the same executor, so whatever differs was already different when
the request reached Provimux:

| | Plain probe (`X2`, `X2b`, `LC2`, `CL2`) | pi (`H4`) |
|---|---|---|
| Prompt | About 2,700 tokens, different from its first token in every sample | About 11,900 tokens: 4,800 that every session shares, then the sample's block |
| Check | The identical request sent again | The conversation with one more turn |
| Cache key, upstream `Session-Id` | One per sample | One per session |
| `X-Client-Request-Id` | None | The session id |
| Reasoning | Not set | `low`, with `include: ["reasoning.encrypted_content"]` |
| Tools, base prompt | None | pi's |
| Date | 6 and 17 November | 7 November |

On Terra the plain probe missed already at one minute (`X2b`: 1 of 3 at 1 min, 2 of 3 at 2 min, 0 of 3 at 4 min)
while its control, sent again within seconds, hit. That is not a cache that ran out with age. Which of the rows
above causes it is *(open)*. OpenAI's guide says no more than this about where a request lands: "On models before
GPT-5.6, use a stable prompt_cache_key", and "On GPT-5.6 and later, OpenAI handles cache routing automatically"
*(doc)*. Groundtruth's own records point at size: on other routes a control with about 2,500 tokens read nothing
where about 8,000 tokens did (`cpa-latest-2026-11-06/L1` and eight OpenRouter runs of 18 November). The cause is
not in Provimux's handling, and no change follows; pair CX-S of section 7 separates the rows.

### 3.2 `devin`: Devin CLI and pi

**What the Devin CLI sends** *(bin, 3000.14.30821, unless marked)*.

| What | Value |
|---|---|
| Endpoint | Connect over HTTPS, binary protobuf: `<server>/exa.api_server_pb.ApiServerService/GetChatMessage`; server `https://server.codeium.com` |
| Cache fields in the request | Not readable: the binary keeps no request field names. The Codeium chat schema that the Antigravity CLI embeds has a per-message `prompt_cache_options` and a per-request `system_prompt_cache_options` (type `CACHE_CONTROL_TYPE_EPHEMERAL`); whether the Devin CLI's request uses them is *(open)* |
| Cache policy per model | Set by Devin's service in the model configuration, not by the user: `OpenAiInferenceConfig` with `extended_prompt_cache_retention`, `disable_prompt_cache_writes`, `disable_explicit_prompt_cache_breakpoints`; breakpoints are recorded with a `ttl` and a `cache_creation_1h_tokens` count |
| Keepalive | `affogato/src/cache_keepalive.rs`: pings that keep a cache alive. On or off, the interval and the number of pings come per model from the service (`ClientCacheKeepalivePolicy`: `ping_interval_seconds`, `max_pings`). One override: `DEVIN_CACHE_KEEPALIVE_MAX_PINGS`. It starts after a generation that ends waiting for an action and stops when the action is answered *(inferred from the log lines and stop reasons)* |
| Process | Since 3000.13.4 "Local sessions now run in a background process", a daemon that "exits on its own about ten minutes after its last session and client are gone" *(doc, the changelog shipped with the CLI)*; `--process-model=daemon\|in-process\|legacy` |
| A resumed session | The messages are kept whole in a local SQLite store, with their message ids and a sequence number per session, and sent again *(inferred from the embedded schema)* |
| Identity | The strings `devin-cli` and `3000.14.30821`; no session header. That they travel in the protobuf metadata is *(inferred)* |

**What Provimux sends for pi** *(src)*.

| What | Value | Where |
|---|---|---|
| Endpoint | The same call | `helps/devin_wire.go:40` |
| Session id (field 15.1) and cascade id (field 16, "session-stable prompt cache key") | One value for both: pi's session id from the `session-id` header, mapped to a UUID that is the same on every request of the session. The stream conductor works the id out from the caller's headers and hands it to the executor in the attempt's context | `sdk/cliproxy/auth/conductor_stream.go:640`, `:659`, `sdk/cliproxy/auth/selector.go:2478-2507`, `devin_executor.go:3214-3252`, `helps/devin_wire.go:478-511` |
| Cache fields | None: the request has the system prompt, the messages, the tools, the completion settings, the two ids and the model. `cache_control` markers in the caller's request are dropped | `helps/devin_wire.go:288-522` |
| Message ids | A new random id for every message on every request | `devin_executor.go:2029-2202`, `helps/devin_wire.go:337-340` |
| Turn counter (field 15.2) | Counts the session's requests for as long as the proxy runs | `helps/devin_wire.go:172-186` |
| Model variant | The model id with Devin's effort suffix. A level the model does not have is moved to one it has | `helps/devin_models.go:98-133` |
| Keepalive | None | |
| Identity | Client name `chisel`, version `3000.10.21`, a device fingerprint, no `User-Agent` | `helps/devin_wire.go:42-45`, `:132-152`, `devin_executor.go:114-116` |

**The differences.**

| Difference | What is known |
|---|---|
| Model variant | pi's `low` on Nemotron 3.5 Super becomes `-medium`: Devin has `none`, `medium` and `high` for it. The CLI run asked for `-none` *(run: `devin-cache-investigation-2026-11-09/README.md`)* |
| Random message ids | Do not matter: the same body sent twice read 21,392 of 21,422 tokens from cache *(run, same README)* |
| Explicit cache marks | The own client may set them; Provimux sets none. Devin's service caches Provimux's requests all the same (Claude models hit at 5 minutes through pi as through the CLI), so the service places breakpoints by itself where the request has none *(inferred)* |
| Keepalive and daemon | The own client has both; neither was recorded for the 12 CLI runs. A sample that answers "OK" and ends should not arm the keepalive, but that is *(inferred)*, and the daemon outlives the command by about ten minutes |
| Client version | Provimux declares 3000.10.21, the installed CLI is 3000.14. Whether Devin's service chooses a cache policy by client version is *(open)* |
| Size | The CLI's own prompt is 15,000 to 24,000 tokens, pi's 5,000 to 7,000 *(run)* |

**The rows.**

- **Claude Opus 5.5: no difference.** Devin CLI (H7) and pi (H8) are identical: 2 of 2 at 5 minutes, cold from 30
  minutes on. The plain probe's "2400" (`cpa-8.0.53/D1`, 4 November) is a misreading. Its checks at 2.5 and 5
  minutes report 4,834 of 4,838 prompt tokens cached. Every check from 7.5 minutes to 2 hours reports 558 cached of
  562 prompt tokens, the figures of the priming request itself: CLIProxyAPI 8.0.53 left Devin's cache-write tokens
  out of the prompt total (fixed by `eb6e25abe`, first in v8.0.63; `devin_executor.go:3231-3240`), so a miss showed
  only the shared 558-token prefix, and the probe's rule (cached at least half of the prompt) called it warm. Read
  correctly, D1 is warm through 5 minutes and cold from 7.5 minutes: the same 300.
- **Nemotron 3.5 Super: two different models.** D11c ran `nemotron-3-5-super-none`, D11p ran pi's `low`, which
  Devin served as `-medium`. With reasoning on, the first turn's reasoning is not part of the second turn's prompt
  and the model resumes from an earlier checkpoint, in the Devin CLI too: N1c (`-medium`, CLI) was cold at 2
  minutes, N1p (thinking off, pi) warm 3 of 3 *(run)*. Same variant, same result.
- **GPT-6.1 Terra: the instrument.** The Devin CLI reported 0 cached tokens for this model in every reply, its
  30-second control included (H9), while pi through Provimux read cached tokens on the same login in the same hour
  (H10: 6 of 6 through 30 minutes). Whether Devin does not cache for its CLI there or the CLI does not report it is
  *(open)*; a run with no cached token in any reply measured nothing.
- **Grok 4.7: chance.** 4 of 10 (CLI) against 5 of 10 (pi) the same evening; pi's second run was 4 of 25 with a cold
  control. The route is unreliable through both.
- **GLM-5.3: not established.** The CLI was warm 10 of 10 through 2 hours (D9c). pi was 6 of 10 the same evening
  (D9p: both checks cold at 30 minutes and at 2 hours, both warm at 1 hour) and 15 of 25 two days later (D9r: all
  five cold at 15 minutes and at 2 hours, all five warm at 30 minutes and 1 hour). A cache that is cold at 15
  minutes and warm at 1 hour did not expire; a whole age landed somewhere cold. Two turns through Provimux cached
  fully on GLM *(run, 9 November)*, so the request is cacheable. What is left: chance (five ages, each all or
  nothing, is five coin flips for the CLI), the unrecorded daemon and keepalive, the size, or something in the
  request that this document could not see. Pair DV-2 and DV-5 of section 7 decide.

### 3.3 `meta-subscription`: Muse Code and pi

**What Muse Code sends** *(bin, 1.4.25, unless marked)*.

| What | Value |
|---|---|
| Endpoint | `https://api.meta.ai/v1`, the Responses format over SSE *(inferred from the body's field names)* |
| `prompt_cache_key` | Sent: a provider option `meta.prompt_cache_key`, with a recorded source (`explicit_prompt_cache_key`, `session_sticky_route`, `configured_provider_option`, `absent`) |
| Retention | None: `prompt_cache_retention` does not occur in the binary. Muse Code 1.4.15 did not send it either *(run: `ember-2026-11-10/README.md`)* |
| Cache marks | None: no `cache_control`, no breakpoint |
| Start of the prompt | The base instructions, then reminders that hold the session id, the session's log path and name, and a memory snapshot "for this run" |
| `previous_response_id`, `store` | Options exist; whether a session uses them is *(open)* |
| Keepalive | None |
| Identity | `x-client-id` (`tbh:tui`, `tbh:serve`, …), `x-tbh-root-session-id`, `x-tbh-thread-id`, `x-meta-ai-gateway-session-id` |

**What Provimux sends for pi** *(src)*.

| What | Value | Where |
|---|---|---|
| Endpoint | `https://api.meta.ai/v1/responses` | `internal/auth/meta/meta.go:27`, `meta_executor_execute.go:163` |
| `prompt_cache_key` | The caller's: pi's session id. Provimux derives none of its own | `meta_executor_execute.go:35-103` |
| `prompt_cache_retention` | The caller's when it sends one (pi never does); otherwise what a payload rule of the configuration adds. `config.example.yaml` shows the rule that adds `"24h"` for `muse-*` | `meta_executor_execute.go:105-119`, `config.example.yaml:335-339` |
| Session headers | A new UUID on every request, in `X-Tbh-Session-Id`, `X-Meta-Ai-Gateway-Session-Id`, `X-Tbh-Thread-Id`, `X-Tbh-Root-Session-Id` | `meta_executor_execute.go:407-425` |
| Identity | `User-Agent: muse-build/1.4.14 (non-interactive; …)`, `X-Client-Id: tbh:exec`, Muse Code's tool namespace | `meta_executor.go:28-50`, `meta_executor_execute.go:85`, `:415-416` |

The owner's configuration has had the 24 h rule since 2026-11-10 *(run: the notes of LM1 to LM3)*.

**The differences.**

| Difference | What is known |
|---|---|
| Retention | Muse Code asks none. Through Provimux the Meta login gets `"24h"` on every request since 10 November, by the owner's rule. Meta documents the field for its Responses API as "a hint, not a guarantee" *(doc)* |
| What is cached | On 7 November Muse Code cached its own system prompt and never the conversation: 0 of 21 checks, control included (H5, version not recorded). pi through Provimux cached the conversation 21 of 21 through 2 hours in the same hour, before the rule existed (H6) |
| Session headers | Muse Code's stay the same through a session *(inferred from their names)*; Provimux's change on every request. The cache holds through Provimux all the same, so it does not hang on them |

**The row.** Real, and the other way round: pi through Provimux gets more than Meta's own client. Two things are in
it, and they are separate:

1. *Without the rule* pi got 2 hours (H6, where the schedule ended) and Muse Code nothing. Why Muse Code 1.4.x did
   not cache a conversation is *(open)*: its binary sends a cache key and has a check of its own for a prefix that
   changed (`prefix_cache_claim_mismatch`), so it is built to cache. Runs MC1 to MC3 of groundtruth's plan show
   whether 1.4.25 still does not.
2. *With the rule* pi got 4 hours (M2: warm 3 of 3 at 4 hours, cold at 12). That is a lifetime the own client does
   not ask for.

Nothing here is a fault of Provimux's request. Whether the rule stays is the owner's (section 8).

### 3.4 `claude-subscription`: Claude Code and pi

No own-client run exists yet; groundtruth plans CC1 to CC8.

**What Claude Code sends** *(doc, `code.claude.com/docs/en/prompt-caching`; bin, 2.1.330)*.

| What | Value |
|---|---|
| Lifetime of the main conversation | "Unless you choose a TTL yourself, Claude Code requests the one-hour TTL only on a Claude subscription within your plan's included usage." In overage: "Claude Code drops the main conversation to the five-minute TTL" |
| Everything else (sub-agents, helpers) | Five minutes, with exceptions the server controls |
| A person's choice | `CLAUDE_CODE_PROMPT_CACHE_TTL=5m\|1h`, `FORCE_PROMPT_CACHING_5M`, `ENABLE_PROMPT_CACHING_1H`, `CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL` |
| Session | `X-Claude-Code-Session-Id` header |
| Warm-up on resume | Exists (`CLAUDE_CODE_RESUME_CACHE_WARM`), off by default and only in remote sessions |

**What Provimux sends for pi** *(src)*.

| What | Value | Where |
|---|---|---|
| Cache marks | pi places its own (system, last user message, last tool) with no lifetime *(pi: `anthropic-messages.js:28-42`, `:1119-1136`, `:1407-1427`, `:1521`)*. For a client that is not Claude Code, Provimux owns the placement and completes it | `claude_executor_execute.go:163-177` |
| Lifetime | Every mark without a lifetime becomes `ttl: "1h"` on a login; a lifetime the caller wrote stays. Sub-agent and probe requests are left at five minutes | `claude_executor_execute.go:179-198` |
| Overage | Not looked at. The source says why: Claude Code "gates the 1h selection on OAuth scopes, a non-overage account and an allowlisted internal query source … the credential check is the reproducible half" | `claude_executor_cloaking.go:3042-3049` |
| Identity | Claude Code's: system prompt, headers, device and session identity ("cloaking"), unless switched off | `claude_executor_cloaking.go`, `config.example.yaml:576-581` |

The plain probe through Provimux measured the 1 hour on four models, with and without a `ttl` in the request
*(run: CS1 to CS8, LF1, LF2)*.

**The difference.** Inside the plan's included usage both ask for one hour. In overage Claude Code asks for five
minutes and Provimux still asks for one hour. That is predicted from the documentation and the source; nothing
measured it. One hour in overage is not free: Anthropic's price list has a one-hour cache write at twice the input
price and a five-minute one at 1.25 times *(doc, `platform.claude.com/docs/en/about-claude/pricing`)*, and overage
is billed.

### 3.5 `antigravity`

Not compared. Google's terms call any third-party access a breach (section 5), so this document proposes nothing
that would make Provimux's Antigravity request more like the Antigravity CLI's. For the record: the CLI has a
prompt-cache keepalive that the service switches on (`enable-prompt-cache-keep-alive`,
`prompt_cache_keep_alive_max_pings`) *(bin)*; Provimux sends a session id derived from the caller's session or, failing
that, from the first user message, and no keepalive (`antigravity_executor_request.go:740-783`,
`:823-866`).

## 4. The changes

### 4.1 The rule for a parity change

A parity change makes Provimux or pi-provimux **ask for the cache the own client asks for**: the cache key, the
session affinity, a lifetime, a model variant. It never adds to what makes the request look like the vendor's
client: no client name, version, User-Agent, originator, installation or device id, and no update of the values
Provimux already sends there. Section 5 says why, per login.

By that rule these are **not proposed**, though each would make a request more like the own client's:

| Not proposed | Why |
|---|---|
| Codex: `thread-id`, `version`, `client_metadata` with an installation id, a newer `codex-tui` version | Identity. None is a cache field, and the measured rows are chance |
| Codex: the warm-up request (`generate: false`) | It is an extra request on the login for every session, and it warms only the base prompt. Not a lifetime |
| Codex: a `prompt_cache_options.ttl` for a login | More than Codex CLI asks. OpenAI lists `prompt_cache_retention` as unsupported for ChatGPT-plan usage in its Sign in with ChatGPT preview *(doc)* |
| Devin: the newer client version, explicit cache marks, a keepalive | Identity, or fields whose use by the own client is not established; a keepalive is extra requests on the login |
| Meta: session headers kept through a session | The cache does not hang on them (3.3), and they are Muse Code's identity |
| Antigravity: anything | Section 5 |

### 4.2 P-1 (Provimux): a Claude login in overage asks for five minutes

**What.** The one-hour upgrade of `claude_executor_execute.go:194` applies only while the login is inside its
plan's included usage. While it draws on overage, marks without a lifetime stay at Anthropic's default of five
minutes, as Claude Code leaves them. A lifetime the caller wrote out (pi with `PI_CACHE_RETENTION=long` writes
`"1h"`) stays, as today.

**How.**

- A per-credential state, `inside the plan | in overage | unknown`, learned from the response headers of the
  login's own answers. `helps/claude_ratelimit.go` already reads `Anthropic-Ratelimit-Unified-Overage-Status` and
  `Anthropic-Ratelimit-Unified-Representative-Claim` for another purpose.
- `unknown` behaves as today (one hour): a credential's first request, and a proxy just started, change nothing.
- A setting chooses the behaviour, default today's: `1h` (always, as now) or `follow` (as Claude Code). The patch
  changes no request until the owner sets `follow`.
- It touches `internal/runtime/executor`, a protected path: it is a patch commit below the Provimux layer, and it
  can be offered to CLIProxyAPI.

**Before it is built.** Which header value on an *answered* request says "this request was billed as overage" is
*(open)*: the code today reads those headers on refusals. It is read from recorded response headers of a login in
overage (Provimux's request log), not guessed.

**Risk.**

| Risk | Size |
|---|---|
| A wrong reading of the headers asks five minutes inside the plan | The cache of a pause longer than five minutes is lost: more usage of the plan, no failure. Bounded by the `unknown` rule and by the setting |
| The lifetime changes in the middle of a session when the state flips | One request rewrites the prefix. Claude Code has the same step |
| Identity | None added: the request asks for less |
| The route itself | Section 5: Anthropic does not permit this login in third-party software. P-1 does not change that either way |

### 4.3 X-1 (pi-provimux): a thinking level the model does not have

**What.** pi-provimux builds each model's thinking levels from the proxy's `supported_reasoning_levels` and hides
the others in pi's interface (`extensions/lib.ts:538-561`; README, "Unsupported pi thinking levels are set to
`null`"). `pi -p --thinking low` on a model without `low` still reached Devin as another variant on 8 November,
with an older extension. To check first, then to fix if it is still so: a level the model does not have is refused
with the levels it has, or moved by pi-provimux to a named level and said so, never moved silently by the proxy.

**Why it is a parity matter.** The level is part of what is asked: on Nemotron it decides whether a resumed
session reads its cache at all.

**Risk.** A script that passes a level a model lacks gets an error where it got an answer. No request changes for a
level the model has.

### 4.4 Nothing for Codex, Devin and Meta in Provimux's code

- **Codex:** key, affinity and lifetime are already the own client's. The measured rows are chance.
- **Devin:** the wire has no cache field a caller could set; the one real row is the model variant (X-1, and M-1).
- **Meta:** Provimux's request asks for nothing; the lifetime comes from the owner's rule (section 8, choice 1).

### 4.5 The measurement (groundtruth, not this repository)

| Id | Change | Why |
|---|---|---|
| M-1 | A pair names the same model variant on both sides and records the variant that answered. Through Provimux a Devin id with its effort suffix (`nemotron-3-5-super-none`) is used as given (`helps/devin_models.go:114-117`) | Nemotron |
| M-2 | The Devin CLI runs with `--process-model in-process` (already in groundtruth's `docs/own-clients.md`) and `DEVIN_CACHE_KEEPALIVE_MAX_PINGS=0`, and the run records both | The keepalive and the daemon |
| M-3 | The samples of one age are primed in three waves at least five minutes apart, so that the three checks of an age fall at three different moments | Whole-age misses (GLM, the Codex plain probe) |
| M-4 | A pi run records the Provimux build, pi's version, pi-provimux's commit, the transport pi used (WebSocket or SSE) and the payload rules that touched the request | None of it is recorded for the runs of 7 to 11 November |
| M-5 | `cpa-8.0.53/D1` gets a note or moves to `excluded` | 3.2 |

## 5. Where each vendor's line is

The rule given for this work: never impersonate a vendor's client where the vendor's terms forbid it. What the
terms say today *(doc)*, what Provimux already sends as identity *(src)*, and what this document adds.

| Login | The vendor on third-party software | The vendor on looking like its client | Provimux today | This document adds |
|---|---|---|---|---|
| Claude | Not permitted. "Anthropic does not permit third-party developers to offer Claude.ai login into their own applications, or to route requests through Free, Pro, Max, or Ultra plan credentials on behalf of their users." OAuth "is designed to support ordinary use of Claude Code and other native Anthropic applications" (`code.claude.com/docs/en/legal-and-compliance`) | "Your product should maintain its own branding and not appear to be Claude Code or any Anthropic product" (Agent SDK overview) | Claude Code's system prompt, headers and identity, by default | Nothing. P-1 asks for less |
| Codex | Permitted through Sign in with ChatGPT with the app's own registered client; pi is listed among its open-source integrations. Using Codex CLI's own client from other software: not addressed | The program asks for "your app's actual name"; no sentence forbids the other | `codex-tui` originator and User-Agent by default (`disable-codex-cloaking: false`). A Sign in with ChatGPT credential (`use_siwc`) is sent under Provimux's own name instead (`signin-with-chatgpt-identifier`, `codex_executor_request.go:570-589`) | Nothing |
| Devin | Not addressed. The terms forbid to "reverse engineer, disassemble, decompile, decode, adapt, or otherwise attempt to derive or gain access to any software component of the Services" (Platform Terms, 2026-11-16) | Not addressed | The CLI's wire format, a client name and version, a device fingerprint | Nothing |
| Meta | Forbidden for the subscription key: it "is for use only with the coding harness under your Coding Harness Subscription … and may not be used to access the Services outside the coding harness" (Model API Terms §3.2). A pay-as-you-go key may be used anywhere | Not named; it would be a way around §3.2 | Muse Code's User-Agent, client id and tool namespace | Nothing |
| Antigravity | A breach: "Using third party software, tools, or services to access the Service (e.g. using OpenClaw with Antigravity OAuth) is a breach of this Agreement" (`antigravity.google/terms`). Wrapping `agy` in other agents "is not supported under consumer plans" (FAQ) | Covered by the above | The Antigravity client's `userAgent` and request shape | Nothing |

Three things follow, and the last is the owner's:

- For Claude, Meta and Antigravity the terms forbid the route itself, not only the look. A parity change there
  could only ever be one that asks for less (P-1). None that asks for more, or looks more like the client, is
  proposed for any login.
- For Codex there is a route under Provimux's own name. It was not measured; pair CX-3 of section 7 can measure it.
- Whether the routes whose terms forbid them stay in use is the owner's decision, made before this document and
  not changed by it (section 8, choice 3).

`docs/own-clients.md` section 1.5 in groundtruth found the Antigravity FAQ's two sentences and left open which of
them a measuring script is. The terms page read for this document is stricter than the FAQ: it names third-party
software as a breach. Driving `agy -p` itself from a script is the "direct scripting" the FAQ intends; Provimux
reaching Antigravity is the other thing.

## 6. What a pair must hold fixed

A pair is one own-client run and one pi run through Provimux: the same login, the same model variant, started
within 15 seconds of each other, the same ages.

| Fixed | How |
|---|---|
| Model variant | M-1 |
| Devin's process model and keepalive | M-2 |
| Independence | M-3 |
| What pi and Provimux were | M-4 |
| The owner's payload rules | Named per pair below: a Meta pair runs once without the 24 h rule and once with it |
| The schedule | groundtruth's ladder from 2.5 minutes, three samples per age, the control first |
| One request per check | As groundtruth's `docs/own-clients.md` section 1 |

**When two runs agree.** Decided before the runs: over the ages both have, no age differs by more than one check
of three, and the totals do not differ at the 5 % level by Fisher's exact test. With 14 ages of three checks a pair
can show a difference of about 30 points in the share of warm checks; it cannot show a smaller one. A pair that
disagrees is run once more on another night before anything is concluded from it.

**What cannot be held fixed.** The own client's prompt is three to five times pi's. If the size matters to a
provider's cache (the Codex plain probe suggests it may), a pair can disagree for that reason alone; CX-S measures
it.

## 7. The pairs to measure

Own-client runs are the ones groundtruth's `docs/own-clients.md` section 4.3 already plans, named in brackets. The pi run is
started beside it and takes its schedule: `std` (2 hours, about 90 requests) or `long` (12 hours, about 105).

| Id | Login | Own client | pi through Provimux | Settles |
|---|---|---|---|---|
| CX-1 | codex | `codex-cli`, `gpt-6.1-terra` (CX4) | the same model | The Terra row: chance or not |
| CX-2 | codex | `codex-cli`, `gpt-6.1-sol` (CX3) | the same model | The Sol row |
| CX-3 | codex | `codex-cli`, `gpt-6.2-sol` (CX1) | the same model, twice: the Codex login, and a Sign in with ChatGPT credential if the owner makes one | The newest model, where the plain probe was cold at 5 minutes; and the route under Provimux's own name |
| CX-S | codex | none: two plain-probe runs on `gpt-6.1-terra` the same night, to 30 minutes | (a) today's request; (b) a request shaped like pi's: about 4,800 shared tokens, then the block, `low` reasoning, a check that adds a turn | Whether the plain probe's early misses come from the request's size and shape |
| DV-1 | devin | `devin-cli`, `claude-opus-5-5-low` (DV1) | the same variant | The control pair: expected 300 on both sides |
| DV-2 | devin | `devin-cli`, `glm-5-3-low`, to 2 h (DV9) | the same variant | The GLM row |
| DV-3 | devin | `devin-cli`, `nemotron-3-5-super-none` (DV11) | `nemotron-3-5-super-none` | The Nemotron row, like with like |
| DV-4 | devin | `devin-cli`, `grok-4-7-low` (DV14) | the same variant | The Grok row |
| DV-5 | devin | `devin-cli`, `glm-5-3-low`, a second run in the default process model with the keepalive left on | none | Whether the daemon and the keepalive change what the CLI measures |
| DV-6 | devin | `devin-cli`, `gpt-6-1-terra-low` (DV5), only if smoke run S3 shows cached tokens | the same variant | The GPT-6.1 Terra row. If S3 shows none, the CLI measures nothing there and no pair is run |
| MC-1 | meta | `muse-code`, `muse-wonder-1.0` (MC1) | the same model, twice: without the 24 h rule, and with it | The Meta row, the rule apart from the client |
| CC-1 | claude | `claude-code`, `claude-sonnet-5-5`, `std+65` (CC3) | the same model through `/v1/messages` | That both ask for one hour inside the plan |
| CC-2 | claude | `claude-code`, `claude-haiku-5-5`, `std+65` (CC4) | the same model | The same, on the cheapest model |
| — | antigravity | none | none | Section 5 |

A pair on a route the owner decides not to use (section 8, choice 3) is not run.

Not planned: a Claude pair in overage. It needs the account in overage, which costs the owner money; P-1's
prediction is checked when the account is there for its own reasons, with CC-1's commands.

**The cost**, in what the clients report: up to 13 pi runs and one extra own-client run (DV-5) on top of
groundtruth's plan, and two plain-probe runs. A pi run through Provimux carries 12,000 to 17,000 input tokens per
request *(run)*: 1.1 to 1.6 million tokens per run, of which about two thirds are read from cache while warm. Per
login, about: codex 3 or 4 pi runs and CX-S, 5 million tokens; devin 4 or 5 pi runs and DV-5, 9 million; meta 2
runs, 2.5 million; claude 2 runs, 2.5 million.

**The order.** CX-S first: it is small and tells whether size has to be read into every other pair. Then the
control pair DV-1, which should agree; if it does not, the method is wrong and the others wait.

## 8. What is the owner's choice

1. **The Meta 24 h rule.** It makes pi ask for a lifetime Muse Code does not ask for. Keep it, and the Meta login
   through Provimux stays at 4 hours where the own client asks for none; or drop it for parity, and pi gets what the
   request without it got (2 hours on Wonder in H6; 5 minutes with scattered later hits on Ember in E1).
   Recommended: keep it as a named setting of the owner's, and publish the two apart, as `observed.json` already
   does with `options.prompt_cache_retention`.
2. **P-1.** Recommended: build it with the default unchanged, and set `follow` only when the owner would rather pay
   Claude Code's five-minute writes in overage than one-hour ones. It is a cost choice, not a correctness one.
3. **The routes whose terms forbid third-party use** (Claude, Meta's subscription key, Antigravity) and the Codex
   disguise where Sign in with ChatGPT exists. Section 5 is the state of the terms on 2026-11-22. This document
   needs no decision here to stand; it adds no identity to any of them.
4. **CX-3's second run.** It needs a Sign in with ChatGPT credential in Provimux, made by the owner.
5. **X-1's behaviour.** Refuse a level the model lacks, or move it to a named one and say so. Recommended: refuse
   in `-p` mode, where nobody reads a notice.

## 9. Not established

| Question | Settled by |
|---|---|
| Why the Codex plain probe misses within minutes on Terra and GPT-6.2 Sol | CX-S |
| Whether the GLM difference is real | DV-2, DV-5 |
| Whether Devin caches GPT-6.1 Terra for its own CLI | S3 of groundtruth's plan, then DV-6 |
| Why Muse Code cached no conversation on 7 November, and whether 1.4.25 still does not | S4 and MC1 to MC3 of groundtruth's plan |
| Whether the Devin CLI sets cache marks in its request, and whether Devin's service chooses a policy by client version | Not by these means: the binary keeps no field names, and nothing here captures a client's traffic |
| Which response header marks an answered Claude request as overage | Recorded headers of a login in overage, before P-1 is built |
| Whether OpenAI's backend treats a `codex-tui` originator differently from another | CX-3's second run, in part |
| Whether the owner's Codex credential uses the upstream WebSocket, and which payload rules are in force | The owner's configuration, not read here; M-4 records them per run |
