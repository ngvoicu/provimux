# Prices and cache lifetimes from providers.dev, given to tools

What the code does. Provimux holds providers.dev's published figures for the entries of a fixed set of providers: prices, cache prices, the cache lifetime to rely on, the input window and the output limit, each with its date and source. It fetches them in the background, in the round that fetches providers.dev's model listings, and keeps a disk copy; nothing a client asks for waits for it. Every answer names its own entry (`answered-by.md`), and `GET /v1/models?include=figures` gives the figures of the entries the caller's own answers named, so a tool knows what the account that answered costs and how long its cache lives without knowing any provider. The operator reads the same figures at `GET /v8/management/provimux/figures`. `models.published-lists: false` turns the request and the figures off.

The contract is `prices-and-cache/openapi.yaml`, contract 1, with three example files beside it. Section 4 says what is held, section 5 the contract, section 6 how an answer's entry is found and how the figures are fetched and kept, section 7 the known limits.

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

**Why the probe is not a client**. Its prompt is about 2,700 tokens and shares nothing with any other sample from its first token, where a client's request begins with thousands of tokens every session shares. Its check is the identical request sent again, where a client's check is the conversation with one more turn. On the Codex login it missed within minutes while its control, sent again seconds later, hit; Pi through the same gateway and the same executor held 15 to 30 minutes.

**What tells the two apart in providers.dev's API today**:

| Field | A real client through Provimux | Plain test requests through Provimux | Tells them apart |
|---|---|---|---|
| `client` | `agent+cliproxyapi` | `cliproxyapi` | **yes**, and it is the only field that does |
| `client_label` | "A coding agent, through Provimux" | "Requests sent through Provimux" | words for a reader, not to be parsed |
| `client_kind` | `proxy` | `proxy` | no |
| `route`, on an evaluation only | the access method: `codex-subscription`, `devin`, `meta-subscription` | the same | no: it says whose account, not who sent |

- On an entry summary the fields are on each item of `cache_lifetime.lifetimes`: `client`, `client_label`, `client_kind`, `chosen`. An item has no `route`.

Provimux keeps the client ids that count as a constant with one member, `agent+cliproxyapi`.

- **0 is a value** where a real client measured it and no age held on every check. It is not "does not cache".
- Where no own client and no direct request has measured a login, providers.dev's safe value can itself be the plain probe's through Provimux; the answer says so (`safe.measured_with` names `cliproxyapi`).
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
| An entry worked out before a request is sent | upstream decides the host and the model inside the executor, per request (6.1) |
| What an account last answered as, as what it will answer as | `answered` is a record with a day; the console and Pi say "last answered as" |

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
2. Per answer: read `X-Provimux-Entry` (on the Responses WebSocket the field `provimux_entry` of the turn's `response.created`). `figures.entries[that value]` is what the answer cost and how long its cache lives. Nothing is matched by kind, provider or host.
3. Where that value is no key of the copy it holds, it reads the list once more. An account's first answer for a name puts its entry into the row and so into `figures.entries`; a second miss means providers.dev has no such entry.
4. `X-Provimux-Account` is the marker of the account that answered: the `account` of its row. `X-Provimux-Model` and `X-Provimux-Model-Basis` say which model answered and whether the vendor said so.
5. No figures: the entry is no key of `entries`, or the answer names no entry, or it names nobody at all. A tool guesses in none of the three (`answered-by.md` section 4).

**Before an answer.** No row has an `entry`: the gateway does not know what a request will be sent as until it sends it (6.1). A row has `answered`: the entries this account's answers for this name named, newest first, each with the day of the last answer that named it, and `null` for answers that named none. It is a record of the answers to the caller's own key, kept 90 days (`answered-by.md`, "The record").

| `answered` | Means | A tool before its first answer |
|---|---|---|
| one item with an entry | every recorded answer of this account for this name was that entry | may show its figures and price by them, as "last answered as", and takes the answer's own entry when there is one |
| several items | this account's answers for this name differ: a name on several models, a variant chosen by the caller's effort, a backend chosen by the client, a rule of the operator | has no one figure. It may show them all |
| one item, `entry` null | its answers named no entry | has no figures, and expects none |
| empty | no answer of this account for this name is recorded | has no figures yet. The account's first answer brings them |

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
| The record | once an answer is written to its caller, the newest item of `answered` on that caller's row of its account under the name asked for is that answer's entry, or `null` where it named none. An item is never an entry no answer of that account for that name carried. Nothing in a row says what the next answer will be |
| The marker | the same on every answer of one account and in every list while the gateway keeps its key; never another account's; compared, not parsed, except for its first character (`answered-by.md` 2.3) |
| The rule of `rely_on_s` | section 4.2 |
| Growth | keys and enum values may be added; a tool ignores what it does not know and treats an unknown enum value as unknown |
| A change that breaks any of this | is served as `"contract": 2` under a new include value, with contract 1 still answered for at least two Provimux releases that say so in their notes |

**What we do not promise.** The values: they are providers.dev's, dated, and change when its dataset does. That an answer has an entry. That the next answer of an account is the entry its row shows. That the account listed first is the one that answers. Which measurement clients providers.dev has. That `figures.state` is `held`: with the switch off or nothing fetched yet, `entries` is empty and the rows' `answered` are still there.

With `models.answered-by: false` there is no statement on an answer, so nothing leads from an answer to a figure: no row has `answered`, and `entries` is empty.

A client that sends no `include` gets nothing new, and no client breaks: pi-provimux keeps only the account fields it knows.

## 6. Holding the figures

### 6.1 Which entry an answer is

An entry is a fact of one answer, never of an account.

`entry` is `<providers.dev provider>/<model id>`, providers.dev's own form of a reference (the provider never contains a slash).

- **It is worked out in one place, when an answer is stated** (`answered-by.md`, "Where an entry is named"), from the request the gateway sent for that answer. Two facts are read from that request as the transport was handed it (`answered-by.md` 4.2): the **host it was sent to** and the **model id in it**. Two are the credential's: the gateway's provider of it (which executor sent) and its kind. The account's configured base URL, the registry, the model the usage reporter was told and anything providers.dev sent are not read.
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
| A Codex login's request to `api.openai.com` (the second sign-in, for a client that is not Codex's own) | neither the ChatGPT backend providers.dev measures as `openai-codex` nor the paid API: a subscription's token on the API's host ; the host says it |
| A Codex key's request to `chatgpt.com` (a key without a base URL) | not the paid API |
| An xAI request to `cli-chat-proxy.grok.com`, or to any host but `api.x.ai`; any xAI login | providers.dev's `xai` is the paid API |
| A Meta key | on `api.meta.ai` it is either the paid API or the key a Muse Code subscription applies to; neither the request nor the account says which |
| A Kimi login | its host decides between two plans, and the model lists' row for it is an inference |
| AI Studio, a Gemini login, the Gemini CLI login, a Gemini Interactions key on Google's host | not the paid API, or not measured as an entry |
| A login's request to any host but its own backend's; any request to a host this table does not name | not known, and never guessed from the model's name |

**The three kinds of no entry.** An answer names no entry when:

1. **The map has no row** for what was sent: the tables above.
2. **The gateway could not be certain what it sent** (`answered-by.md`, "Where an entry is named"): the request left over an upstream WebSocket (Codex or xAI with `websockets` on) or through AI Studio's browser; its body is in a form the reader does not know (a multipart upload, an encoding other than zstd); it carried no model, or one in its URL and another in its body; or more than one request of the attempt could have produced the answer.
3. **A plugin took part**: a plugin made or changed the credential (a plugin can set an account's provider and kind to anything), or a plugin's executor was put in place for the provider since the gateway started.

A model a plugin lists is no longer a kind of its own. Where a plugin's executor answers, the answer names nobody at all (`answered-by.md` 5.3). Where the account's own executor sends the model to the account's own host with a credential the gateway made, the request is what it is, whoever put the name on the list.

A second sign-in, a thinking group and a pool need no rule of their own: the table decides by the host, the variant sent and the model sent.

### 6.2 The request

`GET /api/v1/entries` returns entry summaries, filtered by `provider`, at most 200 a page; a summary carries everything 4.1 reads. **The site's API needs no addition.**

```
GET https://providers.dev/api/v1/entries?provider=<the fixed set>&limit=200&offset=<0, 200, …>
User-Agent: provimux/<version>
If-None-Match: <the page's ETag, when one is held>
```

- **The fixed set** is a constant: `anthropic`, `antigravity`, `claude-subscription`, `devin`, `github-copilot`, `google`, `google-vertex`, `meta-subscription`, `openai`, `openai-codex`, `openrouter`, `xai`. It does not depend on the accounts a user has, so the request is the same for every user and tells the server nothing about him (the rule of `model-lists.md` section 7).
- **Size**: the set without `openrouter` and with one Kimi provider is 190 entries in one page, 51 KB gzip, 1.53 MB decoded; `openrouter` is 400 entries in two pages, 68 KB gzip. So about 580 entries, three pages, about 120 KB on a day the dataset changed; with `If-None-Match` a `304` with no body. What Provimux keeps of 190 entries is about 150 KB as JSON.
- **When:** in the round that fetches the listings (`sdk/cliproxy/provimux_published_lists.go:37-44`): at start in the background, every 6 hours, on `POST /v8/management/models/refresh`; after a failure 10 minutes, then 1 hour, each counted from the start of the failed fetch. Pages one after another until `next_offset` is null, at most 10. A redirect is followed only to the same host over `https`.
- **A fetched document replaces the held one only if** every page is a `200` or a `304`, well-formed, at most 8 MB, all pages name one `dataset_version`, together they hold `total` entries, at least one of them can be read, and `generated_at` is not before the held document's and not more than 24 hours ahead of the gateway's clock. Anything else is a failed fetch: the held document stays, and the failure is recorded as a code. An entry Provimux cannot read is left out alone, and so is a value only a broken summary carries.
- **Disk:** `model-lists/figures.json` beside `published.json`: the kept records, the dataset version, the dates and each page's `ETag`. Mode `0600`, temporary file and rename (`internal/provimux/modellists/published.go:387`). A file that does not parse, is of another schema, whose pages and entries disagree, or that was generated more than 24 hours ahead of the gateway's clock is ignored with one warning and replaced by the next accepted document. The file is the gateway's own: its entries are not read again one by one. Leftovers of an interrupted write are removed when the copy is read.
- **Never blocking:** no request is awaited at start or on any path that serves a client. The disk copy is read at the start of the first round with the switch on, before any request of that round, the listings' included; a start without network gives the figures of the last run, with their dates.
- **Unreachable:** the held document stays, however old; its dates say so. With nothing held, `state` is `none` and there are no figures.
- **The switch:** `models.published-lists: false` already means "Provimux never contacts providers.dev" (`internal/registry/catalog_config.go:21-24`). It turns this request off too: `state` is `off`, no figure is given, the disk copy is not read. A fetch under way when it is turned off asks its remaining pages and is not applied. Off in a test binary and in Home mode, as the listings are.
- **Through the configured proxy,** within 30 seconds a page.

No model list, no route and no request to a vendor depends on any of it. No executor, translator or auth file changes.

### 6.3 Status for the operator

`GET /v8/management/provimux/figures`, read-only, behind the management key: the same `figures` object as the tools get, with `last_failure` (`{at, category, http_status}`, codes only), `from_cache` and the count of held entries; `entries` holds those an `answered` item of a route in the answer of `model-routes` names, every held entry with `?all=true`. So "serve only my names" is applied once, where `model-routes` applies it (`provimuxGated`, `provimux_model_routes.go:146`, `:172`), and a name that is not served brings no entry. `GET /v8/management/provimux/model-routes` gains `answered` on each route (`internal/api/handlers/management/provimux_model_routes.go:72-82`): the same record as the client list's, with the time of each item where the client list gives the day (`pui13/openapi.yaml`, `RouteAnswered`). No route carries a predicted entry. An older Provimux answers `404` on the new path: the console shows no figures.

Log: `prices and cache lifetimes from providers.dev: dataset 89105175… of 2026-11-25, 584 entries`, and the listings' failure line under this name.

## 7. Known limits

- A disk copy edited by hand so that it still parses shows its values until the next accepted fetch (six hours at most).
- The six hours and the back-off are counted from the start of the figures' fetch, so they move by the seconds the listings' request of the same round takes.
- A fetch under way when the switch is turned off asks its remaining pages (at most ten) and is not applied.
- A gateway whose clock is more than 24 hours behind holds no figures: every dataset is dated ahead of it.
- The limits of the entry and of `answered` are those of `answered-by.md`.
