# Model lists: live from the vendors, else from providers.dev, never from router-for-me

Design, 2026-11-22. This is the design only: no code exists yet. Section 9 was added the same day for the owner's later decisions: groundtruth discovers every login's models daily and measures every Monday night, unattended.

**The owner's decision (2026-11-22).** Provimux takes no model list from router-for-me, and nothing of router-for-me's model lists stays in it. A login's list comes first from its vendor, through the login Provimux already holds; else from providers.dev; else from a list built into the release from our own sources. The plugin store stays as it is.

**Read from.**

| Project | Commit |
|---|---|
| Provimux | `provimux/main` at `e5cf5a2d5` (CLIProxyAPI `v8.1.0` + 146 commits, last tag `v8.1.0-pmx.1`) |
| providers.dev | `main` at `73f87b4`; branch `sources-side-by-side` at `36ef758` |
| groundtruth | `main` at `ffdc85d`; branch `own-clients` at `60e65ba` |

Every `file:line` of Provimux below is in that commit.

**Tags.** **[read]** in code. **[ran]** executed by me. **[docs]** in a vendor's public documentation or its own open-source client, read on 2026-11-22. **[infer]** reasoned, not verified. I sent no request with any login and never contacted port 8317.

## 1. The design in one page

A login's list answers two questions, and the design answers them apart.

1. **Which models does this login offer?** One source answers, whole, never mixed:
   - the login's own **live** list, when one is held;
   - else the **published** list of its login type, from providers.dev;
   - else the **built-in** list, a copy of the published lists taken when the release was built.
2. **What are a model's fields** (window, output limit, thinking levels and budgets, modalities, aliases)? Per field, the first of the same three sources that states it.

| Piece | Where | What it does |
|---|---|---|
| Live lists | Provimux, the owner's existing `models.live-lists` patch, widened | Asks each login's vendor for its list. Today: Claude, Codex, Meta. Added: Devin, Kimi, xAI, Gemini keys. Copilot and Antigravity already ask (upstream code) and now also add models nobody has described yet |
| Published lists | providers.dev, new resource `/api/v1/listings` | One list per provider and access method, with each value's source |
| Discovery | groundtruth, daily, new file `listed.json` | The owner's Provimux hands over what each vendor's list said; providers.dev imports it daily and lists every offered model, measured or not |
| The Monday night | groundtruth, weekly, unattended | Measures what is new or in doubt, inside a cap of runs per login and a cap of money (section 9) |
| Built-in lists | Provimux release workflow, job `catalogs` | Fetches providers.dev's listings once per release instead of `router-for-me/models` |
| Removed | Provimux | The three fetchers' URLs, the three embedded files, the release's fetch of `router-for-me/models` |

Three findings shaped it.

- **Much of "live" already exists.** Upstream's own tools `cmd/fetch_{devin,antigravity,copilot,codex}_models` build router-for-me's lists from the vendors' live answers [read]. The owner's patch asks Claude, Codex and Meta. Upstream's runtime asks Copilot and Antigravity. What is missing is Devin, Kimi, xAI and Gemini, and that a model only the vendor knows becomes visible.
- **144 of upstream's tests need the content of router-for-me's lists** [ran, section 7]. So the three files cannot simply leave the source tree: they stay as test fixtures and are never compiled into a binary.
- **Two rules of providers.dev forbid part of this** (section 4.5). They need the owner's word before providers.dev is built.

## 2. Today

### 2.1 What router-for-me feeds

| What | Where | Evidence |
|---|---|---|
| `models.json`, fetched at start and every 3 h | `internal/registry/model_updater.go:19-22`, cadence `:17` | [read] |
| `devin_models.json`, same | `internal/registry/devin_models_updater.go:11-14` | [read] |
| `codex_client_models.json`, same | `internal/registry/codex_client_models_updater.go:11-14` | [read] |
| The three files embedded as the fallback | `model_updater.go:24-25`, `devin_models.go:16-17`, `codex_client_models.go:15-16`; files in `internal/registry/models/` (160 KB, 25 KB, 1 MB) | [read] |
| A fourth list, in Go: 11 Devin models | `staticDevinModels`, `internal/registry/model_definitions.go:102` | [read] |
| **Every release fetches `router-for-me/models` and bakes it in** | `.github/workflows/provimux-release.yml:89-171` (job `catalogs`), checked in each build `:225-240`, named in the release notes `:841` | [read] |

The last row means an installed Provimux carries router-for-me's lists of its release day, not the files in the repository.

**On disk.** Provimux writes no copy of these files to disk. A fetched catalog replaces a value in memory (`catalog_sources.go:336-338`), and `internal/registry` has no file write outside tests [ran: grep]. Section 6.6 says where copies do exist and how an upgrade removes them.

### 2.2 What `models.json` holds per login type

Counts and fields of the embedded file [ran: a script over it].

| Section | Models | Thinking fields used |
|---|---|---|
| `claude` | 20 | `levels`, `min`, `max`, `zero_allowed`, `dynamic_allowed`, `disable_type` |
| `codex-free` / `-team` / `-plus` / `-pro` | 6 / 12 / 12 / 12 | `levels` |
| `copilot` | 30 | `levels`, `min`, `max`, `zero_allowed` |
| `gemini`, `vertex`, `aistudio` | 15, 24, 19 | `min`, `max`, `dynamic_allowed`, `zero_allowed`, `levels` |
| `antigravity` | 15 | all five |
| `kimi`, `xai`, `meta` | 12, 18, 10 | `levels`, `zero_allowed` |
| `devin` (own file) | 52 | `levels` |

Every model also has an id, a display name, an owner, a context window, an output limit and input and output modalities (`ModelInfo`, `internal/registry/model_registry.go:40-120`). Some carry `native_capabilities.web_search` (117 models), and a few Codex models carry three router settings: `supported_parameters`, `support_configuration_update` and `config.override_header`.

The file's `gemini-cli` section (7 models) is read by nothing: the struct has no such field (`model_definitions.go:30-45`) [read].

### 2.3 What already asks a vendor

| Login | Who asks | What happens to a model the catalog lacks |
|---|---|---|
| Claude, Codex, Meta | the owner's patch, `sdk/cliproxy/live_model_lists.go`, off unless `models.live-lists: true` (`catalog_config.go:15-17`) | Claude and Codex: added from the vendor's description (`:246-261`). Meta: nothing, its list has ids only (`:582-583`) |
| Copilot | upstream, every 2 h (`sdk/cliproxy/copilot_catalogs.go:254`, `internal/auth/copilot/catalog.go:24`) | **Dropped**: only ids in the `copilot` section are offered (`catalog.go:155-165`) |
| Antigravity | upstream (`sdk/cliproxy/antigravity_models.go:642`) | **Dropped**: the catalog is filtered by the vendor's ids (`:773-783`) |
| Devin, Kimi, xAI, Gemini, Vertex, AI Studio | nobody | The catalog is the whole list |

## 3. Per login type: does the vendor offer a list?

"Needs" are the fields of section 2.2. "From the published list" means the vendor's answer lacks the field and providers.dev carries it (section 4.4).

| Login | Request | The answer has | The answer lacks | Verified |
|---|---|---|---|---|
| **Claude** login | `GET https://api.anthropic.com/v1/models?limit=1000` | id, name, date, `max_input_tokens`, `max_tokens`, per effort level `supported`, thinking types (`adaptive`, `between_tools`, `disabled`, `enabled`), image input, `server_tools.web_search` | thinking budgets in tokens, aliases | Fields [docs]. Built and released in the patch (`live_model_lists.go:416-431`). The docs show an API key; that a login's token is accepted is the patch's own claim [infer] |
| **Codex** login | `GET https://chatgpt.com/backend-api/codex/models?client_version=99.0.0` | slug, name, description, window, input modalities, reasoning levels, visibility, `available_in_plans`, and the whole Codex client record | an output limit | Built in the patch (`:432-449`). OpenAI's own CLI makes this call [docs: `openai/codex`, `codex-rs/codex-api/src/endpoint/models.rs`] |
| **Meta** login | `GET https://api.meta.ai/v1/models` | ids | everything else | Built in the patch (`:450-455`). No public page found |
| **Copilot** | `GET <account's API host>/models` | id, name, windows (context, prompt, output), vision, thinking budgets, reasoning efforts, adaptive mode, endpoints, policy, picker flag | nothing Provimux needs | Upstream's runtime (`internal/auth/copilot/catalog.go:56-84`). No official page; Microsoft's own client defines the same fields [docs: `microsoft/vscode-copilot-chat`, `endpointProvider.ts`] |
| **Antigravity** | `POST <cloudcode host>/v1internal:fetchAvailableModels` with the project | per id: `displayName`, `maxTokens`, `maxOutputTokens`; `webSearchModelIds` | thinking levels and budgets, modalities | Request: upstream's runtime (`sdk/auth/antigravity_models.go:26`, `:88-125`). Fields: upstream's tool (`cmd/fetch_antigravity_models/main.go:300-320`). The runtime keeps the ids only (`sdk/auth/antigravity_models.go:258-274`). No public page |
| **Devin** | `POST https://server.codeium.com/exa.api_server_pb.ApiServerService/GetCliModelConfigs`, protobuf | per variant: label, multimodal flag, vendor, context window, output limit, and the id, whose suffix names the effort (`-low`, `-high`, `-max-fast`, …) | nothing Provimux needs | Upstream's tool only (`cmd/fetch_devin_models/main.go:46`, `:222-268`, fields `:300-377`). The executor already knows the path (`internal/runtime/executor/devin_executor.go:110`). The registry already joins variants into one model with levels (`devin_models.go:341`). No public page. Not run |
| **Kimi** login | `GET https://api.kimi.com/coding/v1/models` | id, `display_name`, `context_length`, `supports_reasoning`, `supports_image_in`, `supports_video_in` | an output limit; reasoning levels (a yes or no only) | Provimux calls nothing today. Kimi's own CLI makes this call and reads these fields [docs: `MoonshotAI/kimi-cli`, `src/kimi_cli/auth/platforms.py`]. Provimux's base is `https://api.kimi.com/coding` (`internal/auth/kimi/kimi.go:48`). Not run |
| **xAI** | `GET https://api.x.ai/v1/models` and `/v1/language-models` | id, aliases, `context_length`, reasoning efforts, input and output modalities | an output limit | Fields [docs: docs.x.ai]. The login's requests go to `cli-chat-proxy.grok.com` (`internal/auth/xai/types.go:13`); its scope includes `api:access` (`:21`), so `api.x.ai` may accept its token [infer]. **Not verified for the login** |
| **Gemini** API key | `GET https://generativelanguage.googleapis.com/v1beta/models?pageSize=1000`, pages until no `nextPageToken` | id, name, description, `inputTokenLimit`, `outputTokenLimit`, methods, `thinking` (a yes or no) | thinking budgets and levels, modalities | Fields [docs: ai.google.dev]. Provimux's base is the same host (`internal/runtime/executor/gemini_executor.go:33`) |
| **Vertex** | `GET https://aiplatform.googleapis.com/v1beta1/publishers/google/models` | ids, versions, launch stage | every limit; and it exists in the beta API only | [docs: the discovery documents of `v1` and `v1beta1`] |
| **AI Studio** | none | | | Requests go through a websocket to a browser (`internal/runtime/executor/aistudio_executor.go:1-3`); there is no list call [read] |

**So:** nine login types get a live list (Claude, Codex, Meta, Copilot, Antigravity, Devin, Kimi, xAI, Gemini keys). Vertex and AI Studio take the published list. I recommend not using Vertex's beta list: it states no limit, so it could only hide models.

**API keys.** The patch gives API keys no live list (`live_model_lists.go:203-205`). This design keeps that, with one exception: a Gemini key, where the key is the only credential. Keys of Claude, Codex and xAI use the published list of the vendor's API provider. A key with its own `base-url` is never asked: Provimux sends no list request to a host the user configured for something else.

## 4. providers.dev's part

### 4.1 A new resource: the listing

A **listing** is the model list of one provider and access method: what the vendor's own list said, read on a date through a named client, completed by sourced statements. It belongs to a provider that already exists in the catalog. It is not a set of entries: an entry stays "a model evaluated or imported", and a listed model need not have one.

| Provimux login | Listing (provider id) | Provider exists today | The listing's models come from |
|---|---|---|---|
| Claude login | `claude-subscription` | yes, authored | discovery |
| Codex login | `openai-codex` | yes, models.dev (2 models) | discovery, else models.dev |
| Copilot | `github-copilot` | yes, models.dev (44 models) | discovery, else models.dev |
| Antigravity | `antigravity` | yes, authored | discovery |
| Devin | `devin` | yes, authored | discovery |
| Meta login | `meta-subscription` | yes, authored | discovery (ids), fields authored |
| Kimi login | `kimi-code-plan-global` or `-cn` | yes, models.dev | discovery, else models.dev |
| xAI | `xai` | yes, models.dev | models.dev; discovery if the login's list differs |
| Gemini key, AI Studio | `google` | yes, models.dev | models.dev |
| Vertex | `google-vertex` | yes, models.dev | models.dev |
| Claude key, Codex key | `anthropic`, `openai` | yes, models.dev | models.dev |

Provider ids are from `data/upstream/providers/` and `data/authored/providers/` [read]. Which Kimi host is "global" is [infer]. The map from login to listing lives in Provimux, not in providers.dev: the API knows providers, not Provimux.

**Sources of a listing, in one fixed order**, the same order `sources-side-by-side` uses for a value:

1. `listed`: groundtruth's discovery (section 4.3).
2. `providers.dev`: an authored list or field, with `reason` and `evidence` as every authored statement has.
3. `models.dev`: the imported models of the same provider.
4. `pi.dev`: pi's models of the same provider.

The **models** of a listing are taken whole from the first source that has a list for the provider, as a price is taken whole from one source. A **field** of a model is taken from the first source that states it, and the row names that source.

### 4.2 The API shape

Two paths. Both are additions, so `/api/v1` only grows (AGENTS.md:53-57).

| Path | Returns |
|---|---|
| `GET /api/v1/listings` | a page of `Listing`; `models` is `null` unless `view=full` (the rule `/providers/{provider}` already has) |
| `GET /api/v1/listings/{provider}` | one `Listing` with its `models` |

Provimux makes one request, `GET /api/v1/listings?view=full&limit=<max>`, and gets every listing. Every key below is always present: `null` for unknown, `[]` for none (AGENTS.md:48-52).

```json
{
  "provider": "devin",
  "basis": "listed",
  "listed_at": "2026-11-22",
  "client": "provimux",
  "evidence": [{ "kind": "listed", "url": "https://github.com/ngvoicu/groundtruth/blob/main/listed.json", "date": "2026-11-22", "note": null }],
  "model_count": 53,
  "models": [
    {
      "id": "gemini-4-neon",
      "name": "Gemini 4 Neon",
      "owner": "google",
      "entry": null,
      "canonical": null,
      "released": null,
      "context_window": 1048576,
      "max_input": null,
      "max_output": 65536,
      "input_modalities": ["text", "image"],
      "output_modalities": ["text"],
      "reasoning": {
        "levels": ["low", "medium", "high"],
        "budget_min": null,
        "budget_max": null,
        "can_disable": null,
        "dynamic": null,
        "disable_with": null
      },
      "web_search": null,
      "aliases": [],
      "variants": ["gemini-4-neon-low", "gemini-4-neon-medium", "gemini-4-neon-high"],
      "plans": [],
      "hidden": false,
      "sources": {
        "context_window": "listed", "max_input": null, "max_output": "listed",
        "input_modalities": "listed", "output_modalities": "listed", "reasoning": "listed",
        "web_search": null, "aliases": null, "variants": "listed", "plans": null
      }
    }
  ]
}
```

The figures in the example are placeholders.

| Field | Meaning |
|---|---|
| `id` | the id a request names on this access method |
| `entry` | `{provider, model}` of the catalog entry for this model on this provider, or `null`: listed, not evaluated |
| `reasoning` | `null` when no source says anything. `can_disable`: thinking can be turned off. `dynamic`: the model may choose its own budget. `disable_with`: the vendor's word for "off" where it is not the usual one |
| `web_search` | the access method runs the vendor's own web search for this model |
| `variants` | the vendor's own ids where it has one id per reasoning level (Devin); `[]` where `id` itself is what a request names |
| `plans` | plan names the vendor's list itself attaches to the model (Codex: `available_in_plans`); `[]` when the list names none |
| `hidden` | the vendor's list marks the model as not shown in pickers |
| `sources` | per field, the source that gave it: `listed`, `providers.dev`, `models.dev`, `pi.dev`, or `null` |
| `basis`, `listed_at`, `client` | of the listing: the source of its models, the date that source was read, and for `listed` the client that read it |

New components: `Listing`, `ListingModel`, `ListingReasoning`, `ListingSources`. `/api/v1/meta` grows a `listings` object (`count`, `fetched_at`, the date of the discovery file). The version takes the next minor step at the time it lands.

**Freshness.**

| Basis | As fresh as |
|---|---|
| `listed` | the last discovery: one day old at most (section 9.1) |
| `models.dev`, `pi.dev` | the daily import, 04:17 UTC |
| `providers.dev` | the last edit, dated in its evidence |

Every response keeps its existing freshness headers (`ETag`, `X-Dataset-Version`, `X-Dataset-Generated-At`), and a `listed` listing older than `stale_after_days` is marked by the existing stale rule.

### 4.3 Discovery: how a login's list gets to a site with no logins

providers.dev's CI has no logins. groundtruth discovers each login's offered models every day and publishes them in `listed.json`, at the repository root beside `observed.json`. Section 9.1 says how it reads them and why that way.

providers.dev's daily sync fetches `listed.json` as it fetches `observed.json` (`scripts/lib/sync.ts:15`, `:243-260`), into `data/sources/listed.json`. A missing file is "no discovery yet", not an error. Every model of the file is listed, measured or not; the page says which (4.5).

**Why a file of its own, not a part of `observed.json`.** `observed.json` is built from audited raw results, and CI fails when it does not match them (groundtruth's `ci.yml`: `build_observed.py --check` and `--audit-gate`). A list is not a measurement, changes daily and must not wait for Tuesday's audit.

**What the file holds.** Per provider: the date, the client that read the list (`provimux`, a new id in `data/authored/measurement-labels.yaml`, of kind `proxy`), and the models with the fields the vendor stated. It holds no account, email, token, auth file name or login count. Where the owner has several logins of one type, the list is their union.

**No circle.** Only what a vendor's list said is written. A list Provimux took from providers.dev or from its built-in copy is never discovered, so providers.dev never reads back its own data.

### 4.4 The fields a live list lacks

| Provimux needs | Listing field | Who states it when the vendor's list does not |
|---|---|---|
| thinking budgets (`min`, `max`) | `reasoning.budget_min`, `budget_max` | authored, from the vendor's documentation |
| thinking can be off, dynamic budget | `reasoning.can_disable`, `dynamic` | authored; Claude's list states both |
| thinking levels | `reasoning.levels` | models.dev (`reasoning_options`), else authored |
| output limit (Codex, Kimi, xAI) | `max_output` | models.dev (`limit.output`), else authored |
| modalities (Antigravity, Gemini) | `input_modalities`, `output_modalities` | models.dev, else authored |
| native web search | `web_search` | authored; Antigravity's and Claude's lists state it |
| other ids the access method accepts | `aliases` | authored; xAI's list states them |

Authored statements go in `data/authored/listings/<provider>.yaml`, one rule per model or selection, each with `reason` and `evidence` (`kind`, `url`, `date`), validated by a Zod schema like every authored file. The build fails without evidence, as today.

**Not carried by providers.dev:** the three router settings of section 2.2 (`supported_parameters`, `support_configuration_update`, `config.override_header`). They say how a proxy must behave, not what a model is. Section 6.5.

**Not copied:** no authored value is taken from router-for-me's files. A value comes from the vendor's list, the vendor's documentation, models.dev, pi or a groundtruth measurement, and says which.

### 4.5 Two rules to amend, and what the site shows

Two rules of `AGENTS.md` stand against this as written. Both need the owner's word.

| Rule today | Amendment |
|---|---|
| "Vendors' own model lists are not values of an entry; they stay in evaluation notes." (`AGENTS.md:108`) | Stays true and gains one sentence: a vendor's list is published as the provider's listing (`/api/v1/listings`) and is never written into an entry's `limit` or into a safe value |
| "No subscription plans, plan prices, allowances or quota data. Access is a classification only." (`AGENTS.md:76`) | A listing row may carry the plan **names** the vendor's own list attaches to a model. Still no prices, allowances or quotas |

Without the second amendment, `plans` leaves the contract before it is built, and a Codex login whose live list has not arrived yet is offered every plan's models.

**The site** shows a listing on the provider's page, in a section "Model list": the date, the client, and one row per model with id, name, window, output limit and reasoning levels. A model with an entry links to it; one without says "Listed, not evaluated". The page prints fields of `/api/v1/listings/{provider}` and works nothing out (AGENTS.md:114-118). The methodology gains one paragraph on what a listing is and where it comes from.

### 4.6 Beside `sources-side-by-side`

That branch holds a design document only; main has not moved since it forked [ran: `git log main..sources-side-by-side`, 2 commits, 1 file].

| | |
|---|---|
| New files, no conflict | `src/core/listings.ts`, the listing schemas, `data/authored/listings/`, the `listed.json` reader, tests |
| Shared files, small edits | `src/core/contract.ts`, `src/core/openapi.ts` (two `ENDPOINTS` rows and the version), `src/core/api.ts`, `scripts/lib/publish.ts`, `scripts/lib/sync.ts`, `structure/api-v1.json`, `AGENTS.md`, README |
| The real collision | the version line in `openapi.ts` and the structure record. Whichever lands second takes the next minor and runs `npm run structure:update` again |
| Not touched | `safe.ts`, `resolve.ts`, `limits.ts`, `cache.ts`, the pi fence: the code that redesign rewrites |

A listing row states what the list said, not the entry's safe value. Once `sources-side-by-side` is live, a later minor can add the entry's safe window beside it; that is growth, not change.

## 5. Merging, in Provimux

### 5.1 The rule

Per login type Provimux keeps a **section**: every model it knows for that type, with its fields. Per login it keeps the login's **live list**.

| | Rule |
|---|---|
| The section's models | the published list; the built-in list while no published list is held; plus **learned** models: ones a live list of any login of that type named and the published list lacks |
| A model's fields | per field, the first that states it: the live list, the published list, the built-in list. A field no source states stays unknown; nothing is invented |
| A login **with** a live list | offers exactly the models its live list names, plus the models Provimux adds itself (image, video and speech models, `live_model_lists.go:265-276`) |
| A login **without** a live list | offers the published list (else built-in), without learned models |
| On top, unchanged | the user's `excluded-models`, `oauth-model-alias`, model prefixes, and models from the config and from plugins (`sdk/cliproxy/service_models.go:337-350`) |

**A change from today.** The patch lets the catalog's value win for a model both have (`live_model_lists.go:31`). This design lets the vendor's value win per field, which is what "first, live" means. Step P6 lists every value this changes on the owner's logins before anything is removed. Keeping "published wins for a model both have" is the alternative (section 11).

### 5.2 How often

| Source | When |
|---|---|
| Live | at start, in the background; every 3 h per login; on `POST /v8/management/models/refresh`; when a login is added or replaced. After a failure, again in 10 minutes. All as the patch does today (`live_model_lists.go:35-37`, `:131-148`) |
| Published | at start, in the background; every 6 h; on the same refresh. One conditional request (`If-None-Match`): providers.dev changes once a day, so most answers are `304` with no body. After a failure: 10 minutes, then 1 hour, then the normal 6 |
| Built-in | never fetched: it is in the binary |

### 5.3 Failure: never an emptied list, never a blocked start

**Start.** Provimux composes its lists before it listens, from the built-in lists and from its disk cache (section 5.4). Both are local and small. No network request is awaited, so a start with no network gives the lists of the last run.

**A fetched list replaces the held one only if all of this holds:**

| Check | Live | Published |
|---|---|---|
| HTTP 200 and well-formed | yes | yes |
| Complete | Claude: `has_more` false (`live_model_lists.go:495-497`). Gemini: every page read. Others: one answer | every listing of the document |
| Not empty | at least one model (`:525-527`) | per listing; an empty or missing listing keeps the held one **for that login type only** |
| Size | at most 8 MB (`:38`) | at most 8 MB |
| Not older | the login is still the same account (`:348-352`) | `generated_at` not before the held document's |

Anything else is a failed fetch: the held list stays, the failure is recorded as a code (never the error text, as today: `.github/provimux/design/pui13/02-live-lists.md` section 4), and the retry is scheduled.

**A live list that cannot be refreshed** stays in use for 7 days. After that it counts as not held and the login takes the published list, with one log line and a visible state. Without this limit, a vendor that removed its list endpoint would freeze the login's models for good.

**No list from any source.** The vendor gives none, providers.dev cannot be reached, and nothing is cached: the login offers the built-in list. A release is not built without a non-empty built-in list for every login type (section 5.6), so this is the floor. If a login type has none at all (upstream added one between two releases), the login offers only models from the config and plugins, the log warns, and the status endpoint says `none`. Step P1's test makes the sync check fail before such a release exists.

### 5.4 The disk cache

New, and holding only our own sources.

| | |
|---|---|
| Where | a folder `model-lists/` beside the console's `static/` folder, resolved by the same rule (`internal/managementasset/updater.go:146-162`: `WRITABLE_PATH`, else the config file's folder) |
| Files | `published.json`: the last accepted providers.dev document and its `ETag`. `live-<hash>.json`: one login's last live list |
| A live file holds | model ids and fields, the fetch time, and a hash of the account identity. No token, email or file name |
| Mode | `0600`, written to a temporary file and renamed |
| Read-only disk | no cache; lists live in memory only |
| A damaged or foreign file | ignored with one warning, then overwritten |

Why it exists: without it every restart shows the published list until the vendors answer, so a model only the vendor knows would vanish and come back at each restart.

### 5.5 How the user sees the source

**Log.** One line when a login's source or model count changes, one line per accepted providers.dev document, a warning per failure:

```
model list for devin login a8d1…: live, 53 models, 1 not on providers.dev yet (gemini-4-neon)
model lists from providers.dev: dataset 3f9c… of 2026-11-22, 11 listings
model list for kimi login 0b7e…: fetch failed (http_status 401); keeping the list of 2026-11-21
```

**Management API.** A new read-only path, `GET /v8/management/provimux/model-lists`, behind the management key like the other three (`internal/api/server_provimux.go:11-16`). It reports recorded state and predicts nothing, the rule PUI-14 set.

```json
{
  "observed_at": "2026-11-22T18:04:11Z",
  "options": { "live_lists": true, "published_lists": true },
  "built_in": { "dataset_version": "3f9c…", "generated_at": "2026-11-20T04:21:00Z" },
  "published": {
    "state": "held",
    "dataset_version": "5a1e…",
    "generated_at": "2026-11-22T04:20:11Z",
    "fetched_at": "2026-11-22T12:00:03Z",
    "from_cache": false,
    "last_failure": null
  },
  "logins": [
    {
      "auth_index": "a8d167b7d82c8c15",
      "provider": "devin",
      "login": { "name": "devin-office@providers.dev.json", "email": "office@providers.dev", "label": "office@providers.dev" },
      "disabled": false,
      "source": "live",
      "live": { "supported": true, "held": true, "fetched_at": "2026-11-22T16:10:44Z", "from_cache": false, "fetching": false, "listed_models": 53, "last_failure": null },
      "registered_models": 53,
      "learned_models": 1,
      "models": null
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `source` | which source decides this login's models now: `live`, `published`, `built-in` or `none`. Clients treat another value as unknown |
| `published.state` | `held`, `none` (never fetched and nothing cached) or `off` (the option) |
| `live.supported` | the vendor has a list and this login is asked |
| `learned_models` | models this login offers that the published list lacks |
| `models` | `null`, unless the request names one login (`?auth_index=`): then one row per model with its `id`, its `source` and, per field, the source that gave it |
| `?view=live` | adds, per login, the vendor's list as it arrived, unmerged. This is what groundtruth's discovery reads (section 9.1) |
| `last_failure` | `{at, category, http_status}`: codes only |

Rules kept from PUI-13: `auth_index` identifies a login; names follow `GET /v8/management/credentials`; responses only grow.

`GET /v8/management/provimux/live-lists` stays as it is, for consoles already released. Its `providers` list grows. `POST /v8/management/models/refresh` stays the one way to refresh; its answer names providers.dev where it named router-for-me's address.

**Console** (`provimux-ui`). The "Live lists" page becomes "Model lists": per login the source and its age, a mark for a list that is getting old, the count of models not on providers.dev yet, and per login the models with their source. A line at the top says which providers.dev dataset is held, or that providers.dev was not reached and since when. An old console keeps working on the old path; a new console on an old server gets `404` on the new path and shows the old page (the rule of `05-console-contract.md`).

### 5.6 How the built-in list is kept current

The release workflow already fetches the catalogs once, validates them, hands them to every build as one artifact and names their origin in the release notes (`provimux-release.yml:89-171`, `:225-240`). Only the origin changes.

| Step of job `catalogs` | Today | After |
|---|---|---|
| Fetch | `router-for-me/models` at one commit | `GET https://providers.dev/api/v1/listings?view=full`, once; the answer's `X-Dataset-Version` is recorded |
| Validate | sections present, ids unique, the Go validators | the listing schema; a non-empty listing for every login type of Provimux's map; the same Go validators on the lists as a build embeds them |
| Hand over | artifact `model-catalogs` with `SHA256SUMS` | the same, under our file names |
| Release notes | the commit of `router-for-me/models` | the dataset version, its date and the checksum; the file is attached to the release as `provimux-model-lists.json` |
| providers.dev cannot be reached, or a listing is empty | | the build takes `provimux-model-lists.json` of the newest `-pmx` release and says so in the notes; with none, the release fails |

Provimux is released with every CLIProxyAPI release, and upstream made 21 in the two weeks from `v8.0.60` to `v8.1.0` [ran: the tags and their dates], so the built-in list is days old at most.

**The Codex client catalog** is a different thing: not a model list but the record a Codex CLI expects per model, with OpenAI's instructions (1 MB). Its sources:

1. Live: the Codex login's own answer is this catalog. The patch already fetches it (`live_model_lists.go:445`) and throws most of it away.
2. Not providers.dev: it publishes model facts, not a client's prompts.
3. Built in: OpenAI's own public file, `codex-rs/models-manager/models.json` of `openai/codex` (Apache-2.0), fetched at one commit by the same job. It has 14 models in the same shape, and upstream's validator rejects it for one reason only: it has no `base_instructions` [ran: `go run ./cmd/validate_codex_models` on the file]. Step P5 fills that field from the same repository at the same commit, or the owner chooses his own Codex login's answer instead (section 11).

**A copy in the repository.** `internal/registry/provimux_lists/` holds a committed copy so that a build from source works. It is as old as its commit; a release replaces it at build time, as releases replace the catalogs today.

## 6. What changes in Provimux, and the cost of upstream merges

### 6.1 Where the change sits

Everything is in layer commits (trailer `Provimux-Layer: branding`). No protected path is touched (`guard.sh:33`: `internal/runtime/executor`, `internal/auth`, `internal/misc`, `internal/translator`, `sdk/cliproxy/session`). The live requests go through each executor's exported `HttpRequest`, as the patch's do (`live_model_lists.go:396`); every executor has one [ran: grep].

**New files (no conflict possible).**

| File | Holds |
|---|---|
| `internal/registry/provimux_model_lists.go` | the three sources per login type; the rule of 5.1; the conversion into the shapes upstream's registry reads |
| `internal/registry/provimux_builtin_lists.go` | the built-in lists, and the test-binary rule of 6.4 |
| `internal/registry/provimux_lists/lists.json`, `codex_client_models.json` | the committed copy of the built-in lists |
| `internal/provimux/modellists/` | the providers.dev client and the disk cache |
| `sdk/cliproxy/provimux_live_devin.go`, `_kimi.go`, `_xai.go`, `_gemini.go`, `_copilot.go`, `_antigravity.go` | one vendor's request and parser each |
| `internal/api/handlers/management/provimux_model_lists.go` | the endpoint of 5.5 |

**How it hooks in without editing upstream's updaters.** Upstream's three updaters are package variables whose fetch function is a field (`catalog_sources.go:352-370`). The layer's `init()` wraps that field: for the default source it returns the composed lists in upstream's own shape; for an explicit source (`models.catalog` and its two siblings, or a test's own address) it calls upstream's function unchanged. Upstream's updater stays the only writer of the registry's stores and keeps its validation, its change detection and its refresh callback (`catalog_sources.go:102-129`).

**Edits to upstream's lines.**

| File | Lines | Edit | Upstream commits that ever touched these lines |
|---|---|---|---|
| `internal/registry/model_updater.go` | 19-22, 24-25 | the two addresses removed; the embed replaced by a call into the layer | 1 |
| `internal/registry/devin_models_updater.go` | 11-14 | the two addresses removed | 2 |
| `internal/registry/codex_client_models_updater.go` | 11-14 | the two addresses removed | 1 |
| `internal/registry/devin_models.go` | 16-17 | the embed replaced | 1 |
| `internal/registry/codex_client_models.go` | 15-16 | the embed replaced | 2 |
| `internal/registry/model_definitions.go` | 102-225 | `staticDevinModels` becomes an empty list | 1 in the last two weeks, on the whole file |
| `sdk/cliproxy/service_models.go` | one line per login type | `applyLiveModelList(…)` for Devin, Kimi, xAI, Gemini, Copilot, Antigravity, as the patch has for three (`:161`, `:189`, `:226`) | 4 in the last two weeks, on the whole file |
| `internal/registry/catalog_config.go` | 15-17 | `live-lists` defaults to on; `published-lists` added | the patch's own lines |
| `internal/api/server_provimux.go` | one line | the new route (a layer file) | |

Counts are `git log -L` on `v8.1.0` and `git log v8.0.60..v8.1.0` [ran]. A rebase conflicts only when upstream edits one of these exact lines; then the sync check reports the commit and the file, as for any patch.

**The owner's patch file** `sdk/cliproxy/live_model_lists.go` grows: one `case` per vendor in `fetchLiveModelList`, the learned models, the disk cache and the 7-day limit. A layer commit already edits this file (PUI-14, `:355`).

### 6.2 What is removed

| Removed | How |
|---|---|
| The six addresses of `router-for-me/models` and `models.router-for.me` | the edits above. The binary then holds neither string |
| The three embedded files | the three embeds point at the layer's lists; nothing compiled reads `internal/registry/models/` |
| `staticDevinModels` | emptied |
| The release's fetch of `router-for-me/models` | job `catalogs` re-sourced (5.6); the workflow input `models_commit` goes |
| A `models.catalog`, `models.codex-catalog` or `models.devin-catalog` value that names a router-for-me host | ignored at load with one warning. The three keys keep working for the user's own file or address |

**Not removed, and why.**

| Stays | Why |
|---|---|
| The plugin store's address (`internal/pluginstore/registry.go:17`) | the owner's decision |
| `.github/scripts/refresh-model-catalogs.sh` | upstream's script, called only by two of upstream's workflows, both disabled in this repository (`.github/provimux/upstream-workflows.md:9`, `:12`). It never runs here |
| The three files in `internal/registry/models/`, in the source tree | section 6.4 |
| Models written into upstream's code: Codex image models, xAI image, video and speech models, `devin/swe-1-6-slow`, Copilot's `compat.json` | they are how an executor behaves for a model the vendor's list never names, not a fetched list; `compat.json` is on a protected path. The owner decides (section 11) |

### 6.3 How much of upstream's registry stays untouched

Upstream has 9 non-test Go files in `internal/registry` at `v8.1.0` [ran: `git ls-tree`]. Seven get the small edits above. The two large ones, `model_registry.go` (2,297 lines) and `catalog_sources.go` (520 lines), get none. Every function keeps its code (`GetClaudeModels` and its siblings, the lookups, the updater, the refresh result): only what feeds them changes. `service_models.go` gains six lines.

### 6.4 Upstream's tests, and why the three files stay in the tree

I ran upstream's tests twice on `e5cf5a2d5`, over `internal/registry`, `internal/thinking`, `internal/translator`, `internal/runtime`, `internal/api`, `internal/client`, `internal/auth` and `sdk` (90 packages): once as is, once with `models.json` and `devin_models.json` emptied.

| | As is | Lists emptied |
|---|---|---|
| Top-level tests with a result | 8,061 | 7,350 |
| Failing | 1 | 145 |
| Failing only without the lists | | **144**, in 14 packages (`sdk/cliproxy` 63, `internal/runtime/executor` 33, `internal/registry` 15, `internal/thinking` 11, others 22) |
| No result at all | | **711**: the executor package stopped |

The names are in the scratch file given in section 12.

The sync check passes a release only if it is "no worse than upstream" (`SYNC.md`, "The check"). Upstream's tests assume router-for-me's lists; with our lists in their place, every difference between the two would be a failing test at some sync, and a failing test needs the owner.

**So:** in a **test binary** the registry starts from upstream's three files, read from the source tree; in every other binary it starts from the built-in lists. `testing.Testing()` tells the two apart, and the files are found from the module root. The three files therefore stay in `internal/registry/models/`, as upstream's test fixtures. They are in no released binary and in no binary built from source, and nothing fetches them.

A test of the layer proves it (step P1): no `go:embed` in the module names that folder; the six addresses are in no Go file; a built binary contains neither host name. The same test fails when upstream adds a list file, an address or a login type, so a new upstream release cannot bring one in unseen.

Deleting the three files from the tree is possible only together with waiving those 144 tests at every sync. It also needs a second commit of the README commit's kind, because upstream changes `models.json` often (8 commits in the two weeks to `v8.1.0`) and a deletion would stop the rebase each time. I do not recommend it.

### 6.5 The three router settings

`supported_parameters`, `support_configuration_update` and `config.override_header` exist only in router-for-me's file, on Codex models. They are not copied. Step P6's comparison shows what the owner's logins lose without them. Each one that matters is then set again from our own evidence, in a small table of the layer with the reason beside each row, or left out. Codex's live answer has a field `supports_reasoning_effort_updates` that may be the source of the second [infer].

### 6.6 Copies on the owner's machine

| Copy | Removed by |
|---|---|
| In the installed binary: router-for-me's lists of the release day | installing the new release. `brew upgrade` replaces the binary and by default cleans the old version from the Cellar [infer: Homebrew's default; not checked on the owner's machine] |
| In old Docker images | `docker image prune`, or removing the old tag |
| On disk, written by Provimux | none exists (section 2.1) |
| In the config: a `models.*catalog` value naming their host | ignored by the new release; the owner deletes the line. `grep -nE 'catalog:' <config file>` shows it |
| In the source checkout | the fixtures of 6.4 |

The release notes of the first release without the lists say these five lines.

## 7. Privacy

| To | What Provimux sends | When | Never |
|---|---|---|---|
| **providers.dev** | one request, `GET https://providers.dev/api/v1/listings?view=full&limit=<max>`, with `User-Agent: provimux/<version>` and `If-None-Match` | at start and every 6 h | which logins exist, any model id, any account, any identifier of the install, a cookie. The request is the same for every user, so the server cannot tell what a user has. As for any website, the host (Cloudflare) sees the sender's IP address |
| **A vendor** | one list request per login, to the vendor's own host, with that login's credential and the client headers Provimux already sends that vendor | at start and every 3 h (Copilot every 2 h, as upstream) | a prompt, a file, another login's data |
| **router-for-me** | nothing for model lists | | The plugin store's address is still fetched when the store is used, as decided |

- **providers.dev can be switched off:** `models.published-lists: false`. Provimux then never contacts it and uses live and built-in lists.
- **Live lists can be switched off:** `models.live-lists: false`, as today. Logins then take the published list.
- **The request goes through the configured proxy.** Upstream's catalog fetch uses Go's default client (`catalog_sources.go:254`); the new client uses the proxy setting.
- **Hosts.** Every live request goes to the host the login's requests already go to, with one exception to verify: the xAI login's chat goes to `cli-chat-proxy.grok.com` and its list would go to `api.x.ai` (section 3).
- **A list call the vendor's own client also makes:** Codex, Kimi and Copilot [docs]; Devin, by upstream's comment (`devin_executor.go:109`). Not verified for Claude Code, Antigravity and Meta. Claude's and Meta's are in the released patch already.
- **Discovery publishes** model ids and fields per provider, from the owner's logins, in groundtruth's public repository. It names no account. It does show which login types the owner holds, which groundtruth's routes already show, and a Codex list hints at the plan of the account it came from.

## 8. The Neon case

Devin starts offering Gemini 4 Neon on day 0.

| Step | What happens | Where |
|---|---|---|
| 1 | Within 3 hours, or at the next start, or when the owner presses refresh, Provimux asks the Devin login's vendor for its list | new `provimux_live_devin.go` |
| 2 | The answer has the new variants (`gemini-4-neon-low`, `-medium`, `-high`), each with label, window, output limit and multimodal flag | the fields upstream's tool reads (`cmd/fetch_devin_models/main.go:300-377`) |
| 3 | The variants are joined into one model, `gemini-4-neon`, with levels `low`, `medium`, `high` | `aggregateDevinModels`, `devin_models.go:341` |
| 4 | The answer is complete and not empty: it becomes the login's live list, in memory and on disk | 5.3, 5.4 |
| 5 | The published Devin list lacks the model: it is added to the section as learned, with every field from the vendor | 5.1 |
| 6 | The registry is refreshed for Devin logins. `GET /v1/models` shows `devin/gemini-4-neon` | upstream's refresh callback |
| 7 | The log says `live, 53 models, 1 not on providers.dev yet (gemini-4-neon)`; the console shows the model with the tag "from Devin's list" | 5.5 |
| 8 | A request for it is routed to Devin with the effort as the id's suffix | as for every Devin model today [infer: the new id follows the suffix scheme] |
| 9 | That evening groundtruth's discovery writes the Devin list, with Neon, into `listed.json`. The next Monday night measures it first, as a new model | 9.1, 9.2 |
| 10 | At 04:17 UTC providers.dev imports it. The listing `devin` has Neon; the Devin page shows it as "Listed, not evaluated" | 4.1, 4.5 |
| 11 | Another user's Provimux gets Neon from their own Devin login at step 1. If their fetch fails, they get it from providers.dev within 6 hours of step 10 | 5.2 |
| 12 | The next release has it built in | 5.6 |

**Compared with today:** `devin_models.json` still lacked the model on 2026-11-22 (the brief), and Provimux had no other way to learn it.

**What does not help:** if the Devin request fails that day, the login keeps yesterday's list and has no Neon until a fetch succeeds or step 10 has happened. If Devin's answer lacks a field, the field stays unknown; Provimux does not assume the 64,000 output limit upstream's tool assumes (`cmd/fetch_devin_models/main.go:49`).

## 9. groundtruth: daily discovery and the Monday night

**The owner's decisions (2026-11-22, passed on by the chief).**

- Every login's offered models are discovered automatically, every day, without measuring. providers.dev lists all of them, measured or not.
- Measuring runs every Monday night, from 23:00 Europe/Bucharest to Tuesday morning, unattended, on the owner's Mac.
- No hand-picked list of models. New models first, on every route; then the order of 9.2.
- A cap of runs per login and a cap of money per week.
- When access or quota is missing, nothing is skipped silently or worked around: that part stops, the owner is asked at once, the rest goes on.
- Publication waits for an audit, done by a reviewer without the owner.

Read from groundtruth's branch `own-clients` at `60e65ba`; `RUN.md` below is its `docs/own-clients-RUN.md`, `own-clients.md` its design.

### 9.1 Discovery

Three ways to read what a login offers.

| Way | Covers | Sends | Limits |
|---|---|---|---|
| **A. Ask the owner's Provimux** (`view=live`, section 5.5) | every login Provimux holds: nine types | one local request. No vendor request (Provimux has asked already) and no token in groundtruth | needs steps P2 and P4 running as the owner's proxy (a build from the branch is enough). The list is read by a proxy |
| **B. Read it from the vendor's own client**, in the night's folders | only the clients the night drives: Codex and Devin today | nothing, where the client keeps its list in its folder | not established for either client [not verified]: Codex's CLI fetches its list (section 3), but where it keeps it, and whether the Devin CLI shows a list at all, are open |
| **C. groundtruth asks the vendor itself**, with the night's login files | Codex and Devin today | one vendor request per login | groundtruth would read tokens, which the night never does today (`harness/night.py`: "No key is read"). It would rebuild each vendor's request (Devin's is protobuf) and must leave the token's refresh to the client |

**Recommendation: A.** It is the only way that covers every login, and it sends nothing new. It is also the list Provimux's users get, which is what providers.dev's list stands in for.

- **What A costs:** discovery waits for Provimux's steps P2 and P4. They come first in Provimux's order for that reason (section 10).
- **B later:** where a smoke run shows that a client leaves its list in its folder, it becomes a second reader. The listing then names `codex-cli` instead of `provimux`, by the rule that already ranks an own client before a proxy (`own-clients.md` section 3).
- **C** I do not recommend.

**The command**, `python3 harness/discover.py` (new):

1. Reads `GET /v8/management/provimux/model-lists?view=live` from the owner's Provimux. The management key is found as `harness/pool.py` finds it today (`CPA_MGMT_KEY`, or `~/.cli-proxy-api/keys.txt`; never printed).
2. Keeps, per login type, only what the vendor's list said. Drops every identity field. Joins several logins of one type.
3. Writes `listed.json` (`schema: 1`). Its schema has no free text beyond a model's id, name and description, so nothing else can get in.
4. A provider whose logins had no live list that day keeps yesterday's part, with yesterday's date.
5. Commits and pushes the file when it changed, after `harness/scan_secrets.py` passes.

It runs daily at 22:30 from a launchd agent on the owner's Mac, and as the first step of the Monday night. It sends nothing to a vendor and spends nothing. If the Mac sleeps at 22:30, launchd runs it when the Mac next wakes; no wake is scheduled for it.

**When Provimux cannot be reached, or holds no list for a login:** the file keeps its last content, and `$G/discovery.log` says which provider is how many days old. A provider older than 3 days is an ask (9.5).

### 9.2 What a night measures

A **value** is one quantity of one model on one route: its cache lifetime, its input window, its output limit.

The night measures a value only where the harness has a method on that route. A value without one is named in the plan as "no method yet", never left out without a word. Today that is: cache lifetimes on every route the night drives; windows on API-key routes (one refused request); no window through the own clients yet (`own-clients.md` 4.4).

For every value the plan (`harness/plan.py`, new) knows four things.

| What | From |
|---|---|
| How often we measured it, when, and our value | `observed.json`, counting the runs of the clients that count for the route, plus the runs of nights still waiting for their audit |
| What models.dev and pi.dev state now, and whether our value agrees | providers.dev's API. Agreement is providers.dev's verdict, worked out once there and never a second time in groundtruth: today `providers_dev.limits.input_window` and `providers_dev.pi.values`, after 1.4.0 the safe values |
| What models.dev and pi.dev stated on the day we measured | `plan/values.json` (new), written at each publication: per value the count, the date and the two sources' values of that day |
| Which models are offered | logins: `listed.json`. API keys: providers.dev's entries of `openrouter`, `openai` and `anthropic`, which are what models.dev and pi.dev list after the catalog's rules |

**The order.**

| Order | State | Rule |
|---|---|---|
| 1 | New | offered and never measured. First the models first seen since the last night, then the rest |
| 2 | In doubt | measured once, and our value disagrees with models.dev or pi.dev: measured a second time, to confirm or correct |
| 3 | Source moved | models.dev or pi.dev states another value than on the day of our last measurement: measured on the next night, however often before |
| 4 | Ours alone | no source states the value (cache lifetimes; most subscription entries), and our last measurement is older than 90 days |
| never | Agreeing | our value agrees with the sources. It comes back only through 3 |
| never | Settled | measured at least twice, our measurements agree with each other, and they disagree with a source. Our value stands, and providers.dev shows the disagreement. It comes back only through 3 |

- **Within a state:** logins in the vendor's own list order; API keys cheapest first. Ties: the oldest measurement first.
- **Two measurements that disagree with each other:** a third, in state 2. If no two of three agree, the value is unsteady: no further run, and an ask (9.5).
- **A value whose only measurements an audit excluded** counts as never measured.
- **The count.** `plan/values.json` is public and committed with each build. It holds, per value, how often it was measured; the night's report prints each value's state from it.
- **The first time.** For the measurements that exist today, the sources' values of the day the file is first written are recorded as "stated on the day we measured". Without that, every old value would look moved.
- **Before providers.dev 1.4.0 is live,** agreement on the output limit is not published. The plan then says "agreement not available" for those values and orders them by states 1 and 4 only.

### 9.3 The caps

**Logins: a cap of runs.**

| | |
|---|---|
| Cap | 6 cache runs per login per night, plus one smoke run per client |
| What that is | a `std` run is 93 requests and about 2.7 million input tokens, two thirds of them read from cache. Six are about 560 requests and 16 million tokens (`own-clients.md` 2.2). It is what Codex's part of the first own-client night uses |
| Length | `std`: 2 h 10 min, all runs side by side. A model still warm at 2 hours gets a `long` run (12 h 10 min) the next Monday; it is one of the six and is started first |
| The backlog | router-for-me's files list 52 Devin models and 14 Codex models today; the first night measures 17 and 6. The rest takes about 6 Mondays for Devin and 2 for Codex |
| A plan's limit reached | that login's part stops at once and asks (9.5). The runs left are first next Monday |

**API keys: a cap of money.**

| | |
|---|---|
| Cap | 10 US dollars per week, for all API-key routes together |
| What that buys | the 88 direct OpenRouter runs of 18 November cost 16.31 dollars: 0.19 per run on average, from 0.02 to 1.25 (`own-clients.md` 2.2; groundtruth's history). A refused window request costs nothing |
| How it is kept | the night's own ledger, as on 18 November (`results/openrouter-2026-11-18/ledger.py`): prices read again before each run; a run starts only when its estimate fits what is left; the part stops when the provider's own figure reaches the cap |
| One run | estimated above 2 dollars: not started, and an ask |
| Behind the ledger | a key made for the night, with the provider's own limit where it has one |
| A window request answered instead of refused | it was billed for over a million tokens: the window list ends, and an ask |

The money is one pot, spent in the order of 9.2: new models on all three routes first, cheapest first, then states 2, 3 and 4.

A night that runs out of candidates ends early. It never fills a cap with repeats.

### 9.4 The night, unattended

| When (Europe/Bucharest) | What |
|---|---|
| Once, the owner | `sudo pmset repeat wakeorpoweron M 22:55:00`. The logins, in the night's own folder (`night.py login codex`, `login devin`). The API keys, in a file of mode 600 there |
| Once, staff with his OK | two launchd agents in `~/Library/LaunchAgents`: discovery, daily at 22:30; the night, Mondays at 23:00 |
| Mon 23:00 | the night starts: one lock per night, then the snapshot of the everyday state (`untouched --save`) |
| | discovery (9.1), then the plan (9.2), written to `results/night-<date>/PLAN.md` |
| | the checks before anything is spent: each login's own status command (`RUN.md`, step 2); each key by a request that costs nothing. A login that is not logged in, or a refused key: that part is not started, and an ask |
| | one smoke run per client, judged by the automatic checks (`RUN.md`, step 3). A failed check: that client's part is not started, and an ask |
| Mon 23:15 | the runs, staggered as today. Each keeps the Mac awake (`caffeinate -i -w`) |
| Tue, about 01:30 | the `std` runs have ended |
| Tue 11:30 at the latest | the `long` runs have ended. A run still alive gets its stop mark |
| At the end | `untouched --report`; the mechanical audit (9.6); `REPORT.md` and `state.json`; one notification: runs done, parts stopped, asks open |

- **What changes from the first night:** the hand review between smoke and go is replaced by the automatic checks. Everything else of its isolation stays: the folders, the sandbox, the stop marks, no client error text in a public file.
- **The logins stay** in the night's folder between weeks. The owner logs in again only when a check says one expired.
- **A late start.** launchd runs a missed job when the Mac wakes. After 01:00 no `long` run is started. After 03:00 the night does not start: it writes its state and asks.
- **A closed lid.** A laptop with its lid closed sleeps whatever `caffeinate` asks [not verified for the owner's Mac]. The lid stays open, on power.
- **`--dry-run` stays** on every command, and nothing is sent without `--go`. The launchd agent is the one caller that passes it.

### 9.5 Asking the owner

Each case stops its own part only. The rest of the night goes on.

| Case | Seen as | Stops | The ask says |
|---|---|---|---|
| A login expired | the client's status command, before the runs or between two | that login's part | which login, and the command: `python3 harness/night.py login devin` (2 minutes, a browser opens) |
| A key refused | HTTP 401 or 403 on the free request or on a run | that route's part | which key, where its file is, and the command that checks it |
| A plan's limit reached | the client's quota refusal, as `probe.py` records it | that login's part | how many of the six ran; that the rest is first next Monday; the command to run them sooner |
| The week's money spent | the ledger | the API-key part | what was spent, what is left unmeasured, and the command with a higher cap for this week |
| A run over 2 dollars | its estimate | that run | the model, the estimate, and the command that runs it with his go |
| A vendor refuses | a refusal that is neither quota nor login, twice in a row on one route | that route's part | the route, the kind of refusal, and that the account needs a look |
| A window request answered | the reply | the window list | what it cost |
| Discovery stale | a provider's list older than 3 days | nothing | which login Provimux has no list for, and what its status page says |
| The night did not start, or woke after 03:00 | the state file | the night | when the Mac woke, and the command to start the night by hand |
| An unsteady value | three measurements, no two agree | that value | the three values |

**How he is asked.**

- At once: a macOS notification with the ask's first line.
- In full: `$G/asks/open/<id>.md`, with what happened, the exact command and what was not done.
- Until it is solved: every open ask stands at the top of the night's report and of every later report. The check that failed closes it when it passes again; nobody closes one by hand.

A notification at night is easily missed, so the report is what holds: Tuesday's reviewer passes every open ask to the chief, who asks the owner on the board.

### 9.6 Tuesday: the audit without the owner, and the report

Nothing of a night enters `observed.json` before an audit of its raw results (`RUN.md`, "The morning", item 3). That gate stays: the builder, the commit hook and CI refuse the night's folder without `AUDIT.md`.

| Step | Who | Does |
|---|---|---|
| 1 | the night, at its end | `python3 harness/night.py --date <date> audit` (new): every item of the checklist that is a mechanical check. Each public line against its schema. No client version, path, address, account or client text in a public file. Every check's model and, for Codex, `own-turn`. The last `model-scan` line. Codex's session files, listed for any other model and any reroute (the checklist's item B, two `grep`s by hand today). `git status`. Both scans. It writes `$G/night-<date>/audit-draft.md`: per item passed or failed, with counts and no private text |
| 2 | a reviewer, Tuesday morning | on the owner's Mac, reading only. Reads every public line. Judges the `check-invalid`, `retry` and `stop` lines. Reads the console logs and the everyday report for anything private. Looks at each failed item of the draft. Writes `results/night-<date>/AUDIT.md`: the first line as the gate wants it, the date, the role that audited, the runs excluded and why, one line per item |
| 3 | a worker, after the audit | `night.py manifest` (new) writes the manifest entries from the plan and the results, so nothing is typed by hand. Then `build_observed.py` and its `--check`, `plan/values.json`, the commit and the push. providers.dev imports it at 04:17 UTC |

- **The reviewer** is not who wrote the code the night ran that week.
- **Not the reviewer's to decide alone**, and nothing is committed: something private in a public file; a Keychain item a client added or changed; an unsteady value. Each is an ask.
- **The Tuesday task starts whether or not the night ran.** Its first question is whether `state.json` exists.

**The report**, `results/night-<date>/REPORT.md`, committed with the audit; `state.json` stays with the private files.

| Part | Says |
|---|---|
| Open asks | first, each with its command |
| Per part (each login, the API keys) | `done`, `stopped: <case>` or `not started: <case>`; runs planned, run and excluded |
| Per value measured | its state before, the new value, its state now |
| Not measured | the values the caps left for next week, in order; the values with no method yet |
| Spent | requests per login; dollars per route |

**No state file on Tuesday morning** means the night did not start. The reviewer reports that, with the launchd agent's log and the Mac's wake log (`pmset -g log`), and the chief asks the owner.

### 9.7 Where the API-key part runs

| | On the owner's Mac, in the night | In GitHub Actions |
|---|---|---|
| The keys | in the night's folder, mode 600 | in the repository's secrets; groundtruth is public |
| The money cap | one ledger, one process | a second ledger |
| Asking the owner | the same notification and report | a GitHub issue; no macOS notification |
| The audit | the same Tuesday audit | results arrive as a branch: a second path |
| Depends on the Mac waking | yes | no |
| Length of a run | no limit | 6 hours per job: `std` fits, `long` does not |

**Recommendation: on the Mac.** One night, one ledger, one report and one way to ask, and the money keys stay off GitHub. The cost: a Mac that does not wake loses the API-key part too, and Tuesday's task reports that the same day. Moving it to Actions later changes no result, since the probe and its files are the same.

## 10. The steps

Each row is one task. The order:

1. providers.dev, D0 to D4.
2. Provimux's live lists and its status endpoint, P2 and P4, built on the branch and run as the owner's proxy.
3. groundtruth's discovery, G1. From here providers.dev lists every offered model.
4. groundtruth's plan and the unattended night, G2 to G5.
5. The rest of Provimux, P1, P3, P5 and P6.
6. Review and release.

Steps 4 and 5 do not wait for each other.

### providers.dev

| Step | Does | Must hold afterwards | Tests |
|---|---|---|---|
| D0 | The owner's word on the two rules (4.5) and on the order beside `sources-side-by-side` | `AGENTS.md` and the README say the amended rules | `tests/api-structure.test.ts` still finds the stability wording |
| D1 | The contract: the listing schemas, the two paths, `meta.listings`, the version step, `npm run structure:update` | `/api/v1/listings` answers with an empty page when there is no data; the structure only grew from 1.0.0 | `api-structure`, `api` (the served OpenAPI equals `openapi()`), `contract` (two listings have the same keys) |
| D2 | The sources: models.dev's and pi's providers as lists; `data/authored/listings/` with its schema; the order of 4.1 | every provider of 4.1's table that has an imported or authored list has a listing; each field names its source; a statement without evidence fails the build | unit tests of `listings.ts`: the whole list from the first source, each field from the first that states it, fixed keys |
| D3 | The reader of `listed.json`: the file in the sync, with a schema; the client id `provimux` in `measurement-labels.yaml` | a missing file changes nothing; a malformed one keeps the last good copy and raises a sync issue; a `listed` list replaces the lower sources for that provider | sync tests with a fixture file, a missing file and a malformed file |
| D4 | The page section and the methodology paragraph | the provider page prints the listing from the API and works nothing out; a listed model without an entry is not a link | `pages`, `render` |

### groundtruth

After the first own-client night, which is in its last checks today.

| Step | Does | Must hold afterwards | Tests |
|---|---|---|---|
| G1 | Discovery (9.1): `harness/discover.py`, the schema of `listed.json`, CI's check of the file, the daily launchd agent | the file names no account, email or token and has no field outside its schema; a day with no live list for a provider leaves that provider's part and date unchanged; a provider older than 3 days is an ask; nothing is sent to a vendor | a recorded Provimux answer with two logins of one type, with identity fields, and with a login that has no list; the credential scan on the result; CI refuses a file that fails the schema |
| G2 | The plan (9.2): `harness/plan.py`, `plan/values.json` with its first-time record | every value is in exactly one state; the order is the table of 9.2; a settled or agreeing value is never planned; a value with no method is named; the same inputs give the same plan | one fixture per state, and for: two measurements that disagree, three of which no two agree, a value excluded by an audit, a night waiting for its audit, a source that moved, agreement not available |
| G3 | The night, unattended (9.3, 9.4): the weekly command, the lock, the checks before spending, the automatic smoke, the two caps and the ledger, the late start, `state.json` and `REPORT.md`, the launchd agent | a login's part never starts a seventh run; the API-key part never starts a run that does not fit the money left; an expired login or a refused key stops only its part; `--dry-run` sends nothing; a second start for the same night is refused | fake clients and a fake ledger for: each cap reached, each row of 9.5's table, a start after 01:00 and after 03:00, a run still alive at the end. The first night's tests still pass |
| G4 | Asking the owner (9.5): the open asks, the notification, their place in every report | every row of 9.5's table gives an ask with its exact command; an ask closes only when its check passes again; an open ask is at the top of every later report | one test per row; an ask that stays open over two nights; an ask closed by its check |
| G5 | Tuesday (9.6): `night.py audit` and `night.py manifest`; the audit gate for `results/night-*`; the reviewer's checklist | nothing of a night is committed or built without its `AUDIT.md`; the draft holds no private text; the manifest entries are generated, not typed | the gate's tests on the new folder name; the mechanical audit on a night with a reroute, a private string in a public line, and a clean night |
| G6 | One Monday as a dry run, started by launchd with `--dry-run`, then the first real Monday | the dry run's report names every run it would start, inside both caps; after the real night the Tuesday task finds a state file, an audit draft and a report | the dry run's report, read by the chief |

### Provimux

| Step | Does | Must hold afterwards | Tests |
|---|---|---|---|
| P1 | The built-in lists and the test-binary rule (6.4); the six address and embed edits; `staticDevinModels` emptied | a built binary holds neither router-for-me host name; in a test binary upstream's tests see what they saw before | a layer test: no embed names `internal/registry/models/`, no Go file has the addresses, every section of `staticModelsJSON` has a login type in the layer's map. `strings` on a built binary. **The sync check's verdict on the branch: no test fails that passes on the tag** |
| P2 | Live lists for Devin, Kimi, xAI and Gemini keys; learned models for all nine; on by default; the disk cache; the 7-day limit | section 5.1's table, row by row; a failed or partial fetch changes no list; a restart with no network gives the lists of the last run | per vendor: a parser test on a recorded answer, an empty answer, a cut-off answer, a paged answer. Store tests for the cache (damaged file, another account, read-only folder). The patch's existing tests still pass |
| P3 | The providers.dev client and the composition | section 5.1 and 5.3; one request per 6 hours; `304` changes nothing; one bad listing keeps only that login type's held list | a test server: `200`, `304`, `500`, a cut-off body, an older document, an empty listing. A test that the request carries no parameter beyond `view` and `limit` and no header beyond the two of section 7 |
| P4 | The management endpoint and its OpenAPI; the log lines; the console page | every key always present; `view=live` returns only what a vendor said; an old console and an old server each still work with the other | handler tests as for `live-lists` (200 with a source, 503 without); the examples validate against the OpenAPI; the console's own tests |
| P5 | The release workflow: `catalogs` from providers.dev; the Codex client catalog from `openai/codex`; the fallback to the last release's file | a dry run builds every archive with the same lists; the notes name the dataset version; a build with providers.dev unreachable uses the last release's file and says so | a dry run (`workflow_dispatch`); the existing "Check the model catalogs" step on the new files; `cmd/validate_codex_models` passes on the built-in Codex catalog |
| P6 | The comparison, once, on the owner's machine, run by the owner: for every login, the models and fields before the change and after it | a written list of every difference, each accepted by the owner or fixed. Nothing of router-for-me's file is copied to fix one | the report itself; it is the gate for the release |

**The gate.** P1 and P5 are not released before providers.dev has a non-empty listing for every login type of Provimux's map. P2 and P4 are built first and run from the branch, because discovery needs them (9.1).

### Review and release

| Step | Does | Must hold afterwards |
|---|---|---|
| R1 | A critical review of the providers.dev change against sections 4 and 7 | findings fixed or accepted by the chief |
| R2 | A critical review of the Provimux change against sections 5, 6 and 7, and of P6's report | the same; `guard.sh` passes; the sync check passes on the candidate |
| R3 | Release: providers.dev first (a push to `main` deploys), then discovery running daily, then the Provimux `-pmx` release; the release notes carry the five lines of 6.6 | on the owner's machine after the upgrade: no request to a router-for-me host for a model list in 24 hours of the log; every login's source is `live` or `published` on the status page; the Neon model is offered |

## 11. Decisions that are the owner's

| # | Decision | Recommendation | The other way costs |
|---|---|---|---|
| 1 | The three files of router-for-me stay in the source tree as fixtures for upstream's tests, and are in no binary | **Keep them** (6.4) | Deleting them fails 144 of upstream's tests and stops the executor package's 711; every sync would then need a waiver or the owner |
| 2 | The two rules of providers.dev (4.5): a listing as its own resource, and plan names on a row | **Amend both** | Without the first there is no providers.dev part. Without the second, `plans` is not built and a Codex login without a live list is offered every plan's models |
| 3 | How discovery reads a login's models (9.1) | **From the owner's Provimux.** It covers every login and sends nothing new | Through the vendors' own clients it covers Codex and Devin only, and it is not established that either shows its list without a model request. With groundtruth asking the vendors itself, groundtruth reads tokens |
| 4 | For a model both sources know, whose field value wins | **The vendor's**, per field (5.1) | "Published wins" is what the patch does today; it changes nothing on the owner's logins, and a value the vendor raised arrives only through providers.dev |
| 5 | Models written into upstream's code (image, video and speech models, `devin/swe-1-6-slow`, Copilot's `compat.json`) | **Leave them**: they are executor behaviour, compiled in, fetched from nowhere | Moving them to our lists means edits in upstream files, one of them on a protected path |
| 6 | The three router settings of 6.5 | **Do not carry them over**; set again only what P6 shows is needed, from our own evidence | Copying them keeps today's behaviour for those Codex models and keeps a piece of router-for-me's list |
| 7 | The built-in Codex client catalog | **OpenAI's own public file**, with the one missing field filled from the same repository (5.6) | The owner's Codex login's answer, published in groundtruth, would put OpenAI's 1 MB text there |
| 8 | The order beside `sources-side-by-side` | **Listings first**: it is small, touches none of the code that redesign rewrites, and Provimux waits for it | Landing second costs only the version line and the structure record |
| 9 | The cap of runs per login per night (9.3) | **6**: what Codex's part of the first own-client night uses | Devin's backlog of about 35 models takes 6 Mondays. With 12 it takes 3 and uses twice the plan's allowance each Monday |
| 10 | The cap of money per week, and per run (9.3) | **10 dollars a week, 2 dollars a run** | 10 dollars is about 50 average runs. A lower cap stretches the backlog of the API-key routes; a higher one is the owner's money |
| 11 | Where the API-key part runs (9.7) | **On the Mac, in the night** | In GitHub Actions it does not depend on the Mac waking, and costs a second ledger, a second way to ask and money keys in a public repository's secrets |
| 12 | The quarterly check of values only we state (9.2, state 4) | **Keep it, at 90 days** | Without it a cache lifetime, which no other source states, is measured once and never again |
| 13 | On unattended nights the automatic checks replace the hand review between smoke and go (9.4) | **Yes**: a night nobody watches has nobody to review | Keeping the hand review means someone is awake on Monday at 23:10 |

## 12. What I ran, and what I did not verify

| Check | Result |
|---|---|
| A script over the three embedded files | the counts and fields of section 2.2; the Codex client file has 14 models and a `base_instructions` of about 17,000 characters per model |
| `grep` for router-for-me's hosts over the worktree | the six addresses, the plugin store, the release workflow, upstream's refresh script; in tests only `internal/managementasset/updater_provimux_test.go:107` |
| `grep` for file writes in `internal/registry` | none outside tests |
| `git log v8.0.60..v8.1.0` per file; `git log -L` per edited range | the commit counts of sections 6.1 and 6.4 |
| Upstream's tests, as is and with the two lists emptied (90 packages) | section 6.4 |
| `go run ./cmd/validate_codex_models` on `openai/codex`'s `codex-rs/models-manager/models.json` | rejected: `field "base_instructions" must be a non-empty string`; no other complaint reached |
| The vendors' public pages and open-source clients | section 3, the rows tagged [docs] |
| groundtruth's branch `own-clients` at `60e65ba`, read with `git show`: `docs/own-clients.md`, `docs/own-clients-RUN.md`, `harness/night.py`, `harness/pool.py`, `ci.yml`; `observed.json` on `main` | section 9: the run sizes, the costs, the audit gate, the 218 cache runs and 36 limit rows published today |

The test runs' result files (the 144 names, and the two runs as `go test -json`) stayed in the session's scratch folder, outside the repository, and are not kept; the two runs take about 10 minutes to repeat.

**Not verified.**

| Point | Who settles it |
|---|---|
| Whether `api.x.ai` answers the xAI login's token on its list paths | the owner, in P2, with his login |
| Devin's, Kimi's and Antigravity's list answers as they arrive today | P2's recorded answers |
| Whether Claude Code, Antigravity's client and Meta's client make the same list call | not needed for the build; stated in section 7 as unverified |
| That a new Devin id always follows the effort-suffix scheme | P2; a variant that does not is offered as its own model, which is safe |
| Homebrew's cleanup of the old version on the owner's machine | the owner, at R3 |
| Whether Codex's or Devin's own client shows its model list without a model request (9.1, way B) | a smoke run, if way B is wanted |
| That launchd runs the missed night when the Mac wakes late, and that the owner's Mac stays awake with its lid as it is | G6's dry-run Monday |
| Which providers let a key carry its own spending limit | G3; the ledger holds without it |
| How the chief's Tuesday task is started each week | the chief; it must not depend on the night having run |
| Which Kimi host is models.dev's "global" | D2 |
| The one test that fails on `e5cf5a2d5` as is, `TestScheduler_InterleavedMarkResult_DoesNotTriggerRebuild` in `sdk/cliproxy/auth`: not part of this design | |

## 13. Steps P2 and P4 as built

Built on branch `model-lists-live`, on top of this document's two commits, from `e5cf5a2d5`. P1, P3, P5 and P6 are not built: until they are, "the published list" and "the built-in list" of section 5 are one and the same thing, **the catalog** (router-for-me's lists, embedded and refreshed by upstream's updater). The tests' vendor answers are fixtures in the shape each vendor documents or its own client reads (`sdk/cliproxy/testdata/provimux-live/`); none was recorded with a login, and no request was sent with one.

### 13.1 Section 5.1, row by row

| Row | As built |
|---|---|
| The section's models | the catalog, plus learned models: what a held live list of a login of that type names and the catalog lacks. Recorded per type in the registry (`internal/registry/provimux_learned_models.go`) for its lookups: `LookupStaticModelInfoByChannel`, `LookupDevinModel` (the Devin executor reads a model's effort levels through it) and Copilot's section (Copilot routes only ids its section names). `GetXModels` stay without them, so nothing reaches a login without a list. Copilot and Antigravity learn from their own records |
| A model's fields | per field, the vendor's value when its list states one, else the catalog's (`mergeLiveModel`). A false Claude thinking flag counts (Claude states both flags); other vendors' falses do not. Routing fields stay the catalog's (type, owner, name, the router settings of 6.5). A field neither states stays unknown: no default output limit for Devin, no thinking for a Kimi or Gemini model the catalog lacks |
| A login with a live list | exactly the models its list names, plus Codex's image models, xAI's image, video and speech models and `devin/swe-1-6-slow`. Hidden Codex entries are kept only if the catalog has them. Copilot: the account's answer, as upstream routes it, now including models the section lacked. Antigravity: the catalog filtered by the account's ids, plus its other ids with the answer's name, window and output limit; ids upstream's catalog tool skips (`chat_*`, `tab_*` and its six named ids) are never added |
| A login without a live list | the catalog, without learned models. A Copilot account that has not answered yet (upstream's preset catalog) gets no learned model either. With `models.live-lists: false` no login, Copilot and Antigravity included, offers a learned model |
| On top, unchanged | `excluded-models`, `oauth-model-alias`, prefixes, config and plugin models: the live list is applied before them in `service_models.go`, as for the patch's three types. A Gemini key with its own `models` in the config keeps them |

### 13.2 The rest of section 5

- **5.2** Live: at start, every 3 h, on `POST /v8/management/models/refresh`, when a login is added or replaced; after a failure 10 minutes. A list read from the disk cache counts as due, so every login is asked again at start. Copilot (2 h) and Antigravity keep upstream's own cadence.
- **5.3** A list replaces the held one only when the answer is 200, well-formed, complete (Claude and Kimi: no `has_more`; Gemini: every page, at most 20; xAI: both answers) and names a model; above 8 MB is a failed fetch. A failure is recorded as a code and logged as `fetch failed (http_status 401); keeping the list of <date>`. A list not refreshed for 7 days stops counting at once and its login takes the catalog, with one warning per list; the status endpoint shows it as `expired`. What an empty or failed Copilot or Antigravity answer does stays upstream's.
- **5.4** `internal/provimux/modellists`: `model-lists/live-<hash>.json` beside the console's `static/` folder (`WRITABLE_PATH`, else the config file's folder), mode 0600 in a 0700 folder, temporary file and rename. A file holds the login type, a hash of the account identity, the fetch time, the ids and the vendor's descriptions. A damaged file, or one of another account or login type, is ignored with one warning and replaced by the next successful fetch; a read-only disk gives one warning and memory only. It is read the first time a login registers its models, before any network request, so a start without network serves the lists of the last run.
- **5.5** The log line is `model list for devin login <auth_index>: live, 53 models, 1 not in the catalog yet (devin/gemini-4-neon)`; until P3 the comparison is with the catalog, not providers.dev. `GET /v8/management/provimux/model-lists` is in `pui13/openapi.yaml` with three examples. `GET /provimux/live-lists` stays; its `providers` grew to the seven types this fetcher asks.

### 13.3 What did not hold as written

| Design | What happened |
|---|---|
| "On by default" (P2) with upstream's tests passing | Upstream's tests build configs without the key. Two of them pin that an Antigravity model only the account lists is not registered (`TestRegisterModelsForAuth_AntigravityFetchesWebSearchCapability`, `TestAntigravityDynamicRegistrationLifecycle`). So an unset `live-lists` is on except in a test binary (`testing.Testing()`), the rule 6.4 sets for the catalog files. Provimux's own tests set the key |
| A Copilot answer must not be empty (5.3) | Upstream's tests pin an empty Copilot answer as an authoritative empty catalog (`TestCopilotColdDiscoveryOutcomes`, `TestCopilotCatalogFetchOutcomesAndCredentialPreservation`, `TestCopilotCatalogCommitPreservesConcurrentAuthEdits`). Kept upstream's |
| Disk cache and 7-day limit for every live list (5.3, 5.4) | Built for the seven types this fetcher asks. Copilot and Antigravity keep upstream's: Copilot's account catalog is not on disk and lives 2 h, so after a start without network a Copilot login offers the catalog's Copilot models (upstream's preset) until GitHub answers, and one whose answer is older than 2 h offers none; Antigravity's hints are already kept in the auth file by upstream |
| The live requests go through each executor's `HttpRequest` (6.1) | Not for Devin: its `PrepareRequest` sets `application/connect+proto` on every request, and `GetCliModelConfigs` is a unary call. It is sent as upstream's own tool sends it (`application/proto`), through the executor's proxy-aware Devin client |
| The patch's existing tests still pass (P2) | Two encoded rules this design changes and were updated: "catalog values win" (`TestFilterByLiveModelList`, now per field the vendor's) and a Gemini login being uncovered (Gemini keys are). Configs in them now set the key, which became a pointer so that `live-lists: false` survives a config save |
| Section 6.1's list of upstream lines | Also edited, one line each unless said: `LookupStaticModelInfoByChannel` (2), `LookupDevinModel`, `GetCopilotModels`, the Copilot refresher's fetch, the Antigravity model filter, `sdk/auth`'s Antigravity fetch (the answer's names and limits, which upstream's runtime drops), the Gemini key registration (3 lines), the watcher's change check, and the management refresh |
| The status endpoint's shape (5.5) | As designed, plus `login_types`, `live.expired`, and per-model `limits`, `modalities`, `reasoning_levels` and `sources` for six fields. `?auth_index=` returns that login only. `published.state` is `off` and `built_in` is null until P3 and P1 |
| Kimi's answer (section 3) | Kimi's own CLI adds image and video input to `kimi-k2*` whatever the flags say, so only a true flag counts as a statement |
| Gemini's answer | Only `generateContent` models are listed: the answer also has embedding and other models |
