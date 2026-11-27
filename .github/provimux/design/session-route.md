# A conversation goes back to its first route (`session-route`)

Design, 2026-11-27. The first part, a client's show and release of its conversation's route, is built and released in v8.1.7-pmx.4. The second, the automatic return, is section 15, which replaces the rule's first design (sections 5, 6, 8.3 and 10). The contract a build is checked against is `session-route/openapi.yaml`, beside this file.

**What the owner met (2026-11-27).** One model name can be served by several of his accounts, tried by priority. His gateway runs with `routing.session-affinity: true`, so upstream binds a conversation to the account that served it and keeps that account "even if a higher-priority credential recovers" (`config.example.yaml:253-255`). His Pi conversation on one name fell to another login while the first had a cooldown, and stayed there: a 5-minute cache where the first route's has an hour. His words: "how can i make the session fetch again and maybe switch to the default (first one in the list) not fallback?" Today the ways back are an hour without a request, a new session, or a restart of the gateway.

**What he gets.**

1. A command in Pi that shows where this conversation is bound, since when, whether a higher-priority route is free, and what going back costs; and that releases the binding.
2. The gateway by itself: a conversation that sits below a free higher-priority route goes back at its next message once no message of it was routed to an account for a time the operator sets. Off until that time is set (section 15).

**The first release**, v8.1.7-pmx.4 (decided on 2026-11-27): the first of the two only, made right. A client sees its conversation's route and releases it. The rule's first design (a sweep, lifetime evidence, the operator's list and release) is replaced by section 15: one setting, `routing.session-affinity-return.cold-after`, decided at the conversation's own next request.

**Read from.**

| Project | Commit |
|---|---|
| Provimux | `main` at `c80afa86c`, on CLIProxyAPI v8.1.7. Every `file:line` below is in that commit. This design was begun at `9d0786d95` and brought forward when `main` gained the account marker and the restart drain; of the files cited here only `config.example.yaml` and `sdk/cliproxy/service_config.go` had changed, and their lines are the new ones |
| Bindings kept across a restart (not on `main` when this was written) | its design, `session-affinity-persist.md` |
| The account marker | on `main` at `c80afa86c` (`Auth.ProvimuxAccountMarker`, `models.answered-by`). Its design is not in this repository |
| The figures (not on `main`) | their design, not in this repository. A section there is the first sketch of this rule; this file replaces it |
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
| Seeing and releasing, for the operator | `/v8/management/provimux/session-routes`, behind the management key: all of them. **Withdrawn by section 15.5** |
| The rule | every 15 seconds: a binding that sits below a free higher-priority route, and has been quiet for longer than its cache lives, is released. `routing.session-affinity-return`, on unless set. **Replaced by section 15** |
| Pi | `/provimux-route`: shows, says the cost, releases. And Pi starts sending its session id on Claude-format models, without which the gateway cannot name the conversation at all (finding 3) |
| The console | a card on the Models page: the switch, the value in use and why, how many conversations sit on a later route, how many went back. **Replaced by section 15** |

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
- **As built** (decided on 2026-11-27):
  - **Owned by the three values, never by upstream's string** (the second design of the rule, section 15): a conversation's owner is recorded against the three values upstream hands over separately, the provider it bound under, the session identity and the model, never against their joined form; the joined key is only an address into upstream's cache. Two different triples that join to one key are, for upstream, one binding; the book cannot tell whose it is, so for a client that binding is `not-seen` in both directions. As built: the book notes no triple any of whose parts holds `::` (only then can two triples join to one key), and before a show or a release the binding found must have been chosen for that very session (upstream's binding records it).
  - **Entries leave** once upstream holds no binding for them and the binding's lifetime has passed since their last answer: the book holds no more than the cache's TTL of history, besides its 4,096 bound.
  - **Only where session affinity runs.** With `routing.session-affinity` off or in Home mode the book is not subscribed to results and holds nothing; it follows every committed config both ways (one call where the service commits a config, one where a start takes its first). Under a plugin scheduler it stops at the first result it sees and keeps nothing. No timer. A book does not keep its manager alive.
  - **No usage record** is read in this release (the lifetime evidence is the rule's).

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
| 5 | `X-Session-ID`, `X-Session-Affinity` (OpenCode; Pi on Claude-format models after Pi's change, section 11), `X-Slot-Session-Id`, `X-Task-ID`, `X-Conversation-Id`, `X-Thread-Id`, `X-Client-Request-Id` | the same header |
| 6 to 11 | fields of the body: `cachedContent`, `thread_id`, `session_id`, `prompt_cache_key`, `conversation`, `metadata.user_id`, `conversation_id` | the same fields inside `identity` |
| after those | the shared start of the content (the prefix matcher), a derived identity, a hash of the first messages | **cannot be named.** Such a conversation is seen and released by the operator only, and returned by the rule |

`identity` is an object that holds only the body fields a turn of this client carries its session in; the gateway hands it to the extraction as the turn's body would be. Ids travel in headers and the body, never in a URL, so no access log holds them.

**The name** is `model`, as the client sends it on a turn. A binding is per name: a conversation that used two names has two. A thinking suffix is cut as upstream cuts it (`canonicalModelKey`, `selector.go:842`). As built: the name is resolved as the request path resolves a turn's name before an account is chosen, so a turn sent as `auto` is found when asked as `auto`: with "serve only my names" in force the door's `auto` (the first of the owner's served names), then upstream's (`util.ResolveAutoModel`, `sdk/api/handlers/handlers_routing.go:169-189`); none in Home mode. The answer gives the routed name as `routed_model`, a model `GET /v1/models` lists. These are the only renamings before routing; a plugin's model router reads the turn's body and cannot be reproduced. So asking under the name sent finds a turn for a plain name, and for `auto` while the catalog's first model has not changed since the turn; after the catalog changed, or where a plugin rewrote the name, the client asks under the model the answer named (`routed_model`), or the router's target.

### 4.3 Who may

| Caller | Sees and releases |
|---|---|
| A configured client key | a conversation whose book entry holds this key's caller scope: one the gateway has seen this key use since it started |
| The management key | every binding, through the management path. **Withdrawn by section 15.5** |

- **A conversation that is another key's, and one that does not exist, get the same answer:** `conversation: "not-seen"`. Nothing tells them apart.
- **Why not "whoever knows the session id":** an id can be a word the user chose (`X-Session-ID: main`, a `prompt_cache_key`). A second key holder could then move another's conversation at will.
- **The cost of the strict rule** is one case: after a restart that kept the bindings (8.1), the book is empty, and a client's command answers `not-seen` until the conversation's next answer. The rule and the operator are not affected.
- **Under "serve only my names"** a name that is not served is refused as a turn for it would be, with the setting's message.

### 4.4 What is shown

To a client key, per route, exactly what a row of `GET /v1/models?include=accounts` holds: `provider`, `kind`, `priority`, `host`; and `account`, the account marker, where the build has it and `models.answered-by` is on. No login name, e-mail, file, index or cooldown. As built: an account that list does not show now for the name (disabled, or no longer serving it) is shown as `{"listed": false}` and nothing else, and `position` is then `null`; `first` holds only rows the list shows.

| Shown | New to a holder of a client key? |
|---|---|
| the route this conversation is bound to | no: every answer of the conversation names its account (`X-Provimux-Account`), and the list has the row |
| since when; the route it fell back from, and when | no: the same answers, read in order |
| the first routes now (the rows of the highest free priority) | the rows are in the list. **That they are free now is new**, as one fact about the name, not per account: `higher_priority_free`. A request for the name shows the same by who answers it |
| when a higher-priority route is expected free: `position.higher_priority_free_at` (added on 2026-11-27) | the end of the recorded cooldown that ends first among the accounts of a higher priority than the bound one; `null` when one is free now or none has a recorded end. One time for the name, never per account: no account is named, and a request for the name after that time shows the same |
| how long the gateway takes this conversation's cache to live, and on what basis | **withdrawn** (the second design of the rule): the show has no `cache`; `rule` says `in_effect` and `cold_after_s` (section 15.4) |

The operator's list adds what `model-routes` already shows him: the login's name and the recorded cooldown.

**Checking again** (added on 2026-11-27, from the owner's question "will I be able to refresh and check again if the standard path is available?"): the show is that check. Every call answers from the gateway's state at that moment, nothing is cached for it, and it sends nothing to a vendor and ends no cooldown.

### 4.5 Releasing

1. The binding is looked up again in upstream's store. The request may carry `expect`, the token the show answered with; when the binding has changed since, nothing is released and the answer is `409` with the state now. As the second design of the rule says it: **`expect` names one life of a binding**, from the answer after which the book first holds it to its removal by anyone. Upstream's entry has nothing that lasts exactly that long, so the life is the book's own: a number made when the book starts to hold the binding and dropped when it stops, and the check of that number and the removal from the cache are made under the book's lock, in that order, so no answer can start a new life between them. A binding that ended by its TTL and was made again on the same account is the same life (the second design of the rule): an old token still releases it. A token that is present must be a token (`minLength: 1`), and a body that is not one object of the schema is refused. As built: a life ends at a client's release, at a failed answer for which upstream removes the binding (the book reads upstream's own decision right before it), when the binding is found on another account or in another cache instance, and when its account is registered again; a removal the book cannot see (the cache's own eviction at 65,536 keys) is not one. A non-empty `expect` that names no life (a blank one included) is `409`; an empty or `null` one is `400`.
2. Every key of the binding's group is removed from the session cache (the conversation's key and its aliases), or the matcher's session.
3. The book's entry is closed: the release is counted, by whom (a client's request, the operator's, the rule), and the entry's binding (account, since, the route before) is cleared. As built, the entry itself and its caller scopes stay until the binding's lifetime has passed (section 3), so that the key that used the conversation is answered `not-bound` afterwards, as the contract says, and not `not-seen`.

Nothing else happens. No request is sent, no account's state changes, no credit of the weighted strategy moves. Releasing twice is not an error: the second answers `released: false`, `conversation: "not-bound"`.

## 5. The rule (replaced by section 15)

`routing.session-affinity-return`, in effect only where session affinity runs (section 9). Every 15 seconds one function, `sweep(now)`, looks at each binding and releases it when **all** of these hold:

| | Condition | Read from |
|---|---|---|
| a | it sits below a free higher-priority route | 6.1 |
| b | the conversation has been quiet longer than its cache lives, plus 60 seconds | 6.2, 6.3 |
| c | it is still the binding that was judged | the removal compares the account and the expiry it read; a binding used in between is left |

A request that arrives between two sweeps after (b) became true is served by the bound account once more, and its binding is fresh again. That errs towards staying, which loses nothing. One sweep asks for each name's routes once, whatever the number of conversations.

**Why a sweep and not a test where the binding is read.** The test in `Pick` is exact to the request and costs an inserted block in the file upstream changes most (13 times between v8.0.60 and v8.1.3), at three places a binding is honoured (`selector.go:1738`, `:1797`, `:1851`). The sweep is exact to 15 seconds in the harmless direction and costs no line there.

## 6. The three things the gateway must know (replaced by section 15)

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
| `sdk/cliproxy/service_config.go` | +1 | `provimuxFollowSessionRoute(s.coreManager, newCfg)` beside the layer's two calls where a config is committed (`provimuxSetNamesRule`, `provimuxSetAnsweredBy`), so the book follows the hot reload. Upstream's file, at an anchor the layer already has |
| `sdk/cliproxy/builder.go` | +1 | the same for the first config of a start, beside the same two calls. Upstream's file, at an anchor the layer already has |
| `internal/config/config_types.go` | +2 | the field `SessionAffinityReturn`, with its comment on the line, after `SessionAffinitySubagents` (`:608`). Upstream's file |
| `config.example.yaml` | +9 | the setting, commented, after `session-affinity-subagents` (`:269`). Upstream's file |
| `internal/api/server_provimux.go` | +2 | the two management routes. The layer's own file |
| `sdk/cliproxy/auth/selector.go`, `session_cache.go`, `sdk/cliproxy/session/**`, `sdk/cliproxy/service_config.go` | **0** | |

Possibly one more [infer, I4]: `models.only-my-names` needed one line in `internal/config/config_v8.go` for the console's `PUT /config/...` to reach it. The rule's build finds whether a key under `routing` needs the same.

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

A later, small step, now that the bindings are kept across a restart: their snapshot carries the caller scopes of each binding, so that a client's command works right after a restart (the restored scopes, section 13).

### 8.2 The account marker (`models.answered-by`)

It is on `main`. A route carries `account` from `Auth.ProvimuxAccountMarker()`, the function that fills the same field of `GET /v1/models?include=accounts` (`sdk/api/handlers/openai/openai_models_accounts.go:55`).

| Switched off, or where no marker is made (Home mode) | Switched on |
|---|---|
| the function gives nothing and `account` is left out: a route is `provider`, `kind`, `priority`, `host`, and two accounts of one kind and host read the same, as in the list | each route carries `account`, and Pi matches it against the footer's "answered by" |

### 8.3 The figures

Without them, lifetime row 2 is skipped. With them it reads the held document through the reader the figures register (`modellists.HeldFigures`). The rule's build declares the reader as a function value that is `nil` on `main`.

## 9. The edges

| Edge | Settled as |
|---|---|
| **An answer in flight when its conversation is released** | it finishes on the account it started on. Its result cannot bring the binding back: a success only refreshes a binding that still names that account (`SessionCache.Touch`, `session_cache.go:380-395`), and a failure only removes one that does (`CompareAndDelete`, `:398`). If the attempt fails and upstream retries inside the same request, the retry is selected without a binding and goes to the first route. Decided on 2026-11-27 as upstream's behaviour: under the default rebind policy, if the conversation was bound again **to the same login** before that answer ends, and the answer then fails with a fault of that login, upstream removes the newer binding too (with `session-affinity-rebind-on: never` the newer binding stays) (it removes a binding to a login that just failed); the conversation is bound afresh at its next message. A binding to another login is untouched. `TestProvimuxSessionRouteSameLoginFailureRemovesNewer` pins it |
| **A sub-agent** (`session-affinity-subagents: true`) | a child has a binding of its own, made at its first request from the parent's (`selector.go:1796-1810`). Releasing the parent leaves running children where they are: each has its own cache, and they end soon. With inheritance on (upstream's default), a released child's next turn takes its parent's binding again, even where the show named a higher account as first, and a child that starts after a release takes the parent's binding as it is then; with inheritance off, a new child is routed on its own. A Codex fork names its source as its parent but is not a sub-agent: the show says `subagent: false` for it. The rule judges each binding by itself. The parent's last use is not moved by its children ("parent lookups do not extend the parent's TTL", `config.example.yaml:265`), which is right: their requests do not keep the parent's cache warm either. The show says when a conversation is a sub-agent's |
| **`session-affinity-subagents: false`** | nothing inherits; every binding is its own. Nothing differs here |
| **The Codex WebSocket** | decided on 2026-11-27 (I3 did not hold). A connection's follow-up turns are pinned to the login that served it (`pinned_auth_id`, `sdk/api/handlers/openai/openai_responses_websocket.go:1095`), and they **do** ask the selector: the candidates are narrowed to the pinned login and `Pick` still runs, so after a release the next turn on a still-open socket binds the conversation to that login again. **A release holds only if that connection is closed before its next turn**; then the next connection's first turn goes to the first route. A turn on the open connection binds the conversation to the old login again, and closing the connection afterwards does not undo that: release again, then close (corrected on 2026-11-27). No line of the selector changes, and no code of ours undoes what upstream's selection did. Pi keeps an idle socket for 30 minutes (pi-provimux's own setting); right after the release, before any further turn, pi-provimux closes the conversation's socket. The show and the release say `websocket_may_hold: true` for a conversation that has had a turn on a connection pinned to its login (the turn carries the connection's execution session, or a pin), whatever header names it, and for one named by Codex headers. `TestProvimuxSessionRoutePinnedFollowUpRebinds` and `TestProvimuxSessionRouteWebsocket` (a real loopback socket, every way of naming) pin today's behaviour, so a change upstream shows at a sync |
| **`strategy` other than plain priority** | section 2. The show lists the first routes, so the user checks them against the Models page |
| **Several conversations released at once** | a release sends nothing, so nothing arrives at the recovered account at that moment. Each conversation comes at its own next request, and only conversations whose cache is cold were released by the rule. If the account fails again, upstream fails the conversation over as before; it is then below its first route with a fresh last use, and waits out its cache's lifetime again. No loop is faster than that lifetime |
| **Home mode** | nothing: Home selects. Not subscribed, nothing kept; the show and the release answer `affinity: "home"` |
| **A plugin scheduler** | as upstream's own lookup, which answers "unsupported" there (`conductor_selection.go:839-841`): `affinity: "not-supported"`. The book stops at the first result it sees and keeps nothing; a show subscribes it again once affinity runs |
| **`session-affinity: false`** | nothing is bound and nothing to do. Not subscribed, nothing kept: a result costs what it costs without Provimux. The show answers `affinity: "off"`; the command says every request already goes to the first available route. A hot reload that turns affinity on starts the book; one that turns it off stops it and forgets what it held |
| **`session-affinity-rebind-on: quota` or `never`** | fewer bindings move in the first place. One that sits below its first route is judged the same |
| **The routing settings change while running** | upstream builds a new selector with an empty cache (`sdk/cliproxy/service_config.go:110-133`). The book's entries no longer find their binding, are answered `not-bound`, and leave once their lifetime has passed; a token names the cache instance too, so an older token never matches a binding of the new cache |
| **A name served by accounts of several providers** | upstream keys the binding under `mixed`; the book holds the key as upstream made it (section 3), so nothing is derived |
| **One conversation, two client keys** | one binding (finding 4). Both scopes are in the book; either may see and release it |
| **Compaction** | with a session id the binding is the same conversation's. Bound by content only, upstream may start a new binding at a compaction (`config.example.yaml:249-251`); Pi's change (section 11) ends that for Pi |
| **`session-affinity-ttl` shorter than the cache's lifetime** | the binding ends by itself first |

## 10. The setting, its state, and the console (replaced by section 15.4)

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

A list of the conversations with a release button each is a second step, only if wanted: the state already carries what it needs.

## 11. Pi: `/provimux-route`

In pi-provimux.

**a. Pi names its Claude-format conversations.** Models registered with Pi's Anthropic API get `compat.sendSessionAffinityHeaders: true` (`extensions/lib.ts:646-659`). Pi then sends `x-session-affinity: <session id>` on every turn (finding 3), and upstream binds under `affinity:<id>` instead of by content. At the upgrade a running conversation is bound anew once, at its first route. Codex-format models already send `session-id`.

**b. the command.**

1. It takes the session's id (`sessionManager.getSessionId()`, as `auto-compact.ts:51-54`) and the session's model, and sends the show request with the header Pi sends for that model's API.
2. It prints, in this order:
   - the route the conversation is bound to (provider, kind, and the account marker where given, the same value as the footer's "answered by"), since when;
   - whether it fell back, from which route and when; or "not known" after a gateway restart;
   - the first routes now, and whether one is free;
   - the cache: withdrawn from the show (the second design of the rule); Pi says what it knows of its own cache, if anything;
   - what the rule will do by itself: the line of section 15.4, from the show's `rule`.
3. **The cost, before anything is released.** From Pi's own numbers: the context's size (`ctx.getContextUsage()`, as `auto-compact.ts:191`) and the model's prices as Pi holds them. "Going back now: one read of the conversation without cache, about 182,000 tokens, about $0.55 at the list price (more where the vendor charges for writing the cache). Staying: a cached read, about $0.04, while the cache is warm." When the show says the cache is cold: "The cache is cold already: going back costs nothing extra." A subscription route is said as "at API list price, for comparison", as pi-provimux says prices elsewhere.
4. It asks. On yes it sends the release with `expect`, closes the conversation's Codex WebSocket where there is one (`closeOpenAICodexWebSocketSessions(sessionId)`, `extensions/index.ts:848`) before any further turn, and says where the next turn will go. On `409` it shows the new state and asks again (the token names the binding's life: it changes only when the binding was removed and made again, not at a turn or a keep-warm request).
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
| 7 (replaced by 15.6) | the rule never releases a binding whose quiet time is under its lifetime plus 60 s, for each of the three lifetime sources, and at one second under | table tests on `sweep(now)` |
| 8 (replaced by 15.6) | the rule releases only below a free higher-priority route: not on a first route, not while every higher account is cooling, not for a weight-0 account | the same |
| 9 (replaced by 15.6) | a binding refreshed between the judgement and the removal is left | a test that refreshes inside the seam's hook between the two |
| 10 (replaced by 15.6) | with the switch off, `sweep` removes nothing and the paths still work | |
| 11 | Home mode, a plugin scheduler, session affinity off: nothing starts (no subscription, no record, no timer), following the config's hot reload both ways; the show and the release answer as section 9 says | `TestProvimuxSessionRouteFollowsConfig`, `TestProvimuxSessionRouteFollowsHotReload`, `TestProvimuxSessionRouteFootprint` |
| 12 | every answer of the four paths matches `session-route/openapi.yaml` | a contract test over the examples, as the figures' |
| 13 | after a release and a closed socket, the next connection's first turn binds at the first route (decided on 2026-11-27; a pinned follow-up on the still-open socket binds to its login again, and a test pins that too) | `TestProvimuxSessionRoutePinnedFollowUpRebinds` (I3) |
| 14 | Pi sends `x-session-affinity` on a Claude-format model and nothing new on a Codex-format one | pi-provimux's request tests |
| 15 | the command releases nothing before it has said the cost and been answered yes | a test of the command with a stub gateway |

## 13. The parts, and their order

Order: the client's show and release; then Pi's command and the rule side by side; then the console's card.

| | Repository | Holds | May touch | Must prove | Needs |
|---|---|---|---|---|---|
| **The client's show and release** (released in v8.1.7-pmx.4) | provimux | the seam, the book with its result hook, the show and the release for a client key | the new files of 7.1 except the sweep, the usage plugin, the setting and the management handlers; `internal/api/server_routes.go` (+1); the README's section; `session-route/openapi.yaml` only to correct it against what is built | rows 1 to 6, 12 (client paths), 13 | nothing |
| **The rule's first design** (replaced by section 15) | provimux | the rule: the setting, the usage evidence, the three lifetime sources (the figures' as a reader that is `nil` on `main`), the sweep, the state, the operator's list and release | `provimux_session_route.go` (the sweep and the state), `internal/provimux/sessionroute/`, `internal/config/provimux_session_return.go`, `internal/config/config_types.go` (+2), `config.example.yaml` (+9), `internal/api/handlers/management/provimux_session_routes.go`, `internal/api/server_provimux.go` (+2), the README | rows 7 to 12; that `PUT /v8/management/config/routing/session-affinity-return` writes the setting and the state follows without a restart (I4) | the client's show and release |
| **Pi's command** | pi-provimux | parts a and b of section 11; README and changelog | `extensions/lib.ts` (the compat flag), a new `extensions/route.ts`, `extensions/index.ts` (registering the command), tests | rows 14, 15; each case of section 11, step 5, against a stub gateway built from the contract's examples | the client's show and release released, or built against the examples before that |
| **The console's card** | provimux-ui | the card of 15.4 | a new `src/provimux/pages/models/SessionReturnCard.tsx` with its request file, `ModelsSettings.tsx`, `src/provimux/api/{types,normalize,provimuxApi}.ts`, the locale files | the value in use is shown for each `reason`; a 404 hides the card; a write refused by a changed file (412) writes nothing | the rule released |
| **A list of conversations**, only if wanted | provimux-ui | the list of conversations with a release each | the card's files | | the console's card |
| **The restored scopes**, only once the bindings are kept across a restart | provimux | the snapshot carries each binding's caller scopes; the book is filled from it at the start | the bindings' snapshot file and its tests | a client's show works for a restored binding of its own, and `not-seen` for another key's | the client's show and release, bindings kept across a restart |

A release of Provimux can carry the client's show and release alone (the command works), or that and the rule. The first, v8.1.7-pmx.4, carries the show and release alone (decided on 2026-11-27).

## 14. Decisions, and what is not verified

**To decide.** Each with what I recommend.

| | Question | Recommendation |
|---|---|---|
| D1 | Does the rule go by where a binding sits (below a free higher-priority route), or only by a recorded fallback? | Where it sits. A recorded fallback is lost at a restart, and a conversation that started during a cooldown is in the same place for the same reason (finding 2) |
| D2 | Is the first lifetime source the conversation's own answers (the usage record), before providers.dev's figures? | Yes. It is this conversation's cache, said by the vendor, and it exists today. Without it the rule does nothing until the figures are released, and with the figures alone it would wait an hour on every entry that has a 1-hour profile |
| D3 | `cold-after` unless set: one hour, or "never"? | One hour. Under the default TTL the two are the same; with a longer TTL an hour is the longest a cache lives unless a client asked for more |
| D4 | May a client key act only on conversations the gateway has seen it use since it started? | Yes, with the restored scopes later (section 13). The alternative, "whoever sends the id", lets one key holder move another's conversation where the id is a chosen word |
| D5 | Does the command leave a conversation's running sub-agents where they are? | Yes |
| D6 | Does Pi send its session id on Claude-format models (section 11, part a)? | Yes. Without it neither the command nor any client can name the owner's conversation. It changes how upstream recognises those conversations (by id, not by content), once, at the upgrade |
| D7 | Is "a higher-priority route is free now" shown to every client key, as one fact about the name? | Yes. It is what the command is for, and a request for the name shows the same |
| D8 | The names: `routing.session-affinity-return`, `/provimux-route`, `/v1/provimux/session-route` | As written |
| D9 | The console's card on the Models page, and the list of conversations left for later | As written |

**Not verified.** Each [infer] and the test that settles it.

| | Reasoned, not verified | Settled by |
|---|---|---|
| I1 | The result handed to a hook carries the metadata `Pick` wrote (the provider, the model and the canonical session id it bound under). I read where `Pick` writes them and where the selector's own `OnResult` reads them back (`selector.go:2286-2292`), not every path a result's options take through the conductor | **Settled when built: it holds** on the non-streaming, streaming and counting paths, one provider and several (`TestProvimuxSessionRouteResultKeys`). The key's provider part is `mixed` there, while the result's own provider is the account's: a key derived from the result's provider, as `OnResult` falls back to, is not `Pick`'s. So where a path loses the keys the seam derives them as `OnResult` does and then requires upstream's cache to hold that key bound to the result's account; else the book notes no binding (decided on 2026-11-27) |
| I2 | The usage record's `SessionID` is the identity the binding was made under. I read the field and the function that sets it (`usage_helpers.go:217`), not its callers | a test in the rule's build that sends one conversation through a real manager and requires the evidence to land on its book entry. If it does not match, lifetime row 1 is simply absent and rows 2 and 3 apply: the rule stays safe |
| I3 | A follow-up turn on a Codex WebSocket does not ask the selector. I read where the pinned account is put into the request's metadata, not the conductor's use of it | **Settled when built: it does not hold.** A pinned turn asks the selector, which binds it to the pinned login. Decided on 2026-11-27: no selector line; a release holds once the connection is closed (section 9, row 13) |
| I4 | Whether a new key under `routing` needs a line in `internal/config/config_v8.go` to be written through the console's configuration path | the rule's write test |
| I5 | What upstream sends a vendor differently when a turn carries `x-session-affinity`: its executors derive some outgoing session values from the request's identity. I did not read them (they are under a protected path, and no line of ours is involved) | Whoever reviews Pi's change reads `internal/runtime/executor/claude_executor_request.go` for it before the release. `client-parity.md` rule 4.1 stands: nothing is added that makes a request look like a vendor's client |
| I6 | Pi's `x-session-affinity` is sent on the path Pi takes with a Provimux key (the API-key branch of its Anthropic API). I read the branch, and did not run Pi | row 14 |

## Known limits, accepted by the owner on 2026-11-27

The owner's rule: a finding holds work back only if it can happen in ordinary use without someone forcing it. These cannot, and are written down instead. Each says what a user would see.

1. **A release without `expect`, in the instant the gateway forgets its caller.** The release checks the caller's right just before it takes the book's lock. If, in that instant, the book drops this key from the conversation (a fifth key used it, or its entry was forgotten and made again by another key's answer), the release can remove a binding that by then belongs only to another key's conversation of the same name. With `expect` it cannot: the token names the life the caller was shown.
2. **Ids and names that begin or end with a colon.** Ids, provider parts and names containing `::` are refused (section 3). One that begins or ends with a single colon can still meet another's colon at upstream's `::` joint, so two conversations can look like one to upstream's cache, which then holds one binding for both; such a pair may be shown or released together.
3. **`expect` after two rare events.** After upstream's cache dropped a binding at its capacity (65,536 conversations) and the same account was bound again, an old token still matches. After a binding ended by its lifetime and a show came before it was made again on the same account, an old token conflicts (`409`), although the design calls that the same life. In both cases: show again and use the new token.
4. **A model whose every registration is suspended.** The model list then shows no account for it, while a show of a conversation still bound there gives that account's provider, kind and priority (and a release names it in `was`).
5. **Around a configuration reload, or a plugin scheduler added or removed.** A turn that ends in the instant of the change may go unrecorded: a result that comes before the new selector is in place stops the book, and nothing observes until the next show starts it again. The user sees `not-seen` for the conversation until its next message after that show (a check pins that the show starts it: `TestProvimuxSessionRouteShowSubscribesAgain`). In the other direction, one late result may be recorded just after the feature was turned off; when affinity is turned on again, that conversation can be answered `not-bound` instead of `not-seen` until its binding's lifetime has passed.

## 15. The automatic return

The rule by which a conversation goes back by itself. **This section replaces sections 5, 6, 8.3 and 10, the rule's rows of 7, 8.1, 12 and 13, and decisions D1 to D3.** Where they differ from it, it holds. Every `file:line` is on `main` at `760ff97c3`.

The first design judged a conversation from outside any request of it: a sweep every 15 seconds. A sweep has no request in front of it, so everything a request settles by existing (which accounts it may use, whether an answer is under way, which conversations to leave alone, whether anything changed between judging and removing) had to be worked out or remembered, and each was a place to be wrong towards "release". Its review found twelve such places. This rule is decided **at the conversation's own next request**, inside upstream's selection, and what it cannot know there it does not use.

### 15.1 The rule

> When you have told the gateway how long a conversation must be unused before its cache can be taken as cold (`cold-after`), then at a conversation's next request, if it is still bound to an account below the highest-priority account that request could use right now, and it has had no message routed to an account for that long, the gateway drops the binding before choosing, and upstream chooses for that request as for a new conversation. Whenever the gateway is not certain of any part of that, it does nothing, and the conversation stays where it is.

It is in effect only when the operator has set the time. The gateway knows no cache's lifetime and claims none.

**The promise: the rule never moves a conversation that had a message routed to an account within `cold-after`.** What does not count as use: a message the gateway refused before choosing an account (for example while every account that serves the name is cooling), which reaches no provider and reads no cache; and an answer while it streams, so the message after an answer that itself took longer than `cold-after` can go back. That is the right measure: a provider's prompt cache is refreshed when a request reads it, not while the answer is written. The gateway cannot see how long a provider keeps a cache, so the operator chooses the time.

### 15.2 Everything it must know

Seven things, read in one call, in this order. The first that is not certain ends the call with nothing dropped.

| | It must know | Known for certain from | Not certain: nothing |
|---|---|---|---|
| K1 | the operator's time, and that the rule is in effect | one value, loaded once at the top of the call: `cold-after` as committed, or none. Then the manager and the selector as they run: not Home mode, no plugin scheduler, the selector does not refuse to move a bound conversation (`rebind-on: never`), and `cold-after` is below the selector's binding lifetime | no value: the call returns at once. A value but not in effect: the message is noted (K7), nothing is judged |
| K2 | that the client named this conversation, and that it is not a sub-agent or a fork | `explicitID != ""`, `isSubagent`, `isFork`: upstream's own values for this request, computed before the line | a conversation upstream names by a derived identity or by its first messages; a sub-agent or a fork (it takes its parent's account on a miss, so removing its binding would only bind it again). Not noted either |
| K3 | the binding of this request's key: its account and its expiry | upstream's cache entry, read under the cache's write lock, held until the call ends | no entry, or an expired one |
| K4 | that the request's second key, where it has one, is in the same binding | the entry of `fallbackKey` is absent, or is the same group as K3's | another group holds it: after a removal upstream would bind to that group's account |
| K5 | the accounts this request may use now, the bound one among them and below the highest | `available`, the list upstream chooses from right after the line (after the provider set, the route's restrictions and a pinned account were applied), and `authPriority` of each | the bound account is not in the list (upstream fails over by itself); nothing in the list is above it. A pinned request's list is its pinned account alone, so it always ends here |
| K6 | when the conversation last had a message routed to an account | the rule's own note (K7), the latest among the keys of the binding's group | no note: the first message after a start, a restore, a new selector, a change of `cold-after`, or the table emptied |
| K7 | our note: a request of this key came to selection, and when | a table on the selector, written at every call that passed K1's first step and K2, judged or not, **after** the judgement and under the same lock, so the judgement reads the message before this one. Emptied when it reaches 16,384 keys and when the value in use changes (set, changed, removed and set again); an unchanged value keeps it | |

Then one comparison: `now − last routed message ≥ cold-after`, two durations, where `now` is the time `Pick` took for this request. True: the binding's group is removed (`removeAliasGroupLocked`), one counter is raised, and the call returns. Upstream's next line finds no binding and binds the request at the highest available priority.

**Why the note and not upstream's expiry.** Upstream refreshes a binding's expiry at a selection and again at a successful result, and the result's refresh is skipped where the result does not reach affinity (the Responses handler stops after delivering `response.completed`; an access token rotated during the answer makes the result's credential version stale). The expiry can then say "used a moment ago" for a message sent long before. The note is written by the rule at every request of the conversation, pinned turns included, and by nothing else; it holds `Pick`'s monotonic time, so a step of the wall clock does not matter.

**Nothing else is used.** No book, no result hook, no usage record, no figures, no timer, nothing from a file.

### 15.3 Where it sits

Two lines in `sdk/cliproxy/auth/selector.go`, both the layer's: the field (`:1411`) and the call, the statement right before the binding is looked up in `Pick` (`:1739`).

```go
	provimux          provimuxSessionReturn // Provimux: session-route (design section 15)
```

```go
	s.provimuxReturn(explicitID != "" && !isSubagent && !isFork, cacheKey, fallbackKey, available, now) // Provimux: session-route (design section 15)
	if cachedAuthID, ok := s.cache.GetAndRefresh(cacheKey); ok {
```

- **Every value it passes exists there already.** The call returns nothing: it removed the binding or it did not, and upstream's lines after it decide everything else. A conversation the prefix matcher holds returns from `Pick` before the line.
- **The notes are on the selector**, guarded by the cache's own lock, because their life must be the selector's: upstream makes a new selector when the routing settings change, and a note must not outlive the cache it speaks of. Process-wide there is only the value in use and the count.
- **The value in use** is stored by part one's configuration hook, `provimuxFollowSessionRoute`, where a configuration is committed (`sdk/cliproxy/service_config.go:183`) and for the first configuration of a start (`builder.go:320`): `cold-after` and the manager, as one pointer, stored again only when one of them changes. Whether the rule is in effect is not stored: the call reads it from the manager and the selector as they run, and so does the state (15.4), so the two cannot disagree.
- **Unset**, the call is one atomic load and a return: nothing locked, nothing written, and every request is selected exactly as on upstream (`BenchmarkProvimuxSessionReturnCall`).
- **A return ends the binding's life for part one's `expect`.** The rule marks the binding's keys with a return number, kept apart from the notes and dropped only for keys the cache no longer holds. Part one's book reads it in its answer hook: the first answer after a return starts a new life, from the first route (`origin: first`), so a token shown before the return no longer releases the binding.
- **Upstream's unexported names it uses:** the selector's `cache` and `rebindOnNever`; the cache's `mu`, `entries`, `ttl`, `maxEntries`, `ensureInitializedLocked` and `removeAliasGroupLocked`; an entry's `authID`, `expiresAt` and `aliases`; `sameSessionEntryGroup`; `authPriority`; the manager's `Selector`, `HomeEnabled` and `hasPluginScheduler`. A rename stops the build; a change of meaning is caught by rows 1 and 15 of 15.6.
- **Upstream lines:** `selector.go` +2, `internal/config/config_types.go` +2 (the setting's field), `config.example.yaml` +9. Nothing in `sdk/cliproxy/session/**` or `session_cache.go`.

Files: `sdk/cliproxy/auth/provimux_session_return.go` (the rule, the value in use, the state), `internal/config/provimux_session_return.go` (the setting), `internal/api/handlers/management/provimux_session_return.go` (the state path), and their tests.

### 15.4 The setting, the state, the console

```yaml
routing:
  # Provimux: at a conversation's next request, if it is bound to an account below the highest-priority
  # account that request could use, and no message of it was routed to an account for cold-after, the
  # binding is dropped and the request is routed as a new conversation's. Off unless cold-after is set
  # (null is off). Set it to the longest cache lifetime among the accounts your conversations can fall
  # to, plus a margin: the gateway knows no cache's lifetime; a conversation is never moved within it.
  # At least 1m; at or above session-affinity-ttl it does nothing (not-below-ttl). Needs session-affinity;
  # nothing in Home mode, with a plugin scheduler, or with rebind-on "never".
  # session-affinity-return:
  #   cold-after: "6m"
```

- **One key.** No `enabled`: removing the key, or `null`, is off. A value that is not a duration of at least one minute is refused where the configuration is read, with the key named: the file at a start or a reload, and a write through the config API (which is read as YAML; a refused write leaves the file as it was). There is no upper bound to check: the value is kept as the duration it was read as and only ever compared with another duration (`1m0.9s` stays 60.9 seconds).
- **A change takes effect at the next selection.** A selection that loaded the previous value finishes with it.
- **The state:** `GET /v8/management/provimux/session-return`, behind the management key, read-only, from memory (contract: `SessionReturnState`):

| Field | |
|---|---|
| `observed_at` | when the state was read |
| `setting.cold_after_s` | `cold-after` as committed, in seconds with its fraction; `null` when not set. Given in every mode, also when the rule is not in effect |
| `in_effect` | whether the rule judges now: exactly when `Pick`'s call does, as it reads the same value, manager and selector |
| `reason` | when not in effect, the first that applies: `home-mode`, `plugin-scheduler`, `session-affinity-off`, `not-set`, `rebind-never`, `not-below-ttl`; `null` when in effect |
| `ttl_s` | the binding lifetime of the selector in use (`session-affinity-ttl`), in seconds, which `not-below-ttl` compares with; `null` with session affinity off |
| `counts.returned`, `counts.last_returned_at`, `counts.since` | bindings the rule dropped since the start, raised in the step that drops one (one conversation can count more than once); when last; when counting started |

- **The client's show** (part one) says the same in two fields: `rule.in_effect` and `rule.cold_after_s`.
- **The console's card**, on the Models page among the settings below the list: a time in place of a switch ("Return a conversation to its first route once unused for: [ ] minutes", empty is off), under it the value in use with its reason (for `not-below-ttl`, with `ttl_s`: "at or above the binding lifetime of 60 minutes: it does nothing"), and one line "12 conversations went back since the start". A 404 hides the card; a write the gateway refuses writes nothing.
- **Pi's command** (section 11) says the rule's line from the show's `rule`: "by itself: at your next message, once this conversation has been unused for 6 minutes and a higher-priority route is free", or "by itself: never; the operator has set no time".

### 15.5 What it gives up, on purpose

| Given up | Because | What is left |
|---|---|---|
| "A conversation with a warm cache is never moved" | the gateway cannot know a cache is warm | the promise of 15.1, with the operator's time: the longest cache lifetime among the accounts a conversation can fall to, plus a margin |
| On by default | any default that does something (5 minutes) moves a warm 1-hour cache; any default that is safe (an hour, with the default TTL of an hour) does nothing | off until `cold-after` is set |
| A lifetime per conversation, from its answers or the figures | evidence kept beside the binding can be lost while the binding lives, and the rule then judges with less than it once knew | one time, the operator's |
| Returning before the conversation's next request | a release between requests needs a judge without a request | nothing is lost: a binding nobody uses costs nothing, and the request that would have used it is the one that returns |
| Sub-agents and forks | K2 | they follow their parent: one that starts after the parent returned takes its new account |
| Conversations the client did not name | K2 | they stay until `session-affinity-ttl` without a request |
| The operator's list and release, counts of bindings | the rule needs neither | the state counts how often the rule returned a conversation, which it knows exactly |

### 15.6 What must hold

No test depends on the machine's speed or the date. The rule's function takes `now`, so its table tests choose it from the note they made (the time less one nanosecond, then exactly). Tests through a real `Pick` move the notes into the past by margins of hours (`ProvimuxSessionReturnAgeForTest`) and never sleep.

| | Must hold | Proved by |
|---|---|---|
| 1 | through a real manager and selector, under each strategy, on one provider and two, for each kind of named conversation: unused for `cold-after`, the next request goes to the highest priority; used within it, it stays | `TestProvimuxSessionReturnPin`, `TestProvimuxSessionReturnSevenThings` |
| 2 | unset, the same samples select as upstream does, and nothing is written | the unset rows of the pin |
| 3 | only the accounts this request may use count as higher: the image route that may not use a free account; a provider outside the request's set | `TestProvimuxSessionReturnImageRouteStays`, `TestProvimuxSessionReturnOtherProviderIsNotHigher`, `TestProvimuxSessionReturnImageRoute` (over HTTP) |
| 4 | never moved: a pinned request, a sub-agent, a fork, a conversation without a session id (a derived identity, or by content), the first message after the rule was switched on | `TestProvimuxSessionReturnLeftAlone` |
| 5 | two aliases of one binding used in turn: the later use counts, whichever alias asks, and a return drops every alias | `TestProvimuxSessionReturnAliases` |
| 6 | a binding with no note (a new selector; a restore by session-affinity-persist after a restart) is not judged at its first message, and is from its second | `TestProvimuxSessionReturnRestored`, `TestProvimuxSessionReturnRestoredFromSnapshot` |
| 7 | `cold-after` of `1m0.9s`: dropped exactly from that time on, never one nanosecond before | `TestProvimuxSessionReturnBounds` |
| 8 | a request held on the low account while a second request returns the conversation: its success and its failure both leave the new binding | `TestProvimuxSessionReturnHeldRequest` |
| 9 | the count equals the drops, with requests of one conversation and a client's release racing | `TestProvimuxSessionReturnCountRacing`, under `-race` |
| 10 | use is counted from the message routed: an answer that streams for 90 seconds, a token rotated during the answer, over the Responses handler and a real WebSocket; a message refused before selection is not use | `TestProvimuxSessionReturnCountsFromTheMessage`, `TestProvimuxSessionReturnTokenRotatedDuringTheAnswer`, `TestProvimuxSessionReturnLongAnswerSSE`, `TestProvimuxSessionReturnWebsocket`, `TestProvimuxSessionReturnRefusedMessageIsNotUse` |
| 11 | the notes are emptied at their size and when the value changes; switched off and on, no message is judged by an old note; the emptying leaves part one's token | `TestProvimuxSessionReturnNoteTable`, `TestProvimuxSessionReturnOffAndOnStartsAfresh` |
| 12 | a return ends the binding's life for `expect`, also when the request fell back to the same account again | `TestProvimuxSessionReturnEndsTheLife` |
| 13 | the state names each reason, in the contract's order, and the rule judges exactly where it says in effect; the count and its time; the path through the real router against the contract, with `ttl_s`; the show's `rule` | `TestProvimuxSessionReturnState`, `…StateCounts`, `…StatePath`, `…ShowRule`, `…ContractExamples` |
| 14 | the setting: read, refused with the key named, absent or `null` off; a start, a hot reload and a write through the config API each put it in force | `TestProvimuxSessionReturnSetting`, `…SettingRefused`, `…SettingAbsent`, `…FollowsEveryCommit` |
| 15 | `Pick` is the one place in the package where a named conversation's binding decides an account, and the two lines are where 15.3 puts them: a read upstream adds or moves fails the test at a sync | `TestProvimuxSessionReturnPickIsWhereBindingsAreHonoured`, `TestProvimuxSessionReturnTwoLines` |

### 15.7 Known limits

Each needs a timing or a setup that ordinary use does not make; none is built around.

- **The first message after the notes were emptied** (16,384 keys on one selector), after `cold-after` changed, after a restart or a restore, or after a change of the routing settings (a new selector) is not judged. Such a conversation returns one message later.
- **A selection already under way when a configuration is committed** finishes with the value it loaded.
- **At a reload, a request still inside the selector being replaced** is judged there, and a return there is counted, although that selector's cache is about to be dropped.
- **One setting per process.** An embedding that runs several managers gets the rule on every selector, by the mode of the manager of the last committed configuration.
- **While the one answer after a return is under way, when that request's retry came back to the same low login,** the show still gives the earlier token and history; the answer, once in, starts the new life.
- **A held request's failure after a return whose retry came back to the same low login** removes that binding, as upstream does with any binding to a login that just failed; the conversation is bound afresh at its next message.
- **A client's release without `expect` racing a return** can remove the binding the return's request just made on the first route; the next request is routed there again. With `expect` the answer is `409`.
- **Any plugin scheduler** turns the rule off, one that declines every pick included. **After a scheduling plugin is switched off while the gateway runs,** a conversation it routed in the meantime can go back at its first message: while the plugin routed, the selector saw none of its messages, so an older note can still be there.
- **After Home mode is switched on and off again while the gateway runs,** a conversation bound before can go back at its first message, though a message of it was routed within `cold-after`: while Home routed, the selector saw none of its messages, so an older note can still be there.
- **Two selections of one conversation at the same instant** can write their notes out of order (about two milliseconds, seen only when forced); the note can then be that much older than the later message.

### 15.8 What could come next

**Telling the client**: a mark on the answer that a higher-priority account was free for this request, and Pi offering the release. It cannot move anything wrongly, and with the line of 15.3 the fact is exact for the request that carries it. Pi with pi-cache-keeper knows its own conversation's cache, the one thing the gateway cannot know, and could release when that cache is cold with no time set by anybody. It costs a seventh value on the answer's statement and a change to both extensions; it is a design of its own.
