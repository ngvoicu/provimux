# Prices and cache lifetimes from providers.dev, per account: held, shown, and given to tools

Design, 2026-11-25, amended 2026-11-26 (the next section says what changed), amended again that evening by `answered-by.md`, and a third time late that evening after the second review of F1 (the two sections before section 1 say what changed). This is the design only: of its code, the account marker (W1 to W3) is built and F1 was built and is not merged. The contract a build is checked against is `prices-and-cache/openapi.yaml`, with three example files beside it.

**The owner's decision (2026-11-25).** Provimux uses providers.dev's data: in the console, and advertised to clients, "to help developers develop tools, or alternatively use pi-cache-keeper". The shape:

1. Provimux **holds** providers.dev's figures per account: prices, cache prices, the cache lifetime to rely on, the window, each with its date and source. Filled from providers.dev, never blocking, with a switch.
2. The **console** shows them on a name's card per account, and on the Savings page per account.
3. **Clients** get them per account in the model list (`/v1/models`), as a small, documented, versioned contract for tools. With the headers that already say which account answered, a tool knows what that account costs and how long its cache lives without knowing any provider.
4. **pi-provimux** uses it; **pi-cache-keeper**, behind Provimux, has one source and not two.
5. Provimux **acting** on the data itself is a later phase (section 11).

This document designs that shape to be built, and section 2 says where the shape is wrong as stated and what the design does instead.

## Amended 2026-11-26

The owner asked why a cache lifetime through Provimux can differ from the vendor's own client and whether Provimux can behave closer to the vendor clients, and then: "lets add to this spec the improvements you recommend, and new measurements if you need, but only if models were released in the last 31 days." The request parity check (`client-parity.md`, 2026-11-22) answers most of it: for a real client Provimux already asks each vendor for its cache as the vendor's own client does. Its rule 4.1 stands here too: nothing in this document adds to what makes a request look like a vendor's client.

| What changed | Where | Why |
|---|---|---|
| The branch is on today's `main`, `aa76f801b`, with "serve only my names" released | "Read from", the cited lines, 6.3, 7, 12, 13 | F1 waited for it. Six cited line ranges had moved, and one sentence of section 12 was no longer true: the names rule is applied where names are read, not in front of the registry |
| Row 1 of the rule for `rely_on_s` counts only a measurement made by a real client through Provimux; the plain probe's never | 4.2; and findings 2, sections 2, 4.1, 4.3, 7, 8.2, 9.2, 10.1, 10.4, 14. `openapi.yaml`: the rule on `Lifetime`, a required `counts` on `Measurement`. Both examples rebuilt from the dataset of 2026-11-26, with a fourth entry that shows row 1 | The probe is not a client. As first written the rule gave 0 on the Codex login from probe runs alone, where Pi through the same gateway held 15 to 30 minutes and the Codex CLI 150 to 1,500 s |
| Where a Claude login in overage would sit in the figures (P-1), and what either choice costs | 4.4 | P-1 is not decided; the owner decides after the Claude pair is measured |
| A cache key for a caller that sends none | 16 | Asked for as a design. Read in the code: upstream already supplies one on the Codex and Devin routes. Meta is the one login without, and a patch there waits for a measurement |
| The pairs to measure, only for models released in the 31 days before the night | 17 | The brief of the measuring task, with the rule a script applies and the cost |
| Two premises corrected | 16.1 | The plain probe does send a cache key, one per sample. And a caller that sends none does not reach OpenAI without one |

Everything else of the contract and the design is as it was on 2026-11-25.

**Read from.**

| Project | Commit |
|---|---|
| Provimux | `main` at `aa76f801b`, on CLIProxyAPI v8.1.4, for everything written up to the afternoon of 2026-11-26. The evening's amendment and `answered-by.md` read `main` at `bc6f81d73`, on v8.1.7 (released as v8.1.7-pmx.1) |
| provimux-ui | `main` at `4742d6d9` |
| pi-provimux | `7affe86` (105.0.7) |
| pi-cache-keeper | `c75d182` (0.10.7) |
| providers.dev | the live API, version 1.6.3, dataset `c37885999c67cfaa` of 2026-11-26; the site's code at `e0856f6` |
| groundtruth | `6650203` |

Every `file:line` of Provimux below is in `aa76f801b`, and the same lines hold the same text in `bc6f81d73` (compared by a script, 2026-11-26).

**Tags.** **[read]** in code. **[ran]** executed by me on 2026-11-25, and again on 2026-11-26 for every number the amendment touches: plain GETs to `https://providers.dev/api/v1`, and scripts over their answers. **[infer]** reasoned, not verified. I sent no request with a login or a key and never contacted the owner's gateway.

## Amended 2026-11-26, in the evening: who answered

The review of F1 (finding 8) showed that the three answered-by headers cannot tell two accounts of one kind and host apart: two Claude keys, one name, two models, two entries, one set of headers. Section 5's join by kind, provider and host was false. `answered-by.md` designs what replaces it: every answer names its account by an opaque marker, the model that answered, and its own providers.dev entry. This document follows it.

| What changed | Where | Why |
|---|---|---|
| The branch is on today's `main`, on CLIProxyAPI v8.1.7: read at `bc6f81d73`, and moved onto `9d0786d95` when `main` gained two commits that evening | "Read from" | `main` was stacked again. Every cited line range was compared between the bases by a script: all 20 are unchanged, so the line numbers below still hold |
| From an answer to its figures: the answer's own `X-Provimux-Entry`, not a match of three headers | 1, 3, 5, 8.1, 9.2, 10.4; `openapi.yaml`; a third example, `examples/answers.json` | Finding 8. Contract 1 is not released, so it is corrected, not replaced |
| An account row carries a marker, `account`, and may say `entry_per_request` in place of an `entry` | 5; `openapi.yaml`, `Account`; `examples/models-figures.json`, with finding 8's two keys and a model whose entry is decided per request | So that an answer leads to exactly one account, and so that a row need not promise an entry it cannot keep. Settles the two questions F1's review left: a Codex login whose requests can go to `api.openai.com`, and Antigravity's variant by effort. Superseded late that evening (the next note): no row carries an entry at all |
| Savings by account is keyed by the marker, not by the account's index | 7, 15 | The index is made from the file's path: it survives a replaced login. The marker is stable by construction, which closes the point section 15 left open |
| The steps W1 to W7, and F3 after W6 | 13 | F3's contract names the answer's entry |

Two things `main` gained with v8.1.5 to v8.1.7 touch what this design assumes, and neither breaks it: a Home-granted local plugin executor answers `acp/…` models (v8.1.6; a plugin's model: no entry, and nobody is named), and a pool's position is kept per credential (v8.1.5; F1's fix round already reads the pool through upstream's own derivation). One thing was already there on 2026-11-25 and this design had missed it: since v8.1.3 Antigravity chooses a model variant by the caller's effort. F1's review found it; the entry of such a model is decided per request.

## Amended 2026-11-26, late evening: an entry is read from the request that was sent, and never predicted

F1 gave each account row a predicted `entry`, worked out from the configuration and the registry by a mirror of how upstream derives what it will send. Its rule was "never wrong; where it cannot be right there is none". Two reviews found rows with a wrong entry: the first six cases, the second five more (its findings 13 to 17) and a hole in the mirror's drift test (9). All eleven have one cause. Upstream decides the host and the model of a request late, inside each executor, from things a list cannot see: the operator's payload rules, a destination in a credential's metadata, a stored catalog that changes between two reads, a plugin, the route Vertex takes for one alias. A prediction has to copy each of those decisions and is wrong wherever it misses one.

What stood in both reviews is the map from what was really sent to a providers.dev entry: all 50 ordinary cases on both lists. So the map stays and the prediction goes (`answered-by.md` section 4, rewritten).

| What changed | Where | Why |
|---|---|---|
| An entry is worked out in one place, when an answer is stated, from the request the gateway sent for it: the host it went to and the model id in it, both read from the request as the transport was handed it | 6.1 (rewritten); `answered-by.md` 4 | The sent request is the one thing every late decision has already gone into. Findings 13 to 17 then come out right or as no entry by construction (`answered-by.md` 4.6) |
| No account row carries `entry`, and `entry_per_request` is gone. A row carries `answered`: what that account's answers for that name said of themselves, newest first, with the day | 5; `openapi.yaml`, `Account` and the new `Answered`; `pui13/openapi.yaml`, `ModelRoute`; both list examples | A row cannot promise what a request decides. It can say what answers said, and that is true whatever the configuration |
| `figures.entries` holds the entries the rows' `answered` name | 5, 6.3; `openapi.yaml` | The entries to send were chosen by the predicted `entry` |
| The console's lines and Pi's price before an answer rest on `answered`, and say so | 7, 8 | They rested on the predicted `entry` |
| A Savings row takes its entry from the answer, not from a map over the account's configuration | 7 | The same fault as F1's, one step later |
| F1 is not merged. Its map and the map's tests go into W6; its mirror, its drift test, its row function and their tests are dropped | 10, 13; `answered-by.md` 8, 9 | 10.5 says what is kept and what a sync no longer carries |
| The steps: W6 comes before W4 and W5 and needs no owner patch; F3 after W6 and F2 | 13; `answered-by.md` 9 | The model that was sent is now read from the request, not from the usage reporter, which is told a model before the payload rules on four executors |

Read for this amendment: Provimux `main` at `9d0786d95` (v8.1.7); F1 at `4f14c2cff` (`provimux-figures-f1-rebase`); the account marker as built at `9b71b947f` (`provimux-answered-by`); the two reviews of F1 (the second at `/tmp/provimux-fig02-r2-28aErf/FIG-02-second-review.md` when read). Nothing was sent to a provider, the owner's gateway or port 8787.

## 1. The design in one page

| Piece | Where | What it does |
|---|---|---|
| The entry of an answer | Provimux, a constant map over what it sent: the host the request went to and the model id in it | says which providers.dev entry (`provider/model`) this answer is. Read from the request, never predicted (6.1) |
| What an account's answers said | Provimux, a small record per account and name | `answered` on each account row: the entries its answers named, with the day. A record, not a promise |
| The figures | Provimux, one more providers.dev request in the round that fetches the listings; a disk copy | holds, per entry, the price, the cache lifetimes, the window and the output limit, with dates and sources |
| The contract for tools | `GET /v1/models?include=figures` | per model its accounts, per account its marker and what its answers said, and once per answer the figures of every entry named. Contract 1, `prices-and-cache/openapi.yaml` |
| What an answer says of itself | every answer to a model request | the account that gave it, by a marker; the model that answered; its entry. `answered-by.md` |
| The console | `GET /v8/management/provimux/figures`, the same records | the figures on each account's line of a name's card; Savings by account, valued at the account's own list price |
| pi-provimux | reads the contract | prices a model by what the account tried first last answered as, shows the price of the answer itself, and hands each route's figures on |
| pi-cache-keeper | reads what pi-provimux hands on | for a model behind Provimux, Provimux's figures and nothing else; for every other model, providers.dev as today |
| Later | Provimux's selector | a conversation that spilled to a paid key goes back once that key's cache is cold (section 11) |

Five findings shaped it.

1. **Prices are already on every machine; the lifetime is what is missing.** Savings values requests with OpenRouter's list (`internal/provimux/savings/prices.go`). pi-provimux takes a price per model id from models.dev (`extensions/lib.ts:641-643`, `:996`). pi-cache-keeper fills a price from providers.dev only where Pi has none (`model-data.ts:401-424`) [read].
2. **providers.dev's "safe" lifetime is not the lifetime of a route through Provimux, and a plain probe's is not a client's.** On a login the safe value follows the vendor's own client, then a direct request, then a proxy (`SafeLifetime.measured_s` in the OpenAPI) [read]. providers.dev has two kinds of measurement through Provimux, told apart by `client` (4.2): a coding agent holding a conversation (Pi), and plain test requests. Of the 191 entries of the request without OpenRouter, 34 have a measurement through Provimux. 21 have a real client's, and on 11 of those it differs from the safe value, all of them logins. 13 have only the plain probe's [ran, dataset of 2026-11-26]:

   | Entry | Safe value | A real client through Provimux | Plain test requests through Provimux |
   |---|---|---|---|
   | `openai-codex/gpt-6.1-sol` | 450 s, Codex CLI, 2026-11-24 | 1,800 s, 2026-11-07 (21 of 31 checks hit) | 0 s, 2026-11-17 (56 of 86) |
   | `openai-codex/gpt-6-astra` | 300 s, Codex CLI, 2026-11-23 | 7,200 s, 2026-11-11 (16 of 16) | 0 s, 2026-11-17 (62 of 86) |
   | `openai-codex/gpt-6.2-sol` | 150 s, Codex CLI, 2026-11-24 | none | 0 s, 2026-11-17 (3 of 10) |
   | `devin/kimi-k3` | 3,000 s, Devin CLI, 2026-11-23 | none | 0 s, 2026-11-04 (0 of 1) |
   | `meta-subscription/muse-wonder-1.0` | 0 s, Muse Code, 2026-11-07 | 7,200 s, 2026-11-07 (22 of 22) | 600 s, 2026-11-06 (15 of 22) |
   | `anthropic/claude-opus-5-5`, long | 3,600 s, direct, 2026-11-17 | none | 600 s asking `ttl: 1h`, 2026-11-04 (13 of 20) |

3. **providers.dev has entries for the latest models only** (its editorial rules: `GET /api/v1/meta`, `exclusions`). Of the models its listings name, 4 of 15 on the Claude login, 18 of 60 on Devin and 8 of 11 on the Codex login have an entry [ran]. Every other model has no figure.
4. **Only the gateway knows the model behind a name.** pi-provimux maps an account to a provider in two tables of its own (`extensions/upstreams.ts:73-109`) and takes the model id from the name the client sees (`upstreamOf`, `:158-166`) [read]. For a name the owner set (`opus` for `claude-opus-5-5`) providers.dev has no such model, and pi-cache-keeper gets no data. With "serve only my names" that is every name.
5. **No client acts on a figure it is merely given** (section 9.1). The contract makes a tool possible; it replaces no tool. Pi with pi-cache-keeper is the one client that acts today.

## 2. Where the decided shape is wrong, and what the design does

| The shape says | What is true | The design |
|---|---|---|
| "The cache lifetime to rely on", one per account | providers.dev's safe value is, on a login, the vendor's own client's. Through Provimux it can be longer or shorter (finding 2) | `rely_on_s` is Provimux's selection by a written rule (4.2): a measurement by a real client through Provimux first, else the safe value. The safe value and every measurement through Provimux stay beside it, dated. On the Codex login this gives the Codex CLI's 150 s for `gpt-6.2-sol` and Pi's 1,800 s for `gpt-6.1-sol` today, and never the plain probe's 0 |
| Figures "per account" | providers.dev knows a provider, an access method and a model, not an account. Two Claude logins have the same figures | Figures hang on the **entry**; an account names its entry. The contract states each entry once |
| Every account has figures | Entries exist for the latest models only (finding 3), some requests cannot be mapped (6.1), and what an account will send is decided per request | No entry and no figures are normal answers, with their own states; nothing is filled in by guessing. An account has figures only through an answer it gave |
| "True money per account" on Savings | providers.dev states list prices, not charges. A subscription is not billed per token. The one real charge Provimux sees is what OpenRouter reports | Savings by account, valued at the account's own list price and called an estimate; a subscription's rows say "at API list price"; the reported charge stays the only real one (section 7) |
| "The window" as a figure | Provimux already serves a window per model, from the vendor's list and the catalog. providers.dev's safe input window can differ | The figure is given with its source and changes nothing Provimux registers; the gap-filling rule of the model lists stays the only way a window reaches the registry |
| pi-provimux shows "the right cost for the route that answered" | Pi prices a message itself, from the model's one price, before the answer names an account (pi-provimux computes no cost [read]) | The model is priced by the account tried first; the footer names the account that answered with its price. A pooled name whose accounts differ in price cannot be exact in Pi's own total (8.1) |
| Clients learn nothing new | With the entry, a holder of a client key learns the model behind a name | Decision 5 |

## 3. Who reads what

One source for each reader, so that no two can disagree about the same model.

| Reader | Reads | From |
|---|---|---|
| Provimux | entry summaries | providers.dev, `GET /api/v1/entries`, the only request (section 6) |
| Console | the figures and what each route last answered as | Provimux's management API |
| A tool | the contract of section 5 | Provimux, `GET /v1/models?include=figures`, and what each answer says of itself (`answered-by.md`) |
| pi-provimux | the same contract | Provimux. models.dev stays only for a gateway without figures and for the fast-mode price |
| pi-cache-keeper, a model behind Provimux | the figures pi-provimux hands on with each route | Provimux, through pi-provimux. It looks nothing up for such a model |
| pi-cache-keeper, any other model | providers.dev itself | as today |
| Savings | the figures Provimux holds, then OpenRouter's list | Provimux |

Before this design, pi-cache-keeper behind Provimux read providers.dev's safe value while the gateway would state another; with it, the value for a model behind Provimux has one author.

## 4. The figures

### 4.1 What is held, and where each comes from

All of one entry summary of `GET /api/v1/entries`.

| Figure | From the summary | Unit |
|---|---|---|
| Input, output price | `price_in`, `price_out` | US dollars per million tokens |
| Cache read price | `pricing.rules`: `meter` `text.cache_read`, `operation` `set`, no `when` | the same |
| Cache write prices | the rule for `text.cache_write` without `when` (lifetime not stated), and one per lifetime where `when` is `cache.retention_s eq N`. The long-cache price is the row of the longer lifetime | the same |
| Prices above a prompt size | `tiers` of the source in `safe.price.sources` that `safe.price.source` names | the same, per tier |
| Other rates exist | any other rule with a `when` (fast mode, region, time) | a flag; the entry's page has them |
| Whose price, its date, whether sources disagree | `safe.price.source`, `.date`, `.agreement` | |
| Charged or not | `pricing.billing`, `price_from` | `per_use`; `reference` = not billed per token, the API price for comparison; else unknown |
| Safe lifetime per profile, its basis | `cache_lifetime.short`, `.long`: `safe_s`, `basis`, `source`, `date`, `documented_s`, `documented_status` | seconds |
| Who measured the safe value | the items of `cache_lifetime.lifetimes` with `chosen: true` | client id, label, kind |
| Measured through Provimux | the measured items whose `client` is `agent+cliproxyapi` (a real client: it counts in 4.2) or `cliproxyapi` (plain test requests: evidence only) | seconds, date, checks, hits, and whether it counts |
| Caching documented, a lifetime can be asked for | `cache`, `cache_retention` | |
| Input window, output limit | `safe.input_window`, `safe.max_output`: `value`, `at_least`, `source`, `date` | tokens |

The two profiles are providers.dev's: **short** is what a request gets without asking, **long** what it gets when it asks for the longer cache.

### 4.2 The lifetime to rely on

Per entry and profile, Provimux gives one value, `rely_on_s`, and says what it rests on:

| Order | Taken when | `basis` |
|---|---|---|
| 1 | providers.dev has measurements of the entry made **by a real client through Provimux**: the **lowest** of them, and of equal ones the newest | `measured_through_provimux` |
| 2 | else the entry's safe value is measured | `measured` |
| 3 | else the entry's safe value is documented | `documented` |
| 4 | else | `none`, value `null` |

**A real client** is a program that holds a conversation through the gateway: Pi, or another coding agent. **The plain probe** is groundtruth's test program, which sends single requests. Its measurement never counts in row 1, whatever it measured. It stays in the answer as evidence, marked (`through_provimux[].counts` is `false`).

**Why the probe is not a client** [read: `client-parity.md` 3.1; groundtruth `src/run.ts`]. Its prompt is about 2,700 tokens and shares nothing with any other sample from its first token, where a client's request begins with thousands of tokens every session shares. Its check is the identical request sent again, where a client's check is the conversation with one more turn. On the Codex login it missed within minutes while its control, sent again seconds later, hit; Pi through the same gateway and the same executor held 15 to 30 minutes. A missing cache key is not the reason: the probe sends one per sample (16.1).

**What tells the two apart in providers.dev's API today** [ran: `/api/v1/entries`, `/api/v1/evaluations/cache` and the OpenAPI, version 1.6.3; read: the site's `data/authored/measurement-labels.yaml` and `src/core/resolve.ts:595-600`, groundtruth's `scripts/build_observed.py:33-50`]:

| Field | A real client through Provimux | Plain test requests through Provimux | Tells them apart |
|---|---|---|---|
| `client` | `agent+cliproxyapi` | `cliproxyapi` | **yes**, and it is the only field that does |
| `client_label` | "A coding agent, through Provimux" | "Requests sent through Provimux" | words for a reader, not to be parsed |
| `client_kind` | `proxy` | `proxy` | no |
| `route`, on an evaluation only | the access method: `codex-subscription`, `devin`, `meta-subscription` | the same | no: it says whose account, not who sent |

- On an entry summary the fields are on each item of `cache_lifetime.lifetimes`: `client`, `client_label`, `client_kind`, `chosen`. An item has no `route`.
- groundtruth writes `pi+cliproxyapi` for a run of its Pi harness and `cliproxyapi` for a run of its TypeScript probe. The site publishes the first as `agent+cliproxyapi`: the agent is not named in public (the owner, 2026-11-13).
- Today: 24 cache evaluations by `agent+cliproxyapi` (15 on Devin, 5 on Meta, 4 on Codex, none on the Claude login) and 41 by `cliproxyapi`.

So the data tells them apart today, and **nothing has to change at providers.dev or groundtruth first**. Provimux keeps the client ids that count as a constant with one member, `agent+cliproxyapi`. Two rules keep the constant true, and they are the measuring side's:

1. Every run made by an agent that holds a conversation through Provimux, whichever agent, is published under `agent+cliproxyapi`: one row per agent in the site's labels file, all with that public id.
2. Every run of the TypeScript probe through Provimux stays `cliproxyapi`, also when its request is shaped like an agent's (CX-S in section 17).

A hardening for later, not a condition: the id says it by convention, not as a stated fact. When a third id appears, the growth to ask of providers.dev is an attribute, not a longer constant here. It is a small change of its own in two repositories:

| Repository | Files |
|---|---|
| groundtruth | `scripts/build_observed.py` (beside `CLIENT_KINDS`, a map `client_conversation`: true for `pi+cliproxyapi` and the own clients, false for `direct` and `cliproxyapi`), `docs/own-clients.md` section 3, `README.md` ("observed.json") |
| site | `src/core/schema.ts` (reads the map), `data/authored/measurement-labels.yaml` (the fallback per client), `src/core/resolve.ts` (beside `clientKind`), `src/core/contract.ts` (`client_conversation` on `CacheLifetimeItem` and `CacheEvaluation`), the API as version 1.7.0 |

**What the rule gives today** [ran, dataset of 2026-11-26]:

| Entry, profile | `rely_on_s` | Taken from | providers.dev's safe value | The plain probe |
|---|---|---|---|---|
| `openai-codex/gpt-6.2-sol`, short | 150 | row 2: the Codex CLI, 2026-11-24 | 150 | 0 (2026-11-17), not counted |
| `openai-codex/gpt-6.2-astra`, short | 1,200 | row 2: the Codex CLI, 2026-11-23 | 1,200 | 0, not counted |
| `openai-codex/gpt-6.1-sol`, short | 1,800 | row 1: a coding agent, 2026-11-07 | 450, the Codex CLI, 2026-11-24 | 0, not counted |
| `openai-codex/gpt-6.1-terra`, short | 900 | row 1, 2026-11-07 | 1,500, the Codex CLI, 2026-11-23 | 0, not counted |
| `openai-codex/gpt-6-astra`, short | 7,200 | row 1, 2026-11-11 | 300, the Codex CLI, 2026-11-23 | 0, not counted |
| `devin/glm-5-3`, short | 300 | row 1, 2026-11-10 | 600, the Devin CLI, 2026-11-24 | 0, not counted |
| `devin/kimi-k3`, short | 3,000 | row 2: the Devin CLI, 2026-11-23 | 3,000 | 0 (one check), not counted |
| `meta-subscription/muse-wonder-1.0`, short | 7,200 | row 1, 2026-11-07 | 0, Muse Code, 2026-11-07 | 600, not counted |
| `anthropic/claude-opus-5-5`, long | 3,600 | row 2: direct, 2026-11-17 | 3,600 | 600, not counted |
| `claude-subscription/claude-opus-5-5`, both | 3,600 | row 2 | 3,600, which is itself the plain probe's (see below) | the same measurement |

Against the rule as first written, the value changes on 11 of the 41 entry profiles that have a measurement through Provimux, and no value on the Codex login is 0 any more.

**What the amended rule is, said plainly.**

- **It no longer errs low.** As first written it took the lowest of everything and cost a tool a warm-up too many. Now, where a real client measured, its value stands even above the safe one. That is so on 8 entry profiles: `openai-codex/gpt-6-astra` (7,200 against 300), `gpt-6.1-sol` (1,800 against 450), `gpt-6-luna` (300 against 150), `devin/claude-opus-5-5` (300 against 150), `devin/deepseek-v4-1-flash-1017` (7,200 against 0), `devin/muse-wonder-1-0` (1,800 against 0), `meta-subscription/muse-wonder-1.0` short (7,200 against 0) and `muse-ember-1.0` long (14,400 against 7,200). On 3 it is below: `devin/glm-5-3` (300 against 600), `devin/grok-4-7` (0 against 150), `openai-codex/gpt-6.1-terra` (900 against 1,500). A value too high costs a tool a missed cache, not a warm-up.
- **Row 1's measurements are older than the safe values beside them**: 7 to 11 November, two or three checks an age, against own-client runs of 23 and 24 November. `client-parity.md` 1.1 calls most of these differences chance. Each date is in the answer. A tool that wants the careful value takes the lower of `rely_on_s` and `safe.seconds`; the console shows both (section 7).
- **Only the pairs of section 17 renew row 1**, and only for models released in the 31 days before their night (the owner's word). Older models keep the values above. The owner's standing rules send every other login measurement through the vendor's own client, and never an API key through a proxy.
- **0 is a value** where a real client measured it and no age held on every check (`devin/grok-4-7`, `devin/gemini-4-argon`, `devin/nemotron-3-5-super`, `devin/swe-2`). It is not "does not cache".
- **The probe still stands behind row 2 on seven entries.** Where no own client and no direct request has measured a login, providers.dev's safe value is itself a plain probe's through Provimux: the four Claude login entries (3,600 s), `antigravity/gemini-3.8-flash-high` (150 s), `devin/deepseek-v4-pro` (0 s) and `meta-subscription/muse-spark-1.3` short (450 s). The answer says so (`safe.measured_with` names `cliproxyapi`), and so does the console. Keeping it is decision 1b: for those entries it is the only measurement anyone has, and the Claude pair of section 17 replaces the first of them.
- The rule is Provimux's and is part of the contract. The values are providers.dev's and change with its dataset.

### 4.3 Never stated as fact

| Never | Because |
|---|---|
| A documented lifetime without the word | it was not measured |
| A reference price as what a subscription account costs | the entry is not paid per token |
| A lifetime of 0 as "does not cache" | 4.2 |
| A plain probe's lifetime as what a client gets | it is not a client (4.2) |
| One price or lifetime for a name several accounts serve | each account has its own; the name has none |
| A figure without its date and source | a price can be stale |
| A figure for a model matched by rewriting its id | Savings does that for an estimate and says so; here an entry is matched exactly or not at all |
| An entry worked out before a request is sent | upstream decides the host and the model inside the executor, per request (6.1); two reviews of a predicted entry found eleven wrong rows |
| What an account last answered as, as what it will answer as | `answered` is a record with a day; the console and Pi say "last answered as" |

### 4.4 A Claude login in overage (P-1, not decided)

`client-parity.md` 4.2: inside the plan's included usage Claude Code and Provimux both ask for one hour. In overage Claude Code asks for five minutes and Provimux still asks for one hour. That is predicted from Anthropic's documentation and the source; nothing measured it. The owner decides after the Claude pair of section 17. This is where it would sit:

| | P-1 not built, or its setting at `1h` (today) | P-1 built and set to `follow` |
|---|---|---|
| What Provimux asks for a mark without a lifetime | 1 hour, inside the plan and in overage | 1 hour inside the plan and while the state is not known; 5 minutes while the login draws on overage |
| What a tool is told to rely on, short profile | the entry's `rely_on_s`: 3,600 s today on all four Claude login entries | inside the plan the same; in overage **300 s, said on the account, not on the entry** |
| Long profile (the caller wrote `ttl: "1h"`) | 3,600 s | 3,600 s: a lifetime the caller wrote stays |

Why on the account: a figure hangs on the entry and is the same for every user (section 2), and overage is what this gateway knows about one login at one moment. So if P-1 is built, an account in the contract gains one optional object, absent in every other case:

```json
{ "provider": "claude", "kind": "oauth", "priority": 5, "account": "a.jvwtubr4vdp5dfnhj7jm",
  "answered": [ { "entry": "claude-subscription/claude-fable-5-2", "on": "2026-12-02" } ],
  "cache_now": { "short_s": 300, "because": "overage", "since": "2026-12-02T14:02:11Z" } }
```

- A tool's rule becomes: `account.cache_now.short_s` on the row of the account that answered (found by `X-Provimux-Account`) where it is there, else `figures.entries[the answer's entry].cache.short.rely_on_s`.
- It is growth, an added optional key, so contract 1 holds. It is not in `openapi.yaml` until the owner says yes.
- The list is read now and then, and the state can flip between two reads. The exact source for one request is its answer: Claude's usage reports cache writes by lifetime, five-minute and one-hour tokens apart (`internal/provimux/savings/record.go:63` reads them), and the gateway passes the usage on. pi-cache-keeper takes the lifetime of the last write the answering account reported, where one is reported, before any figure. Section 11 A uses the same source.
- The console, on the account's line: "In overage since 14:02: asks for 5 minutes, as Claude Code does".
- Nothing of providers.dev changes. Its two `known` rows for Claude Code (3,600 s inside the plan, 300 s in overage; groundtruth `docs/own-clients.md` section 3) describe the own client and stay evidence.
- Not measured: that a one-hour mark is honoured in overage at all. Today's 3,600 s is the plain probe's, inside the plan.

**What either choice costs him**, at providers.dev's reference prices for the login's models: the API list prices of `anthropic/…`, from models.dev, dated 2026-11-26, in dollars per million tokens. Overage is billed; at what rate against these prices is Anthropic's to say.

| Model | Input | Cache read | Cache write, 5 min | Cache write, 1 h | 1 h costs more, per million tokens written |
|---|---|---|---|---|---|
| `claude-fable-5-2` | 8 | 0.20 | 10 | 16 | 6 |
| `claude-opus-5-5` | 4 | 0.20 | 5 | 8 | 3 |
| `claude-sonnet-5-5` | 2 | 0.10 | 2.50 | 4 | 1.50 |
| `claude-haiku-5-5` | 0.10 | 0.01 | 0.125 | 0.20 | 0.075 |

A conversation of 200,000 tokens on Fable 5.2 in overage that grows by 5,000 tokens a turn:

| Event | Asking 1 hour (today) | Asking 5 minutes (`follow`) |
|---|---|---|
| A turn within 5 minutes of the last | writes 5,000 tokens: $0.08 | $0.05 |
| A turn after a pause of 5 to 60 minutes | reads the 200,000: $0.04 | writes all 200,000 again: $2.00 |
| A turn after a pause of more than an hour | writes all 200,000 again: $3.20 | $2.00 |

So `follow` saves 3 cents a turn and $1.20 on each return after more than an hour, and costs $1.96 more on each return after 5 to 60 minutes. One such return undoes the saving of about 65 turns. `follow` is the cheaper setting only for someone who almost never pauses between five minutes and an hour, or whose tool keeps the cache warm (a read of the 200,000 tokens, $0.04, every few minutes). The proportions are the same on the other three models. The recommendation of `client-parity.md` stands: build it with the default unchanged. Setting `follow` is a cost choice, and for most use it is the dearer one.

## 5. The contract for tools

`prices-and-cache/openapi.yaml`, with `examples/models-figures.json`. In short:

```
GET /v1/models?include=figures          (a client key, as for every /v1 request)
```

```json
{
  "object": "list",
  "data": [
    { "id": "opus", "object": "model", "owned_by": "anthropic",
      "accounts": [
        { "provider": "claude", "kind": "oauth",  "priority": 5, "account": "a.jvwtubr4vdp5dfnhj7jm",
          "answered": [ { "entry": "claude-subscription/claude-opus-5-5", "on": "2026-11-26" } ] },
        { "provider": "claude", "kind": "apikey", "priority": 1, "host": "api.anthropic.com",
          "account": "a.vymubsbmffkad6cmardq",
          "answered": [ { "entry": "anthropic/claude-opus-5-5", "on": "2026-11-24" } ] }
      ] }
  ],
  "figures": {
    "contract": 1,
    "source": "https://providers.dev",
    "state": "held",
    "dataset_version": "89105175cb0af1aa",
    "generated_at": "2026-11-25T09:49:05Z",
    "fetched_at": "2026-11-25T12:00:03Z",
    "entries": { "anthropic/claude-opus-5-5": { "…": "price, cache, input_window, max_output" } }
  }
}
```

**How a tool uses it.**

1. Once, and again when it likes (the answer is cheap): read the list. Per model, `accounts` are in the order they are tried.
2. Per answer: read `X-Provimux-Entry` (on the Responses WebSocket the field `provimux_entry` of the turn's `response.created`). `figures.entries[that value]` is what the answer cost and how long its cache lives. Nothing is matched by kind, provider or host (amended 2026-11-26: that match was false for two accounts of one kind, `answered-by.md`).
3. Where that value is no key of the copy it holds, it reads the list once more. An account's first answer for a name puts its entry into the row and so into `figures.entries`; a second miss means providers.dev has no such entry.
4. `X-Provimux-Account` is the marker of the account that answered: the `account` of its row. `X-Provimux-Model` and `X-Provimux-Model-Basis` say which model answered and whether the vendor said so.
5. No figures: the entry is no key of `entries`, or the answer names no entry, or it names nobody at all. A tool guesses in none of the three (`answered-by.md` section 4).

**Before an answer** (amended 2026-11-26, late evening). No row has an `entry`: the gateway does not know what a request will be sent as until it sends it (6.1). A row has `answered`: the entries this account's answers for this name named, newest first, each with the day of the last answer that named it, and `null` for answers that named none. It is a record of answers to any client of the gateway, kept 90 days.

| `answered` | Means | A tool before its first answer |
|---|---|---|
| one item with an entry | every recorded answer of this account for this name was that entry | may show its figures and price by them, as "last answered as", and takes the answer's own entry when there is one |
| several items | this account's answers for this name differ: a name on several models, a variant chosen by the caller's effort, a backend chosen by the client, a rule of the operator | has no one figure. It may show them all |
| one item, `entry` null | its answers named no entry | has no figures, and expects none |
| empty | no answer of this account for this name is recorded | has no figures yet. The account's first answer brings them |

What each reader loses by this against a predicted entry, and what it has instead, is in `answered-by.md` 4.5.

**The three includes.**

| Request | The answer |
|---|---|
| `GET /v1/models` | upstream's, byte for byte as today |
| `?include=accounts` | as today, and each account gains its marker `account` and its record `answered` |
| `?include=figures` | accounts as above, and the `figures` object. `include=accounts,figures` is the same |

**What we promise to keep, for contract 1.**

| Promise | |
|---|---|
| Keys | every key the OpenAPI lists as required is present. None is removed, and none changes its type, unit or meaning |
| Unknown | a value not known is `null`, never 0 and never left out |
| Units | prices in US dollars per million tokens; lifetimes in seconds; windows in tokens; dates as `YYYY-MM-DD`, times as RFC 3339 in UTC |
| The join | an answer's `X-Provimux-Entry` is the key of `figures.entries`, and its `X-Provimux-Account` is the `account` of the row that answered |
| The record | from the moment an answer's first byte leaves, the newest item of `answered` on the row of its account under the name asked for is that answer's entry, or `null` where it named none. An item is never an entry no answer of that account for that name carried. Nothing in a row says what the next answer will be |
| The marker | the same on every answer of one account and in every list while the gateway keeps its key; never another account's; compared, not parsed, except for its first character (`answered-by.md` 2.3) |
| The rule of `rely_on_s` | section 4.2 |
| Growth | keys and enum values may be added; a tool ignores what it does not know and treats an unknown enum value as unknown |
| A change that breaks any of this | is served as `"contract": 2` under a new include value, with contract 1 still answered for at least two Provimux releases that say so in their notes |

**What we do not promise.** The values: they are providers.dev's, dated, and change when its dataset does. That an answer has an entry. That the next answer of an account is the entry its row shows. That the account listed first is the one that answers. Which measurement clients providers.dev has. That `figures.state` is `held`: with the switch off or nothing fetched yet, `entries` is empty and the rows' `answered` are still there.

With `models.answered-by: false` there is no statement on an answer, so nothing leads from an answer to a figure: no row has `answered`, and `entries` is empty.

A client that sends no `include` gets nothing new, and no client breaks: pi-provimux keeps only the account fields it knows (`extensions/sources.ts:30-42`) [read].

## 6. Holding the figures

### 6.1 Which entry an answer is

Rewritten 2026-11-26, late evening. Until then this section said which entry an **account** is, worked out ahead of any request; F1 built that, and two reviews found it wrong in eleven cases (the note before section 1). An entry is now a fact of one answer.

`entry` is `<providers.dev provider>/<model id>`, providers.dev's own form of a reference (the provider never contains a slash).

- **It is worked out in one place, when an answer is stated** (`answered-by.md` 4.3), from the request the gateway sent for that answer. Two facts are read from that request as the transport was handed it (`answered-by.md` 4.2): the **host it was sent to** and the **model id in it**. Two are the credential's: the gateway's provider of it (which executor sent) and its kind. The account's configured base URL, the registry, the model the usage reporter was told and anything providers.dev sent are not read.
- The provider comes from the map below. A host matches when the request went to exactly that host over `https` on the default port.
- The model is the id in the request, unchanged, with three exceptions that name the same model: OpenRouter's routing suffix (`:nitro`, `:floor`) is taken off; a Devin effort variant is its base model in Devin's catalog (`registry.LookupDevinModel`); on Vertex's native route the id is the one in the URL, without Google's publisher. Vertex's OpenAPI route sends `<publisher>/<model>`, and that is the id.
- Exact or nothing: no dots for dashes, no dates removed.
- It says where the model would be, not that an entry exists.
- Where any of the facts is missing there is no entry. There are three such kinds, below the table.

| The request was sent by | to the host | with | providers.dev provider |
|---|---|---|---|
| the Claude executor | `api.anthropic.com` | a login's token: the request carries `Authorization` and no `x-api-key` | `claude-subscription` |
| the Claude executor | `api.anthropic.com` | a key: the request carries `x-api-key` | `anthropic` |
| the Codex executor | `chatgpt.com` | a login | `openai-codex` |
| the Codex executor | `api.openai.com` | a key | `openai` |
| the Copilot executor | `api.githubcopilot.com` or another `*.githubcopilot.com` host the login was given | a login | `github-copilot` |
| the Antigravity executor | `cloudcode-pa.googleapis.com`, `daily-cloudcode-pa.googleapis.com`, `daily-cloudcode-pa.sandbox.googleapis.com` | a login | `antigravity`; the variant that was sent (`…-high`) is the model |
| the Devin executor | `server.codeium.com` | a login | `devin` |
| the Meta executor | `api.meta.ai` | a login | `meta-subscription` |
| the xAI executor | `api.x.ai` | a key that nothing on the account marks as not the paid API (no `using_api` other than true in its attributes or metadata) | `xai` |
| the Gemini executor | `generativelanguage.googleapis.com` | a key | `google` |
| the Vertex executor | a host the executor treats as Google's (`helps.IsGoogleVertexHost`: `aiplatform.googleapis.com`, `<region>-aiplatform…`, `<region>.aiplatform…`, `aiplatform.<us\|eu>.rep.googleapis.com`) | a service account or a key | `google-vertex` |
| any executor | `openrouter.ai` | a key | `openrouter` |
| any executor | `api.deepseek.com`, `api.moonshot.ai`, `api.mistral.ai`, `api.groq.com`, `api.cerebras.ai`, `api.fireworks.ai`, `api.together.ai`, `api.together.xyz` (the exact host, no subdomain) | a key | `deepseek`, `moonshotai`, `mistral`, `groq`, `cerebras`, `fireworks-ai`, `togetherai`: a reference; no figures until the provider joins the request of 6.2 |

Rows the table does not have, on purpose, each **none**:

| Sent | Why none |
|---|---|
| A Codex login's request to `api.openai.com` (the second sign-in, for a client that is not Codex's own) | neither the ChatGPT backend providers.dev measures as `openai-codex` nor the paid API: a subscription's token on the API's host (`answered-by.md`, decision 8). It needs no rule of its own any more: the host says it |
| A Codex key's request to `chatgpt.com` (a key without a base URL) | not the paid API |
| An xAI request to `cli-chat-proxy.grok.com`, or to any host but `api.x.ai`; any xAI login | providers.dev's `xai` is the paid API |
| A Meta key | on `api.meta.ai` it is either the paid API or the key a Muse Code subscription applies to; neither the request nor the account says which |
| A Kimi login | its host decides between two plans, and the model lists' row for it is an inference (decision 6) |
| AI Studio, a Gemini login, the Gemini CLI login, a Gemini Interactions key on Google's host | not the paid API, or not measured as an entry |
| A login's request to any host but its own backend's; any request to a host this table does not name | not known, and never guessed from the model's name |

**The three kinds of no entry.** An answer names no entry when:

1. **The map has no row** for what was sent: the tables above.
2. **The gateway did not read what it sent** (`answered-by.md` 4.2 and 4.3): the request left over an upstream WebSocket (Codex or xAI with `websockets` on) or through AI Studio's browser; its body is in a form the reader does not know (a multipart upload, an encoding other than zstd); it carried no model, or one in its URL and another in its body; or the attempt sent two requests that were answered and differ in host or model.
3. **The gateway does not vouch for the credential**: it is held in memory only, or a plugin made it or changed it (a plugin can set an account's provider and kind to anything).

A model a plugin lists is no longer a kind of its own. Where a plugin's executor answers, the answer names nobody at all (`answered-by.md` 5.3). Where the account's own executor sends the model to the account's own host with a credential the gateway made, the request is what it is, whoever put the name on the list (decision 18).

Before, a kind of no entry was "the request decides": a second sign-in, a thinking group, a pool. Every entry is now decided by its request, and those three come out by the table: the host, the variant sent, the model sent.

### 6.2 The request

`GET /api/v1/entries` returns entry summaries, filtered by `provider`, at most 200 a page; a summary carries everything 4.1 reads [read: OpenAPI 1.6.2, `EntrySummary`]. **The site's API needs no addition.**

```
GET https://providers.dev/api/v1/entries?provider=<the fixed set>&limit=200&offset=<0, 200, …>
User-Agent: provimux/<version>
If-None-Match: <the page's ETag, when one is held>
```

- **The fixed set** is a constant: `anthropic`, `antigravity`, `claude-subscription`, `devin`, `github-copilot`, `google`, `google-vertex`, `meta-subscription`, `openai`, `openai-codex`, `openrouter`, `xai`. It does not depend on the accounts a user has, so the request is the same for every user and tells the server nothing about him (the rule of `model-lists.md` section 7).
- **Size** [ran]: the set without `openrouter` and with one Kimi provider is 190 entries in one page, 51 KB gzip, 1.53 MB decoded; `openrouter` is 400 entries in two pages, 68 KB gzip. So about 580 entries, three pages, about 120 KB on a day the dataset changed; with `If-None-Match` a `304` with no body. What Provimux keeps of 190 entries is about 150 KB as JSON.
- **When:** in the round that fetches the listings (`sdk/cliproxy/provimux_published_lists.go:37-44`): at start in the background, every 6 hours, on `POST /v8/management/models/refresh`; after a failure 10 minutes, then 1 hour. Pages one after another until `next_offset` is null, at most 10.
- **A fetched document replaces the held one only if** every page is a `200` or a `304`, well-formed, at most 8 MB, all pages name one `dataset_version`, together they hold `total` entries, and `generated_at` is not before the held document's. Anything else is a failed fetch: the held document stays, and the failure is recorded as a code. An entry Provimux cannot read is left out alone.
- **Disk:** `model-lists/figures.json` beside `published.json`: the kept records, the dataset version, the dates and each page's `ETag`. Mode `0600`, temporary file and rename (`internal/provimux/modellists/published.go:387`). A damaged or foreign file is ignored with one warning and replaced by the next accepted document.
- **Never blocking:** no request is awaited at start or on any path that serves a client. The disk copy is read when the model lists' folder is opened; a start without network gives the figures of the last run, with their dates.
- **Unreachable:** the held document stays, however old; its dates say so. With nothing held, `state` is `none` and there are no figures.
- **The switch:** `models.published-lists: false` already means "Provimux never contacts providers.dev" (`internal/registry/catalog_config.go:21-24`). It turns this request off too: `state` is `off`, no figure is given, the disk copy is not read. Off in a test binary and in Home mode, as the listings are. Decision 3.
- **Through the configured proxy,** within 30 seconds a page.

No model list, no route and no request to a vendor depends on any of it. No executor, translator or auth file changes.

### 6.3 Status for the operator

`GET /v8/management/provimux/figures`, read-only, behind the management key: the same `figures` object as the tools get, with `last_failure` (`{at, category, http_status}`, codes only), `from_cache` and the count of held entries; `entries` holds those an `answered` item of a route in the answer of `model-routes` names, every held entry with `?all=true`. So "serve only my names" is applied once, where `model-routes` applies it (`provimuxGated`, `provimux_model_routes.go:146`, `:172`), and a name that is not served brings no entry. `GET /v8/management/provimux/model-routes` gains `answered` on each route (`internal/api/handlers/management/provimux_model_routes.go:72-82`): the same record as the client list's, with the time of each item where the client list gives the day (`pui13/openapi.yaml`, `RouteAnswered`). No route carries a predicted entry. An older Provimux answers `404` on the new path: the console shows no figures.

Log: `prices and cache lifetimes from providers.dev: dataset 89105175… of 2026-11-25, 584 entries`, and the listings' failure line under this name.

## 7. The console

**On a name's card, per account** (`pages/models/NameCard.tsx`, the line of a route; the look is the designer's). The figures on a line are those of the entry the account last answered as for this name: the newest item of the route's `answered` (amended 2026-11-26, late evening). The line says so, with the time, because it is a record and not a promise.

| Case | Words |
|---|---|
| The account answered this name, one entry recorded | "Last answered as anthropic/claude-opus-5-5, 2 hours ago", then the lines below for that entry |
| Several entries recorded | "Its answers differ: openai-codex/gpt-6.1-sol (today), no entry (7 days ago)", and the lines below for the newest that has figures |
| Its answers named no entry | "Answers without a providers.dev entry (last 3 hours ago)", small. No figures |
| No answer recorded | "No answer yet for this name: prices and cache lifetime appear after its first answer", small. No figures, and none are guessed from the account's kind |
| A key | "$4 in, $20 out per million tokens · cache read $0.20 · cache write $5 (5 min), $8 (1 h)" and under it "providers.dev, from models.dev, 2026-11-24 · base rates" |
| A subscription | "Not billed per token. API list price for comparison: $2 in, $10 out" |
| Measured by a real client through Provimux | "Cache through Provimux: 30 min, measured with a coding agent, 2026-11-07" |
| From the safe value | "Cache: 2 min 30 s, measured with OpenAI's Codex CLI, 2026-11-24. Not measured with a client through Provimux" (for a key: "measured straight at the provider") |
| Both, and they differ | both lines, the real client's through Provimux first, each with its date |
| A real client measured 0 | "No cache lifetime held through Provimux (4 of 26 checks hit, 2026-11-10)" |
| The safe value is itself the plain probe's | "Cache: 1 h, measured with plain test requests through Provimux, 2026-11-16. Not measured with a client" |
| A plain probe's measurement beside another value | in the card's details only, never as the lifetime: "Plain test requests through Provimux: 3 of 10 checks hit, 2026-11-17" |
| Documented only | "Cache: 5 min, documented, not measured" |
| Window differs from the registered one | "providers.dev: input up to 999,500 tokens (measured 2026-11-17)" in the card's details |
| The entry it answered as is not at providers.dev | "providers.dev has no entry for this model", small, once per card |
| Old | a date more than 90 days back (providers.dev's stale threshold today) is marked "old" |
| Sources disagree on the price | a warning mark, and the link |

Every line links to the entry's page (`https://providers.dev/entries/<provider>/<model>/`). The name's header gets no price and no lifetime while more than one account serves it. A line in the page's settings says "Prices and cache lifetimes: providers.dev dataset of 2026-11-25, fetched 2 hours ago", or since when providers.dev was not reached, or that they are off; it puts the unused published-list strings (`en.json:613-618`) back to use. One read on page load and on refresh, not in the 30-second poll.

**On Savings, per account.** What exists: a day's rows are kept by account kind, provider, host, model and client key, and the record's account is deliberately not read (`internal/provimux/savings/record.go:25-27`); since "serve only my names" the name the client asked for is kept too, apart from the rows (`savings/requested.go`, `include=requested_models`); tokens are valued at OpenRouter's list price for the maker's model; a day keeps the prices it was first valued with [read]. What is added:

| | |
|---|---|
| The account in a row | a row is also keyed by the account's marker (`answered-by.md` section 2; `model-routes` gives it beside the login's name). Not by the index: that is made from the file's path and survives a replaced login. Days recorded before have none and show as "before accounts were recorded" |
| A grouping | `group=account`: per account its provider, kind, host, the login's name as `GET /credentials` names it today (null when the account is gone), its requests, tokens, estimate and reported charge |
| The account's own price | a new first step in the price search (`savings/02-prices.md` section 4): the entry of the row, when the figures hold a `per_use` or `reference` price for it. Source `providers.dev`, price id `providers.dev:<entry>`, match `entry`. Then the steps of today |
| Where a row's entry comes from | from the attempt that produced the usage record: the entry `answered-by.md` 4.3 works out for that attempt, kept in the row beside the marker. Never `EntryProvider` over the account's configured host and the recorded model: that is F1's prediction one step later, and wrong in the same cases (a destination from a credential's metadata, a rewritten model). A row whose attempt had no entry is valued by the steps of today. How the usage record gets the attempt's entry is F6's first design task: the record is published from the executor's reporter under the attempt's context, and the attempt's tracker holds what was sent (`answered-by.md` 8.1) |
| What stays | a day keeps its prices, so no past number moves. Prices above a prompt size are still not applied: a day's totals hold no request sizes, and the answer keeps saying `base` |
| Words | "at this account's list price (providers.dev, 2026-11-24)"; a subscription's rows "would have cost at API list price"; the only real charge on the page stays the reported one |

The savings contract files (`savings/openapi.yaml`, `schemas/day.schema.json`, `02-prices.md`, `03-endpoint.md`, `04-console-page.md`) are amended as the first task of that phase; this section is their brief. The marker is the same after a restart and after a token refresh by how it is made; a login replaced by another account's starts new rows.

## 8. The two Pi extensions

### 8.1 pi-provimux

| | |
|---|---|
| Reads | `GET /v1/models?include=figures` in place of `?include=accounts` (`extensions/lib.ts:1112`, `extensions/upstreams.ts:233-256`). A gateway without figures answers as before, and everything is as today |
| A model's price | the price of the entry the account tried first last answered as (the newest item of its `answered`), with its size tiers, where `figures` has one (`per_use` or `reference`); when several accounts share the first place, or one account's answers differ, and the prices differ, the highest, and the name says so. An account with no answer recorded, or whose answers named no entry: models.dev, as today. Fast mode: models.dev, as today. After each answer whose entry is new to it, it reads the list again and prices the model anew for the messages that follow |
| The footer | "answered: Anthropic api · $4 in / $20 out" (or "· not billed per token"), from the entry the answer itself names (`X-Provimux-Entry`, or `provimux_entry` on the WebSocket) |
| What it cannot do | Pi totals a message from the model's one price, so on a pooled name a message answered by another account than the first is counted at the first's price. The footer shows the right one. Whether an extension can correct a message's recorded cost is not verified |
| Writes no cache lifetime into Pi | Pi's own warmer never checks a warm-up; choosing the value for a pooled name is pi-cache-keeper's work |
| Hands on | each upstream of its `model-upstreams` events gains `answered` (the row's record) and, where that names one entry, `entry` and `figures` (the entry's record as Provimux gave it, or `null` when Provimux held figures and has none for it). Its `model-upstreams:answered` event carries the answer's own entry and that entry's figures, read again from the list when they are new. The upstream's `model` is taken from the entry |

### 8.2 pi-cache-keeper

| | |
|---|---|
| An upstream with `figures` | its window, output limit, lifetimes (`rely_on_s` per profile, by 4.2 as amended) and price are taken from it. Its status names what the lifetime rests on, from `basis` and `date`: "measured with a coding agent through Provimux, 2026-11-07", or "the Codex CLI's, 2026-11-24". Nothing is looked up for it, and its provider is not asked of providers.dev (`lookupsFor`, `model-data.ts:321-338`; `neededProviders`, `index.ts:1195-1207`) |
| An upstream with `figures: null` | no data, as for an entry providers.dev lacks |
| An upstream with `answered` and no `entry` (no answer recorded, answers that differ, answers without an entry) | no data from Provimux, and **no lookup of its own**: behind a gateway that says what answered, a guess from the name is what this design removes. The model keeps the window Provimux registered for it. The lifetime is not needed yet: pi-cache-keeper acts on a cache only after an answer wrote or read one (`index.ts:679`), and takes the lifetime of what answered (`index.ts:1016-1017`), which that answer names |
| An upstream without `answered` | as today: its own lookup at providers.dev (an older pi-provimux or gateway, or figures off) |
| Everything after that | unchanged: several upstreams are merged as today, the answered route swaps in as today, `models.json` is written under today's rules |
| Its status line | names the source: "providers.dev through Provimux, dataset of 2026-11-25" |

## 9. Advertising it, and what stays true

### 9.1 What a client does with it, with no code of its own

| Client | A price | A cache lifetime |
|---|---|---|
| Pi with pi-provimux | the model's price and the footer (8.1) | nothing without pi-cache-keeper |
| Pi with pi-cache-keeper too | as above | acted on, from Provimux's figures (8.2) |
| opencode, Claude Code, Codex CLI | nothing | nothing |

For the last row this is from what each is known to do, not from reading their code today [infer]. So the public words say what a tool **can** do, never that Provimux does it for a client.

### 9.2 The words

For Provimux's README, its page on the site and the notes of the release (the owner's word on public text first):

- "Per account behind a model name, Provimux gives providers.dev's list price, cache prices, cache lifetime and window, each with its date and source: in the console, and to tools in `/v1/models?include=figures`."
- "Every answer names the account that gave it by an opaque marker, the model that answered, and its providers.dev entry (`X-Provimux-*`), read from the request Provimux sent for it. So a tool knows what the answer cost and how long its cache lives, without knowing any provider. pi-cache-keeper is such a tool for Pi."
- "Before an answer, the model list says what each account last answered you as: what its answers to your own key said. It is a record, not a promise: the request decides, each time." (Amended 2026-11-27 by the lead: the record is per client key, `answered-by.md`, the amendment of that day.)
- (Amended 2026-11-27 (2) by the lead, `answered-by.md`, the second amendment of that day.) "In the gateway as shipped, an answer names its providers.dev entry where Provimux made every part of it: one of its built-in executors, an HTTP client it built, a credential its own loader read from the auth folder or the configuration, and the one request that produced the answer, read from the bytes it sent. In a program that embeds Provimux's SDK with an executor, a transport or a credential of its own, answers name no entry."
- "What each account last answered you as is a best-effort memory: Provimux keeps what its own server wrote to you, rebuilds it with the next answer, and promises no more. It can miss an answer made while the auth folder changes; a second Provimux on the same auth folder keeps its own only until it stops; and with an access provider of your own, keys that sign in as one subject share one record."
- "The cache lifetime is the one providers.dev measured with a real client through Provimux where it has such a measurement, and its safe value otherwise. Plain test requests never decide it. Every value says what it rests on and when it was measured."
- A README section "Figures for tools": the three steps of section 5, the promises, and the example.

Never: that Provimux knows an account's entry, price or cache lifetime before the account has answered; that Provimux knows what a request cost, that it guarantees a lifetime, that it gives the lifetime the vendor's own client gets, that it behaves as or looks like a vendor's client, or a price or cache field on a model itself, where a client would take it for the gateway's own.

## 10. The build

### 10.1 Provimux

Layer commits only (trailer `Provimux-Layer: branding`). No protected path (`guard.sh:33`).

**New files.**

| File | Holds |
|---|---|
| `internal/provimux/modellists/figures.go` | the request of 6.2, the acceptance rule, the kept record as Go types, `figures.json`, the rule of 4.2 |
| `internal/provimux/modellists/figures_map.go` | the map of 6.1, keyed by what was sent. Built in F1 and kept: W6a's first commit (10.5) |
| `sdk/cliproxy/executor/provimux_sent.go`, `sdk/cliproxy/provimux_answer_entry.go`, `sdk/cliproxy/provimux_answered_record.go`, `internal/pluginhost/provimux_plugin_credential.go` | what an attempt sent, the entry of an answer, the record `answered`: `answered-by.md` 8.1 (W6a, W6b). They replace F1's `sdk/cliproxy/provimux_entry_refs.go` |
| `sdk/cliproxy/provimux_figures.go` | the held document, its place in the listings round, the source the handlers read |
| `internal/api/handlers/management/provimux_figures.go` | the endpoint of 6.3 |
| `sdk/api/handlers/openai/openai_models_figures.go` | `include=figures` |
| their `_test.go` files | 10.4 |

```go
// internal/provimux/modellists

// EntryProvider returns the providers.dev provider of a request by the gateway's provider of the
// credential that sent it, the credential's kind ("oauth" or "apikey"), the host the request went to
// and the names of its credential headers, or "" where the map of prices-and-cache.md 6.1 has no row.
// (Amended 2026-11-26, late evening: host was the host of the account's base URL.)
func EntryProvider(provider, kind, host, signer string) string

// EntryRef joins a providers.dev provider and a model id as providers.dev writes a reference.
func EntryRef(provider, model string) string

// FigureProviders returns the fixed set the figures request asks for, sorted.
func FigureProviders() []string

// FiguresRequestURL is one page of the request Provimux sends, built on base.
func FiguresRequestURL(base string, offset int) string

// Figures is what Provimux keeps of one entry: openapi.yaml, components/schemas/Entry.
type Figures struct { /* Provider, Model, URL, Access, Price, Cache, InputWindow, MaxOutput */ }

// FiguresDocument is an accepted answer, as held in memory and written to figures.json.
type FiguresDocument struct { /* Schema, ETags, DatasetVersion, GeneratedAt, FetchedAt, Entries by ref */ }

// FetchFigures asks every page. A result with NotModified keeps the held document.
func FetchFigures(ctx context.Context, client *http.Client, base string, held *FiguresDocument, userAgent string, now time.Time) (FiguresResult, error)

// CountsForRelyOn reports whether a measurement by this providers.dev client id is a real client's
// through Provimux (prices-and-cache.md 4.2): today only "agent+cliproxyapi". A plain probe's
// ("cliproxyapi") never counts.
func CountsForRelyOn(client string) bool

// MeasuredLifetime is one measurement through Provimux: openapi.yaml, components/schemas/Measurement.
type MeasuredLifetime struct { /* Client, Label, Counts, Seconds, Date, Checks, Hits, FirstMissSeconds, Asked */ }

// RelyOn applies prices-and-cache.md 4.2 to one profile of an entry: the lowest measurement that
// counts (of equal ones the newest), else the safe value by its basis, else nothing.
func RelyOn(safeSeconds *int, safeBasis string, safeDate string, throughProvimux []MeasuredLifetime) (seconds *int, basis string, date string)

func (s *Store) ReadFigures() (*FiguresDocument, error)
func (s *Store) WriteFigures(doc *FiguresDocument) error

// sdk/cliproxy

// provimuxEntryOfAnswer returns the entry of an answer from what its attempt sent, or "":
// answered-by.md 8.1. There is no function that gives an account's entry ahead of a request.
func provimuxEntryOfAnswer(auth *coreauth.Auth, sent cliproxyexecutor.ProvimuxSentRequest) string
```

Each body is a worker's. As first written this frame had `provimuxEntryOf(auth, model)`, an account's entry for a registered name, and called "which field of the registry holds the upstream id" the one open question of the build. F1 answered it with a mirror of upstream's derivation, and two reviews showed the question was the wrong one: the id is decided after the registry, per request (6.1). The frame of what replaces it is in `answered-by.md` 8.1.

**Edits to existing files: additions only.**

| File | Edit | Lines |
|---|---|---|
| `sdk/api/handlers/openai/openai_models_accounts.go` | `answered` on `modelAccount` (`:15-23`), filled through a function variable `sdk/cliproxy` sets (W6b); `include=figures` recognised beside `accounts` (F3) | +8 |
| `sdk/api/handlers/openai/openai_handlers.go` | where the list is written (`:85-128`): the `figures` object when asked | +5 |
| `internal/api/handlers/management/provimux_model_routes.go` | `answered` on `provimuxRoute` (W6b) | +5 |
| `sdk/cliproxy/executor/context.go` | the attempt's observations on the tracker, and the decorator where the tracker is made (W6a; `answered-by.md` 8.2) | +2 |
| `sdk/cliproxy/provimux_published_lists.go` | one call in `refreshPublishedLists` (`:208`), one in `loadCache` (`:112`) | +2 |
| `internal/api/server_provimux.go` | the route | +1 |
| README, `config.example.yaml` | the second request; what the switch also turns off; "Figures for tools" | text |

`openai_handlers.go` is upstream's file with the owner's patch in it (the `accounts` lines); the five added lines sit beside those. The others are the layer's own or the owner's patch file.

### 10.2 The console

| File | Change |
|---|---|
| `src/provimux/api/types.ts`, `api/normalize.ts`, `api/provimuxApi.ts` | `answered` on `ProvimuxModelRoute` (missing and empty read alike); `ProvimuxFiguresResponse`; one read |
| `pages/models/catalog.ts`, `NameCard.tsx`, `ModelsSettings.tsx` | the join, the lines of section 7, the status line |
| `pages/savings/` | the account grouping and its words |
| `locales/*.json` | the strings, in all seven languages |
| `tests/provimux/` | each row of section 7's table; a gateway without the path; `off`, `none`, an old date |

### 10.3 The extensions

Section 8. pi-provimux: `sources.ts` (`account`, `answered`), `lib.ts` (the price, and pricing anew after an answer), `upstreams.ts` (the events), `answered-footer.ts`. pi-cache-keeper: `model-data.ts` (an upstream's figures before its own lookup), `index.ts` (`neededProviders`, the status line).

### 10.4 What must hold

| Phase | Must hold afterwards |
|---|---|
| F1 | built, reviewed twice, not merged (the note before section 1). What had to hold of it now has to hold of W6a and W6b: `answered-by.md` 8.4. In short: every row of 6.1's table, each "none" row included, by the host that was sent; for every executor and path, the answer's entry is the map applied to what a stand-in vendor received; `/v1/models` without `include` is byte-identical; both account lists give the same `answered` for the same account (`test/provimux/model_routes_parity_test.go` grows) |
| F2 | a test server: `200` on three pages, `304`, `500`, a cut-off body, pages of two dataset versions, an older document, an entry with no rules, a rule the reader does not know; the request carries no parameter beyond `provider`, `limit`, `offset` and no header beyond the two; nothing is fetched with the switch off or in a test binary; a start with the server down serves the disk copy; no path that answers a client waits for the fetch; 4.2's rule row by row, with the six entries of finding 2 as fixtures: a plain probe's measurement alone never gives row 1 (`openai-codex/gpt-6.2-sol` gives the Codex CLI's 150, not 0), a real client's does also above the safe value (`gpt-6.1-sol`: 1,800), the lowest of two real clients' wins, and every probe item is in the answer with `counts` false |
| F3 | all three examples validate against `prices-and-cache/openapi.yaml`, and so does the handler's answer for a fixture; after each answer of the fixture its `X-Provimux-Entry` is the newest item of its row's `answered` (`answered-by.md` 8.4); every entry an `answered` item names whose figures are held is a key of `entries`, and `entries` has no other key; an entry met for the first time is a key of the list read again; `include=accounts` alone carries no `figures`; with the switch off `state` is `off` and `entries` is empty |
| F4, F6 | the console's own tests; `bun run verify`; the pages checked against a stand-in gateway; Savings' examples regenerated and its past days unchanged |
| F5 | pi-provimux: a gateway with figures, without, with a malformed entry; a row with one item, with several, with a `null` item, with none; the price after an answer whose entry is new; the footer; the events. pi-cache-keeper: an upstream with figures is never looked up and its provider never fetched; `figures: null` gives no data; an upstream with `answered` and no entry is not looked up either; without the key, today's tests pass unchanged |

### 10.5 What of F1 is kept

F1 is on `provimux-figures-f1-rebase` at `4f14c2cff` and is not merged. Kept: the map (`figures_map.go`) and its matrix test, keyed by the host that was sent; the three kinds of no entry, redefined in 6.1. Dropped: the mirror of upstream's derivation and its drift test, the function that gave a row its entry and its three test files, the plugin model lookup, the `entry` hooks and schema. File by file, with what a sync carried then and carries now: `answered-by.md` 8.5.

## 11. The later phase: Provimux acting on the data

Not part of phases F1 to F7 and not in their way. Compaction stays the client's: only the client knows its own context.

**A. Back to the preferred account once the cache is cold. Build it, as the first acting feature.**

| | |
|---|---|
| What upstream does | With `routing.session-affinity: true` a conversation is bound to the account that served it. Every request refreshes the binding; it ends after `session-affinity-ttl` without a request, one hour unless set, the same for every account (`sdk/cliproxy/auth/session_cache.go:122-144`). The binding outranks priority: "once a session is bound, that credential is kept even if a higher-priority credential recovers" (`config.example.yaml:240-242`; `Pick`, `sdk/cliproxy/auth/selector.go:1738-1745`) [read]. The session is found from the headers of Claude Code, Codex, OpenCode and pi, from `X-Session-ID`, a cache key, or the shared start of the conversation (`config.example.yaml:231-238`). So "stay on the account that holds the cache" exists, for every client, and the owner has it on |
| What is missing | The way back. A conversation that failed over from a subscription to a paid key stays on the key while it keeps talking, and for an hour after |
| The rule | Opt-in (`routing.session-affinity-return: "cold"`). A binding to an account below the highest available priority is not honoured once the conversation has been quiet for longer than that account's cache lifetime for the model. The request is then routed as a conversation without a binding |
| Which lifetime | what the bound account's last answer in this conversation reported for its cache write, where the vendor reports one (Claude's format does; `internal/provimux/savings/record.go:63`); else the largest lifetime the entry's figures state, in either profile. The largest, because returning late costs nothing new and returning early gives up a warm cache. Unknown: the binding holds as today |
| Cost | No request is added and none is changed. A lifetime known too short: one cache write on the preferred account where a read on the key would have done |
| Risks | The vendor sees the same requests from the same accounts, so nothing about subscription terms changes. The risk is the code: an inserted block in upstream's selector, a file upstream changed 13 times between v8.0.60 (2026-11-08) and v8.1.3 (2026-11-25) [ran: `git log`] |
| Why the data fits | the account a conversation spills to is almost always an API key, where providers.dev's lifetime is a direct measurement or the documented one |
| The build | new files `sdk/cliproxy/auth/provimux_affinity_return.go` (the rule; a read of a binding's account and last use that does not refresh it) and `sdk/cliproxy/provimux_cache_lifetimes.go`; inserted lines in `Pick` before the binding is read (`selector.go:1738`) and wherever else a binding is honoured (the fallback key `:1797`, the shared-start matcher `:1851`; not read in full); the setting beside the affinity settings (`sdk/cliproxy/service_config.go:60-63`, `internal/config/config_types.go:620`). With the setting off, upstream's selector tests pass unchanged. Size L |

**B. A keep-warm request sent by the gateway. Do not build it.**

| | |
|---|---|
| What upstream does | Nothing: no code re-sends a request to keep a cache alive [ran: a search; the matches are a client's own websocket pre-warm and a latency probe] |
| What it takes | each conversation's last request held in memory, re-sent with one output token before the lifetime ends, to the same account, and a client's own warm-ups told apart so nothing is warmed twice |
| Cost | a cache read of the whole prompt each time: for 300,000 tokens at Claude Opus 5.5's cache-read price ($0.20 per million) $0.06, twelve times an hour on a 5-minute cache, for a conversation nobody may come back to. On a subscription, quota and requests against the plan's limits |
| Risks | the gateway would send a user's content to a vendor in a request no client asked for; on a login that is traffic the vendor's own client would never send, under that client's name. A re-sent request can have effects (a stored response, a turn in Devin's session). A wrong lifetime turns every warm-up into a full cache write |
| Instead | a tool on the contract of section 5, which knows whether its user is still there; pi-cache-keeper is one. Asking the vendor for the longer lifetime on an API key, per key and opt-in, is the gateway-side alternative: it changes a request to a vendor, so it is an owner patch and its own decision, and the one measurement of it through Provimux held 10 minutes, not an hour (finding 2, last row) |

## 12. Fit with what is being built

**"Only my names"** is released (v8.1.4-pmx.2; the console's step is on its `main`), so the build waits for nothing. As built it differs from what this section first said: the rule is not a filter in front of the registry. Accounts keep registering everything they offer, and the rule is applied where names are read (`only-my-names.md` section 16; `internal/provimux/onlymynames`, `provimuxHeld` in the registry's lists, `provimuxGated` in `model-routes`). What follows for this design:

| | |
|---|---|
| An answer's entry | is read from the request that was sent, so the rule has no part in it (6.1) |
| `answered` | is recorded under the id the account registers, with the rule on or off, and shown only on a row that is listed |
| A name that is not served | is in neither account list, so it has no account line and brings no entry into `figures.entries`. The management path follows `model-routes` for the same reason (6.3). No second gate is written |
| Shared lines | `internal/api/server_provimux.go`: one more route under `/provimux/names` (`:21`). `provimux_model_routes.go`: `answered` on `provimuxRoute` (`:72-82`). The console's `NameCard.tsx` and `catalog.ts` now also carry the names rule's verdict; the figures' lines are added beside it. The locale files: the unused published-list strings are still at `src/provimux/locales/en.json:613-618` |
| `pui13/openapi.yaml` | now also holds the names path. It gains `answered` on a route (on the design branch since the amendment of the late evening; W2 added `account` to the same schema on its own branch, in another place of it); the new paths stay in this design's own contract file, so there is no second edit there |
| It makes this design matter more | with the rule on, every served name is one the owner set, and for such a name only the gateway knows the model behind it (finding 4) |

**Savings** keeps OpenRouter's list as the source for every model without an entry price, and every stored day as it is.

**The model lists (P3)** are unchanged. Their Kimi row is the one open point this design found (6.1).

## 13. The phases

Each ships alone. S: up to a day of one worker. M: one to two days. L: three to five.

| Phase | Repo | Size | Holds | Depends on | What a user has after it |
|---|---|---|---|---|---|
| Phase 0 | groundtruth; then Provimux | M; S | the measuring night of section 17; then, only if 16.4 says so, the Meta cache key of 16.2, and P-1 if the owner says yes | the owner's one-time logins | row 1 of 4.2 rests on new pairs for the newest models |
| F1 | Provimux | M | built and reviewed twice; **not merged**. Its map and the map's tests are W6a's first commit; the rest is dropped (10.5) | | |
| W1 to W3 | Provimux | built | `answered-by.md` section 9: the marker, in the lists and on every answer | nothing | every answer says which account gave it |
| W6a, W6b | Provimux | M each | 6.1 and `answered-by.md` 4: what an attempt sent, the answer's entry (W6a); `answered` on both account lists (W6b) | W3. No owner patch | every answer over HTTP names its entry, read from its own request; each account row says what its answers were |
| F2 | Provimux | M | 6.2, 6.3: the request, the disk copy, the switch, the rule of 4.2, `GET /provimux/figures`, the log | nothing (it needed F1 only for the fixed set of 6.2, a constant) | the gateway holds the figures |
| F3 | Provimux | S | section 5: `include=figures`, the contract test, the README section | F2, and W6b | tools can read them; a release note names contract 1 |
| W4, W5 | Provimux | S each | the model the vendor named, and the effort (W4 an owner patch) | the owner's yes; W6a | an answer says whether the vendor confirmed the model. The measuring needs it; the figures do not |
| W6s | Provimux | S | what was sent over an upstream WebSocket (an owner patch) | the owner's yes; W6a | an answer over an upstream WebSocket names its entry too |
| F4 | provimux-ui | M | section 7, the name's card and the status line | F2 released | figures per account on the Models page |
| F5 | pi-provimux, pi-cache-keeper | M each | section 8 | F3 released; built against the examples before that | Pi prices by account; pi-cache-keeper has one source behind Provimux |
| F6 | Provimux, provimux-ui | M each | section 7, Savings by account; its contract files amended first | F2 | Savings by account, at the account's list price |
| F7 | the site, the READMEs | S | section 9.2, after the owner's word | F3 and F4 released | it is advertised |
| Review | each phase | S | W6a, W6b: the list of `answered-by.md` 8.4, first an answered request the gateway did not see beside one it did. F2, F3: a figure for a wrong entry, the request's privacy, a start without network, the sync check no worse than upstream. F4, F6: every row of 4.3. F5: no model with two sources | | |
| Later | Provimux | L | section 11 A | F2 | conversations return from a paid key |

Order (amended 2026-11-26, late evening): W6a, W6b, with F2 beside them; then F3; then F4 and F5 side by side; then F6; then F7. W4 and W5 run on their own line once the owner has said yes to the patch, and W6s after W6a if he wants it. A release of Provimux can carry W1 to W3 alone, W1 to W6b, or those with F2 and F3. Phase 0 runs beside them and blocks none: F2 builds the rule of 4.2 as amended, whatever the night measures.

## 14. Decisions

| # | Decision | Recommendation | The other way costs |
|---|---|---|---|
| 1 | What "the lifetime to rely on" is | **The rule of 4.2 as amended** (decided 2026-11-26): a measurement by a real client through Provimux first, else providers.dev's safe value, both always shown; the plain probe's never in row 1 | The safe value alone is one field of providers.dev and no rule of ours, and on 11 login entries today it is not what a real client through Provimux measured. The price of the amendment: the value no longer errs low (8 entry profiles above the safe value, 4.2) |
| 1b | A safe value that is itself the plain probe's (seven entries, the four Claude login entries among them) | **Keep it in row 2**, named as what it is | Giving `none` there leaves the Claude login without a lifetime until the Claude pair is measured |
| 2 | The words on Savings | **"At this account's list price", an estimate**; only a reported charge is called a charge | "True money" for rows no vendor reported a charge for is a claim the data cannot carry |
| 3 | The switch | **`models.published-lists` turns both requests off** | A second key lets a user keep lists and refuse figures; one more setting for one host |
| 4 | OpenRouter in the request | **Yes**: 400 entries, 85 with a measured lifetime, two more pages a day | Without it the paid route most users have has no figures |
| 5 | The model behind a name becomes visible to a holder of a client key: on each answer to his own request, and in the list as what an account last answered as | **Accept**: the list already gives provider, kind and host | A setting to hide it; then no tool, and no pi-cache-keeper, has data for his own names |
| 6 | Kimi, the xAI login, AI Studio, a Meta key | **No entry**, and the model lists' Kimi row is checked against the login's host | A guess gives another plan's figures as this account's |
| 7 | How long contract 1 is kept after a contract 2 | **Two Provimux releases that say so** | Longer costs two shapes in one handler for longer; shorter breaks a tool between two of its own releases |
| 8 | pi-provimux writes no cache lifetime into Pi | **Yes**; "or use pi-cache-keeper" is the owner's own alternative | Pi's warmer would act on it and never checks a warm-up |
| 9 | The later phase | **A, not B** (section 11) | |
| 10 | The public sentences of 9.2 | the owner's words; the draft is there | |
| 11 | A cache key for a caller that sends none | **Codex and Devin: build nothing in the request path, pin upstream's behaviour with a test. Meta: one owner patch, only if the measurement of 16.4 says so** | Building one for Codex would put a second derivation over upstream's, in a protected path, for no measured gain |
| 12 | P-1, a Claude login in overage asks 5 minutes | **Build it with the default unchanged**; decide after the Claude pair (4.4) | `follow` costs $1.96 more on each return after 5 to 60 minutes of the example and saves 3 cents a turn |
| 13 | What the measuring caps hold (2 dollars a run, 10 dollars a week) | **Billed dollars**, as groundtruth's ledger counts them for the API keys; the logins are held by the cap of six runs a login a night and by a Claude run stopping at the first sign of overage (17.5) | Held against list-price value, only the two Terra pairs, the Ember pair and the probe runs fit a run's 2 dollars |
| 14 | Which gateway the Pi side of a pair goes through | **The owner's running one**, on his word, at an hour he is not using those models (17.2) | A second gateway with logins of its own keeps the night apart from his traffic and costs four more one-time logins |
| 15 | What is dropped first if the night must be smaller | the order of 17.6 | |
| 16 | Who answered: the marker, the model, the names of the headers, the switch, an owner patch in the usage reporter | `answered-by.md` section 10, decisions 1 to 9 | |
| 17 | What an account row carries, now that no entry is predicted; whether that record outlives a restart; a day or a time in the client list; an owner patch for the upstream WebSocket; the xAI row | `answered-by.md` section 10, decisions 10 to 13 and 15 | |
| 18 | A model a plugin put on an account's list, sent by the account's own executor to the account's own host with a credential the gateway made | **It has the entry of what was sent** (`answered-by.md`, decision 14). A plugin's executor answering names nobody; a credential a plugin made or changed has no entry | F1's rule withheld entries that were right (the second review's note 18) and needs the plugin host's lists read at every answer |

## 15. What I ran, and what I did not verify

| Check | Result |
|---|---|
| `GET /api/v1/openapi.json` | version 1.6.2; `EntrySummary` carries prices, `pricing`, `safe` and `cache_lifetime`; `/entries` takes `provider`, at most 200 a page; the listings carry no price or cache field |
| `GET /api/v1/entries?provider=<twelve providers>&limit=200`, plain and with `If-None-Match` | 190 entries, one page, 51 KB gzip, 1.53 MB decoded; `304` on the second; `https://providers.dev/entries/<provider>/<model>/` is the entry's page |
| `GET /api/v1/entries?provider=openrouter`, two pages | 400 entries, 68 KB gzip; 290 with a price; 85 short lifetimes measured directly, 9 items through Provimux |
| A script over the 190 summaries | 164 have a price, 137 a cache read price, 55 a cache write price, 119 have other rates, 8 have sources that disagree on the price; 72 have a safe lifetime in a profile, 34 a measurement through Provimux, 17 of those differ from the safe value (16 logins); `price_in` equals the base input rule on all 190 |
| `GET /api/v1/listings?view=full&limit=40&provider=<the same twelve>`; `GET /api/v1/meta` | finding 3; the editorial rules; `stale_after_days` 90 |
| `prices-and-cache/openapi.yaml` through `openapi-spec-validator`; both examples against its schemas with unknown properties forbidden | valid OpenAPI 3.1.0; 0 errors in each; an example with five planted faults (a missing key, a wrong type, an unknown enum value, an extra key, another contract number) gave five errors. The examples' figures are today's answer for those entries, built by a script from the fetched summaries by the rules of 4.1 and 4.2 |
| Read: Provimux's account list, model routes, the listings client and its round, Savings' recording and prices, the affinity selector's `Pick` and the session cache; "only my names" as built; pi-provimux's tables, `upstreamOf` and where a price is set; pi-cache-keeper's reader | sections 1, 6 to 8, 10, 11 |
| `git log v8.0.60..v8.1.3` on `selector.go` and `session_cache.go`; a search of the tree for a warm-up | 13 commits; no request is re-sent to keep a cache alive |

**Run and read on 2026-11-26, for the amendment.**

| Check | Result |
|---|---|
| The branch moved onto `main` at `aa76f801b`: the three design commits, no conflict | every cited line range of Provimux, pi-provimux, pi-cache-keeper and the console compared between the old base and the new by a script: six ranges moved (corrected above), none in the extensions or the console's locale file |
| `GET /api/v1/openapi.json`, `/api/v1/entries?provider=<the eleven without OpenRouter>`, `/api/v1/evaluations/cache?client=agent+cliproxyapi` and `?client=cliproxyapi` | version 1.6.3, dataset `c37885999c67cfaa`; 191 entries; 34 with a measurement through Provimux, 21 with a real client's, 11 of those unlike the safe value, 13 with the probe's only; 24 and 41 cache evaluations; `client` is the one field that tells them apart (4.2) |
| A script applying the rule as first written and as amended to the 191 summaries | 41 entry profiles with a measurement through Provimux, 11 change value; the table and the lists of 4.2 |
| Both examples rebuilt by a script from those summaries by 4.1 and 4.2, then validated against `openapi.yaml` with unknown properties forbidden | for the three entries of 2026-11-25 the script gives yesterday's records except the rule's fields, `counts` and newer dates; 0 errors in each; five planted faults (a missing `counts`, an unknown `basis`, a wrong type, an extra key, another contract number) give five errors |
| Read: `session.Enrich` and `DeriveID`, the Codex, Meta and Devin executors' handling of a cache key and session id, the handlers' caller scope; `git grep` of the same on the tags v8.0.53 to v8.1.4 | section 16.1 |
| Read: groundtruth's probe (`src/cli.ts`, `src/run.ts`, `src/formats.ts`), one published Codex probe run's own record, `docs/own-clients.md`, `own-clients-RUN.md`, `weekly-night.md`, `harness/ledger.py` | 16.1, 17 |
| `release_date` and the price rules of every entry on the four logins | 17.1, 17.5, 4.4 |

Nothing was sent to a provider, a login, the owner's gateway or port 8787.

**Not verified.**

| Point | Who settles it |
|---|---|
| Which field of `registry.ModelInfo` holds the upstream id on each kind of route | no longer needed: the id is read from the request that was sent (6.1). F1 answered it by mirroring upstream, and two reviews found eleven routes where the request was sent as something else |
| Everything the amendment of the late evening rests on and did not verify | `answered-by.md` section 11 |
| That a page's `ETag` changes whenever an entry on it changes | F2: the document is also checked by `dataset_version` on every page |
| That `agent+cliproxyapi` stays the one id of a real client through Provimux and `cliproxyapi` the one of the plain probe | F2 keeps both as constants; the two rules of 4.2 are the measuring side's. If a third id appears, the attribute named in 4.2 is the growth to ask for |
| Whether Meta's cache hangs on a `prompt_cache_key` at all | MK of 16.4 |
| Whether a payload `filter` rule can take the derived key off a Codex request; the key is set in `cacheHelperDoc`, and where the payload rules run against it was not read | the pinning test of 16.2, which also says what the switch is worth |
| That a one-hour cache mark is honoured on a Claude login in overage | the Claude pair, if the login is in overage for its own reasons (4.4) |
| Which reasoning levels `gemini-4-neon` has on Devin, and whether the Devin CLI reports cached tokens for it and for `gpt-6-1-terra` | the night's smoke runs (17.3) |
| That an account's index survives a restart and a token refresh | no longer needed: Savings is keyed by the marker (section 7, amended 2026-11-26) |
| Whether a Pi extension can correct a message's recorded cost | F5; without it 8.1's limit stands |
| Which host a Kimi login of the owner uses; whether a Meta key can be told from a Muse Code subscription's key | decision 6 |
| Every place the selector honours a binding beyond `Pick`'s cache-hit branch | the later phase's first task, as a test |
| What opencode, Claude Code and Codex CLI do with the answer | not needed: they send no `include` |
| Nothing was built or run against a gateway | every phase's tests |

## 16. A cache key for a caller that sends none

Asked on 2026-11-26: `client-parity.md` found that Provimux passes on the caller's cache key and session id, Pi's; a simple tool that sends none may land on a cold cache every time. Should Provimux supply one?

### 16.1 What is true today

Two premises of the question do not hold, and the answer starts there [read at `aa76f801b`; groundtruth at `6650203`]:

1. **The plain probe is not a caller without a key.** groundtruth's TypeScript probe sends a `prompt_cache_key` per sample by default on the chat and responses formats (`src/cli.ts:42`, `src/run.ts:223`; `--no-cache-key` turns it off). The published Codex probe runs say so in their own record: `"cacheKey": "per-sample"` (`results/logins-long-2026-11-17/CL3-…`). So its 0 on the Codex login is not a missing key, and `client-parity.md` 3.1 already lists "one per sample" for it.
2. **A caller that sends none does not reach OpenAI without one.** Upstream derives a conversation for it and the Codex executor sends that as the key. This is in CLIProxyAPI since v8.0.53 or earlier [ran: `git grep` on the tags], so every measurement of November went through it.

**How upstream derives a conversation.** `session.Enrich` runs on every execution (`sdk/cliproxy/auth/conductor_execution.go:343`, `:524`, `:605`). When the request names no session of its own (no session header of Claude Code, Codex, OpenCode or Pi, no `X-Session-ID`, no `prompt_cache_key`, no conversation id: the list at `sdk/cliproxy/auth/selector.go:2496-2508`), it derives one: a SHA-256 over the request's format, the caller's scope, the leading instructions and the first complete user input (`sdk/cliproxy/session/identity.go:240`, `:327-341`, `:492-530`, `:824-831`). The caller's scope is a SHA-256 of the client API key the request came with (`identity.go:210-217`, `sdk/api/handlers/handlers.go:433-441`).

**Per login, where the vendor's cache hangs on a key:**

| Login | The cache hangs on | The caller names a conversation | The caller names none |
|---|---|---|---|
| Codex | `prompt_cache_key`, and the `Session-Id` header: the Codex CLI's source says "ChatGPT derives cache affinity from the Responses session-id header" (`client-parity.md` 3.1) | the caller's key, unchanged, as both (`internal/runtime/executor/codex_executor_request.go:179-187`, `:241-243`) | **supplied already**: a UUID made from the derived conversation, as both (`:197-203`, `helps/derived_session.go:24-36`). On the websocket path the same (`codex_websockets_request.go:80`) |
| Devin | the session id and the cascade id, the "session-stable prompt cache key" (`client-parity.md` 3.2) | the caller's session, mapped to a UUID | **supplied already**: the same derived conversation, mapped to a UUID (`devin_executor.go:3214-3229`, `:3242-3252`). With nothing to derive from, a new random id per request |
| Meta | `prompt_cache_key`, as far as Muse Code shows: it sends one. Whether the cache needs it is not established | the caller's key, passed on | **nothing**: the body is the caller's, translated, and no key is added (`meta_executor_execute.go:35-110`). The session headers are a new UUID on every request (`:425-433`) |
| Claude | the prefix and its cache marks. There is no key | | nothing to supply |
| Antigravity | a session id, which the executor already derives (`client-parity.md` 3.5) | | not touched: `client-parity.md` section 5 |

**What "a conversation" is for a caller that names none**, and how that stands against what was asked of a supplied key:

| Asked | What upstream's derivation gives |
|---|---|
| One conversation keeps one key | Yes, while the client sends the conversation again from its top, unchanged. A client that rewrites its instructions or drops its first message (a compaction, a date in the system prompt) gets a new key at that request. Its cached prefix changed at the same moment, so nothing is lost by it |
| Two conversations never share one | Two **client keys** never share one: the caller's scope is in the hash. Two conversations of **one** client key share a key when their format, their instructions and their first user input are the same: a script that asks the same first question twice. No gateway can tell these apart without the caller naming them, and what they share is exactly the prefix a cache would serve to both. A tool that fires many such conversations a minute should send its own key |
| A cache field only | Codex: `prompt_cache_key` and `Session-Id`, the two fields the Codex CLI itself uses for cache affinity. Devin: the session id and the cascade id. Never `client_metadata`, an installation id, `thread-id`, `originator`, a `User-Agent`, a version or a device fingerprint |
| It gives nothing away | SHA-256, then a UUID with the provider's name hashed in: neither the client key nor the text can be worked back from it, and the Codex and the Devin value of one conversation differ |
| Never over a caller's own | A caller's key is used wherever it sent one; the derivation runs only for a request without a session of its own |
| A switch | **None exists** [read: none found]. A payload `filter` rule on `prompt_cache_key` is not one: it would take a caller's own key off too |

One more thing upstream does, for the record: a Chat Completions request to Codex with nothing to derive from (no user input at all) gets a key made from the client key alone (`codex_executor_request.go:191-195`), one for all of that key's such requests. A request with a user message never reaches it.

### 16.2 The design

**Codex and Devin: nothing is built in the request path.** What was asked for exists, in upstream's code, under every measurement there is. A second derivation of Provimux's own would sit in a protected path over upstream's, for no measured gain. What Provimux adds is a test that pins the behaviour, so a sync that changes it fails loudly and is looked at: `test/provimux/supplied_cache_key_test.go`, in the folder that already drives the real executors against a stand-in vendor (`upstream_headers_test.go`). It is a layer commit: `test/` is no protected path.

**Meta: one owner patch, and only if the measurement of 16.4 says so.** A request to the Meta login that carries no `prompt_cache_key` gets the UUID of the derived conversation, made as the Codex route makes it, with `meta` as the provider's name.

| | |
|---|---|
| Where | `internal/runtime/executor/meta_executor_execute.go`, in `prepareResponsesRequest`, before the payload rules are applied (`:94`), so that a payload rule still has the last word (upstream's rule for every executor). About five lines, and a helper in a new file |
| What it is made from | upstream's derived conversation (16.1), nothing new: the caller's scope, the format, the instructions, the first user input. One definition of a conversation in the whole gateway: the selector's affinity, the Codex key, the Devin ids and this |
| The field | `prompt_cache_key` in the body, and nothing else. The four session headers stay a new UUID per request, as today: they are Muse Code's identity, and `client-parity.md` 4.1 rules out keeping them through a session |
| The switch | one setting, read at each request, on unless set off, named in `config.example.yaml`. Proposed: `supplied-cache-key` in a `meta:` block, shaped as the Codex route's request settings are under `codex:` (`internal/config/config.go:179`); the name and place are the build's. Off: the request is byte for byte today's |
| What kind of commit | an owner patch below the layer: it changes a request to a vendor and touches a protected path (`guard.sh:33`). It can be offered to CLIProxyAPI, where it is the Codex route's own behaviour carried to one more route |
| Terms | it adds a cache field Muse Code itself sends, and no name, version or device id. `client-parity.md` section 5 stands as it is: Meta's terms forbid the subscription key outside its harness, and this changes that neither way |

```go
// internal/runtime/executor/helps/supplied_cache_key.go (owner patch; a new file)

// SuppliedCacheKey returns the prompt_cache_key a route sends for a request that carries none:
// the provider-scoped UUID of the request's derived conversation, as the Codex route makes it
// (ProviderSessionUUID). It returns "" when the setting is off, or when the request names no
// conversation and none can be derived; the caller then sends no key, as today.
func SuppliedCacheKey(cfg *config.Config, provider string, metadataSets ...map[string]any) string
```

The body is a worker's.

**Claude and Antigravity: nothing.**

### 16.3 What it must never do, and the tests

| Never | The test (a stand-in vendor records what it is sent) |
|---|---|
| Change within one conversation | T1: a request without key or session header, then the same conversation with one more turn: the same key both times; on Codex also the same `Session-Id` |
| Be shared between two client keys | T2: the same request under two client keys: two keys |
| Be shared between two different conversations | T3: two requests of one client key with different first user input: two keys. And the known case, pinned as known: the same instructions and first input twice give one key |
| Override a caller's own key | T4: a request with the caller's `prompt_cache_key`: that value is sent unchanged, and the derived one appears nowhere in the request |
| Appear in an identity field | T5: the request sent for a caller with a key and for one without differ in the cache fields of 16.1 and in nothing else: no other header, no other body field |
| Carry the client key or the text | T6: the value is a UUID, and neither the client key nor any part of the prompt is in it |
| Stay on with the switch off (Meta) | T7: off, the request carries no `prompt_cache_key` and equals today's byte for byte |
| Beat a payload rule (Meta) | T8: a payload rule that sets or filters the field wins over the supplied value |
| Touch Meta's session headers | T9: they are a new UUID on each request, with the key supplied or not |

T1 to T6 are the pinning test for Codex and Devin and run against upstream's code as it is. T7 to T9, and T1 to T6 for Meta, come with the patch.

### 16.4 What must be measured before anything is built

Both are plain-probe runs through Provimux. They are published as `cliproxyapi` and never count in row 1 of 4.2. Each uses a model released in the 31 days before its night (17.1), three samples per age primed in three waves, the control first, the ladder from 2.5 to 30 minutes.

**MK: does a caller without a key lose its cache on the Meta login?** Two runs on `muse-ember-1.0`, started within 15 seconds of each other, the Responses format, a prefix of about 8,000 tokens in both (groundtruth's records have a control of about 2,500 tokens read nothing where about 8,000 did, and size is not what is measured here):

| Run | Sends |
|---|---|
| MK-a | the probe's default: a `prompt_cache_key` per sample |
| MK-b | `--no-cache-key`: the request reaches Meta without a key |

The run records the payload rules in force. The owner's 24-hour rule adds its field to both runs alike.

| Result | It means |
|---|---|
| MK-a's control is warm and MK-b's is cold, also in its second, fresh control | **build it** |
| Both controls are warm, and MK-b is clearly worse: an age differs by more than one check of three, or the totals differ at the 5 % level (the test of `client-parity.md` section 6); and a second night shows the same | **build it** |
| Both controls are warm and the runs agree by that test | **close it**: Meta's cache does not hang on the key, and the patch would change a request for nothing |
| MK-a's control is cold | nothing was measured. Once more with a larger prefix; cold again, close it as not measurable with the probe |

**CX-S: why does the plain probe miss on the Codex login?** `client-parity.md` section 7 planned it on `gpt-6.1-terra`, which leaves the 31 days on 2026-11-26. It runs on `gpt-6.2-sol`, where the probe held nothing (3 of 10 checks). Three runs the same night:

| Run | Request | Against CX-S-a it tells |
|---|---|---|
| CX-S-a | today's: about 2,700 tokens, different from the first token in every sample, the check an identical re-send, a key per sample | |
| CX-S-b | shaped like Pi's: about 4,800 tokens every sample shares, then the sample's block, `low` reasoning, the check adds a turn, a key per sample | whether size and shape are why the probe misses early: the open question of `client-parity.md` section 9 |
| CX-S-c | as CX-S-a with `--no-cache-key`, so the key OpenAI gets is the gateway's derived one | whether the derived key serves as well as a caller's own |

Nothing is built from CX-S either way. If CX-S-c is worse than CX-S-a, that is a fault in upstream's derivation, reported with the runs. If CX-S-b holds where CX-S-a does not, the probe's numbers on every login are read as a small single request's, which is what 4.2 now does anyway.

## 17. The pairs to measure

The brief of the measuring task. It is `client-parity.md` sections 6 and 7 and groundtruth's `docs/own-clients.md` and `docs/own-clients-RUN.md`, narrowed by the owner's word of 2026-11-26: **only models released in the 31 days before the night of the run.**

### 17.1 The 31-day rule, for a script

```
night    the calendar date, Europe/Bucharest, on which the run's first request is sent
entries  GET https://providers.dev/api/v1/entries
             ?provider=claude-subscription,openai-codex,devin,meta-subscription&limit=200
         read on that date before the first request; its meta.dataset_version is recorded with the run
in       entry.release_date is a date  and  0 <= night - entry.release_date <= 31   (whole days)
```

- A model whose entry has no `release_date`, and a model of a login with no entry at providers.dev, is out. Nothing is taken from a listing, from the model's name or from the day it was first seen. This is narrower than the weekly night's rule on purpose (`docs/weekly-night.md`: two calendar months, with three fallbacks for a date); that rule is not changed.
- A pair is in when its entry is in on the night the pair starts. A second night for a pair that disagreed is checked again on that night.
- The plain-probe runs of 16.4 follow the same rule.
- The script prints, per entry, the release date, the days since and the last night it is in, and refuses a run whose model is out, by name.

On 2026-11-26 [ran, dataset `c37885999c67cfaa`]:

| Entry | Released | Days | Last night in |
|---|---|---|---|
| `claude-subscription/claude-fable-5-2` | 2026-11-09 | 17 | 2026-12-10 |
| `openai-codex/gpt-6.2-astra` | 2026-11-17 | 9 | 2026-12-18 |
| `openai-codex/gpt-6.2-sol` | 2026-11-17 | 9 | 2026-12-18 |
| `openai-codex/gpt-6.1-terra` | 2026-10-26 | 31 | **2026-11-26** |
| `devin/gemini-4-neon` | 2026-11-18 | 8 | 2026-12-19 |
| `devin/gpt-6-1-terra` | 2026-10-26 | 31 | **2026-11-26** |
| `meta-subscription/muse-ember-1.0` | 2026-11-09 | 17 | 2026-12-10 |

Every other entry on the four logins is 34 days old or more (`devin/kimi-k3-1-pro`, 2026-10-23, is the nearest). The night waits for the owner's one-time logins, so it will not be 2026-11-26: both Terra pairs are then out by the rule, and five models remain.

### 17.2 What a pair is

One own-client run and one Pi run through Provimux: the same login, the same model variant, started within 15 seconds of each other, the same ages, the same night.

| Held fixed | How |
|---|---|
| The model variant (M-1) | the same id on both sides; a Devin id with its effort suffix is given to Provimux as it is, and each run records the variant that answered |
| Devin's process model and keepalive (M-2) | `--process-model in-process` and `DEVIN_CACHE_KEEPALIVE_MAX_PINGS=0`, both recorded |
| Independence (M-3) | an age's three samples are primed in three waves at least five minutes apart, on both sides |
| What Pi and Provimux were (M-4) | the Provimux build, Pi's version, pi-provimux's commit, the transport Pi used, and the payload rules that touched the request |
| The account that answered (M-6, new) | the Pi run reads `X-CPA-AUTH-KIND`, `-PROVIDER` and `-HOST` of each answer. A priming or a check answered by another account than the pair's login is `check-invalid`. A name several accounts serve is otherwise a pair of two logins |
| The label (M-7, new) | the Pi run is published as `pi+cliproxyapi`, the own-client run under its own client's id, a probe run as `cliproxyapi` (the two rules of 4.2) |
| The schedule | `std`: the ladder from 2.5 minutes to 2 hours, three samples an age, the control first. `std+65` for Claude, so that the ages around one hour are there. A `long` run only for a model warm at 2 hours on both sides, on another night |
| One request per check; no check sent twice | groundtruth `docs/own-clients.md` section 1, rules 1 and 2 |

**When the two runs agree** (`client-parity.md` section 6, decided before the runs): over the ages both have, no age differs by more than one check of three, and the totals do not differ at the 5 % level by Fisher's exact test. A pair that disagrees is run once more on another night before anything is concluded.

**What a pair feeds.** The own-client run gives providers.dev's safe value of the login. The Pi run gives row 1 of 4.2. Both come from one night, so the two values beside each other in the answer are for once of the same date.

**Which gateway.** The Pi side needs a Provimux that holds the logins. Recommended: the owner's running one, on his word, at an hour he is not using those models, with M-4 recording what it was; that is how the Pi runs of November were made. The other way is a second gateway with logins of its own, apart from his traffic and pinned to a release, for four more one-time logins. Decision 14.

### 17.3 The pairs

| Id | Login | Model, released | Own client (groundtruth's run) | Pi through Provimux | Settles |
|---|---|---|---|---|---|
| CX-B | codex | `gpt-6.2-sol`, 2026-11-17 | `codex-cli` (CX1), `std` | the same model, the Codex login | the owner's question on the newest Sol: the Codex CLI's 150 s against what a client through Provimux gets. Today no real client has measured it |
| CX-A | codex | `gpt-6.2-astra`, 2026-11-17 | `codex-cli` (CX2), `std` | the same model | the same on Astra: 1,200 s by the Codex CLI |
| CX-T | codex | `gpt-6.1-terra`, 2026-10-26 | `codex-cli` (CX4), `std` | the same model | the 1,500 against 900 of today. **Only on a night up to 2026-11-26** |
| CC-F | claude | `claude-fable-5-2`, 2026-11-09 | `claude-code` (CC1), `std+65` | the same model through `/v1/messages` | that both ask for one hour inside the plan; the first real-client and the first own-client measurement of the Claude login; what the owner decides P-1 on |
| MC-E | meta | `muse-ember-1.0`, 2026-11-09 | `muse-code` (MC2), `std` | the same model, the payload rules as configured and recorded | whether Muse Code still caches no conversation, and what the 300 s of today is worth |
| DV-N | devin | `gemini-4-neon`, 2026-11-18 | `devin-cli` (DV18), `std`, the lowest level the model has | the same variant | whether the Devin CLI's 0 of 2026-11-24 is the model or the instrument. **Only if the smoke run shows a cached token through the CLI** |
| DV-T | devin | `gpt-6-1-terra`, 2026-10-26 | `devin-cli` (DV5), `std` | the same variant | `client-parity.md`'s DV-6. **Only on a night up to 2026-11-26, and only if the smoke run shows a cached token** |

The plain-probe runs of 16.4, the same night as their login's pairs: CX-S-a, -b and -c on `gpt-6.2-sol`; MK-a and MK-b on `muse-ember-1.0`.

Not run: an Antigravity pair (`client-parity.md` section 5); a Claude pair in overage (it needs the account in overage, which costs the owner money; the Claude runs start when the plan's meter is low and stop at the first five-minute cache write, as groundtruth's Claude runs do); any model outside the 31 days, including every row of `client-parity.md` section 7 not named above.

**The order.** CX-S and MK first: they are small, and CX-S says whether size has to be read into every pair. Then CX-B, the pair the question came from.

### 17.4 What has to exist before the night

| | |
|---|---|
| The owner's one-time logins in the state folders | Claude Code, Codex and Devin, and the Muse key in the Keychain (`own-clients-RUN.md`, "You, once") |
| Pi in the night's commands | the first night took the Pi client out of `harness/clients.py`; a pair needs it back, with its state folder, M-4's record, M-6's check, and three waves on its side too |
| Claude Code and Muse Code in the night's commands | the first night ran Codex and Devin only; the drivers exist and are tested, the night's commands and the Claude overage stop are not in |
| The 31-day script of 17.1 | and the ids in `harness/model-ids.json` |
| The owner's word on the gateway | decision 14 |

### 17.5 The cost

Nothing here is billed per token: all four are subscriptions. A run costs allowance, and money only if the Claude login is in overage.

| Run | Requests | Input tokens |
|---|---|---|
| An own-client `std` or `std+65` run | 93 | about 2.7 million, about two thirds read from cache while warm (groundtruth `docs/own-clients.md` 2.2) |
| A Pi run beside it | 93 | 1.1 to 1.6 million, 12,000 to 17,000 a request (`client-parity.md` section 7) |
| A plain-probe run to 30 minutes | about 50 | about 0.13 million at 2,500 tokens; 0.4 to 0.6 million for the two shaped or sized larger |

What each would be worth at providers.dev's reference prices of 2026-11-26, with a third of the input written at the cache-write price of the lifetime the route gets and two thirds read (output is a few tokens a request):

| Pair or run | Prices per million: input, read, write | The own-client run | The Pi run | Together |
|---|---|---|---|---|
| CC-F, Fable 5.2 | 8, 0.20, 16 (1 h) | $14.80 | $6.10 to $8.70 | $21 to $23 |
| CX-A, GPT-6.2 Astra | 8, 0.40, 10 | $9.70 | $4.00 to $5.70 | $14 to $15 |
| DV-N, Gemini 4 Neon | 3, 0.15, none stated: input | $3.00 | $1.20 to $1.75 | $4.20 to $4.70 |
| CX-B, GPT-6.2 Sol | 2, 0.10, 2.50 | $2.40 | $1.00 to $1.40 | $3.40 to $3.90 |
| CX-T, GPT-6.1 Terra | 1, 0.05, 1.25 | $1.20 | $0.50 to $0.70 | $1.70 to $1.90 |
| DV-T, GPT-6.1 Terra | the same | $1.20 | $0.50 to $0.70 | $1.70 to $1.90 |
| MC-E, Muse Ember | 0.10, 0.004, none stated: input | $0.10 | $0.05 | $0.15 |
| CX-S, three probe runs on Sol | | | | about $1.20 |
| MK, two probe runs on Ember | | | | about $0.10 |

| | Billed | At list price | Of the allowance, in input tokens |
|---|---|---|---|
| Everything above | $0 expected | $47 to $53 | Claude 3.8 to 4.3 million; Codex 12 to 14 million; Devin 7.6 to 8.6 million; Meta about 5 million |
| The same from 2026-11-27, without the two Terra pairs | $0 expected | $44 to $49 | Codex 8.5 to 9.5 million; Devin 3.8 to 4.3 million |

**Against the owner's caps for measuring, 2 dollars a run and 10 dollars a week.** In groundtruth these are the ledger's caps for the API keys, in billed dollars (`harness/ledger.py`, `docs/weekly-night.md`, "The caps"); a login is held by another cap, six cache runs a login a night.

- **Read as billed dollars** (recommended, decision 13), the whole list fits: nothing is billed. The one way to a bill is the Claude login in overage. There Provimux asks one hour whatever the state, so the Pi run alone would be $6 to $9 of billed writes: the Claude pair starts only with the plan's meter low, and both of its runs stop when Claude Code's side reports its first five-minute write.
- **Read as list-price value**, a run fits 2 dollars only on Terra, on Ember and for the probe runs, and a week's 10 dollars hold the two Terra pairs, the Ember pair, the probe runs and Sol's Pi run, but not Sol's own-client run ($2.40).
- **Six runs a login a night**: Codex has six pair runs with all three pairs, or four from 2026-11-27, and the three CX-S runs on top. The probe runs are small beside a pair run (0.13 to 0.6 million tokens against 2.7); counting them apart, like the smoke run, is the owner's word, as is a seventh run on one login.

### 17.6 What to drop first

If the night must be smaller, in this order, the first dropped first:

1. **DV-T.** Out by the rule from 2026-11-27, and conditional on a smoke run the Devin CLI failed for this model in November.
2. **CX-T.** Out by the rule from 2026-11-27. An older Codex model, and the row `client-parity.md` already called chance.
3. **DV-N.** The Devin CLI held no age on this model two days ago; the pair may measure the instrument.
4. **CX-A.** The dearest Codex model. CX-B answers the same question on the same login for a quarter of it.
5. **CC-F.** The dearest of all and the one with a way to a bill. Dropping it leaves P-1 undecided and the Claude login's lifetime resting on the plain probe (decision 1b).

Never dropped, because the rest hangs on them: CX-B, MC-E, CX-S and MK. Together they are about $5 at list price and about 10 million input tokens, half on the Codex allowance and half on Meta's.
