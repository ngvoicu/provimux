# Who answered: an account marker and the answering model on every answer

Design, 2026-11-26. This is the design only: no code exists yet. The contract a build is checked against is `prices-and-cache/openapi.yaml` (amended with this document: the schemas `Account`, `AnswerStatement` and the six headers) with `prices-and-cache/examples/answers.json` beside it.

**Why.** Three pieces of work met the same wall on 2026-11-26: a client of Provimux cannot tell firmly which account answered a request, nor which model.

1. The review of the first build step of the figures (`prices-and-cache.md`, F1), finding 8: two Claude keys on one host, one name set on two different models. Both rows read `claude, apikey, no host`, each with another providers.dev entry, and the three answered-by headers say the same for both.
2. Four reviews of the measuring of a login's cache through Pi and Provimux: two logins of one provider cannot be told apart, a login replaced in its file cannot be seen, the Responses WebSocket names no account, and the answer carries the model the client asked for, so an alias, a pool, a fallback or an effort variant answering cannot be seen.
3. pi-provimux's footer "answered by" reads the same three headers.

**Read from.**

| Project | Commit |
|---|---|
| Provimux | `main` at `bc6f81d73`, on CLIProxyAPI v8.1.7 (released as v8.1.7-pmx.1). Every `file:line` below is in that commit. `main` gained two commits while this was written and the branch sits on them (`9d0786d95`): a fix to a key's model list, and a test that pins the three headers on the wire. Neither changes a line cited here |
| The owner's patches this builds on | `f4fda78ea` "name a credential only where its answer is written" and its followers up to `a74fe07c3` |
| F1 in its fix round | `provimux-figures-f1` at `679b8182e` (not on `main`) |
| pi-provimux | `7affe86` |
| The two reviews of 2026-11-26 | of F1's build (findings 8, 1 and 3), and the fourth of the measuring harness in groundtruth (its section on identity and model evidence at the released gateway) |

**Tags.** **[read]** by me in the code. **[helper]** read by a helper agent of mine that returned file and line; I did not open every line again. **[ran]** a command of mine (`git log`, a script). **[infer]** reasoned, not verified; section 11 names what settles each. I sent no request to a provider, to the owner's gateway or to port 8787, and opened no credential file.

## 1. The design in one page

| Piece | What it is |
|---|---|
| The account marker | `a.` or `s.` and 20 characters: a keyed hash of the account's own identity at its vendor, under a key only this installation holds. The same on every answer of one account, another for every other account, the same after a restart or a reorder, another when another account's login takes a file. It says nothing a reader can use except "the same" and "not the same" |
| The answering model | three facts, each said as what it is: the model id the gateway asked the vendor for, the model id the vendor's answer named, and the effort the gateway sent. Where the vendor's answer named none, the answer says `not-confirmed` |
| The entry of the answer | the providers.dev entry of this one answer, worked out by the gateway from the account that answered and the model it asked for. This is the route from an answer to `figures.entries` |
| Where it is written | as five more values of the statement the owner's patch already makes: confirmed where the first byte of the answer is written, and nowhere else. **No new place writes it** |
| On HTTP and SSE | six response headers, `X-Provimux-*`, beside the three `X-CPA-AUTH-*` |
| On the Responses WebSocket | six fields `provimux_*` on each turn's `response.created`, beside `cpa_auth_*` |
| In the lists | every account row of `GET /v1/models?include=accounts` and of `model-routes` carries the marker as `account` |
| The switch | `models.answered-by: false` turns all of it off. On unless set |

Six findings shaped it.

1. **The statement is already one value that travels with the answer** [read]. The conductor states who answered once per call (`publishAnsweredAuthMetadata`, `sdk/cliproxy/auth/conductor_execution.go:3353`), the handlers keep it per call and offer it with the result (`sdk/api/handlers/handlers_answered.go`), each route confirms it before its first byte (`handlers.ConfirmAnswer`), and one function writes or removes all headers of it together (`internal/logging/cpa_trace.go:302-320`). A test lists every place allowed to confirm: 31 routes and 6 relays (`internal/api/server_auth_confirm_sites_test.go:21-56`). So the marker and the model need no rule of their own. They are more fields of that value.
2. **The gateway holds a vendor identity for most accounts and uses none of it** [helper]. A Claude login's file has `account_uuid` and `organization_uuid`, a Codex login's `account_id`, Copilot's the GitHub user id, Devin's `user_id` and `org_id`, xAI's `sub`. `Auth.ID` and the index are made from the file's path or the key (`sdk/cliproxy/auth/types.go:686-772`), so neither changes when another account's login takes the file.
3. **Every answer already carries an account-like value, and it is the wrong one** [read, helper]. `X-CPA-TRACE-ID` is `<time>-<auth index>-<request id>`. The index is 16 hex digits of an unkeyed SHA-256 of the login file's absolute path, or of an API key's base URL and the key itself (`types.go:631-638`, `:699-766`). It is set when a credential is **selected**, so after a failover it can name one that did not answer, it stays on errors, and it survives the replacement of a login. Whoever knows a key, or guesses a file's path, can check the guess against it. It is upstream's header (decision 5).
4. **Only the executor's usage reporter knows which model answered** [read, helper]. Every executor tells one object three things: the model it was given, the id it wrote into the request, and the model the vendor's answer named (`internal/runtime/executor/helps/usage_helpers.go:167`, `:283-318`, `:427-438`). That object publishes a usage record after the answer. Nothing carries any of it back to where the answer is written: not the response, not a stream chunk, not a callback.
5. **Upstream already keeps a per-attempt note for one executor** [read]. Since v8.1.3 an attempt's tracker holds the model Antigravity chose for the caller's effort (`SetUpstreamAttemptModel`, `sdk/cliproxy/executor/context.go:20-25`, `:119-138`; written at `antigravity_executor_request.go:117`). The same tracker decides today whether anybody is named at all (`UpstreamAnswered`, `context.go:102`). It is the place for the reporter's three facts.
6. **The model in the body is not evidence of anything** [helper]. A translator writes the vendor's model where it has one and the model the executor was given where it has none; a forced alias writes the client's name over it (`sdk/cliproxy/auth/response_model_rewriter.go:35-53`); the same format passes through untouched. A client cannot tell which of the three it reads. So the model is said beside the body, and the body is left as it is.

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

The marker is the same for every client key (decision 3).

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
| the model id behind a name, the effort sent and the vendor's own id for it | the id was already his to read as `entry` (`prices-and-cache.md`, decision 5); the effort and the vendor's id are new |
| the e-mail, key, file, organisation or vendor id of an account; how many accounts serve names he cannot see; any order | no |

The operator reads the same marker beside the login's name in `GET /v8/management/provimux/model-routes`, behind the management key. No answer to a client ever pairs a marker with a name.

## 3. The model that answered

### 3.1 Three facts

| Fact | What it is | Who knows it |
|---|---|---|
| **sent** | the model id the gateway wrote into its request to the vendor, after the name, the alias, the pool's turn and the executor's own mapping | the gateway. Always firm: it is its own act |
| **reported** | the model id the vendor's answer named | the vendor. Present only when its answer named one before the answer's first byte left |
| **effort** | the reasoning effort in the outgoing request, as read from its final body | the gateway, where the executor reads it |

The gateway judges nothing between the first two. A vendor that answers `claude-opus-5-5-20261101` to `claude-opus-5-5` reports a dated id, not another model; a vendor that answers another model reports that. Both strings are given and the reader compares.

**Why the fallback or the pool's turn is not said as a reason.** It is said as its result: the model sent. A pooled name answers `sent: a` on one request and `sent: b` on the next. A reason would need a line in each of the conductor's attempt loops, in the file upstream changed most (section 8.3), for a word no tool prices or measures by.

### 3.2 On the wire

| Header | WebSocket field | Value | Absent when |
|---|---|---|---|
| `X-Provimux-Account` | `provimux_account` | the marker | nobody is named (section 5.3), or the switch is off |
| `X-Provimux-Model` | `provimux_model` | the reported id where there is one, else the sent id | the answer came through a relay (section 5.2) |
| `X-Provimux-Model-Basis` | `provimux_model_basis` | `vendor`: the vendor's answer named this model. `not-confirmed`: it named none before the first byte; this is the id the gateway asked for | with `X-Provimux-Model` |
| `X-Provimux-Model-Sent` | `provimux_model_sent` | the sent id | with `X-Provimux-Model` |
| `X-Provimux-Effort` | `provimux_effort` | the effort in the outgoing request | the executor read none from the request |
| `X-Provimux-Entry` | `provimux_entry` | the providers.dev entry of this answer (section 4) | the gateway has no entry for it |

- **Whole or not at all.** The six are written in the one pass that writes the three `X-CPA-AUTH-*`, from the one confirmed statement. An answer has `X-Provimux-Account` or none of the six. Inside a statement, a missing header means what the last column says, and nothing else.
- **Plain ASCII.** A value is sent as the provider is today (`AuthStatement.OnTheWire`, `cpa_trace.go:56-77`): a byte outside printable ASCII, a space or `%` as `%` and two hex digits. Every id a vendor uses today goes out unchanged.
- **The body is not touched.** No field of any body changes. On the WebSocket the fields sit at the top of `response.created`, where the three `cpa_auth_*` already are.
- **`not-confirmed` is said, not implied.** A tool that needs the model proven (a measurement) accepts `vendor` only.

### 3.3 What each executor knows when the first byte leaves

[helper] for the lines; the reporter's own functions [read].

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

`X-Provimux-Entry` is `<providers.dev provider>/<model>`, by the rules of `prices-and-cache.md` 6.1 as F1 built them, applied to **this answer**: the account that answered and the model the attempt was given (for Antigravity, the variant it chose). The same function gives the list its `entry`, so the two cannot disagree: where a row has an `entry`, every answer of that account for that name carries it.

| The answer has | Means | The tool |
|---|---|---|
| `X-Provimux-Entry`, and it is a key of `figures.entries` | this is what the answer cost and how long its cache lives | uses the figures |
| `X-Provimux-Entry`, no such key | the gateway knows where the model would be; providers.dev has no entry for it, or figures are off or not fetched | has no figures. Says so. Guesses nothing from the name |
| a statement without `X-Provimux-Entry` | the gateway has no entry for this account, this destination or this model (`prices-and-cache.md` 6.1: an unknown host, a plugin's model, a login sent elsewhere) | the same |
| no statement | nobody is named (section 5.3) | the same; and it does not know which account answered |

**One more guard.** Where the executor recorded the id it wrote into the request and that id is another model than the entry's (a payload rule of the operator rewrote the model), the answer carries no entry. An entry for a model that was not asked for is worse than none.

### 4.1 An entry decided per request

With the marker and the entry on each answer, a row of the list no longer has to promise one entry. Where the entry depends on the request, the row says so: `entry` is left out and `entry_per_request` is `true`. This settles the two questions left from F1's review, and a third the same review found.

| Case | Exact condition [read] | The row | The answer |
|---|---|---|---|
| **A Codex login with the second sign-in bound** | the login has `use_siwc` true (`IsCodexUseSIWC`, `sdk/cliproxy/auth/types.go:996`), **and** the request is not from a Codex client: no `User-Agent` holding `codex-desktop`, `codex-rs`, `codex_cli_rs`, `codex-tui`, `codex_exec` or `codex-acp`, and not an image request (`IsCodexUseSIWCForRequest`, `types.go:1085`; `conductor_execution.go:344`). Then the request goes out with the login's second token to `api.openai.com/v1`. A Codex client on the same login goes to `chatgpt.com/backend-api/codex` with the first | `entry_per_request` | a Codex client: `openai-codex/<model>`. Any other client: **no entry**. providers.dev measures the ChatGPT backend as `openai-codex` and the paid API as `openai`; a subscription's token on `api.openai.com` is neither, and a host alone does not make the paid API's price or cache apply |
| **Antigravity, a model with thinking levels** | the caller asked for a level (a suffix or the body; a budget counts as its level, `auto` and `none` do not), the login's stored list has the model as a group with that level, and the level is not excluded for the login (`SelectAntigravityModel`, `sdk/cliproxy/auth/antigravity_model_enums.go:191-276`, `:415-450`) [helper]. Then the request names the variant (`…-high`), chosen by the gateway before it sends | `entry_per_request` where the login has the model as such a group; else the one `entry` | the variant sent, e.g. `antigravity/gemini-3.8-flash-high`. Known before the request leaves: upstream writes it on the attempt (finding 5) |
| **One name on several models of one account** (a pool) | several `models` rows of one key give one alias; the conductor takes them in turn, and the next when one fails or is cooling (`conductor_models.go:524-563`) [helper] | `entry_per_request` (F1 gives none today) | the model that answered |

A row with `entry_per_request` has no price before the answer. pi-provimux prices such a model as it does a model without figures and shows the answer's own entry in the footer.

**What this means for the measuring of a login's cache:** Pi is not a Codex client by its `User-Agent`. On a Codex login with the second sign-in bound, Pi's requests go to `api.openai.com` and the Codex CLI's to the ChatGPT backend. A pair of those two compares two backends. The answer says which: no `X-Provimux-Entry` on the one, `openai-codex/…` on the other. Whether the owner's login has the binding is in his file, which I did not open.

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
| The Responses WebSocket | six fields at the top of each turn's `response.created`. The copies an upstream gateway may have put there are removed first, as for `cpa_auth_*` (`openai_responses_websocket_forward.go:56-87`, `:285-288`) [read] | the turn's sink, which today receives three strings and then receives the whole statement (8.2) |
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
- **Under "serve only my names" nothing differs.** The rule decides which names are served and listed (`only-my-names.md` section 18). An answer is always for a served name, its account is in the list under that name, and the list shows only served names. The model behind his name is shown, as the list's `entry` already shows it (`prices-and-cache.md`, decision 5): the rule was never a promise to hide it. The switch below does not hide the list's `entry` either. Hiding the model behind a name from a key holder would be one setting over both, and nobody has asked for it.
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

  Off in a test binary unless a test turns it on, as the two list switches are, so that every existing test of the headers reads what it read. With it off, every answer and every WebSocket event is byte for byte what `bc6f81d73` writes, and the lists are what they are without this design.

## 7. What uses it

### 7.1 The figures contract

`prices-and-cache/openapi.yaml`, amended with this document. Contract 1 has not been released, so this is a correction of it, not a contract 2.

| | Was | Is |
|---|---|---|
| From an answer to its figures | match `X-CPA-AUTH-KIND`, `-PROVIDER`, `-HOST` to an account row; take the row's `entry` | read `X-Provimux-Entry` on the answer; it is the key of `figures.entries` |
| "Accounts that agree in all three have the same entry" | promised, and false (finding 8) | removed |
| An account row | `provider`, `kind`, `priority`, `host`, `entry` | and `account` (the marker), and `entry_per_request` where it applies |
| From an answer to its account | not possible | `X-Provimux-Account` equals the `account` of the row |
| No entry | "an account without `entry` has no figures" | the four cases of section 4 |
| The promises | "an account is found from the three headers" | "an answer names its entry and its account itself; where a row has an `entry`, every answer of that account for that name carries it" |

Finding 8's case, after it: both rows read `claude, apikey`, each with its own `account` and `entry`; the answer carries the marker of the key that answered and `anthropic/provimux-test-dear` or `…-cheap`. Nothing is matched by kind, provider or host.

### 7.2 The others

| Reader | Change |
|---|---|
| Savings by account (`prices-and-cache.md` section 7, F6) | a stored row is keyed by the marker, not by the index. That it holds across a restart and a token refresh is then by construction, and a replaced login starts new rows, which is right. The marker is taken where the row is recorded, from the account the record names |
| A login in overage (`prices-and-cache.md` 4.4, `cache_now`) | the row it would sit on is found by `account` |
| pi-provimux (F5) | the footer shows the answer's own entry and price; its `model-upstreams:answered` event gains `account`, `model`, `model_basis`, `entry`. Its two tables that guess a provider from kind, provider and host are no longer needed for a gateway that says it |
| The measuring (groundtruth) | per sample: the same `a.` marker on the prime and on every check, basis `vendor`, the model and effort it expected, and the entry of the route it claims. An `s.` marker, `not-confirmed` or a missing statement makes the sample not proven. `X-CPA-TRACE-ID` is no longer read |
| The console | the marker beside each route; nothing else in this step |

## 8. The build

### 8.1 New files

| File | Holds | In |
|---|---|---|
| `internal/logging/provimux_answer.go` | the six header names and field names, the type `ProvimuxAnswer`, how it goes on the wire, the function that sets or removes the six headers | layer |
| `sdk/cliproxy/auth/provimux_account_marker.go` | the identity of 2.3 per kind, the rule of 2.2, the key's setter | layer |
| `sdk/cliproxy/auth/provimux_answered.go` | builds the answer's statement at the place the conductor states who answered: the marker, the attempt's report, the entry | layer |
| `sdk/cliproxy/provimux_answered_key.go` | makes, keeps and reads the key; the switch's default | layer |
| `sdk/api/handlers/handlers_provimux_answered.go` | the capture's second setter; the WebSocket turn's sink for a whole statement | layer |
| `sdk/api/handlers/openai/openai_responses_websocket_provimux.go` | removes and sets the six fields of `response.created` | layer |
| `sdk/cliproxy/executor/attempt_report.go` | the attempt's report and the handle the reporter writes through | **owner patch** |
| their `_test.go` files, and `test/provimux/answered_by_test.go` | 8.4 | |

```go
// internal/logging

// ProvimuxAnswer is what Provimux adds to the statement of the credential that produced a response: the account's
// marker, and what the attempt knew of the model when the response was returned. It is part of AuthStatement, so it is
// captured, offered, withdrawn, confirmed and written with it, and never apart from it.
type ProvimuxAnswer struct {
	Account       string // "a." or "s." and 20 characters; "" names nobody and sends nothing
	ModelSent     string // the model id written into the request to the vendor
	ModelReported string // the model id the vendor's answer named before the response was returned; "" = none
	Effort        string // the reasoning effort read from the outgoing request; "" = none read
	Entry         string // the providers.dev entry of this answer; "" = the gateway has none
}

// Fields returns the header name, the WebSocket field name and the value of each of the six, in a fixed order, as
// they are sent: none when Account is empty, and no model field when ModelSent is empty.
func (a ProvimuxAnswer) Fields() []ProvimuxField

// SetProvimuxAnswerHeaders sets, or deletes, all six headers from one statement.
func SetProvimuxAnswerHeaders(header http.Header, statement AuthStatement)

// sdk/cliproxy/auth

// SetProvimuxMarkerKey gives the key of the account markers. Without one no marker is made and nothing is sent.
func SetProvimuxMarkerKey(key []byte)

// ProvimuxAccountMarker returns the account's marker (answered-by.md 2.2 and 2.3), or "" without a key.
func (a *Auth) ProvimuxAccountMarker() string

// provimuxAccountIdentity returns the basis ("a" or "s") and the bytes hashed for a.
func provimuxAccountIdentity(a *Auth) (basis string, identity []byte)

// publishProvimuxAnswer completes the statement the conductor has just made for the response it returns, and hands
// it to cliproxyexecutor.ProvimuxAnswerCallbackMetadataKey. It is called only from publishAnsweredAuthMetadata, after
// its callback, with the same attempt context and credential; for an empty statement it hands over the empty answer.
func publishProvimuxAnswer(attemptCtx context.Context, meta map[string]any, auth *Auth, statement logging.AuthStatement)

// provimuxAnswerEntry is set by sdk/cliproxy to the one function that gives an entry (F1). nil = no entry.
var provimuxAnswerEntry func(ctx context.Context, auth *Auth, model string) string

// sdk/cliproxy/executor (the owner patch)

// AttemptReport is what the executor's usage reporter knew of one attempt when asked: the model the attempt was
// given or chose, the id written into the outgoing request where the executor records one, the model the vendor's
// answer named so far, and the reasoning effort read from the outgoing request.
type AttemptReport struct{ Model, UpstreamModel, ResponseModel, Effort string }

// AttemptHandleFromContext returns the attempt's tracker as a handle a reporter keeps; its methods do nothing
// without a tracker.
func AttemptHandleFromContext(ctx context.Context) *AttemptHandle
func (h *AttemptHandle) SetModel(model string)
func (h *AttemptHandle) SetUpstreamModel(model string)
func (h *AttemptHandle) SetResponseModel(model string)
func (h *AttemptHandle) SetEffort(effort string)

// AttemptReported returns what the attempt's reporter has told its tracker so far.
func AttemptReported(ctx context.Context) AttemptReport

// sdk/api/handlers

// WithAnsweredStatementCallback installs a callback that receives the whole statement of the result the execution
// layer returns, where WithAnsweredAuthCallback receives its first three values. A websocket turn uses it.
func WithAnsweredStatementCallback(ctx context.Context, callback func(logging.AuthStatement)) context.Context
```

Each body is a worker's.

### 8.2 Lines added to files that exist

| File | Whose lines there | Edit | Lines |
|---|---|---|---|
| `internal/logging/cpa_trace.go` | upstream's file, the owner's patch | `AuthStatement` gains the field `Provimux`; `OnTheWire` and `trimmedAuthStatement` keep it; `applyTraceHeader` calls `SetProvimuxAnswerHeaders` | +2, 2 changed |
| `sdk/cliproxy/auth/classification.go` | the owner's function `AuthStatement` | its statement carries the marker, so the six relays send it with no line of their own | 1 changed |
| `sdk/cliproxy/auth/conductor_execution.go` | the owner's function `publishAnsweredAuthMetadata` (`:3353-3366`) | one call after its callback. **None of the five places that call it changes** | +1 |
| `sdk/api/handlers/handlers_execution.go`, `handlers_stream.go` | beside the owner's three `reqMeta[…AnsweredAuthCallbackMetadataKey] = answered.set` | the second callback, three times | +3 |
| `sdk/api/handlers/handlers_answered.go` | the owner's file | `offerAnsweredAuth` hands a turn's whole-statement sink the statement | +4 |
| `sdk/api/handlers/openai/openai_responses_websocket.go` | beside the owner's sink (`:1069`) | the whole-statement sink, storing into the same value | +3 |
| `sdk/api/handlers/openai/openai_responses_websocket_forward.go` | beside the owner's two calls (`:286-287`) | remove and set the six fields | +2 |
| `sdk/api/handlers/openai/openai_models_accounts.go` | the owner's file | `account` on a row | +2 |
| `internal/api/handlers/management/provimux_model_routes.go` | the layer's | `account` on a route | +3 |
| `internal/registry/catalog_config.go` | the layer's lines in upstream's struct | `AnsweredBy *bool` and its default | +5 |
| `internal/api/server_provimux.go` | the layer's | read the key at start | +1 |
| `config.example.yaml`, README | | the switch; a section "Who answered" | text |
| **Owner patch:** `sdk/cliproxy/executor/context.go` | upstream's tracker (`:20-25`), with the owner's `pluginBytes` beside it | one field | +1 |
| **Owner patch:** `internal/runtime/executor/helps/usage_helpers.go` | upstream's, **a protected path** | the reporter keeps the attempt's handle (one field, two lines where it is made) and tells it what it is told: `SetAttemptModel`, `ObserveResponseModel`, `SetResponseModel`, `SetUpstreamModel`, `SetTranslatedRequestMetadata`, one line each | +8 |

In all about 35 added and 3 changed lines in files that exist, 8 of the added in a protected path. No executor file, no translator and no auth file changes, and no request to a vendor changes by a byte.

**Why an owner patch, and what the other way is.** The reporter is the one place every executor already tells the three facts, and it sits under `internal/runtime/executor`, which a layer commit may not touch (`guard.sh`). The patch changes no request. Without it the layer can only know the model the conductor chose, which needs a line in each of its five attempt loops, and can never say `vendor`: it would have to read the model back out of a translated body, which finding 6 shows is not evidence. Decision 1.

### 8.3 What it costs to carry

Upstream commits that touched each file, and the stack's own [ran: `git log`; v8.0.30 is of 2026-10-15, v8.1.7 of 2026-11-27].

| File | Upstream, the 90 days to 2026-11-26 | Upstream, v8.0.30 to v8.1.7 | The stack's own | Where our lines sit |
|---|---|---|---|---|
| `sdk/cliproxy/auth/conductor_execution.go` | 79 | 49 | 8 | inside the owner's own function, not in upstream's loops |
| `internal/runtime/executor/helps/usage_helpers.go` | 37 | not counted | 0 | at the head of five small setters and in the constructor |
| `sdk/api/handlers/openai/openai_responses_websocket.go` | 30 | 17 | 3 | beside the owner's sink |
| `sdk/api/handlers/handlers_stream.go` | 23 | 12 | 3 | beside the owner's line |
| `sdk/api/handlers/openai/openai_responses_websocket_forward.go` | 20 | 13 | 6 | beside the owner's two calls |
| `sdk/api/handlers/handlers_execution.go` | 19 | 10 | 2 | beside the owner's two lines |
| `sdk/cliproxy/executor/context.go` | 4 | not counted | 1 | the tracker's struct |
| `sdk/cliproxy/auth/classification.go` | 3 | 1 | 3 | the owner's function |
| `internal/logging/cpa_trace.go` | 2 | 1 | 8 | the owner's lines |
| `internal/registry/catalog_config.go` | 1 | not counted | 4 | the layer's own |

Every line but the eight in `usage_helpers.go` sits beside a line the owner's patches already carry there, so it moves when that line moves and conflicts when that line conflicts, not more often. The eight are the new cost: a file upstream touches about three times a week, with our lines at the heads of functions it has not renamed in that time [infer from the commit subjects; the sync check shows the first time it is wrong].

### 8.4 What must hold

| Step | Must hold afterwards |
|---|---|
| The marker | every row of 2.3, each from a fixture login or key and no real one. The same marker after the config's keys are reordered, after the file is renamed or moved, after a restart with the kept key; another after another account's login is written into the same file, for every `a` kind and for Kimi; the two bold rows pinned as not changing. Two keys on one host differ. One login in two files is one marker. No key: no marker anywhere. Two installations give the same account two markers. A marker is never in a log line |
| In the lists | both lists give one account the same `account`; `/v1/models` without `include` is byte for byte `bc6f81d73`'s; with the switch off no row has `account`; `pui13/openapi.yaml` and its example grow in the same commit (not on the design branch: F1's branch changes that file and is not on `main` yet) |
| On the answer | the layer's wire test (`internal/pluginhost/provimux_answered_by_wire_test.go`, on `main` since `9d0786d95`: a credential, a plugin, a fallback and a plugin again on one connection, and three turns on one WebSocket) grows the six values: a credential's answer has them, a plugin's has none, a stale one never survives. The owner's matrix of each path's first commit (`internal/api/server_auth_first_byte_test.go`, `sdk/api/handlers/openai/auth_first_byte_test.go`) with two accounts of **one kind and one host**: each answer carries the marker of the one that answered; after a failover the second's; a keep-alive first, an error, a plugin, an interceptor, a plugin's own call: none of the six. The six are present together or absent together on every one of the 37 places. A WebSocket turn, a second turn on another account, a retry inside a turn. An upstream's own `provimux_*` fields never pass through. With the switch off every header and every event is byte for byte `bc6f81d73`'s. The confirm-sites test passes unchanged |
| The attempt's report (the owner patch) | per executor a stand-in vendor that answers **another** model id than the one asked: the report holds both, and for a stream it holds the reported one before the first chunk is handed on exactly on the executors section 3.3 says yes for, and not on the four it says no for. No request to any stand-in differs by a byte from the same test without the patch |
| The model on the answer | `vendor` and `not-confirmed` per row of 3.3; a forced alias leaves the body's model the client's name and the header the vendor's; a pool answers two requests with two sent ids; Devin's sent id carries the effort |
| The entry on the answer | for every route of F1's fixture config the answer's entry equals the row's `entry`; the three rows of 4.1, each with both outcomes; the guard of section 4; finding 8's two keys, each answer with its own entry |

## 9. The steps, in order

Each is small enough to review alone. W1 to W3 need nothing of F1 and unblock the measuring's first need (which account). W5 needs the owner patch. W6 needs F1 on `main`.

| Step | Kind | Holds | Depends on | What exists after it |
|---|---|---|---|---|
| W1 | layer, new files only | the key, kept and read; the marker of 2.2 and 2.3; no wire change | | a function and its proof |
| W2 | layer | `account` in both lists; the switch | W1 | a tool can read the markers |
| W3 | layer | the marker on every answer: the statement's new field, the headers, the WebSocket fields, the relays | W1 | a tool can tell which account answered, on HTTP, SSE and the WebSocket |
| Review A | | W1 to W3 together: what a marker gives away, the key's life, every row of 5.3, bytes with the switch off | | |
| W4 | **owner patch** | the attempt's report: one new file, one field, eight lines in the reporter. No wire change | the owner's yes (decision 1), and the stack re-ordered to hold it below the layer | |
| W5 | layer | sent, reported, basis and effort on the answer | W3, W4 | a tool can tell which model answered and whether the vendor said so |
| W6 | layer | the answer's entry; `entry_per_request` in both lists, with the three cases of 4.1 | W5; F1 on `main` | the route from an answer to its figures; F3 (`include=figures`) can be built on it |
| Review B | | W4 to W6: no request changed, each row of 3.3 against a stand-in, the entry never wrong | | |
| W7 | docs | the README's section, the release's note, the contract file as built | W6 | |
| After | other repositories | pi-provimux (F5 reads the statement), groundtruth (the measuring reads it) | a release with W3, or with W6 | |

A release can carry W1 to W3 alone.

## 10. Decisions that are the owner's

| # | Decision | Recommendation | The other way costs |
|---|---|---|---|
| 1 | An owner patch of eight lines in the usage reporter, under a protected path, so that the answer can say which model the vendor named | **Yes.** It changes no request, and it is the one place the fact exists | Without it no answer can ever say `vendor`: the model is `not-confirmed` everywhere, and even the sent id needs five lines in the conductor's loops, the file upstream changes most |
| 2 | The names: `X-Provimux-*` and `provimux_*`, beside the `X-CPA-AUTH-*` the owner's patches named | **`X-Provimux-*`**: the marker needs a key only Provimux keeps, and a name upstream may one day use for something else cannot collide | `X-CPA-AUTH-ACCOUNT` and so on reads as one family, and would be upstream's to redefine |
| 3 | One marker per gateway, the same for every client key | **Per gateway** | Per key, two holders cannot compare notes, which tells them nothing the list does not; a tool with two keys can then prove nothing, and the operator needs a second marker |
| 4 | Always on, with `models.answered-by: false` to turn it off | **Always on** | Asked for per request protects nothing and puts a header on every request of every tool |
| 5 | `X-CPA-TRACE-ID` keeps upstream's unkeyed index of the selected credential | **Leave it, and say in the README that it is not the account that answered.** It is upstream's header and format | Taking the index out of it is three lines in upstream's own function and changes a value operators match against their logs. Leaving it leaves this: whoever holds a key of the gateway can check a guessed API key, or a guessed path of a login file, against it |
| 6 | An API key's identity is its base URL and the key, not its host and the key | **The base URL** | With the host alone, two accounts behind one gateway host and one key would share a marker |
| 7 | Meta and Antigravity are told apart by the e-mail their vendor names, inside the keyed hash and never shown | **Yes** | Without it both are `s`: a replaced Meta login keeps its marker, which is the case the measuring could not see |
| 8 | A Codex login with the second sign-in bound has no entry when a client other than Codex's own is answered by `api.openai.com` | **No entry**, until providers.dev measures that route as an entry of its own | Calling it `openai-codex` gives it the ChatGPT backend's cache lifetime; calling it `openai` gives a subscription the paid API's price |
| 9 | The words of the README section and the release's note | the owner's | |

## 11. What I ran and read, and what is not verified

| Check | Result |
|---|---|
| The design branch moved onto `main` at `bc6f81d73` (`git rebase --onto origin/main aa76f801b`): four commits, no conflict | done |
| Every `file:line` of `prices-and-cache.md` compared between `aa76f801b` and `bc6f81d73` by a script: 20 cited ranges | all 20 unchanged |
| `git log v8.1.4..v8.1.7`: 74 commits | two touch what the designs assume: the Home local plugin executor (`6acc778c0`, v8.1.6: one more path that names nobody) and pool positions kept per credential (`c3570f755`, v8.1.5). The Antigravity variant by effort is older (`2f86992c9`, v8.1.3, with the attempt's model note): the figures design had missed it, and F1's review found it |
| Read: the owner's patch `f4fda78ea` and the state after its followers: `cpa_trace.go`, `handlers_answered.go`, `handlers_context.go:115-135`, `classification.go:60-124`, `publishAnsweredAuthMetadata`, the tracker in `executor/context.go`, the WebSocket's sink and the two functions that stamp `response.created`, the confirm-sites test | sections 1, 5, 8 |
| Read: the usage reporter (`usage_helpers.go:33-80`, `:96-175`, `:283-340`, `:420-438`); the Codex destination rule (`types.go:996-1014`, `:1064-1093`); Claude's made-up account id (`claude_executor_auth.go:84-128`); Savings' secret (`saver.go:205-235`, `clientkey.go`); the account list's handler; the switches' struct | sections 2, 3, 4, 6 |
| Three helper agents, read-only, each returning file and line: what every executor knows of the model (15 executors); every place an answer is written and the WebSocket; the identity each kind of credential holds | sections 2.3, 3.3, 5; tagged [helper] |
| `git log` per file for 8.3, mine, against a helper's independent count | the same numbers |
| `prices-and-cache/openapi.yaml` through `openapi-spec-validator`; the three examples against its schemas with unknown properties forbidden; planted faults | section 7.1's files; the results are in the commit's message |

**Not verified.**

| Point | Who settles it |
|---|---|
| That the context an executor makes its reporter with is the attempt's, tracker included, on every executor | W4's test, per executor |
| That a Codex stream kept open over several turns (the duplex stream) cannot name another model on a later `response.created` than the statement made at its start | W5's test. If it can, the model fields are left off every `response.created` after the first of such a stream |
| The exact text of the effort the reporter reads (`high`, a budget) per executor | W5; the contract says only "as read from the outgoing request" |
| That Devin's request id is never the same string for two efforts | W5's test over Devin's catalog |
| That every vendor names its real variant in the answer (Antigravity's `modelVersion` for a `-high` variant) | not needed: where it does not, the basis is `not-confirmed` or the two ids differ, and both are said |
| That a Claude login always has an organisation id once its profile was read | W1: without one it is `s`, which is safe |
| That the key-value store is open before the first request is served, in every start mode | W1 |
| Whether the owner's Codex login has the second sign-in bound | his file; it decides what a Pi measurement on that login measures (section 4.1) |
| Nothing was built or run against a gateway | every step's tests |
