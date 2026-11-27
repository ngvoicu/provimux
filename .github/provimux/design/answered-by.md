# Who answered: an account marker and the answering model on every answer

What the code does. The first section is the behaviour; sections 2 to 7 are the account marker, the model statement and the reading of upstream's code that explains where the entry is taken and written. The contract a build is checked against is `prices-and-cache/openapi.yaml` (the schemas `Account`, `Answered`, `AnswerStatement` and the six headers) with `prices-and-cache/examples/answers.json` beside it.

## What the code does

Every answer names the account that gave it by its marker (section 2). Where the gateway can be certain what it sent for the answer, it also names the model it sent (`X-Provimux-Model-Sent`, and `X-Provimux-Model` with basis `not-confirmed`) and that request's providers.dev entry (`X-Provimux-Entry`). Both account lists carry, per account row, `answered`: what that account's answers for the name said to the caller's own key. With `models.answered-by: false` none of this is attached, and requests to vendors and `/v1/models` without `include` are the same bytes as without the feature.

### Where an entry is named

Only in the gateway as shipped. `cmd/server/main.go` sets the **process mark** as the first thing `main` does (`logging.ProvimuxShippedGateway`, one line). The mark lives in `internal/provimux/origin`, an internal package: a program in another Go module cannot import it, so a program that embeds the SDK never sets it. Without the mark no answer names an entry or a model, nothing is recorded, the record's file is neither read nor written, and the lists carry no `answered`. The account marker does not depend on it. Tests that stand for the shipped gateway set it through `origin.ShippedGatewayForTests`, which does nothing outside a test binary; `TestProvimuxOriginCallers` fails when another product file calls the setter.

With the mark set, an answer names the model it sent and its entry when all of this holds; otherwise it names the account alone:

1. **The executor** that served the attempt is one of the gateway's built-in executors, on a closed list kept by a type switch over `internal/runtime/executor` (`sdk/cliproxy/provimux_builtin_executors.go`): Codex, Claude, Gemini and Gemini Interactions, Vertex, Antigravity, Kimi, xAI, Copilot, Devin, Meta, OpenAI-compatible. AI Studio is the gateway's own and names none. `TestProvimuxBuiltinExecutorListIsClosed` fails when the service makes an executor the list does not know. The executor is looked up when the answer is stated, as the conductor looks it up.
2. **No plugin took part for the provider.** Once an executor that is not on the list (a plugin's, the plugin-refresh wrapper) is registered for a provider, that provider names no entry until the gateway restarts. The mark is set in `Manager.RegisterExecutorWithRevision` (one added line), before the executor is in place; the gateway putting its own executors back, however often, never sets it. The log says it once per provider.
3. **No plugin made or changed the credential**: a plugin's virtual credential, one with the plugin host's storage, or one whose metadata carries `provimux_plugin_credential: true`. The plugin host sets that mark where it makes a credential (one line); a native login that writes the record anew sets it to `false` (one line in each of the four token stores).
4. **Exactly one request could have produced the answer.** The attempt's requests answered with a 2xx, other than the answering executor's closed housekeeping list (Google's token endpoints for Vertex, Antigravity and Gemini; Antigravity's project discovery, onboarding and user information; Copilot's token exchange and model catalog, each by method, exact host, exact path and shape), number exactly one. A refused request (not a 2xx) is never a candidate: a failover, a compression retry, a redirect and the second try on the same account after an overload refusal (`routing.retry.same-account-retry`) still name the request that answered (`TestProvimuxAnswerEntryAfterASecondTry`).
5. **That request is the attempt's own and went where its URL says**: it was made on the attempt's context, with no `Host` other than its URL's.
6. **No connection was made in the attempt that the decorator did not see.** An upstream WebSocket, or a client no helper built, leaves one: no entry.
7. **The model was read** (below), and the map has a row for what was sent: https on the default port, and an exact row for the host, the credential's kind and the signer (`modellists.EntryProvider`). An xAI credential marked as not the paid API has none. Where only the map lacks a row, the model headers are sent and the entry is not.

### What was sent

Every HTTP client a built-in executor sends a model request with is built by a helper of `internal/runtime/executor/helps` that applies upstream's observation seam (`sdk/httptransport`) after the proxy, the pool and the TLS profile are chosen. Provimux puts one decorator on that seam, in one place: `WithUpstreamAttemptTracker` (one added line), together with a log of the attempt's own and a connection trace (`sdk/cliproxy/executor/provimux_sent.go`). The decorator notes, for each request: the scheme, host and port of the URL, the names of its credential headers, whether its `Host` differs from the URL's, whether it was made on the attempt's context, and the status of its response. When the response's headers are back with a 2xx, it reads the model from the request: the `<id>` of a path `.../models/<id>:<method>`; from the body as `GetBody` gives it again (zstd decoded where `Content-Encoding` says so), the top-level `model` of a JSON body or a form, field 21 of a Connect message to Devin's chat method. Two models that differ, a form it does not know, a body without `GetBody` or larger than 64 MiB: not read, no entry. Only the model is kept; the body read for it is let go before the round trip returns. Every built-in executor builds its body from bytes, so `GetBody` is always there (`TestProvimuxAnswerEntryEveryExecutorOnTheWire` checks each executor on the wire). An attempt keeps at most 16 requests; one that makes more names nothing.

**What it costs.** With the switch on (the default), one read of the body of each 2xx model request, once its headers are back, and nothing held after: `TestProvimuxSentHoldsNoCopyOfTheBody` sends 32 MiB and fails if the attempt holds more than 4 MiB once its headers are back.

### The record: `answered`

- **What is noted.** A statement is noted once it has been written to its client, never when it is only offered: the trace writer (`internal/logging/cpa_trace.go`) hands the record the statement it applied to the response's headers, once they are written; a Responses WebSocket turn is noted once its `response.created` carrying the marker is written. An answer that named the account and no entry is noted as `null`.
- **Per client key, credential, marker and name.** The client key is kept as a keyed hash under the markers' key, the credential's id likewise; callers without a key share one record. The row is the name called where the credential registers exactly it, else that name without its thinking suffix, else none; no case folding. At most four outcomes, newest first by the order the notes were accepted in (a sequence under the record's lock, never the clock); a fifth drops the oldest. An item older than 90 days is not shown and is dropped from the file at the next write. A client's list gives its own key's items, by day; the management list gives every key's together, with the time.
- **The file.** A cache: `provimux-answered.v1` in the auth folder (no `.json`, so the auth watcher does not read it), JSON with a version, read with the repository's JSON library, written whole with `modellists.WriteFileAtomic`. A new outcome or a new day is written at once; a later time on the same day at the next write or at the stop. A file that does not parse, or is of another version, is ignored with one line in the log and replaced at the next write. When the auth folder changes, memory is written to the old file and replaced by what the new one holds. The stop is final: the last write runs and no later note is accepted. With the switch off, in Home mode and without the process mark, nothing is noted and the file is neither read nor written.

### Upstream lines

Against `main` (v8.1.7-pmx.4), in files of upstream v8.1.7, 33 added lines in 15 files, none changed: `cmd/server/main.go` 1 (the process mark), `internal/logging/cpa_trace.go` 8 (the statement the trace writer applied, noted once the headers are written), `internal/pluginhost/auth_provider.go` 1 and the four token stores 1 each (the plugin mark), `sdk/api/handlers/openai/openai_handlers.go` 10, `openai_responses_websocket_forward.go` 1 and `openai_speech_handlers.go` 1 (`answered`, `include=figures`, the WebSocket note, the name called), `sdk/cliproxy/auth/conductor_execution.go` 1 and `conductor_lifecycle.go` 1, `sdk/cliproxy/executor/context.go` 1, `sdk/cliproxy/kv_store.go` 2 (the record's place and its stop), `config.example.yaml` 2.

### Known limits

- **A plugin for a provider costs that provider its entries** until the gateway restarts, by design.
- **An answer can come without its entry** when, while it is in flight, the executor in place for its provider is taken out or replaced by a plugin's (a plugin loaded for the provider, and a login of that provider added): the executor is looked up when the answer is stated. The entry is missing, never wrong; the next answer names it again.
- **A record file that does not parse is ignored and replaced at the next write**: what it held is lost.
- **Two gateways on one auth folder** each write the record's file whole; the next start reads the last one written.
- **A note made while the auth folder changes** may be missed.
- **A request whose `GetBody` gives other bytes than its body** names the model of `GetBody`. No built-in executor builds such a request.
- **A login a plugin wrote with a version before the plugin mark** carries nothing that says a plugin made it: where the gateway's loader parses it natively, its answers name the entry of what the built-in executor sent.
- **A program in a module whose path is inside Provimux's** can import the internal packages and set the process mark, as a fork that edits `main` can. It is a fork.

## 2. The account marker

### 2.1 What it must be, and how each point is met

| It must | How |
|---|---|
| be the same for every answer of one account | it is made from the account's identity at its vendor, not from the request, the time or the file |
| differ for any other account | a keyed hash of 100 bits; inside the hash each kind of account has its own tag |
| say nothing about the account | HMAC under a key that is never in the config, in an answer or in a log. Without the key a reader cannot check a guess (an e-mail, a key, an organisation id) against it |
| reveal no count and no order | it is not an index and has no sequence |
| stay the same across restarts and reorders | the key is kept on disk; nothing in the hash depends on a position in the config or on a path, except for the `s.` kinds of 2.3 |
| change when another account's login takes the file | the identity is read from the login as it is now, at every use; nothing is remembered per file |
| be matched against the lists | the same function gives `account` on every row of both lists |

### 2.2 How it is made

```
marker   = basis "." base32( HMAC-SHA-256( key, "provimux account marker v1" 0x00 basis 0x00 identity ) )[first 20 characters]
basis    = "a"  the account as its vendor names it      "s"  this gateway's credential, where the vendor's name is not held
identity = kind tag 0x00 field 0x00 field …             (2.3)
base32   = RFC 4648, lower case, no padding
```

It is always 22 characters, `^[as]\.[a-z2-7]{20}$`, plain ASCII. A change of this rule is `v2` inside the hash: every marker changes at once, and nothing else has to move, because a marker is only ever compared with another from the same gateway.

**The key.** 32 random bytes, made once, kept in the gateway's key-value store (`server.kv-store`, a bbolt file beside the logins) under a key of its own, `provimux:answered:v1:key`. It is read before the first request is served: a local file, never a network call. It is not Savings' secret: that one is read inside Savings' save rounds and can change with the store in use (`internal/provimux/savings/saver.go:213-231`) [read]. Where no store keeps it (the store disabled, a read-only disk, Home mode), the key is random for the run, the markers hold until the restart, and the log says so once. Removing the stored key gives every account a new marker.

### 2.3 Per kind of account

Fields are the stored login's own, trimmed, compared as bytes. [helper] for where each field is stored; the Claude and Codex rows [read].

| Account | Identity inside the hash | Basis | Another account's login in the same file | Two credentials of one account |
|---|---|---|---|---|
| Claude login | `organization_uuid`, `account_uuid` | `a` | changes | the same marker |
| Claude login without an organisation id, or whose account id the gateway made up itself (a setup token, a profile the vendor refused: a version-5 UUID of the file's id, `claude_executor_auth.go:88-122`) | the credential's id | `s` | **does not change** | differ |
| Codex login | `account_id` (the ChatGPT workspace), and the user id of its id token where the token names one | `a` | changes | the same |
| Copilot | the GitHub user id (`account_id`) | `a` | changes | the same |
| Devin | `org_id`, `user_id` | `a` | changes | the same |
| xAI login | `sub` | `a` | changes | the same |
| Vertex service account | the service account's `client_email` | `a` | changes | the same |
| Meta login | the e-mail the vendor's token answer names (`user_email`, written again at every mint, `meta_executor.go:148-171`) | `a` | changes | the same |
| Meta login whose vendor answer named no e-mail | the credential's id | `s` | **does not change** | differ |
| Antigravity | the e-mail of the Google account (the only account field the login holds) | `a` | changes | the same |
| Kimi login | its `device_id`, which the gateway makes anew at each login | `s` | changes | differ |
| An API key, any family | the base URL (scheme and host in lower case, the path without a trailing slash, nothing after it) and the key; without a base URL, the provider's name and the key | `a` | n/a: another key is another account | the same |
| An `openai-compatibility` entry without a key | its base URL | `a` | n/a | the same |
| AI Studio (a browser's connection), any runtime-only credential, a credential a plugin's parser made | the credential's id | `s` | new at each connection or load | differ |
| A plugin's executor, a plugin that was handed the bytes, the Home local executor | no statement at all, as today | | | |

- **`a`** means: equal markers are one account at the vendor, and unequal ones are two. **`s`** means: equal markers are one credential of this gateway. Unequal `s` markers can still be one account, and on the two rows marked in bold an equal marker can hide a replaced login. A tool that must prove "the same account" (a cache measurement) accepts `a` and treats `s` as not proven.
- **Meta has no account id** [helper: every stored field listed, `internal/auth/meta/meta.go:89-104`]. Its e-mail is the vendor's own word at every mint, so it follows the login in the file. An e-mail can be changed by its owner, which would give a new marker: the marker then errs towards "not the same", never towards "the same".
- **Why the base URL and not only the host for a key:** one host can front several accounts by path (a gateway in front of several vendors). Two paths of one account then give two markers, which again errs towards "not the same".
- **One account held twice** (the same login in two files, one key in two config rows) has one marker on two rows. That is true: it is one account. Which row answered does not matter for a price, because the answer names its own entry (section 4).
- **A Claude login whose file holds no ids yet** gets them at its first request: the Claude executor reads the profile then (`claude_executor_auth.go:84-128`). Its marker is `s` until then and `a` after. A tool that meets a marker it has not seen reads the list again.

### 2.4 Per gateway, not per client key

The marker is the same for every client key.

| | One marker per gateway | One per client key |
|---|---|---|
| Two key holders comparing notes learn | that their requests met the same account | nothing |
| They knew already | the same rows: the gateway routes every key over the same accounts, and the list is the same for every key (it does not read the caller, `openai_models_accounts.go:44-74`) [read] | |
| A tool with two keys (a probe and an agent in one measurement) | can show both met one account | cannot; the measurement falls back to the owner's word |
| The console, Savings, a log line | one value per account | a second, operator-only marker is needed to say which is which |

### 2.5 What a holder of a client key learns

| Learns | New? |
|---|---|
| which row of the list answered each of his own requests, so how his requests spread over the accounts, and when an account stopped answering | yes. It is the purpose |
| that two rows under different names are one account | yes. The count of accounts under each name was already in the rows |
| that the login of an account was replaced | yes |
| the model id behind a name, the effort sent and the vendor's own id for it | yes, on the answers to his own requests. The list shows it too, as what an account last answered as, on the caller's own rows ("The record") |
| that a name was used on a given day, by somebody, on a given account | yes: the day of an item of `answered`. Not the time, and not by whom |
| the e-mail, key, file, organisation or vendor id of an account; how many accounts serve names he cannot see; any order | no |

The operator reads the same marker beside the login's name in `GET /v8/management/provimux/model-routes`, behind the management key. No answer to a client ever pairs a marker with a name.

## 3. The model that answered

### 3.1 Three facts

| Fact | What it is | Who knows it |
|---|---|---|
| **sent** | the model id in the request the gateway sent to the vendor, read from that request as the transport was handed it (4.2): after the name, the alias, the pool's turn, the executor's own mapping and the operator's payload rules | the gateway. Firm where it read the request. Where it could not (an upstream WebSocket), the id is the one its executor was given, and the answer names no entry |
| **reported** | the model id the vendor's answer named | the vendor. Present only when its answer named one before the answer's first byte left. Not read yet: every answer says `not-confirmed` |
| **effort** | the reasoning effort in the outgoing request, as read from its final body | the gateway, where the executor reads it. Not written yet |

The gateway judges nothing between the first two. A vendor that answers `claude-opus-5-5-20261101` to `claude-opus-5-5` reports a dated id, not another model; a vendor that answers another model reports that. Both strings are given and the reader compares.

**Why the fallback or the pool's turn is not said as a reason.** It is said as its result: the model sent. A pooled name answers `sent: a` on one request and `sent: b` on the next. A reason would need a line in each of the conductor's attempt loops, in the file upstream changes most, for a word no tool prices or measures by.

### 3.2 On the wire

| Header | WebSocket field | Value | Absent when |
|---|---|---|---|
| `X-Provimux-Account` | `provimux_account` | the marker | nobody is named (section 5.3), or the switch is off |
| `X-Provimux-Model` | `provimux_model` | the reported id where there is one, else the sent id; today always the sent id | no model was read (section 4) |
| `X-Provimux-Model-Basis` | `provimux_model_basis` | `vendor`: the vendor's answer named this model. `not-confirmed`: it named none before the first byte, or the gateway did not read it; this is the id the gateway asked for. Today always `not-confirmed` | with `X-Provimux-Model` |
| `X-Provimux-Model-Sent` | `provimux_model_sent` | the sent id | with `X-Provimux-Model` |
| `X-Provimux-Effort` | `provimux_effort` | the effort in the outgoing request | always, today: not written yet |
| `X-Provimux-Entry` | `provimux_entry` | the providers.dev entry of this answer (section 4) | the gateway has no entry for it |

- **Whole or not at all.** The six are written in the one pass that writes the three `X-CPA-AUTH-*`, from the one confirmed statement. An answer has `X-Provimux-Account` or none of the six. Inside a statement, a missing header means what the last column says, and nothing else.
- **Plain ASCII.** A value is sent as the provider is today (`AuthStatement.OnTheWire`, `cpa_trace.go:56-77`): a byte outside printable ASCII, a space or `%` as `%` and two hex digits. Every id a vendor uses today goes out unchanged.
- **The body is not touched.** No field of any body changes. On the WebSocket the fields sit at the top of `response.created`, where the three `cpa_auth_*` already are.
- **`not-confirmed` is said, not implied.** A tool that needs the model proven (a measurement) accepts `vendor` only.

### 3.3 What each executor knows when the first byte leaves

[helper] for the lines; the reporter's own functions [read]. The column **Sent** says what each executor writes; the gateway reads it from the request (4.2), not from the executor's own account of it.

| Executor | Sent | Reported, non-stream | Reported, at a stream's first chunk | Effort |
|---|---|---|---|---|
| Claude (login, key; Kimi over Claude's protocol) | the id after its own mapping | `message.model`, and Anthropic's own fallback model where it used one | yes: `message_start` is the first event | from the request |
| Codex, HTTP and its upstream WebSocket | the final body's `model` | `response.model` | yes: `response.created` | from the request |
| Meta | the id it was given | yes | yes | from the request |
| xAI | the id it was given | yes | yes | from the request |
| Gemini key, Vertex, AI Studio | the id in the URL | `modelVersion` | yes: on every chunk | from the request |
| Antigravity | the variant it chose for the caller's effort | `modelVersion` | yes | from the request |
| Kimi | its mapped id (several names map to one) | yes | yes | from the request |
| `openai-compatibility`, OpenRouter | the id it was given | yes | **no**: the reporter gets the stream's model when the stream ends (`usage_helpers.go:1299-1301`, `:1570`) | from the request |
| Copilot | the bare id | yes | **no**: at the end (`copilot_executor_response.go:276`) | from the request |
| Devin | its own request id, which carries the effort | yes, where the usage frame names one | **no**: the usage frame comes late | not said: the effort is inside the sent id |
| Token counts, every executor | as above | **no**: a count answer names no model | | as above |
| A plugin's executor | no statement | | | |

So `not-confirmed` is the answer today on: a stream from an OpenAI-compatible key, OpenRouter, Copilot or Devin; every token count; and any vendor answer that names no model. Nothing is echoed there: the value is the id the gateway asked for, and the basis says so.

**Two limits, said in the contract.**

- The statement is of the answer's **first byte**. A vendor that switches model later in a stream is in the gateway's usage record and not in the headers. The record is the operator's.
- For Devin a client cannot read the effort as its own value. It is in `X-Provimux-Model-Sent`, the id Devin was asked with.

## 4. The entry of an answer

`X-Provimux-Entry` is `<providers.dev provider>/<model>`. **It is read from the request the gateway sent for this answer, in one place, when the answer is stated. Nothing predicts it**, so no row of a list carries one.

| The answer has | Means | The tool |
|---|---|---|
| `X-Provimux-Entry`, and it is a key of `figures.entries` | this is what the answer cost and how long its cache lives | uses the figures |
| `X-Provimux-Entry`, no such key in the copy the tool holds | the account had not answered for this name when the tool read the list, or providers.dev has no entry for it, or figures are off or not fetched | reads the list once more. Still no key: it has no figures, says so, and guesses nothing from the name |
| a statement without `X-Provimux-Entry` | the map has no row for what was sent, the gateway could not be certain what it sent, or a plugin took part (`prices-and-cache.md` 6.1, the three kinds) | has no figures |
| no statement | nobody is named (section 5.3) | the same; and it does not know which account answered |

### 4.1 Why it cannot be predicted

An earlier build predicted an account's entry from the configuration and the registry. Two reviews found eleven rows whose answer was sent as something else. Each time the cause was a decision upstream takes inside an executor, after the last thing a list can see:

| Decided | Where | A list cannot see it because |
|---|---|---|
| The model in the body | the operator's payload rules. They run last, on the finished body, and no rule guards the path `model` (`helps/payload_helpers.go:274-414`). Upstream's own rule for every executor is that they are "the final semantic barrier before sending" (`AGENTS.md`) | a rule can match on a request header. The same account and name send two models |
| The host | the account's base URL; a base URL in a login's file or metadata (`meta_executor.go:320-365`, `devin_executor.go:3157-3194`, `antigravity_executor_request.go:889-954`); xAI's `using_api` (`xai_executor_request.go:279-344`); the Codex second sign-in by the client's `User-Agent` (`codex_executor_auth.go:529-551`); Vertex's route by the alias asked for (`helps/vertex_openapi.go:112-173`) | half of these are per request, and one account can send to two hosts |
| The variant | Antigravity's stored catalog at the moment of the request (`antigravity_model_enums.go:191-276`) | the catalog changes between the list and the call, and a failed read looks like "no group" |
| Who owns the account | a plugin may change an account's provider in the call that publishes its models (`service_executors.go:699-779`), and nothing records that it did | |

A prediction must copy every one of these and is wrong where it misses one. The request that was sent has all of them in it already.

### 4.2 The facts, and where each is taken

| Fact | Taken from | It is the sent one because |
|---|---|---|
| **The host** | the URL of the `*http.Request` the executor's HTTP client hands to its transport | every choice of a destination has been made: a redirect the client follows is one more request and is seen too |
| **The model** | the same request, as its bytes are, by where its protocol puts the model: the top-level `model` of a JSON body; the id in a URL path of the form `…/models/<id>:<method>` (Gemini and Vertex); field 21 of the Connect message (Devin, `helps/devin_wire.go`). Where a request names one in its URL and one in its body they must be the same, or nothing is read | the payload rules have run, and so has every mapping of the executor. The body is the one the vendor gets |
| **What signed it**, where one host serves two kinds of access | the names of the request's credential headers (for Claude on `api.anthropic.com`: `x-api-key`, or `Authorization` without it). Never a value | a login's token configured as a key is sent as a login's token (`claude_executor_request.go:1231-1251`) |
| The executor's provider, the credential's kind | the credential the conductor states (`auth.Provider`, `auth.AuthKind()`), as today | they are not on the wire. They choose a row of the map and are never enough for one |

**The place is upstream's own.** `sdk/httptransport` (commit `51952a7b2`, 2026-10-13, unchanged since) is "an additive, request-scoped observation seam": a decorator on the execution context wraps the transport a client has selected, "to observe final HTTP requests". Every HTTP client an executor sends a model request with is built by a helper that applies it after the proxy, the pool and the TLS profile are chosen [helper, each executor's client read]:

| Client | Built by | Decorated |
|---|---|---|
| Claude; Codex over HTTP; Kimi's Claude route | `helps.NewUtlsHTTPClient` (`utls_client.go:483-499`) | yes |
| Copilot, OpenAI-compatible and OpenRouter, Kimi, xAI over HTTP, Meta, Gemini, Vertex (both routes, and its token exchange), Codex images | `helps.NewProxyAwareHTTPClient` (`proxy_helpers.go:36-37`) | yes |
| Devin | `helps.NewDevinHTTPClient` (`proxy_helpers.go:516-517`) | yes |
| Antigravity | `newAntigravityHTTPClient` (`antigravity_executor.go:514-550`), which decorates once itself | yes |
| Codex and xAI over the upstream WebSocket (`websockets` on, and the client came over a WebSocket) | a `websocket.Dialer`; no `http.RoundTripper` sees the handshake or a frame (`codex_websockets_connection.go:233-293`, `xai_websockets_executor.go:1386-1409`) | **no** |
| AI Studio | a JSON message on a socket the browser opened (`internal/wsrelay`) | **no** (and it has no row in the map) |

Provimux attaches one, in one place: where an attempt gets its tracker (`WithUpstreamAttemptTracker`, `sdk/cliproxy/executor/context.go`). For each request it is handed it notes on the attempt's log, as one **observation**: the scheme, host and port of the URL, the credential header names, and, when the response's headers arrive, the status; on a 2xx, the model, read from the body as `GetBody` gives it again (a second reader over the same bytes; the body the transport sends is never consumed). It delegates to the transport below it and changes nothing. The only other decorator in the gateway is the same-account retry's, which runs Provimux's inside its own.

**What is not read, and why** [read, helper]:

| Source | Why not |
|---|---|
| The model the usage reporter was told (`SetUpstreamModel`, the model it was made with) | told **before** the payload rules on Claude (`claude_executor_execute.go:45` against `:254`), Copilot (the route's id, `copilot_executor.go:297`, rules at `:263`), Kimi (`:151` against `:170`), Devin (`:256`, the id from before the rules), and never corrected on the OpenAI-compatible, xAI and Meta executors. Only Codex reads it from the final body |
| The request upstream logs (`RecordAPIRequest`) | it does nothing in commercial mode; Antigravity gives it no body unless request logging is on (`antigravity_executor_request.go:228-258`); Codex gives it the plain JSON while the wire is zstd, and nothing when it manages the instructions; it has the URL before a redirect |
| The account's base URL (`X-CPA-AUTH-HOST`) | empty for every login, and the configured host for a key even when the request went elsewhere |
| The registry, a listing, the name asked for | a prediction (4.1) |

## 5. Where it is written

### 5.1 The rule

Only where the answer is written. The six are made from fields of the statement the owner's patch carries; they are captured with it, offered with it, withdrawn with it, confirmed with it and written by the same lines. This design adds **no confirming place and no writing place**: the test that lists the 37 places is unchanged, and a route that names nobody today names nobody after.

### 5.2 Per transport and path

| Path | Carries | By |
|---|---|---|
| HTTP JSON: chat completions, completions, Responses and its compact, Claude messages, Gemini `generateContent` and Interactions | all six, where known | `handlers.ConfirmAnswer` at the write, as today |
| SSE streams of the same | all six, on the response's headers, committed with the first chunk of the answer | the same. A stream's reported model is what the first chunk's upstream frames named (3.3) |
| Token counts (`/v1/messages/count_tokens`, Gemini `countTokens`) | all but a reported model: basis `not-confirmed` | the same. A count the gateway worked out itself names nobody, as today |
| Images, videos, speech | the marker; the model where the executor reports one | the same |
| The Responses WebSocket | six fields at the top of each turn's `response.created`. The copies an upstream gateway may have put there are removed first, as for `cpa_auth_*` (`openai_responses_websocket_forward.go:56-87`, `:285-288`) [read] | the turn's sink, which receives the whole statement |
| The relays that hold their credential themselves: Codex alpha search, the live call routes, the Muse relay (6 places) | the marker only. They run no executor, so there is no model statement and no entry | `Auth.AuthStatement()`, which they already confirm |
| The realtime WebSockets (`/v1/realtime`, the live sideband) | nothing, as today: their frames are relayed raw | out of scope |
| `GET /v1/models`, management answers | no statement: they are not a vendor's answer | |

**Why fields of `response.created` and not a frame, a first message or the upgrade's headers.** A connection is not one account: each turn selects, a failed attempt retries on another credential inside the turn, and the upgrade's `101` is written before any credential is chosen (`openai_responses_websocket.go:459`, `:1022-1070`) [helper, the sink read]. `response.created` is the first event of every answer, the three fields are there already, and pi-provimux reads them there per response id. A frame of our own would be a new event type every client of the socket has to ignore.

### 5.3 Where nobody is named

Unchanged, and now true of nine values and not three. Each is a missing statement, which a tool reads as "not known", never as an account [helper for the lines].

| Case | |
|---|---|
| An error, the gateway's own or a vendor's passed on | every error return offers nothing |
| A keep-alive that left before the answer | it commits the headers; the confirmation after it does nothing (`handlers_answered.go:57`). Only with `streaming.keepalive-seconds` and `send-keepalive-before-ft`, or `nonstream-keepalive-interval`, set: all off by default (`config.example.yaml:333-339`) |
| A plugin's executor, a plugin handed the bytes, the Home local executor (`acp/…`) | the attempt is marked (`MarkPluginBytes`) |
| A response interceptor that changed the body; any stream with a chunk interceptor | the offer stays empty |
| A plugin's own model call | it answers the plugin, not the client |
| A WebSocket turn answered locally (the pre-warm), or with no `response.created` | nothing to put it on |

A measurement that needs every answer named runs with the keep-alives off, or counts an unnamed answer as not proven.

## 6. Who may see it, and the switch

- **Every holder of a client key, on the answers to his own requests and in the list he may read.** There is no narrower audience to give it to: the gateway has no rule per client key, and a holder who can ask can also be answered.
- **Under "serve only my names" nothing differs.** The rule decides which names are served and listed (`only-my-names.md` section 18). An answer is always for a served name, its account is in the list under that name, and the list shows only served names. The model behind his name is shown, on the answer and in the list's `answered`: the rule was never a promise to hide it. The switch below hides both, since the list's record is made of the answers' statements; it is the one setting over both.
- **Always on, not asked for per request.** Asking protects nothing: the one who asks is the one who would be refused. Vendor clients send no header of ours, and the tools that need it most (a probe, a footer) would each have to add one to every request.
- **The switch.** `models.answered-by`, beside `live-lists` and `published-lists` (`internal/registry/catalog_config.go:16-28`):

```yaml
models:
  # On unless set to false. Each answer names the account that gave it by an opaque marker, and the
  # model that answered (the X-Provimux-* response headers; on the Responses WebSocket the provimux_*
  # fields of response.created), and the account lists carry the marker. false: none of it. The three
  # X-CPA-AUTH-* headers are not affected.
  # answered-by: false
```

  Off in a test binary unless a test turns it on, as the two list switches are, so that every existing test of the headers reads what it read. With it off, every answer and every WebSocket event is byte for byte what it is without this design, and so are the lists.

## 7. What uses it

### 7.1 The figures contract

`prices-and-cache/openapi.yaml`, contract 1. From an answer to its figures: read `X-Provimux-Entry` on the answer; it is the key of `figures.entries`. From an answer to its account: `X-Provimux-Account` equals the `account` of a row. A row has `account` and `answered`, never an `entry`: no row says what the next answer will be. An answer without an entry has no figures (the cases of section 4).

### 7.2 The others

| Reader | Use |
|---|---|
| Savings by account | a stored row is keyed by the marker, not by the index: it holds across a restart and a token refresh, and a replaced login starts new rows. The row's entry is the answer's own |
| pi-provimux | the footer shows the answer's own entry and price; before an answer it prices a model by the row's `answered` |
| A cache measurement | per sample: the same `a.` marker on the prime and on every check, and the entry it claims. An `s.` marker, `not-confirmed` where the model must be proven, or a missing statement makes the sample not proven |
| The console | the marker beside each route, and each route's `answered` with its times |
