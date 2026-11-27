# A conversation goes back to its first route (`session-route`)

Design, 2026-11-27. This is the design only: no code exists. The contract a build is checked against is `session-route/openapi.yaml`, beside this file.

**What the owner met (2026-11-27).** One model name can be served by several of his accounts, tried by priority. His gateway runs with `routing.session-affinity: true`, so upstream binds a conversation to the account that served it and keeps that account "even if a higher-priority credential recovers" (`config.example.yaml:253-255`). His Pi conversation on one name fell to another login while the first had a cooldown, and stayed there: a 5-minute cache where the first route's has an hour. His words: "how can i make the session fetch again and maybe switch to the default (first one in the list) not fallback?" Today the ways back are an hour without a request, a new session, or a restart of the gateway.

**What he gets.**

1. A command in Pi that shows where this conversation is bound, since when, whether a higher-priority route is free, and what going back costs; and that releases the binding.
2. The gateway by itself: a conversation that sits below a free higher-priority route goes back once its cache is cold anyway. On unless set; a conversation with a warm cache is never moved by this rule.

**Read from.**

| Project | Commit |
|---|---|
| Provimux | `main` at `c80afa86c`, on CLIProxyAPI v8.1.7. Every `file:line` below is in that commit. The branch was made at `9d0786d95` and moved when `main` gained the account marker and the restart drain; of the files cited here only `config.example.yaml` and `sdk/cliproxy/service_config.go` had changed, and their lines are the new ones |
| Bindings kept across a restart (not on `main`) | `provimux-session-persist-v3-on-main` at `df87a41a1`, its `session-affinity-persist.md` |
| The account marker | on `main` at `c80afa86c` (`Auth.ProvimuxAccountMarker`, `models.answered-by`). Its design, `answered-by.md`, is on `provimux-answered-by` at `516b99486` |
| The figures (not on `main`) | `provimux-figures-f3` at `5676b27b5`, its `prices-and-cache.md`. Section 11 A there is the first sketch of this rule; this file replaces it |
| provimux-ui | `main` at `4742d6d9` |
| pi-provimux | `7affe86`, with Pi's library `pi-ai` 1.6.0 as installed there |
| pi-cache-keeper | `67770ff` |

**Tags.** **[read]** by me in the code. **[infer]** reasoned, not verified; section 14 names the test that settles each. I sent no request to a provider, to the owner's gateway or to port 8787, and opened no credential file.

## 1. The design in one page

| Piece | What it is |
|---|---|
| The truth about a binding | stays upstream's: its session cache and its prefix matcher. Nothing of ours decides a selection |
| Releasing | removing the binding from upstream's store. The conversation's next request is then, for upstream, a conversation without a binding: it goes to the first available route by priority (`Pick`, `selector.go:1832-1842`). **No line of upstream's selector changes** |
| The book | a small table of ours, in memory: per bound conversation, which account answers it, since when, which account answered it before, which client keys used it, and what its answers said about their cache. Filled by two observers upstream already offers: the manager's result hook and the usage record |
| Seeing and releasing, for a client | two requests of the gateway's own under `/v1/provimux/session-route`, named by the same session headers a turn carries, limited to conversations that client key has used |
| Seeing and releasing, for the operator | `/v8/management/provimux/session-routes`, behind the management key: all of them |
| The rule | every 15 seconds: a binding that sits below a free higher-priority route, and has been quiet for longer than its cache lives, is released. `routing.session-affinity-return`, on unless set |
| Pi | `/provimux-route`: shows, says the cost, releases. And Pi starts sending its session id on Claude-format models, without which the gateway cannot name the conversation at all (finding 3) |
| The console | a card on the Models page: the switch, the value in use and why, how many conversations sit on a later route, how many went back |

Six findings shaped it.

1. **A release needs no change to the selection** [read]. `Pick` reads the binding (`selector.go:1738`), and with none it binds to the highest available priority (`:1702`, `:1835-1842`). Upstream's own cache has the removal (`SessionCache.Invalidate`, `session_cache.go:425`), and the prefix matcher has one too (`RemoveSessionBefore`, `sdk/cliproxy/session/lcp.go:1008`). A file of ours in package `auth` reaches both.
2. **Whether a binding "came from a fallback" can be read from where it sits** [read]. A conversation without a binding is only ever given an account of the highest available priority (`highestPriorityAuths`, `selector.go:1050`, used at `:1702`). So a binding to an account below the highest available priority exists for one of three reasons: the conversation failed over, it started while the higher accounts were cooling, or the priorities were changed since. In all three the answer the owner wants is the same. The rule therefore tests the state, not a history, and it holds after a restart, where no history is left.
3. **Pi's Claude-format conversations carry no session id today** [read]. `pi-ai` sends `x-session-affinity` on its Anthropic API only when the model's `compat.sendSessionAffinityHeaders` is true, and the default is true for OpenRouter only (`dist/api/anthropic-messages.js:151`, `:1002-1004`). pi-provimux registers Claude-format models with the compat flags of Pi's own catalog (`extensions/lib.ts:646-659`), which has no such flag. So upstream binds the owner's conversation by the shared start of its content (the prefix matcher), and no client can name it. On Codex-format models Pi sends `session-id` and `x-client-request-id` (`dist/api/openai-codex-responses.js:1330-1331`).
4. **Upstream's cache keys do not hold the caller** [read]. A key is `<provider>::<session identity>::<model>` (`selector.go:1705`). Two client keys that send the same session id share one binding. Keeping a caller to its own conversations therefore needs a record of ours: the book notes the caller scope upstream already computes for every request (an irreversible hash of the client key, `sdk/api/handlers/handlers.go:377-378`, `:433-441`; `sdk/cliproxy/session/identity.go:210`).
5. **Every answer already says which cache it wrote** [read]. Upstream's usage record carries the conversation, the account, the name the client sent, the client key, and the cache tokens written with a 5-minute and with a 1-hour lifetime where the vendor reports them (`sdk/cliproxy/usage/manager.go:26-49`, `:119-121`). Savings reads the same record (`internal/provimux/savings/record.go:63-66`). So for a conversation in Claude's format the gateway knows the lifetime of this conversation's cache from the conversation itself, today, without providers.dev.
6. **The management routes exist only with a management key** [read] (`internal/api/server.go:286-288`). A path for clients cannot be registered where Provimux's management paths are; it costs one line where the `/v1` routes are set up.

## 2. Words

Each is checkable against the console's Models page, on the card of a name.

| Word | Means |
|---|---|
| **A route** of a name | one account that serves the name: a row of the name's card, with its priority |
| **The first route** | the accounts with the highest priority among those that are free now: not disabled, no cooldown recorded for the name. It is a set, not one account |
| **Bound** | upstream holds, for this conversation and this name, the account that last served it, and sends the next request there while that account is available |
| **Below its first route** | bound to an account whose priority is lower than the first route's |
| **Released** | the binding is removed. Nothing is sent. The next request of the conversation is routed as a new conversation's |

**What "the first route" is under each `routing.strategy`** (`config.example.yaml:227`) [read: `selector.go:1079`, `:1138`, `:1239`]:

| Strategy | A released conversation's next request goes to |
|---|---|
| `round-robin` (the default, and the owner's) | the accounts of the highest free priority, in turn. With one such account, that account. With several, the next in the round: on the Models page, any row that shares the top priority number and shows no cooldown |
| `fill-first` | the first account of the highest free priority, the same one every time |
| `weighted-round-robin` | an account of the highest free priority, by the weights. An account with weight 0 is never chosen, so it does not count as a first route |

Two accounts of the same priority are both first routes. A conversation that moved from one to the other is not below its first route: the rule never touches it, and the command says that releasing lets the strategy choose again and may choose the same account.

## 3. The book

One table in memory, of ours, in package `auth`. It is a note about bindings, never their source: every reader checks upstream's store first, and an entry whose binding is gone is dropped.

| Field | From | Used for |
|---|---|---|
| the binding's own key in upstream's cache, or the prefix matcher's session id and namespace | the result's options: upstream writes the provider, the model and the canonical session id it bound under into the request's metadata (`selector.go:1644-1645`, `:1679`; the matcher at `:1976-1977`) [infer, I1] | finding the binding again, exactly, without deriving a key a second time |
| the session identity and the name's key | the same | answering a client that names a conversation and a name |
| the account that answers now, and **since** when | the first successful result of this account for this conversation | "bound to, since" |
| the account before it, and when it stopped | the same, when the account changes | "fell back from" |
| the last successful answer's time | each successful result | the quiet time, beside the binding's own last use (section 6.3) |
| up to 4 caller scopes | the request's metadata (`caller_scope`) | who may see and release it |
| the parent conversation's identity | the request's metadata (`parent_session_id`) | saying that a conversation is a sub-agent's |
| the longest cache lifetime its answers on this account reported writing | the usage record (finding 5), joined by conversation and account [infer, I2] | the rule's lifetime, first choice (6.2) |

- **Two observers, both upstream's own:** `Manager.AddHook` (`sdk/cliproxy/auth/hooks.go:23`: "subscribes to lifecycle events without replacing the constructor hook"), whose `OnResult` runs for every recorded result (`conductor_cooldown.go:2457`), and a usage plugin (`coreusage.RegisterPlugin`, `sdk/cliproxy/usage/manager.go:833`), as Savings is. Neither is on the path that selects or sends: both run after the result.
- **Bounded:** 4,096 conversations, the least recently answered dropped first. An entry is a few hundred bytes.
- **Not kept on disk.** After a restart the book is empty. What that means is in section 8.1.
- **Clock:** one function of ours, `provimuxSessionRouteNow`, which tests replace.

## 4. Seeing and releasing

### 4.1 By a request of the gateway's own, not a header on the next turn

| | A path of the gateway's own | A header on the conversation's next request |
|---|---|---|
| Shows without sending a turn | yes | no: the answer to "where am I" would cost a model request |
| Says the cost before releasing | yes: show, then release | no: the release and the turn are one request |
| Upstream lines | one, where routes are registered | one on the request path, before the selection, in the file that builds every request's options |
| Who may | must be decided (4.3) | whoever sends the turn |

The path wins on the first two rows: both are what the owner asked for.

### 4.2 How a conversation is named

**By the same headers a turn carries, read by upstream's own function.** The request to `/v1/provimux/session-route` carries the session header the client sends on its turns, and the gateway runs `session.ExtractSessionInfo` over it (`sdk/cliproxy/session/info.go:96`), the function every turn goes through. The identity that comes out is the one the binding was made under, by construction: no second list of headers exists to drift.

Upstream's order, as its comment lists it (`info.go:83-95`), and what a client of this path sends:

| Order | A turn is named by | The request names it by |
|---|---|---|
| 1 | `X-Claude-Code-Session-Id` (with `X-Claude-Code-Agent-Id` for a sub-agent) | the same headers |
| 2 | Claude Code's `metadata.user_id` in the body | `identity.metadata.user_id` in the request's body |
| 3 | `Session-Id`, `Thread-Id` (Codex and compatible clients; Pi on Codex-format models) | the same headers |
| 4 | `X-Http-Session-Id` | the same header |
| 5 | `X-Session-ID`, `X-Session-Affinity` (OpenCode; Pi on Claude-format models after P1), `X-Slot-Session-Id`, `X-Task-ID`, `X-Conversation-Id`, `X-Thread-Id`, `X-Client-Request-Id` | the same header |
| 6 to 11 | fields of the body: `cachedContent`, `thread_id`, `session_id`, `prompt_cache_key`, `conversation`, `metadata.user_id`, `conversation_id` | the same fields inside `identity` |
| after those | the shared start of the content (the prefix matcher), a derived identity, a hash of the first messages | **cannot be named.** Such a conversation is seen and released by the operator only, and returned by the rule |

`identity` is an object that holds only the body fields a turn of this client carries its session in; the gateway hands it to the extraction as the turn's body would be. Ids travel in headers and the body, never in a URL, so no access log holds them.

**The name** is `model`, as the client sends it on a turn. A binding is per name: a conversation that used two names has two. A thinking suffix is cut as upstream cuts it (`canonicalModelKey`, `selector.go:842`).

### 4.3 Who may

| Caller | Sees and releases |
|---|---|
| A configured client key | a conversation whose book entry holds this key's caller scope: one the gateway has seen this key use since it started |
| The management key | every binding, through the management path |

- **A conversation that is another key's, and one that does not exist, get the same answer:** `conversation: "not-seen"`. Nothing tells them apart.
- **Why not "whoever knows the session id":** an id can be a word the user chose (`X-Session-ID: main`, a `prompt_cache_key`). A second key holder could then move another's conversation at will.
- **The cost of the strict rule** is one case: after a restart that kept the bindings (8.1), the book is empty, and a client's command answers `not-seen` until the conversation's next answer. The rule and the operator are not affected.
- **Under "serve only my names"** a name that is not served is refused as a turn for it would be, with the setting's message.

### 4.4 What is shown

To a client key, per route, exactly what a row of `GET /v1/models?include=accounts` holds: `provider`, `kind`, `priority`, `host`; and `account`, the account marker, where the build has it and `models.answered-by` is on. No login name, e-mail, file, index or cooldown.

| Shown | New to a holder of a client key? |
|---|---|
| the route this conversation is bound to | no: every answer of the conversation names its account (`X-Provimux-Account`), and the list has the row |
| since when; the route it fell back from, and when | no: the same answers, read in order |
| the first routes now (the rows of the highest free priority) | the rows are in the list. **That they are free now is new**, as one fact about the name, not per account: `higher_priority_free`. A request for the name shows the same by who answers it |
| how long the gateway takes this conversation's cache to live, and on what basis | from the conversation's own answers, the figures the list already serves, or the setting |

The operator's list adds what `model-routes` already shows him: the login's name and the recorded cooldown.

### 4.5 Releasing

1. The binding is looked up again in upstream's store. The request may carry `expect`, the token the show answered with (it stands for this binding on this account); when the binding has changed since, nothing is released and the answer is `409` with the state now.
2. Every key of the binding's group is removed from the session cache (the conversation's key and its aliases), or the matcher's session.
3. The book's entry is closed: the release is counted, by whom (a client's request, the operator's, the rule), and the entry is dropped.

Nothing else happens. No request is sent, no account's state changes, no credit of the weighted strategy moves. Releasing twice is not an error: the second answers `released: false`, `conversation: "not-bound"`.

## 5. The rule

`routing.session-affinity-return`, in effect only where session affinity runs (section 9). Every 15 seconds one function, `sweep(now)`, looks at each binding and releases it when **all** of these hold:

| | Condition | Read from |
|---|---|---|
| a | it sits below a free higher-priority route | 6.1 |
| b | the conversation has been quiet longer than its cache lives, plus 60 seconds | 6.2, 6.3 |
| c | it is still the binding that was judged | the removal compares the account and the expiry it read; a binding used in between is left |

A request that arrives between two sweeps after (b) became true is served by the bound account once more, and its binding is fresh again. That errs towards staying, which loses nothing. One sweep asks for each name's routes once, whatever the number of conversations.

**Why a sweep and not a test where the binding is read.** The test in `Pick` is exact to the request and costs an inserted block in the file upstream changes most (13 times between v8.0.60 and v8.1.3, `prices-and-cache.md` 11 A), at three places a binding is honoured (`selector.go:1738`, `:1797`, `:1851`). The sweep is exact to 15 seconds in the harmless direction and costs no line there.

## 6. The three things the gateway must know

| What | How it is known | Upstream lines |
|---|---|---|
| The binding sits below a free higher-priority route; and what it fell back from | 6.1 | 0 |
| The higher-priority account has recovered | 6.1 | 0 |
| The conversation's cache is cold | 6.2 and 6.3 | 0 |

### 6.1 Below a free higher-priority route

For a binding to account B under name N: take the accounts that serve N, ask upstream's own availability for them as `Pick` does (`selector.go:1698-1702`), through the form of that function that takes no request (`getAvailableAuthsAcrossPriorities`, `:977`), then `highestPriorityAuths`; with the weighted strategy, only accounts of positive weight (`:1684-1686`); and compare. B is below its first route when the highest available priority is above B's.

- This is the same judgement upstream makes for a conversation without a binding, by the same functions. So "released" always leads where the rule said it would, and an account upstream would not choose (cooling, disabled, token expired, weight 0) is never counted as free.
- "Recovered" is therefore not a second fact: it is this judgement turning true. The recorded cooldowns that `model-routes` shows are the same state, shown to the operator beside it.
- **From what it fell back** is history, and only the book has it: the account that answered this conversation before B. It is shown, and never decides. After a restart it is `not-known`.

### 6.2 How long the cache lives

The first that applies, per binding:

| Order | Lifetime | Known when |
|---|---|---|
| 1 | what this conversation's own answers on the bound account reported writing: 3,600 s when any reported a 1-hour write, else 300 s when any reported a 5-minute write | the vendor reports it (Claude's format), and the book has seen such an answer |
| 2 | the figures of the entry the bound account last answered this name as (`answered`, `prices-and-cache.md` 5): the largest lifetime that entry states, in either profile, `rely_on_s` or the safe value | the build has the figures and `answered`, both are on, and the entry is held |
| 3 | `cold-after`, the operator's time. One hour unless set | always |

- **Row 1 first** because it is this conversation's own cache, said by the vendor, not a measurement of another conversation.
- **The largest in row 2** because the gateway does not know there whether the conversation asked for the longer cache. Going back late costs nothing new; going back early gives up a warm cache, which the rule must never do.
- **Row 3's hour** is the longest lifetime a cache has unless a client asks a vendor for more. With upstream's default `session-affinity-ttl` of one hour a binding ends by itself at that point, so where nothing better is known the rule moves nothing: only the command does. An operator with a longer `session-affinity-ttl` gets the hour, and may set another time.
- **A lifetime of 0** in row 2 (a measured "held on no check") is taken as 0: the conversation goes back once it has been quiet for the 60 seconds of condition (b).

### 6.3 How long the conversation has been quiet

From the later of two times: the book's last successful answer, and the binding's own last use, which is its expiry minus the cache's TTL (upstream moves the expiry at every selection and every successful result, `session_cache.go:124-144`, `:380-395`). The second needs no book, so it holds after a restart. A stream counts from its end. For a conversation the gateway recognises by its content only, the matcher shows no time through what it exports, so the book's is the only one; such a conversation has no binding after a restart anyway.

Anything that sends a request in the conversation resets it: a turn, a token count, pi-cache-keeper's keep-warm request. A conversation that is being kept warm is warm, and the rule leaves it. For it, the command is the way back.

### 6.4 When none of it is known

| Not known | Then |
|---|---|
| the lifetime (rows 1 and 2 both absent) | row 3: an hour unless set |
| the last answer's time (no book entry) | the binding's own last use |
| the bound account (it is no longer registered) | nothing to do: upstream drops such a binding itself at the next request |
| a conversation bound by its content only, after a restart | it has no binding: the matcher is not kept across a restart on any branch |

The rule never guesses below these.

## 7. Where it sits in upstream's code

### 7.1 Files

New files, all the layer's (commits with `Provimux-Layer: branding`):

| File | Holds |
|---|---|
| `sdk/cliproxy/auth/provimux_session_route_seam.go` | **every use of an unexported name of upstream's**, and nothing else: the selector's `cache` and `matcher` fields; a cache entry's account, expiry and aliases under the cache's read lock; `canonicalModelKey`; `getAvailableAuthsAcrossPriorities` and `highestPriorityAuths`; the manager's selector and config snapshot. About 150 lines |
| `sdk/cliproxy/auth/provimux_session_route.go` | the book, the show, the release, the sweep, the state. Calls the seam and exported names only |
| `internal/provimux/sessionroute/` | the start: subscribes the hook and the usage plugin, runs the sweep's ticker, follows a replaced manager. Started once, where the client routes are registered |
| `internal/api/provimux_session_route.go` | the two client handlers and `registerProvimuxClientRoutes` |
| `internal/api/handlers/management/provimux_session_routes.go` | the two management handlers |
| `internal/config/provimux_session_return.go` | the setting's type and its defaults |
| tests beside each, and `test/provimux/session_route_test.go` | section 12 |

Lines added to files that exist:

| File | Lines | What |
|---|---|---|
| `internal/api/server_routes.go` | +1 | `s.registerProvimuxClientRoutes(v1)` after the `/v1/models` routes (`:72-76`). Upstream's file |
| `internal/config/config_types.go` | +2 | the field `SessionAffinityReturn`, with its comment on the line, after `SessionAffinitySubagents` (`:608`). Upstream's file |
| `config.example.yaml` | +9 | the setting, commented, after `session-affinity-subagents` (`:269`). Upstream's file |
| `internal/api/server_provimux.go` | +2 | the two management routes. The layer's own file |
| `sdk/cliproxy/auth/selector.go`, `session_cache.go`, `sdk/cliproxy/session/**`, `sdk/cliproxy/service_config.go` | **0** | |

Possibly one more [infer, I4]: `models.only-my-names` needed one line in `internal/config/config_v8.go` for the console's `PUT /config/...` to reach it. G2 finds whether a key under `routing` needs the same.

**The two settings side by side.** The persistence branch adds its field after `SessionAffinityRebindOn` (`config_types.go:620`) and its example lines after `session-affinity-rebind-on` (`config.example.yaml:278`). This design's lines sit at another anchor in both files on purpose, so whichever lands second rebases without a conflict.

### 7.2 What the guard says

`guard.sh` passes. Rule 3 protects `internal/runtime/executor`, `internal/auth`, `internal/misc`, `internal/translator` and `sdk/cliproxy/session`: nothing is added or changed under any of them. `sdk/cliproxy/session` is only called, through names it exports (`ExtractSessionInfo`, `BoundSessionIdentity`, `CallerScope`, the matcher's `LookupSession` and `RemoveSessionBefore`). Rule 6 (no date in a layer test) is met by section 12's clock.

### 7.3 What a sync costs, and how a drift shows

- **A rebase:** three inserted hunks in upstream's files, each one or two lines at a line upstream rarely moves. No hunk in the selector.
- **A rename upstream** of something the seam uses stops the build of one file, the seam, and the sync check says which name.
- **A change of meaning upstream** that still builds (a key made differently, a refresh that works differently) is caught by a pin, as the persistence branch pins its own reading: `TestProvimuxSessionRouteSeam` sends sample conversations through a real `Manager` with a real `SessionAffinitySelector` and requires, for each kind of identity in section 4.2, that the key the book noted is the key upstream's cache holds, that the release leaves no binding, and that the next `Pick` chooses from the highest priority. When upstream changes how it binds, this test fails at the sync and names the kind.
- **In a test binary the start does nothing** unless a test starts it, as the two list switches and `answered-by` do. Every test of upstream's then runs exactly as on upstream.

## 8. What it rests on

Each piece below can be in the build or not, or switched off. Nothing here needs any of them.

### 8.1 Bindings kept across a restart (`routing.session-affinity-persist`)

| | Without it | With it |
|---|---|---|
| After a restart | no binding: every conversation starts at its first route. Nothing to design | the session cache's bindings are back; the book is empty |
| The rule | | works on a restored binding as on any other: the account, the last use (6.3) and conditions (a) and (b) need no book. Lifetime row 1 is unknown until the conversation's next answer, so rows 2 and 3 apply: it errs towards staying |
| The operator's list | | shows it, with `since` and `fell_back_from` as `null` and `origin: "not-known"` |
| A client's command | | `not-seen` until the conversation's next answer, then as usual (4.3) |
| A binding released just before a crash | | may be restored from the last snapshot (written every 5 minutes and at a clean stop). It is then a binding below its first route like any other, and the rule judges it again |
| Its files, its pin, its tests | | untouched: this design changes no line it reads, and removes bindings only through the cache's own `Invalidate` |

A later, small step, only once that branch is on `main`: its snapshot carries the caller scopes of each binding, so that a client's command works right after a restart (G3, section 11).

### 8.2 The account marker (`models.answered-by`)

It is on `main`. A route carries `account` from `Auth.ProvimuxAccountMarker()`, the function that fills the same field of `GET /v1/models?include=accounts` (`sdk/api/handlers/openai/openai_models_accounts.go:55`).

| Switched off, or where no marker is made (Home mode) | Switched on |
|---|---|
| the function gives nothing and `account` is left out: a route is `provider`, `kind`, `priority`, `host`, and two accounts of one kind and host read the same, as in the list | each route carries `account`, and Pi matches it against the footer's "answered by" |

### 8.3 The figures

Without them, lifetime row 2 is skipped. With them it reads the held document through the reader that branch registers (`modellists.HeldFigures`). G2 declares the reader as a function value that is `nil` on `main`.

## 9. The edges

| Edge | Settled as |
|---|---|
| **An answer in flight when its conversation is released** | it finishes on the account it started on. Its result cannot bring the binding back: a success only refreshes a binding that still names that account (`SessionCache.Touch`, `session_cache.go:380-395`), and a failure only removes one that does (`CompareAndDelete`, `:398`). If the attempt fails and upstream retries inside the same request, the retry is selected without a binding and goes to the first route |
| **A sub-agent** (`session-affinity-subagents: true`) | a child has a binding of its own, made at its first request from the parent's (`selector.go:1796-1810`). Releasing the parent leaves running children where they are: each has its own cache, and they end soon. A child that starts after the release inherits the parent's new account. The rule judges each binding by itself. The parent's last use is not moved by its children ("parent lookups do not extend the parent's TTL", `config.example.yaml:265`), which is right: their requests do not keep the parent's cache warm either. The show says when a conversation is a sub-agent's |
| **`session-affinity-subagents: false`** | nothing inherits; every binding is its own. Nothing differs here |
| **The Codex WebSocket** | a connection's follow-up turns are pinned to the login that served its first (`pinned_auth_id`, `sdk/api/handlers/handlers.go:357-358`) and do not ask the selector. So a release takes effect at the conversation's next connection, or its next HTTP request. Pi keeps an idle socket for 30 minutes (pi-provimux's own setting). After the command, pi-provimux closes the conversation's socket, so the next turn opens one and is routed anew. After the rule, the gateway closes nothing: the next turn on a still-open socket stays on its login, without a binding, and the first turn of the next connection goes to the first route. The show says `websocket_may_hold: true` for a conversation named by Codex headers [infer, I3] |
| **`strategy` other than plain priority** | section 2. The show lists the first routes, so the user checks them against the Models page |
| **Several conversations released at once** | a release sends nothing, so nothing arrives at the recovered account at that moment. Each conversation comes at its own next request, and only conversations whose cache is cold were released by the rule. If the account fails again, upstream fails the conversation over as before; it is then below its first route with a fresh last use, and waits out its cache's lifetime again. No loop is faster than that lifetime |
| **Home mode** | nothing: Home selects. Not started, no binding listed; the show answers `affinity: "home"`, the state says `home-mode` |
| **A plugin scheduler** | as upstream's own lookup, which answers "unsupported" there (`conductor_selection.go:839-841`): `affinity: "not-supported"` |
| **`session-affinity: false`** | nothing is bound and nothing to do. The show answers `affinity: "off"`; the command says every request already goes to the first available route |
| **`session-affinity-rebind-on: quota` or `never`** | fewer bindings move in the first place. One that sits below its first route is judged the same |
| **The routing settings change while running** | upstream builds a new selector with an empty cache (`sdk/cliproxy/service_config.go:110-133`). The book's entries no longer find their binding and are dropped at the next sweep |
| **A name served by accounts of several providers** | upstream keys the binding under `mixed`; the book holds the key as upstream made it (section 3), so nothing is derived |
| **One conversation, two client keys** | one binding (finding 4). Both scopes are in the book; either may see and release it |
| **Compaction** | with a session id the binding is the same conversation's. Bound by content only, upstream may start a new binding at a compaction (`config.example.yaml:249-251`); P1 ends that for Pi |
| **`session-affinity-ttl` shorter than the cache's lifetime** | the binding ends by itself first |

## 10. The setting, its state, and the console

### 10.1 The setting

```yaml
routing:
  # Provimux: a conversation bound to an account below a free higher-priority one goes back once its
  # cache is cold anyway: it has been quiet longer than the cache lives. On unless enabled is false.
  # The lifetime is what the conversation's own answers reported, else providers.dev's figure for the
  # account's entry, else cold-after (default 1h). A conversation with a warm cache is never moved.
  # Needs session-affinity; nothing in Home mode. A client releases its own conversation at any time
  # through POST /v1/provimux/session-route/release, whatever this says.
  # session-affinity-return:
  #   enabled: false
  #   cold-after: "1h"
```

- `enabled`: on unless `false`. `cold-after`: a duration of at least one minute; anything else is refused where the configuration is read, with the key named.
- Beside the affinity settings, because it means nothing without them and the console's routing panel reads the same object.
- Turning it off stops the rule only. Seeing and releasing stay: they are the user's own act.

### 10.2 The value in use

`GET /v8/management/provimux/session-routes` says both what is set and what holds, as the persistence state does:

| Field | |
|---|---|
| `setting.enabled`, `setting.set` | the value as it applies, and whether the configuration sets it |
| `setting.cold_after_s`, `setting.cold_after_set` | the same for the time |
| `in_effect`, `reason` | whether the rule runs, and when not: `session-affinity-off`, `home-mode`, `plugin-scheduler`, `switch-off` |
| `lifetimes` | which of the three sources this build has: `answers`, `figures`, `setting` |
| `counts` | bindings now; below their first route now; released since the start by the rule, by a client, by the operator; the last rule release's time |
| `conversations` | the bindings, each with its routes and a handle to release it by |

### 10.3 The console, in words

A card on the Models page, among the settings below the list (`src/provimux/pages/models/ModelsSettings.tsx`, Provimux's own file), because that page shows the routes and priorities the card speaks of. Not in the routing panel, which is upstream's file.

- **Title:** "Conversations go back to their first route".
- **The switch**, written as "Serve only my names" is written (the configuration path of the key, with the page's tag).
- **Under it, the value in use, always:** "On: a conversation on a later route goes back once its cache is cold." or "Set on, not in effect: session affinity is off." or "Off: a conversation stays where it fell until an hour without a request." The reason comes from the state, never from the switch.
- **A line of counts:** "3 conversations are on a later route now. 12 went back today."
- **The time** (`cold-after`) is shown as the value in use with its source ("1 hour, the default"); editing it is the configuration page's.
- The card is not shown when the gateway has no such path (an older Provimux, or stock CLIProxyAPI): a 404 is "not this build", as for every Provimux path.

A list of the conversations with a release button each is a second step (C2), only if wanted: the state already carries what it needs.

## 11. Pi: `/provimux-route`

In pi-provimux.

**P1a: Pi names its Claude-format conversations.** Models registered with Pi's Anthropic API get `compat.sendSessionAffinityHeaders: true` (`extensions/lib.ts:646-659`). Pi then sends `x-session-affinity: <session id>` on every turn (finding 3), and upstream binds under `affinity:<id>` instead of by content. At the upgrade a running conversation is bound anew once, at its first route. Codex-format models already send `session-id`.

**P1b: the command.**

1. It takes the session's id (`sessionManager.getSessionId()`, as `auto-compact.ts:51-54`) and the session's model, and sends the show request with the header Pi sends for that model's API.
2. It prints, in this order:
   - the route the conversation is bound to (provider, kind, and the account marker where given, the same value as the footer's "answered by"), since when;
   - whether it fell back, from which route and when; or "not known" after a gateway restart;
   - the first routes now, and whether one is free;
   - the cache: "the gateway takes this conversation's cache to live 5 minutes (its own answers said so); it is cold" or "warm for about 3 more minutes";
   - what the rule will do by itself: "it goes back by itself once the cache is cold", or why not.
3. **The cost, before anything is released.** From Pi's own numbers: the context's size (`ctx.getContextUsage()`, as `auto-compact.ts:191`) and the model's prices as Pi holds them. "Going back now: one read of the conversation without cache, about 182,000 tokens, about $0.55 at the list price (more where the vendor charges for writing the cache). Staying: a cached read, about $0.04, while the cache is warm." When the show says the cache is cold: "The cache is cold already: going back costs nothing extra." A subscription route is said as "at API list price, for comparison", as pi-provimux says prices elsewhere.
4. It asks. On yes it sends the release with `expect`, closes the conversation's Codex WebSocket where there is one (`closeOpenAICodexWebSocketSessions(sessionId)`, `extensions/index.ts:848`), and says where the next turn will go. On `409` it shows the new state and asks again.
5. What it says in the other cases: `affinity: "off"` ("nothing is bound: every request already goes to the first available route"); `"home"`; `not-seen` ("this gateway has seen no request of this conversation from your key since it started; send a message and ask again"); `not-bound`; a gateway without the path ("this Provimux has no session routes; they came with <version>"); already on a first route (it still offers the release and says the strategy may choose the same account).

pi-cache-keeper is not changed. Its state would give the cost line a closer "warm until", and the two extensions already exchange events; that is a refinement for later, not a condition.

## 12. What must hold

No test depends on the machine's speed or on the date: every time is on `provimuxSessionRouteNow`, which tests set; the sweep is the function `sweep(now)`, called by the test, never a ticker waited for; no test sleeps. A binding's expiry is upstream's own clock, so a test takes its times from the entry it made (its expiry, minus the TTL, plus the lifetime), never from a literal.

| | Must hold | Proved by |
|---|---|---|
| 1 | with the start not run, every test of upstream's passes as on upstream | the sync check; the start is off in a test binary |
| 2 | the key the book notes is the key upstream holds, for each kind of identity of 4.2 | `TestProvimuxSessionRouteSeam` (7.3) |
| 3 | after a release, the conversation's next `Pick` chooses from the highest available priority, under each of the three strategies | the same, per strategy |
| 4 | a release during an answer in flight: the answer's success does not bring the binding back, nor does its failure remove a newer one | a test that holds a result back with a channel, not a wait |
| 5 | a client key gets `not-seen` for another key's conversation and for none, byte for byte the same | a test of the handler with two keys |
| 6 | the show's routes hold no field a row of `include=accounts` lacks, except `account` | a test that compares the JSON keys with that list's |
| 7 | the rule never releases a binding whose quiet time is under its lifetime plus 60 s, for each of the three lifetime sources, and at one second under | table tests on `sweep(now)` |
| 8 | the rule releases only below a free higher-priority route: not on a first route, not while every higher account is cooling, not for a weight-0 account | the same |
| 9 | a binding refreshed between the judgement and the removal is left | a test that refreshes inside the seam's hook between the two |
| 10 | with the switch off, `sweep` removes nothing and the paths still work | |
| 11 | Home mode, a plugin scheduler, session affinity off: nothing starts, the state names the reason, the show answers as section 9 says | |
| 12 | every answer of the four paths matches `session-route/openapi.yaml` | a contract test over the examples, as the figures' |
| 13 | a follow-up turn on a pinned connection after a release makes no binding | a test with `pinned_auth_id` set (I3) |
| 14 | Pi sends `x-session-affinity` on a Claude-format model and nothing new on a Codex-format one | pi-provimux's request tests |
| 15 | the command releases nothing before it has said the cost and been answered yes | a test of the command with a stub gateway |

## 13. The work packages

Each is one worker and one review. Order: G1; then P1 and G2 side by side; then C1.

| | Repository | Holds | May touch | Must prove | Needs |
|---|---|---|---|---|---|
| **G1** | provimux | the seam, the book with its result hook, the show and the release for a client key | the new files of 7.1 except the sweep, the usage plugin, the setting and the management handlers; `internal/api/server_routes.go` (+1); the README's section; `session-route/openapi.yaml` only to correct it against what is built | rows 1 to 6, 12 (client paths), 13 | nothing |
| **G2** | provimux | the rule: the setting, the usage evidence, the three lifetime sources (the figures' as a reader that is `nil` on `main`), the sweep, the state, the operator's list and release | `provimux_session_route.go` (the sweep and the state), `internal/provimux/sessionroute/`, `internal/config/provimux_session_return.go`, `internal/config/config_types.go` (+2), `config.example.yaml` (+9), `internal/api/handlers/management/provimux_session_routes.go`, `internal/api/server_provimux.go` (+2), the README | rows 7 to 12; that `PUT /v8/management/config/routing/session-affinity-return` writes the setting and the state follows without a restart (I4) | G1 |
| **P1** | pi-provimux | P1a and P1b of section 11; README and changelog | `extensions/lib.ts` (the compat flag), a new `extensions/route.ts`, `extensions/index.ts` (registering the command), tests | rows 14, 15; each case of section 11, step 5, against a stub gateway built from the contract's examples | G1 released, or built against the examples before that |
| **C1** | provimux-ui | the card of 10.3 | a new `src/provimux/pages/models/SessionReturnCard.tsx` with its request file, `ModelsSettings.tsx`, `src/provimux/api/{types,normalize,provimuxApi}.ts`, the locale files | the value in use is shown for each `reason`; a 404 hides the card; a write refused by a changed file (412) writes nothing | G2 released |
| **C2**, only if wanted | provimux-ui | the list of conversations with a release each | the card's files | | C1 |
| **G3**, only once the persistence branch is on `main` | provimux | the snapshot carries each binding's caller scopes; the book is filled from it at the start | that branch's snapshot file and its tests | a client's show works for a restored binding of its own, and `not-seen` for another key's | G1, that branch |

A release of Provimux can carry G1 alone (the command works), or G1 and G2.

## 14. Decisions, and what is not verified

**To decide.** Each with what I recommend.

| | Question | Recommendation |
|---|---|---|
| D1 | Does the rule go by where a binding sits (below a free higher-priority route), or only by a recorded fallback? | Where it sits. A recorded fallback is lost at a restart, and a conversation that started during a cooldown is in the same place for the same reason (finding 2) |
| D2 | Is the first lifetime source the conversation's own answers (the usage record), before providers.dev's figures? | Yes. It is this conversation's cache, said by the vendor, and it exists today. Without it the rule does nothing until the figures are released, and with the figures alone it would wait an hour on every entry that has a 1-hour profile |
| D3 | `cold-after` unless set: one hour, or "never"? | One hour. Under the default TTL the two are the same; with a longer TTL an hour is the longest a cache lives unless a client asked for more |
| D4 | May a client key act only on conversations the gateway has seen it use since it started? | Yes, with G3 later. The alternative, "whoever sends the id", lets one key holder move another's conversation where the id is a chosen word |
| D5 | Does the command leave a conversation's running sub-agents where they are? | Yes |
| D6 | Does Pi send its session id on Claude-format models (P1a)? | Yes. Without it neither the command nor any client can name the owner's conversation. It changes how upstream recognises those conversations (by id, not by content), once, at the upgrade |
| D7 | Is "a higher-priority route is free now" shown to every client key, as one fact about the name? | Yes. It is what the command is for, and a request for the name shows the same |
| D8 | The names: `routing.session-affinity-return`, `/provimux-route`, `/v1/provimux/session-route` | As written |
| D9 | The console's card on the Models page, and C2 left for later | As written |

**Not verified.** Each [infer] and the test that settles it.

| | Reasoned, not verified | Settled by |
|---|---|---|
| I1 | The result handed to a hook carries the metadata `Pick` wrote (the provider, the model and the canonical session id it bound under). I read where `Pick` writes them and where the selector's own `OnResult` reads them back (`selector.go:2286-2292`), not every path a result's options take through the conductor | row 2 of section 12. If a path loses them, the seam derives the key as the selector's `OnResult` does (`selector.go:2348-2367`), by the same unexported functions, and the pin covers that instead |
| I2 | The usage record's `SessionID` is the identity the binding was made under. I read the field and the function that sets it (`usage_helpers.go:217`), not its callers | a test in G2 that sends one conversation through a real manager and requires the evidence to land on its book entry. If it does not match, lifetime row 1 is simply absent and rows 2 and 3 apply: the rule stays safe |
| I3 | A follow-up turn on a Codex WebSocket does not ask the selector. I read where the pinned account is put into the request's metadata, not the conductor's use of it | row 13 |
| I4 | Whether a new key under `routing` needs a line in `internal/config/config_v8.go` to be written through the console's configuration path | G2's write test |
| I5 | What upstream sends a vendor differently when a turn carries `x-session-affinity`: its executors derive some outgoing session values from the request's identity. I did not read them (they are under a protected path, and no line of ours is involved) | P1's reviewer reads `internal/runtime/executor/claude_executor_request.go` for it before the release. `client-parity.md` rule 4.1 stands: nothing is added that makes a request look like a vendor's client |
| I6 | Pi's `x-session-affinity` is sent on the path Pi takes with a Provimux key (the API-key branch of its Anthropic API). I read the branch, and did not run Pi | row 14 |
