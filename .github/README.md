# Provimux

Provimux is one local endpoint for your AI subscriptions and API keys. Sign in with your Claude and Codex
subscriptions, add your API keys and an OpenRouter key, and point your tools at `http://127.0.0.1:8317`. Provimux is a
distribution of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI): everything CLIProxyAPI does, plus the
features below.

![A diagram. On the left, "Your tools": pi, Codex CLI, Claude Code, and any OpenAI-, Claude- or Gemini-format client. They point at the middle box, "Provimux, http://127.0.0.1:8317", which lists: which account serves each model, and which one answered; each login's own model list; OpenRouter caching, sessions and limits; the right context and output limits. Provimux points at "Your accounts" on the right: Claude subscription (oauth), Claude API key (apikey), Codex subscription (oauth), and OpenRouter key (apikey, openrouter.ai).](provimux/media/overview.png)

What Provimux adds:

- [Which account serves each model, and which one answered](#which-account-serves-each-model-and-which-one-answered)
- [Live model lists for each login](#live-model-lists-for-each-login)
- [OpenRouter built in](#openrouter-built-in)
- [The right context and output limits for configured models](#the-right-context-and-output-limits-for-configured-models)
- [Devin: real output limits, and a quota pause per model](#devin-real-output-limits-and-a-quota-pause-per-model)
- [The longer prompt cache on OpenAI API keys](#the-longer-prompt-cache-on-openai-api-keys)
- [Codex WebSocket: a full replay stays a replay](#codex-websocket-a-full-replay-stays-a-replay)
- [Works with pi](#works-with-pi)

Then: [Install](#install) · [Versions](#versions) · [Requests to providers](#requests-to-providers) ·
[Built on CLIProxyAPI](#built-on-cliproxyapi) · [License](#license)

## Which account serves each model, and which one answered

**What you get.** The OpenAI-format model list can tell you, for each model, which of your accounts serve it: the
provider, whether it is a subscription login (`oauth`) or an API key (`apikey`), its priority, and for an API key the
host it calls. Every response served by an account then says which kind of account answered it.

**Why it matters.** The same model can be reached through a subscription and through a paid API key, and the two
differ in limits, prompt-cache lifetime and price. A Claude login keeps the prompt cache for an hour, a Claude API key
for five minutes by default. Without this, a client behind the proxy cannot tell which one it is using.

![A terminal. The first command is curl 'http://127.0.0.1:8317/v1/models?include=accounts' piped to jq, printing each model id and its accounts. claude-opus-5-5 has two: {"provider":"claude","kind":"oauth","priority":10}, highlighted as the one tried first, and {"provider":"claude","kind":"apikey","priority":0}. gpt-6.1-terra has one: {"provider":"codex","kind":"oauth","priority":0}. anthropic/claude-opus-5.5 has one: {"provider":"openai-compatible-openrouter","kind":"apikey","priority":0,"host":"openrouter.ai"}. The second command sends a chat completion with curl -si and greps the X-CPA-AUTH-KIND header: "X-Cpa-Auth-Kind: oauth", highlighted.](provimux/media/model-accounts.png)

**How to use it.** Nothing to turn on.

- `GET /v1/models?include=accounts` adds an `accounts` array to every model, highest priority first, so the first
  entries are the ones Provimux tries first. Without the parameter the list is unchanged. Only this plain
  OpenAI-format list has it: the Codex-format list (`/v1/models?client_version=…`) and the Claude-format list carry no
  `accounts`.
- Each account has `provider`, `kind` (`oauth` or `apikey`) and `priority`. An API key with a base URL also has
  `host`, which names the service even when you gave the entry another name (`openrouter.ai` for an entry called
  `openrouter`, `my-router` or anything else). A credential's own email, file and key fields are left out. The name
  you chose for an OpenAI-compatible entry is shown: its `provider` is `openai-compatible-<name>`, so clients that can
  read the model list see that name, including an email if you named the entry with one.
- A response served by an account (a completion, for example) carries an `X-CPA-AUTH-KIND` header (`oauth` or
  `apikey`) for the account that answered, next to CLIProxyAPI's `X-CPA-TRACE-ID`. The model list itself has no such
  header. When a request is retried on another account before the answer starts, the header names the one that
  answered. Browsers can read it, like the trace ID.
- On the Codex WebSocket (`/v1/responses`), which has response headers only once per connection, each turn's
  `response.created` event carries `"cpa_auth_kind": "oauth"` or `"apikey"`.

```bash
curl -s 'http://127.0.0.1:8317/v1/models?include=accounts' -H "Authorization: Bearer $KEY"
```

Priorities are CLIProxyAPI's: set `priority` on an API-key entry, or in a login's file, and the higher value is tried
first. For example a Claude subscription at `10` and a Claude API key at `0` uses the subscription and falls back to
the key.

## Live model lists for each login

**What you get.** Each Claude, Codex and Meta login registers the models its own account lists, not only the shared
catalog. Models the provider has retired, or doesn't offer to your account, disappear from the list, and new models
appear on the day the provider lists them.

**Why it matters.** The shared catalog is edited by hand. It can list a model a day late, keep a retired model that
then answers 404, or list a model your plan is refused.

![A diagram titled "models.live-lists: each login registers what its own account lists", with two columns, "The shared catalog" and "What the login serves". Claude login: the catalog has claude-opus-5-5, claude-sonnet-5-5, claude-haiku-5-5, claude-opus-4-1-20250805 and claude-3-7-sonnet-20250219; the login serves the first three, and the two older models are struck through. Codex login: the catalog has gpt-6.1-terra and gpt-6-chat-thinking; the login serves gpt-6.1-terra, gpt-6-chat-thinking is struck through, and gpt-6.2-sol is added in green. The legend reads: in both: the catalog's values; not in the login's list: hidden; not in the catalog yet: added from the provider.](provimux/media/live-lists.png)

**How to turn it on.** It is off by default.

```yaml
models:
  live-lists: true
```

- Provimux fetches each login's list in the background: at startup, every 3 hours, and on
  `POST /v8/management/models/refresh`. Requests never wait for it.
- A login registers only the catalog models its list contains. Models the catalog doesn't have yet are added from
  the provider's own description. For Claude that is the context window, output limit, reasoning levels and image
  input. For Codex it is the context window, reasoning levels and input types, but no output limit: Codex's list does
  not give one, so a new Codex model has none until the catalog lists it. Meta lists only model ids, so Meta logins
  are only filtered.
- For models the catalog already has, the catalog's values are kept. Hidden Codex entries are never added, and
  CLIProxyAPI's Codex image models stay.
- Until a list arrives, or while fetches fail (retried every 10 minutes), the catalog or the last good list is used:
  a provider outage never empties a login. A list belongs to the login's account, so a login file replaced by another
  account never keeps the old list.
- API keys keep the catalog. Changing `live-lists` in a running instance applies at once.

## OpenRouter built in

**What you get.** An OpenRouter key works like a built-in provider: prompt caching for models that cache only when
asked (Claude among them), one conversation kept on one upstream so its cache keeps being read, and each model's real
context window, output limit and reasoning levels.

**Why it matters.** Through a plain OpenAI-compatible entry, a Claude model on OpenRouter is cached only when the
client itself sends OpenAI chat-format requests with `cache_control` markers: CLIProxyAPI forwards those as they are.
A Claude-format client's markers are lost when its request is translated, and a client that sends no markers gets
none, so every such request pays for the whole prompt. Every model also gets the same generic limits and the levels
low, medium and high, with `max` lowered to `high`.

![A diagram titled "An openai-compatibility entry whose base URL is openrouter.ai". On the left, "Your client sends": POST /v1/chat/completions with model anthropic/claude-opus-5.5 and four messages, system "You are a coding agent…", user "Read main.go", assistant "…" and user "Now add a test", with plain string contents. On the right, "OpenRouter receives": a header "X-Session-Id: 00000000-0000-4000-8000-000000000001", same for the whole conversation, and the same messages, where the system message and the last user message now have text parts with "cache_control": {"type": "ephemeral"}, highlighted. Below: Limits and levels, from OpenRouter's model list: context 1,000,000, output 128,000, reasoning low, medium, high, xhigh, max.](provimux/media/openrouter.png)

**How to turn it on.** Nothing to configure: any `openai-compatibility` entry whose base URL is on `openrouter.ai`
gets it.

```yaml
api-keys:
  openai-compatibility:
    - name: "openrouter"
      base-url: "https://openrouter.ai/api/v1"
      keys:
        - api-key: "sk-or-v1-..."
      models:
        - name: "anthropic/claude-opus-5.5"
        - name: "openai/gpt-6.1-terra"
```

| Part | What Provimux does |
|---|---|
| Prompt cache | A request without cache markers gets them on the last system message and on the latest user, tool or assistant message that can carry one (text, file parts and assistant prefills). An OpenAI chat-format request that already has markers keeps them where your client put them, as CLIProxyAPI does; Provimux adds none, though for `openai/*` models it may add `mode: "explicit"` to the request's `prompt_cache_options` (next row). A Claude-format request loses its markers in translation, so it gets Provimux's: a marker your client put on an early user message ends up on the last turn instead, and a 1-hour lifetime asked for anywhere in the request is kept. |
| OpenAI cache lifetime | For `openai/*` models, a client's `prompt_cache_options.ttl` (for example `"12h"`) is sent the way OpenRouter applies it, with `mode: "explicit"`; a `mode` the client sent is kept. |
| Session | An `X-Session-Id` header, stable for the conversation, so OpenRouter keeps it on one upstream. An `X-Session-Id` set in the entry's `headers` in your config wins, then your client's own `X-Session-Id`, then the one Provimux derives. No `X-Session-Id` is sent only when all three are absent: no header in your config, no header from your client, and no session Provimux can derive from the conversation. |
| Limits | OpenRouter's public model list is fetched in the background and refreshed; each entry gets its context window and output limit from it. `max-context-length` in your config still wins. |
| Reasoning levels | The model's own levels (for example `low` to `max`), when your config sets no `thinking` for it. |
| Owner | `owned_by` is the model's vendor (`anthropic` for `anthropic/claude-opus-5.5`), not the entry's name. |

## The right context and output limits for configured models

**What you get.** Models you declare under an API key (Claude, Codex, Gemini, Vertex, xAI, Meta), for example an
alias, report the same context window and output limit as the catalog's model they point to.

**Why it matters.** Clients size their conversations and compaction from these numbers. Without the catalog's
values, Codex-format clients fell back to a generic template and Claude-format clients to made-up defaults:

| An alias of `claude-opus-5-5` on a Claude API key | Context window | Output limit |
|---|---|---|
| CLIProxyAPI 8.0.77 (Codex-format model list) | 500,000 | none |
| Provimux | 1,000,000 | 128,000 |

**How to turn it on.** Nothing to configure. `max-context-length` on a model still wins.

```yaml
api-keys:
  claude:
    - name: claude-key
      keys:
        - api-key: "sk-ant-..."
      models:
        - name: "claude-opus-5-5"
          alias: "opus"
```

- When several accounts serve one alias, the list reports the **smallest** limit among them. `[1m]` variants keep
  their 1M window, and thinking suffixes are ignored when looking the model up.
- When one of them has no known limit, no shared limit is published: the model lists send `null` rather than a
  made-up default.
- OpenAI-compatible entries have no catalog; OpenRouter entries get their limits from
  [OpenRouter built in](#openrouter-built-in).

## Devin: real output limits, and a quota pause per model

**Real output limits.** CLIProxyAPI lists every Devin model with a 64,000-token output limit and enforces it.
Provimux's Devin list carries each model's own limit, read from Devin's model configuration. For example:
`claude-opus-5-5` and `swe-2` 128,000, `kimi-k2-6` 8,192, `kimi-k2-7` 16,000, `kimi-k3` 131,072,
`deepseek-v4-1-flash-1017` 384,000, `gemini-4-argon` 1,000,000. A model with several variants takes the smallest of
them.

The list ships inside the binary. By default Provimux, like CLIProxyAPI, replaces it with CLIProxyAPI's published
list, which still says 64,000. To use the real limits, start with `--local-model`, or generate a list and point the
config at it:

```bash
go run ./cmd/fetch_devin_models --output /path/to/devin_models.json
```

```yaml
models:
  devin-catalog: "/path/to/devin_models.json"
```

**A quota pause per model.** When a Devin login runs out of its daily or weekly quota, CLIProxyAPI pauses the whole
login until the quota resets, including the models Devin keeps serving without a quota (`swe-2`, `swe-2-flash`). With
this option the pause stays on the model that was refused, the same option the Codex and Claude providers have:

```yaml
oauth:
  providers:
    devin:
      model-level-cooling: true
```

It is off by default. One known limit: with the option on, Provimux may not learn the quota's reset time, so a metered
model retries with a growing backoff (1 second up to 30 minutes, one quick refused request each time) instead of
waiting for the reset.

## The longer prompt cache on OpenAI API keys

**What you get.** A client that asks for a longer prompt-cache lifetime on an OpenAI API key gets it:
`prompt_cache_options.ttl` (for example `"12h"`) is forwarded on Codex API-key credentials. OpenAI's default is 30
minutes on GPT-5.6 and later.

**Why it matters.** CLIProxyAPI removes `prompt_cache_options` for every Codex credential. That is required for
ChatGPT logins, which answer 400 to it, but an API key supports it.

**How to use it.** Send it in a Responses request; nothing to configure.

```json
{ "model": "gpt-6.1-terra", "prompt_cache_options": { "ttl": "12h" }, "input": "…" }
```

Only `ttl` is carried, and only when the client sends it as a non-empty string and the request arrives in the
Responses format. No default is added. Subscription logins are unchanged.

## Codex WebSocket: a full replay stays a replay

**What you get.** On the `/v1/responses` WebSocket, a client that sends its whole conversation again (without
`previous_response_id`) gets that conversation answered, not a merged one.

**Why it matters.** CLIProxyAPI treats such a request as an addition to the previous turn unless it contains
assistant or tool items. A client that re-sends its first request unchanged, for example to keep the prompt cache
warm, had it merged: previous input, previous output, then the same input again. The model saw the conversation
twice and the request was billed at about twice the input.

**How to turn it on.** Always on. A request with no `previous_response_id`, the same `model` as the previous request,
and an input that starts with every input item of the previous request, in order, replaces the transcript. An
incremental request that names no model still appends, and pending tool calls answered by a replay are kept.

## Works with pi

Provimux works with any client CLIProxyAPI works with. For [pi](https://pi.dev), two extensions read what
[which account serves each model](#which-account-serves-each-model-and-which-one-answered) reports:

- [pi-cliproxyapi-provider](https://github.com/ngvoicu/pi-cliproxyapi-provider) adds Provimux (or CLIProxyAPI) as a
  pi provider. Each model's name ends with where it comes from: `sub` for a subscription login, `api` for an API key,
  then what it falls back to. pi 1.5.2 or newer shows the name on every line of `/model`, after the id.
- [pi-cache-keeper](https://github.com/ngvoicu/pi-cache-keeper) keeps the prompt cache warm and compacts from it. Its
  footer shows `sub` or `api` for the account that answered last, and warns when an API key answered for a model you
  reach through a subscription.

![A terminal titled "pi 1.5.2 · /model, with pi-cliproxyapi-provider". Each line of pi's model list shows the model id, its name with where it comes from in parentheses, and [cliproxyapi]: "claude-opus-5-5 (Claude Opus 5.5 · Anthropic sub, fallback api)", selected and current; "claude-sonnet-5-5 (Claude Sonnet 5.5 · Anthropic sub, fallback api)"; "gpt-6.1-terra (GPT 6.1 Terra · OpenAI sub)"; "anthropic/claude-opus-5.5 (anthropic/claude-opus-5.5 · OpenRouter api)". Below the list, for the selected model: "Model Name: Claude Opus 5.5 · Anthropic sub, fallback api". Then the last line of pi's footer with pi-cache-keeper, in two states: "cache warm · 59m left · sub · window ✓ · cache short ✓ · long ✓ (in use) · price ✗", and "cache warm · 4m left · api · window ✓ · cache short ✓ (in use) · long ✓ · price ✗ · ⚠ answered by an API key".](provimux/media/pi.png)

```bash
pi install git:github.com/ngvoicu/pi-cliproxyapi-provider
pi install git:github.com/ngvoicu/pi-cache-keeper
```

Their READMEs describe the settings.

## Install

Provimux uses the same `config.yaml`, auth folder (`~/.cli-proxy-api`) and management console as CLIProxyAPI, so an
existing CLIProxyAPI setup works as it is: stop CLIProxyAPI and start Provimux in its place.

**Release archives:** download the archive for your system from
[Releases](https://github.com/ngvoicu/provimux/releases):

| System | Archive | Binary |
|---|---|---|
| macOS, Apple silicon | `provimux_<version>_darwin_aarch64.tar.gz` | `provimux` |
| macOS, Intel | `provimux_<version>_darwin_amd64.tar.gz` | `provimux` |
| Windows, x64 | `provimux_<version>_windows_amd64.zip` | `provimux.exe` |
| Windows, Arm | `provimux_<version>_windows_aarch64.zip` | `provimux.exe` |
| Linux, x64 | `provimux_<version>_linux_amd64.tar.gz` | `provimux` |
| Linux, Arm | `provimux_<version>_linux_aarch64.tar.gz` | `provimux` |
| Linux, x64, portable | `provimux_<version>_linux_amd64_no-plugin.tar.gz` | `provimux` |
| Linux, Arm, portable | `provimux_<version>_linux_aarch64_no-plugin.tar.gz` | `provimux` |
| FreeBSD, x64 | `provimux_<version>_freebsd_amd64.tar.gz` | `provimux` |
| FreeBSD, Arm, no plugins | `provimux_<version>_freebsd_aarch64_no-plugin.tar.gz` | `provimux` |

`<version>` is the Provimux version, for example `8.0.77-pmx.1`; `checksums.txt` in each release lists the SHA-256 of
every archive. The default Linux builds need glibc 2.17 or newer; the Linux `_no-plugin` builds also run on musl-based
or older systems such as OpenWrt. `_no-plugin` builds do not support plugins.

**Docker** (linux/amd64 and linux/arm64):

```bash
docker run -d --name provimux -p 8317:8317 \
  -v "$PWD/config.yaml:/CLIProxyAPI/config.yaml" \
  -v "$PWD/auths:/root/.cli-proxy-api" \
  ghcr.io/ngvoicu/provimux:latest
```

The image uses CLIProxyAPI's paths: the config at `/CLIProxyAPI/config.yaml`, the auth folder at
`/root/.cli-proxy-api`, logs in `/CLIProxyAPI/logs` and plugins in `/CLIProxyAPI/plugins`, so a CLIProxyAPI
`docker-compose.yml` works with only the image name changed. Each release is also tagged with its version
(`ghcr.io/ngvoicu/provimux:8.0.77-pmx.1`).

**From source** (Go):

```bash
git clone https://github.com/ngvoicu/provimux
cd provimux
go build -o provimux ./cmd/server
./provimux --config /path/to/your/config.yaml
```

A fresh checkout has no `config.yaml`, only `config.example.yaml`. Pass your existing CLIProxyAPI config with
`--config`, or copy the example (`cp config.example.yaml config.yaml`), edit it, and start with
`--config config.yaml`.

For logins, the configuration reference and the console, CLIProxyAPI's
[documentation](https://github.com/router-for-me/CLIProxyAPI#readme) applies; `config.example.yaml` in this
repository includes the Provimux options.

## Versions

A Provimux version names the CLIProxyAPI release it is built on: `v8.0.77-pmx.1` is CLIProxyAPI 8.0.77 with Provimux
build 1. A new Provimux release follows each CLIProxyAPI release.

The startup banner and the `X-CPA-VERSION` response header show the Provimux version, with the CLIProxyAPI release it
is based on. The version sent to providers is CLIProxyAPI's (`8.0.77` in this example), so providers see the same
client as with CLIProxyAPI.

## Requests to providers

The requests Provimux sends to providers are the same as CLIProxyAPI's, except where a feature above says otherwise:

- for OpenRouter entries: the cache markers, the cache lifetime in explicit mode, and the `X-Session-Id` header;
  Provimux also reads OpenRouter's public model list;
- on OpenAI API keys: a client's `prompt_cache_options.ttl`;
- on the Codex WebSocket: the transcript of a full replay;
- for Devin: the output limit of each model, when the real limits are in use;
- with `models.live-lists: true`: each Claude, Codex and Meta login asks its provider for its model list (at startup
  and every 3 hours).

## Built on CLIProxyAPI

Provimux is built on [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) by
[Router-For.ME](https://github.com/router-for-me), started by [Luis Pater](https://github.com/luispater). Nearly
everything Provimux does is CLIProxyAPI's work: the logins, the providers, the translation between formats, the
routing and the console. Provimux adds the features above on top of each CLIProxyAPI release.

## License

MIT, like CLIProxyAPI. See [LICENSE](../LICENSE).
