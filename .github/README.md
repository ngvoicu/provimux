<h1>
  <a href="https://providers.dev"><picture>
    <source media="(prefers-color-scheme: dark)" srcset="provimux/media/brand/lockup-dark.svg">
    <img alt="Provimux by providers.dev" src="provimux/media/brand/lockup-light.svg" width="224" height="56">
  </picture></a>
</h1>

<p>
  <a href="https://github.com/ngvoicu/provimux/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/ngvoicu/provimux?label=release&amp;color=4338ca"></a>
  <a href="../LICENSE"><img alt="License: MIT" src="https://img.shields.io/github/license/ngvoicu/provimux?color=4338ca"></a>
  <a href="https://github.com/router-for-me/CLIProxyAPI"><img alt="Built on CLIProxyAPI" src="https://img.shields.io/badge/built%20on-CLIProxyAPI-4338ca"></a>
</p>

**One key for every AI account.**

A self-hosted AI gateway: one OpenAI-, Claude- and Gemini-compatible endpoint for your API keys and accounts, with the
prompt cache kept in use and the account behind every answer shown.

**Provimux**, by [providers.dev](https://providers.dev), is a self-hosted AI gateway for you or your whole team. Your
subscriptions and API keys behind one endpoint, open to everyone you choose. Pools of models from every provider, with
the prompt cache kept in use. Run it on your own machine or on a server your team shares, give each person their own
client key, and point their tools at it. To keep the prompt cache in use, Provimux adds cache markers on OpenRouter,
keeps each conversation on one upstream, and forwards the longer cache lifetime on OpenAI API keys. It tells
you which accounts serve each model and which one answered, and its console shows why a route is paused. Provimux is an open-source (MIT) distribution of
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) by router-for-me, tested against each CLIProxyAPI release.

**Provimux** · the endpoint (this repository) &nbsp;·&nbsp; **[provimux-ui](https://github.com/ngvoicu/provimux-ui)** · its
web console &nbsp;·&nbsp; **[providers.dev](https://providers.dev)**

```bash
brew install ngvoicu/tap/provimux
brew services start provimux
```

Then open the console at `http://127.0.0.1:8317/management.html` (on the same machine; from another machine, use the
server's address, see [For a team](#for-a-team)). Docker, release archives and building from source:
[Install](#install). An existing CLIProxyAPI setup works as it is.

## What Provimux adds

**Cache: you pay for a prompt once and read it after that.**

- **[OpenRouter built in](#openrouter-built-in).** Cache markers added where the client sent none (Claude among
  them), a 1-hour lifetime kept through translation, one upstream per conversation so the cache keeps being read, and
  each model's real limits and reasoning levels for an OpenRouter key.
- **[The longer prompt cache on OpenAI API keys](#the-longer-prompt-cache-on-openai-api-keys)**: a client's
  `prompt_cache_options.ttl` (for example `"12h"`) is forwarded instead of removed.
- **[A Codex WebSocket replay that stays a replay](#codex-websocket-a-full-replay-stays-a-replay)**: a re-sent
  conversation is no longer merged into a double transcript billed at about twice the input.

**Clarity: you see which account has which models, which one answered, and why a route is paused.**

- **[Which account serves each model, and which one answered](#which-account-serves-each-model-and-which-one-answered).**
  The model list names, per model, the API keys (`apikey`) and logins (`oauth`) behind it, by priority, and every
  response says which one answered.
- **[Its own console pages](#the-console-provimux-ui)** in provimux-ui: **Accounts**, **Models** (every name you can
  send and the accounts behind it, by priority, with the cooldown recorded and its reason) and **Savings**, read from
  [Provimux's own endpoints](#model-routes-and-live-list-status-for-the-console).
- **[Live model lists for each login](#live-model-lists-for-each-login).** Each Claude, Codex and Meta login serves
  what its own account lists: retired models disappear, new ones appear the day the provider lists them.
- **[Model lists for API keys that keep themselves up to date](#model-lists-for-api-keys).** A key with its own
  models (every OpenAI-compatible entry, such as OpenRouter) also offers the models its service lists, without a
  save in the console; `excluded-models` and `fixed-models` keep it to what you want.
- **[Serve only my names](#serve-only-my-names).** One switch, off by default: only the names you set (your aliases,
  and the names you keep) are listed and served; every other name is treated as excluded, and a tool that asks for one
  is told why.
- **[The right context and output limits](#the-right-context-and-output-limits-for-configured-models)** for the
  models you declare under an API key, and **[Devin's real output limits](#devin-real-output-limits-and-a-quota-pause-per-model)**
  with a quota pause per model.
- **[Savings](#savings-what-prompt-caching-was-worth-and-what-each-key-cost)**: daily usage totals kept across restarts, valued
  at OpenRouter's list prices: what prompt caching was worth, subscription against API key, what the paid keys cost.
- **[Works with Pi](#works-with-pi)**, through two extensions that show which account answered.

![A diagram headed "Your subscriptions and API keys behind one endpoint, open to everyone you choose". On the left, "Your tools, and your team's": Pi, Codex CLI, Claude Code, and any OpenAI-, Claude- or Gemini-format client. They point at the middle box, Provimux at http://&lt;your-server&gt;:8317, which lists: which account serves each model, and which one answered; each login's own model list; OpenRouter caching, sessions and limits; the right context and output limits. Provimux points at "Your accounts" on the right: Claude subscription (oauth), Claude API key (apikey), Codex subscription (oauth), and OpenRouter key (apikey, openrouter.ai).](provimux/media/overview.png)

Then: [Install](#install) · [For a team](#for-a-team) · [The console](#the-console-provimux-ui) ·
[Versions and releases](#versions-and-releases) · [Features in detail](#features-in-detail) · [Requests to providers](#requests-to-providers) ·
[Support](#support) · [Built on CLIProxyAPI](#built-on-cliproxyapi) · [License](#license)

## Install

Provimux uses the same `config.yaml`, auth folder (`~/.cli-proxy-api`) and port (8317) as CLIProxyAPI, so an existing
CLIProxyAPI setup works as it is: stop CLIProxyAPI and start Provimux in its place.

### Homebrew

macOS (Apple silicon and Intel) and Linux (x64 and Arm), from the tap [ngvoicu/homebrew-tap](https://github.com/ngvoicu/homebrew-tap):

```bash
brew install ngvoicu/tap/provimux
brew services start provimux
```

The config is `$(brew --prefix)/etc/provimux.conf`, created from `config.example.yaml` on first install and never
overwritten. To reuse an existing Homebrew CLIProxyAPI setup, stop its service (both listen on port 8317 by default) and
point Provimux at its config:

```bash
brew services stop cliproxyapi
ln -sf "$(brew --prefix)/etc/cliproxyapi.conf" "$(brew --prefix)/etc/provimux.conf"
brew services start provimux
```

### Docker

linux/amd64 and linux/arm64:

```bash
docker run -d --name provimux -p 8317:8317 \
  -v "$PWD/config.yaml:/CLIProxyAPI/config.yaml" \
  -v "$PWD/auths:/root/.cli-proxy-api" \
  ghcr.io/ngvoicu/provimux:latest
```

The image uses CLIProxyAPI's paths: the config at `/CLIProxyAPI/config.yaml`, the auth folder at
`/root/.cli-proxy-api`, logs in `/CLIProxyAPI/logs` and plugins in `/CLIProxyAPI/plugins`, so a CLIProxyAPI
`docker-compose.yml` works with only the image name changed. Each release is also tagged with its version
(`ghcr.io/ngvoicu/provimux:8.0.79-pmx.1`).

The image's default `TZ` is upstream's `Asia/Shanghai`, and [Savings](#savings-what-prompt-caching-was-worth-and-what-each-key-cost)
keeps its totals per day of that zone: set `TZ` (e.g. `-e TZ=Europe/Bucharest`) before the first run, since days
cannot be re-cut later.

### Release archives

Download the archive for your system from [Releases](https://github.com/ngvoicu/provimux/releases):

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

`<version>` is the Provimux version, for example `8.0.79-pmx.1`; `checksums.txt` in each release lists the SHA-256 of
every archive. The default Linux builds need glibc 2.17 or newer; the Linux `_no-plugin` builds also run on musl-based
or older systems such as OpenWrt. `_no-plugin` builds do not support plugins.

### From source

With Go:

```bash
git clone https://github.com/ngvoicu/provimux
cd provimux
go build -o provimux ./cmd/server
./provimux --config /path/to/your/config.yaml
```

A fresh checkout has no `config.yaml`, only `config.example.yaml`. Pass your existing CLIProxyAPI config with
`--config`, or copy the example (`cp config.example.yaml config.yaml`), edit it, and start with
`--config config.yaml`.

For logins and the configuration reference, CLIProxyAPI's
[documentation](https://github.com/router-for-me/CLIProxyAPI#readme) applies; `config.example.yaml` in this
repository includes the Provimux options.

### Restarts and upgrades

A restart no longer cuts the answers being written. On a stop or restart (`brew services restart provimux`,
`docker stop`, a systemd stop, Ctrl+C), Provimux lets the HTTP answers in flight finish for up to 25 seconds and
then exits; with nothing in flight it exits at once. This holds for existing installs too, with nothing to edit:
a `shutdown-timeout` of 0 no longer means no wait, and one set above 25 still gives its longer wait.

- New connections are refused during the wait; a client that retries reaches the new process.
- Not waited for: a Responses WebSocket turn (Pi and the Codex CLI use these on some routes) and an answer
  through the AI Studio relay.
- What is still running when the wait ends is cut. A second Ctrl+C does not shorten the wait.

`server.restart-drain-seconds`, a whole number from 0 to 30, sets the wait, and 0 turns it off. A number above 30
is taken as 30 and one below 0 as 0, with a warning in the log.

```yaml
server:
  restart-drain-seconds: 0
```

The service manager has to allow the wait. The Homebrew service allows 40 seconds from Homebrew 6.0.13; an older
Homebrew leaves launchd's own limit, about 5 seconds on macOS 27. `docker stop` kills a container after 10
seconds: give it more with `docker stop -t 40`, `docker run --stop-timeout 40`, or `stop_grace_period: 40s` in
Compose.

### Your accounts

Add API keys under `api-keys` in your config: `claude`, `codex` (OpenAI), `gemini`, `vertex`, `xai`, `meta`, and
`openai-compatibility` for OpenRouter and other OpenAI-compatible services. For a company, use API keys and cloud
accounts (for example Vertex), billed to the company under its own agreement with the provider.

Provimux also accepts subscription logins (`oauth`) for your own account, for your own use. Whether a provider allows
a third-party tool to use its subscription login is that provider's decision: Anthropic's terms do not allow it, and
OpenAI has not authorized it. Read your provider's terms before you add a login; the account is yours and so is the
risk. Provimux is not affiliated with or endorsed by Anthropic or OpenAI.

## For a team

Your subscriptions and API keys behind one endpoint, open to everyone you choose. Run Provimux on a server the team can
reach, with [Homebrew](#homebrew), a [release archive](#release-archives) or the [Docker image](#docker) each release
publishes (`ghcr.io/ngvoicu/provimux`), and point everyone's tools at `http://<your-server>:8317`. It listens on every
interface by default (`server.host: ""`); `server.host: "127.0.0.1"` keeps it to the machine itself.

- **One client key per person**, under `access.api-keys`. Each person's tools send their key as
  `Authorization: Bearer <key>`, and removing it from the list shuts out only that person.
  [Savings](#savings-what-prompt-caching-was-worth-and-what-each-key-cost) shows each key by its last 4 characters
  (for keys of 16 characters or more), so give every key a different ending.

  ```yaml
  access:
    api-keys:
      - "replace-with-a-long-random-key-1"
      - "replace-with-a-long-random-key-2"
  ```

- **The console from other machines.** Until `management.allow-remote: true`, the management API and the console
  answer only requests from the server itself. Every request, local or remote, needs the management key,
  `management.secret-key` (hashed on startup; while it is empty the management API is off), and five wrong keys from
  one address block that address for 30 minutes. Give the management key only to whoever runs the server: it can
  read and change the whole config, every client key included.
- **TLS.** Client keys and the management key travel with every request, so outside a network you trust, serve
  HTTPS: either Provimux's own `server.tls` (`enable: true`, with `cert` and `key` as paths to PEM files), or a
  reverse proxy in front of it. Behind a reverse proxy on the same machine, every request reaches Provimux from
  127.0.0.1 and the console would count it as local: set `server.trusted-proxies` to the proxy's address so that
  Provimux uses the client's real address.

The curl examples below use `$PROVIMUX` for the server's address (`http://127.0.0.1:8317` on the same machine).

## The console: provimux-ui

Provimux's web console is [provimux-ui](https://github.com/ngvoicu/provimux-ui), the CLI Proxy API Management Center
in the providers.dev look, with Provimux's own pages: **Accounts**, every subscription and API key in one list, whether
each works now and its priority; **Models**, every model name you can send, with the accounts that serve it in the
order Provimux tries them, the cooldowns it recorded and each login's list, your own names, and the switch
[Serve only my names](#serve-only-my-names); and **Savings**, what prompt caching was worth and what each account kind
served. The original's pages stay one click away, under Advanced settings.
[Its README](https://github.com/ngvoicu/provimux-ui/blob/main/.github/README.md#provimuxs-own-pages) describes each
page.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="provimux/media/console-models-dark.png">
  <img alt="The console's Models page, on its Names tab, beside Excluded models (0). The sidebar reads Provimux by providers.dev and lists Accounts, Routing, Models, Savings and Advanced settings under Gateway. A card Serve only my names, switched off: Off: all 41 names are served, 13 of them yours. Chips read 41 names, 13 of yours, 4 served by more than one account, 2 cooling down, 1 needs a look. The section Your names: opus, served by a Claude subscription, 1840 requests in 30 days; claude-opus-5-5, 4 accounts, served in this order by a Claude subscription at priority 10, a Devin subscription at priority 5 that is cooling down, and a Claude API key and OpenRouter sharing priority 0; terra, served by a Codex subscription; sonnet, marked Needs a look, with a second route Provimux ignores because the login type already uses the name; haiku, served by a Claude API key." src="provimux/media/console-models-light.png">
</picture>

Nothing to install: Provimux downloads the latest provimux-ui release by itself, at startup and every 3 hours, and
installs it only when it matches the release's `checksums.txt`. A config that still names the original console
(`management.panel-github-repository`) gets provimux-ui too. Open `http://127.0.0.1:8317/management.html` (on the
same machine; from another machine, use the server's address once [the console is open to it](#for-a-team)) and sign in
with your management key (`management.secret-key` in your config; while it is empty, the management API is off). To keep a
console version, turn the updates off with `management.disable-auto-update-panel: true`.

## Versions and releases

A Provimux version names the CLIProxyAPI release it is built on: `v8.0.79-pmx.1` is CLIProxyAPI 8.0.79 with Provimux
build 1.

Releases are automatic. When CLIProxyAPI publishes a release, a workflow moves Provimux onto it, builds and tests it,
and when the check passes publishes `v<base>-pmx.1` on its own: the release archives, the Docker image and the Homebrew
formula. A release that fails a check waits for the owner, and the reason is in an issue.
[.github/provimux/SYNC.md](provimux/SYNC.md) describes the whole process.

The startup banner and the `X-CPA-VERSION` response header show the Provimux version, with the CLIProxyAPI release it
is based on. The version sent to providers is CLIProxyAPI's (`8.0.79` in this example), so providers see the same
client as with CLIProxyAPI.

## Features in detail

### Which account serves each model, and which one answered

**What you get.** The OpenAI-format model list can tell you, for each model, which of your accounts serve it: the
provider, whether it is a subscription login (`oauth`) or an API key (`apikey`), its priority, and for an API key the
host it calls. Every response served by an account then says which account answered it.

**Why it matters.** The same model can be reached through more than one account, and accounts differ in limits,
prompt-cache lifetime and price: a Claude API key keeps the prompt cache for five minutes unless the request asks for
an hour, and an OpenRouter key bills at OpenRouter's prices. Without this, a client behind the proxy cannot tell which
one it is using.

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
  `apikey`), an `X-CPA-AUTH-PROVIDER` header (its `provider`, as in the model list) and, for an API key with a base
  URL, an `X-CPA-AUTH-HOST` header, for the account that answered, next to CLIProxyAPI's `X-CPA-TRACE-ID`. The model
  list itself has no such header. When a request is retried on another account before the answer starts, the header names the one that
  answered. Browsers can read it, like the trace ID.
- On the Codex WebSocket (`/v1/responses`), which has response headers only once per connection, each turn's
  `response.created` event carries `"cpa_auth_kind": "oauth"` or `"apikey"`, with `cpa_auth_provider` and
  `cpa_auth_host` beside it.

```bash
PROVIMUX=http://127.0.0.1:8317   # on the same machine; from another machine, use the server's address
curl -s "$PROVIMUX/v1/models?include=accounts" -H "Authorization: Bearer $KEY"
```

Priorities are CLIProxyAPI's: set `priority` on an API-key entry, or in a login's file, and the higher value is tried
first. For example a Claude API key at `10` and a second Claude API key at `0`: the first is tried first, and the
second takes over while the first has a cooldown recorded.

### Live model lists for each login

**What you get.** Each Claude, Codex, Meta and Devin login, and each Gemini API key, registers the models
its own account lists, not only the shared catalog. Copilot and Antigravity logins, whose lists CLIProxyAPI already
reads, also offer the models the catalog doesn't have yet. Models the provider has retired, or doesn't offer to your
account, disappear from the list, and new models appear on the day the provider lists them.

The shared catalog is router-for-me's, and it stays Provimux's base list: a login's own live list decides which
models it offers, the catalog gives the values of every model it has, and a login without a live list (Kimi and xAI
for now, API keys, or before the first fetch) offers the catalog.

**Why it matters.** The shared catalog is edited by hand. It can list a model a day late, keep a retired model that
then answers 404, or list a model your plan is refused.

![A diagram titled "models.live-lists: each login registers what its own account lists", with two columns, "The shared catalog" and "What the login serves". Claude login: the catalog has claude-opus-5-5, claude-sonnet-5-5, claude-haiku-5-5, claude-opus-4-1-20250805 and claude-3-7-sonnet-20250219; the login serves the first three, and the two older models are struck through. Codex login: the catalog has gpt-6.1-terra and gpt-6-chat-thinking; the login serves gpt-6.1-terra, gpt-6-chat-thinking is struck through, and gpt-6.2-sol is added in green. The legend reads: in both: the catalog's values; not in the login's list: hidden; not in the catalog yet: added from the provider.](provimux/media/live-lists.png)

**How to turn it off.** It is on by default.

```yaml
models:
  live-lists: false
```

- Provimux fetches each login's list in the background: at startup, every 3 hours, on
  `POST /v8/management/models/refresh`, and when a login is added or replaced. Requests never wait for it.
- A login registers exactly the models its list names, plus the image, video and speech models CLIProxyAPI adds
  itself. For a model the catalog has, the catalog's values stay, as before: window, output limit, reasoning levels,
  input types. The provider's description only fills a field the catalog leaves empty (never the reasoning settings),
  and a value no source states stays unknown. Models the catalog doesn't have yet are added from the provider's own
  description: Claude gives the context window, output limit, reasoning levels and image input; Codex the context
  window, reasoning levels and input types; Devin the context window, output limit, image input and, through its
  variant ids, the reasoning levels; Gemini keys the input and output limits. Meta lists only model ids. Hidden Codex
  entries are never added.
- Kimi and xAI logins are not asked yet: they keep the catalog exactly as before. Their lists will be asked once one
  real answer from each has been recorded and checked against the ids their requests use.
- A fetch replaces a list only when it is complete (every page) and names at least one model, within 30 seconds; a
  provider that does not answer in time counts as a failed fetch.
  Until a list arrives, or while fetches fail (retried every 10 minutes), the catalog or the last good list is used:
  a provider outage never empties a login. A list belongs to the login's account, so a login file replaced by another
  account never keeps the old list.
- Lists are kept on disk, in `model-lists/` beside the console's `static/` folder (mode 0600: model ids and values,
  the fetch time and a hash of the account, no token, email or file name), so a start without network serves the
  lists of the last run. A list that could not be refreshed for 7 days (by its own fetch time) gives way to the
  catalog, with one log line. Files are never deleted, also those of removed logins: each is small, one per account,
  two logins of one account share it, and a file of another account or login type is ignored when read.
- API keys without their own models list keep the catalog, except a Gemini key on Google's own host. API keys with
  one refresh it as the next section says. Changing `live-lists` in a running instance applies at once.

### Model lists for API keys

**What you get.** An API key with its own `models` list (every `openai-compatibility` entry, and Claude, Codex,
xAI, Meta and Gemini keys with rows) keeps that list up to date by itself. The key offers:

1. every row of its `models` in `config.yaml`, exactly as written: name, alias and every setting;
2. plus every model its service lists that no row already names, under the service's own id;
3. minus its `excluded-models`.

An `openai-compatibility` entry with no rows offers its service's whole list. Before, such a list changed only
when you pressed "Get the list from …" in the console and saved; that button stays, and is how a model gets an
alias or its own settings.

**Why it matters.** Services such as OpenRouter add models every week. A key now offers them the day its service
lists them, and a model the service retires goes with its next list, unless a row of yours names it: rows are
never dropped, and the console marks a row the service no longer lists.

**Upgrading: what changes.** It is on by default, so on the day you upgrade every key with rows starts offering its
service's other models too. When one of them is a model a subscription of yours also serves (an OpenAI key listing
a model your Codex login has, a Claude key beside a Claude login), requests for it are shared between the two at
equal `priority`, and the key's share is paid per token. The controls:

- `priority` on the key or the login, as before: the higher one is used first;
- `excluded-models` on the key, with wildcards (`gpt-5.5*`, `x-ai/*`, `text-embedding-*`); `openai-compatibility`
  entries have it too now, and it applies after the refresh, so taking a model out of `models` no longer keeps it
  out: `excluded-models` does;
- `fixed-models: true` on the key (or its group under `api-keys:`): it offers exactly its rows and its service is
  never asked. Vertex and Interactions keys accept it and are never asked anyway. Set it with the console or a
  `PUT` of the key list; the older per-entry `PATCH` endpoints do not take it.

```yaml
openai-compatibility:
  - name: openrouter
    base-url: https://openrouter.ai/api/v1
    excluded-models: ["x-ai/*", "text-embedding-*"]
    # fixed-models: true   # offer exactly the rows below; never ask OpenRouter for its list
    models:
      - name: anthropic/claude-opus-5.5
        alias: or-opus
```

The **Model lists** endpoint below shows, per key, which models it learned from its service (`key.learned`) apart
from its rows, and which rows its service no longer lists (`key.unlisted`). The console's Models page shows every
account serving a model, and marks a model a key learned.

- The request is the one the console's button sends, to the host you configured for the key, with that key, its
  headers and its proxy: `<base-url>/models` for an `openai-compatibility` key (once more without the key and headers
  when the service answers 401 or 403, for services whose list is public), `/v1/models` for Claude (with
  `anthropic-version`), Codex, xAI and Meta keys, every page of `/v1beta/models` for a Gemini key on its own host.
  A Gemini key on Google's own host already had its list read: it is now added to the rows instead of ignored.
  Vertex and Interactions keys are not asked.
- Fetching, failures and the disk cache work as for logins: at startup, every 3 hours, on
  `POST /v8/management/models/refresh` and when a key is added or changed; 30 seconds per fetch; a failure keeps the
  last good list (or the rows) and is retried after 10 minutes; a list not refreshed for 7 days gives way to the rows.
  Each key holds its own list, so two keys of one entry can see different models. The disk cache holds a hash of the
  key, never the key.
- **Nothing is written to `config.yaml`**: the models a key learned exist only in memory and in the cache, so a save
  from the console or a reload never carries them. Requests never wait for a fetch.
- A learned model gets what a row with only its name would get: on OpenRouter, OpenRouter's window, output limit and
  reasoning efforts; on a Claude, Codex, xAI, Meta or Gemini key, the catalog's values when the catalog has the id.
  A model that needs a setting of its own (`use-max-completion-tokens`, an alias) needs a row, as before.
- A key's list never teaches the shared catalog: a gateway behind a Claude key can list anything.
- `models.live-lists: false` turns this off for every key and login. A service that lists non-chat models
  (embeddings, speech) offers them too; hide them with `excluded-models`.

### Serve only my names

**What you get.** With `models.only-my-names.enabled: true`, the gateway lists and serves only the names you set; every
other name is treated as an excluded model is: it is not listed to clients, and a request for it is refused. Your
accounts keep registering exactly what they offer; the switch decides what clients see and may ask for. A name you set
is one your config sets, and nothing else:

1. the `alias` of a `models` row of an API key or an `openai-compatibility` entry, when it differs from the row's `name`;
2. the `alias` of an `oauth-model-alias` row;
3. a name under `models.only-my-names.keep`: a model kept under its vendor's name (`claude-opus-5-5`,
   `devin/claude-opus-4.6`, `team-a/gpt-5.5`). Exact names only; an entry with `*`, and `auto` (which clients send
   for "the first available model"), are ignored, with a warning in the log (a pattern would let tomorrow's models in
   without your word).

A copy of one of your names under a `prefix` your config gives a key or provider entry is yours too: with
`prefix: crew` on an entry and `mine` yours, `crew/mine` is served. An alias written in a login's own file is not
one of your names, and a prefix written only in a login's own file is not one the config gives: the status lists such
a name, or such a copy, with the other names, and one click on the console (or the full name under `keep`) makes it
yours. So what is yours follows from the saved config alone, and a request is answered from the rule of one saved
config and the name it asks for, whatever the accounts are registering at that moment.

Names are compared exactly, as a request is matched to a model: trimmed, and nothing else. `keep: [mixed-case]` does
not make `Mixed-Case` yours, and a request must spell the name as you wrote it: with the switch on, `MINE` is refused
when your name is `mine` (with the switch off CLIProxyAPI would also try the lower-case spelling). A name with a
thinking suffix is served when its name without it is yours: `opus(high)` when `opus` is.

**Why it matters.** Logins, live lists and keys that learn their service's list offer hundreds of names. With the
switch on, your tools see the names you chose and nothing else, and a model a provider adds tomorrow stays out until
you name it.

**How to use it.**

```yaml
models:
  only-my-names:
    enabled: true
    keep:
      - claude-opus-5-5
```

- It is by name, not by route: when a name is yours, every account that answers it serves it, in the same order and
  pool as before.
- Applied on reload, without a restart: the rule is in force for the next list and the next request from the moment
  the reload commits the saved config; no account has to register again. `enabled: false` (or removing the key)
  brings every name back, and `keep` stays for the next time. Removing an alias from the config stops serving its
  name at that reload, even while a login still registers it. With the switch on and no name in the config, nothing
  is served, and the log says so at every reload.
- A refused name gets the same HTTP 400 and `model_not_found` as an unknown model, with a message that says why:
  `model gpt-5.5 is not served: this Provimux serves only the names its owner set (models.only-my-names). Keep it on
  the console's Models page, or ask for a served name (GET /v1/models).` Every refused request gets that message
  and is counted, whether or not an account offers the name (the check reads the rule and the name, nothing else).
- Before turning it on, `GET /v8/management/provimux/names` shows your names (with the ids served through each: its
  copies under a prefix your config gives, and its spellings with a thinking suffix an account registers) and every
  other name your accounts offer (what would stop being served), with the accounts behind
  each; once it is on, also which names tools asked for and were refused (in memory, up to 256 names). It is computed
  when asked, from the rule in force and what the accounts register, with the same check every list and request
  passes; its `applied` block names the config whose rule is in force (its ETag), so a save has taken effect once that
  ETag is the one the save returned. The console's Models page uses it, with Savings' requests by the name your tools
  sent (`include=requested_models`).
- The management API and the console never depend on the switch: they are the way back. In Home mode the switch does
  not apply.

**What the switch does not cover, or covers differently.**

- **Realtime and live** (`/v1/realtime…`, `/v1/live`): these routes choose a Codex login without naming a registered
  model, so with a Codex login they are served whatever the switch says.
- **A plugin executor reached through a plugin's model router, for one of your names**: the plugin decides what it
  runs. For a name that is not yours no router is asked on the request routes, and the request is refused with the
  setting's message, as is a router's rewrite to a provider or to another model that is not yours. Alpha search
  (`/v1/alpha/search`) asks its router first, as CLIProxyAPI does, and then checks both the requested name and the
  router's choice: the router sees the name, nothing is served under it.
- **The Gemini format** (`GET /v1beta/models`) names a model by the name it shows a Gemini client (a plugin executor's
  provider-native name; for an alias spelled `models/gemini-2.5-pro`, the resource `models/gemini-2.5-pro`, which a
  Gemini client generates as `gemini-2.5-pro`). When that name is not yours, the Gemini list leaves the model out and
  its detail answers 404, as its generation is refused; the other formats list the name you kept.
- **A Gemini key whose alias is a full resource name** (`models/gemini-2.5-pro`): keeping the short name
  (`gemini-2.5-pro`) lists the resource and answers its detail, but generation under it is refused, because no account
  registers the short name; CLIProxyAPI routes such an alias the same way with the switch off. Keep the alias's full
  name, or write the alias without `models/`.
- **A plugin that rewrites the final model list** a client receives: what it adds is shown (the switch filters the
  lists the gateway builds, not what a plugin writes over them); a request for it is still refused.
- **The Responses WebSocket** (`GET /v1/responses`): a prewarm (`response.create` with `generate: false`) for a name
  that is not yours is acknowledged by CLIProxyAPI itself (`response.created`, `response.completed`) without asking a
  provider; the generating request is refused, and CLIProxyAPI then closes the socket without the setting's message.
  No model is served and nothing reaches a provider.
- **Speech and video names**: `/v1/audio/speech` and `/v1/tts` turn `tts-1` and the like into `grok-tts`, and
  `/openai/v1/videos` turns `sora-2` into `grok-imagine-video`, before the switch looks at the name. Keep the fixed
  name (`grok-tts`, `grok-imagine-video`); a refusal, and its count, name it too. Fetching a video by id uses
  `grok-imagine-video`, so it is refused while that name is not yours.
- **Claude-shaped model lists** (`GET /v1/models` with `Anthropic-Version`, or Claude Code): CLIProxyAPI shows its own
  generated Claude-like id for each served name and accepts it; it reaches the model behind your name, so nothing you
  did not keep is served through it.
- **An exchange under way when the rule changes** (a stream, a WebSocket session's follow-up on its login) finishes
  under the rule it started with.
- **A request for `auto` already under way when the switch is turned on** is resolved from the names served after the
  save: it may be answered by another of your names than a moment before, or, if none is eligible, refused with
  CLIProxyAPI's own error.
- **A login being replaced by another**: the account count the status shows for a name can be one read behind while
  the replacement happens; the next read is right.
- **A name no account offers**: with the switch on it gets the setting's message instead of CLIProxyAPI's
  unknown-model words, and takes one of the 256 slots of the refusal count. The machine-readable fields are as before:
  HTTP 400, `invalid_request_error`, `model_not_found`, `param: model`.
- **Between a save's answer and its reload** the previous rule is in force; `GET /provimux/names` says which.
- **Home mode**: the switch does not apply.
- **CLIProxyAPI's own management views** (the models of an auth file, `registered_models` of
  `GET /provimux/model-lists`) show what each account offers, which with the switch on includes names that are
  refused.

### Model routes and live-list status for the console

**What you get.** Read-only management endpoints that the Provimux console,
[provimux-ui](https://github.com/ngvoicu/provimux-ui), uses for its own pages: for each
model, every login and key that registered it with the cooldown recorded on it; and for each login, which list
decides its models and the state of its live model list.

**Why it matters.** The model list for clients names no account by design. The operator needs to see which logins
and keys carry a model, which of them have a cooldown recorded and until when, and whether a login's live list is
current or failing, without reading the log.

**How to use it.** Nothing to turn on. They take the management key, like every `/v8/management` route.

- `GET /v8/management/provimux/model-routes`: per model, its routes in priority order, each with `provider`, `kind`
  (`oauth` or `apikey`), `priority`, the `host` of an API key, the login named as the credentials page names it
  (`name`, `email`, `label`, only for logins that page lists; otherwise `auth_index` identifies it), its limits and
  reasoning levels, and a `recorded_cooldown` (for the credential or for one model, until when, and a reason code).
  `?model=<id>` (repeatable) narrows the list. While [`models.only-my-names`](#serve-only-my-names) applies, it lists
  only the names it serves.
- `GET /v8/management/provimux/model-lists`: per login, the `source` that decides its models (`live`, `published`,
  `built-in` or `none`), its live list (`held`, `expired` after 7 days, `fetched_at`, `from_cache`, how many models
  it listed, the last failed fetch as a category with the provider's HTTP status), how many models it serves and how
  many of them the catalog doesn't have yet. `?auth_index=<index>` adds that login's models with the source of each
  value; `?view=live` adds each login's list as the provider returned it. A `keys` array has a row per API key whose
  list refreshes itself or could: whether its service is asked (`key.not_asked`: `fixed`, `live_lists_off`,
  `no_list_endpoint`), when its list was refreshed and the last failure, the ids it learned (`key.learned`), and its
  rows the service no longer lists (`key.unlisted`); with `?auth_index=` every model it serves, `learned` or a row,
  and whether the service lists it (`listed`).
- `GET /v8/management/provimux/live-lists`: the older form of the same state, kept for consoles released before
  `model-lists`: per login, `source` (`live` or `catalog`), when the list was fetched, how many models it listed and
  how many the login serves, and the last failed fetch as a category (`http_status`, `credentials`,
  `invalid_response`, `network`, `other`) with the provider's HTTP status. The reason in words stays in the log.
- `GET /v8/management/provimux/names`: the state of [`models.only-my-names`](#serve-only-my-names): the switch, your
  names with who set each (`alias` or `keep`), how many accounts serve each and the ids served through it (its copies
  under an account's prefix, its spellings with a thinking suffix), the `keep` entries it ignores, every other name
  an account offers with those accounts, the names refused since the switch went on, and `applied` (the ETag and
  generation of the config whose rule is in force; `applying` is always false). Every registered name is in exactly
  one of your names and the others, judged as a request for it would be; each answer is built from one rule.
- They report what Provimux has recorded. They do not predict the router: they don't say which route a request
  would get, whether a route is selectable right now, which route answered last, or when a list is fetched next.
- No response carries an API key, a token, an auth ID, a base URL beyond its host, or any provider error text. Stock
  CLIProxyAPI answers 404 on these paths.

The contract is [`openapi.yaml`](provimux/design/pui13/openapi.yaml), with example responses next to it.

```bash
curl -s "$PROVIMUX/v8/management/provimux/model-routes" -H "Authorization: Bearer $MANAGEMENT_KEY"
```

### Savings: what prompt caching was worth, and what each key cost

**What you get.** Provimux adds up every request into daily totals, kept in the local store across restarts, and
values them at public list prices. `GET /v8/management/provimux/savings` answers, for a range of days: tokens by
bucket (uncached input, cache reads, cache writes with their 5 minute and 1 hour parts, output, reasoning), the cache
hit rate, the estimated cost, the cost without the cache and the worth of prompt caching (the difference, at API list prices), and the cost an upstream
reported (OpenRouter's `usage.cost`). Breakdowns by day, month, account kind (`oauth` or `apikey`), provider, host,
model and client key come in the same call. The console's Savings page reads it.

**Why it matters.** It is the proof of what the routing does: how much ran on subscriptions instead of paid keys,
what prompt caching was worth, what the paid keys really cost.

**How to use it.** Nothing to turn on: recording starts with the management routes (a management key is
configured) and needs the local store (`server.kv-store`, on by default) to survive a restart. Removing the
management key later hides the endpoint (404) but recording continues: the totals stay in your own local store.

- A request is one usage record: one attempt at one upstream; a retried request counts once per attempt. It counts
  under the provider of the account that served it (a Kimi login stays `kimi`, also on its Claude Messages route).
- A day is the server's local calendar day (`TZ`; see [Docker](#docker)). The answer names the zone and the server's
  today.
- Everything under `estimate` is an estimate at base list prices (`prices.rates` says `base`): the higher rates some
  models charge above a prompt size (long context) are not applied, so long prompts are valued below what they cost.
  The lists are OpenRouter's public list
  (`https://openrouter.ai/api/v1/models`), and a small table of Provimux's own for models it does not list. The saving
  subtracts the cache-write premium and can be negative; a model without a published cache-read price claims none. A
  model without a price is counted and named, and left out of the amounts. The worth of caching includes
  subscription requests at list price: there caching saves quota, not money, so it is a worth, not money saved; and
  the value of subscription requests at API prices is not what the subscription saved.
- **A new outbound request:** to have prices, every installation downloads OpenRouter's public model list, at most
  every 3 hours and only while requests are being recorded (after a failed download, no sooner than a minute later),
  and never under `--local-model`. No key and nothing about the installation is sent. The last list is kept in the
  local store and used when the download fails.
- A client key appears as an id only this server can compute (an HMAC with a secret in the local store) and, for a key
  of 16 characters or more, its last 4 characters. No response, log or stored value carries a key, a token, an auth
  ID, an account email or a base URL beyond its host. Stock CLIProxyAPI answers 404 on the path.
- Rows count a request under the model the account ran: an alias's traffic under the vendor's model, without an
  account's prefix. `include=requested_models` adds `requested_models`: requests and tokens by the name the client
  sent (an alias, a prefixed name, without a thinking suffix, in the case it was sent), over the whole range, filters
  aside. It is recorded since the version that added it; `requested_models.from` is the first day that has it, and
  earlier days count only in the rows. Names recorded before the version that kept their case are lower case: a
  tool that sent `B-Coder` then shows as `b-coder`, which only-my-names (exact names) does not take for `B-Coder`. Nothing else in the answer changes.

The contract is [`openapi.yaml`](provimux/design/savings/openapi.yaml), with example answers next to it.

```bash
curl -s "$PROVIMUX/v8/management/provimux/savings?period=month&group=kind&group=key" \
  -H "Authorization: Bearer $MANAGEMENT_KEY"
```

### OpenRouter built in

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

### The right context and output limits for configured models

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
- When one model name is served by several accounts (aliases on logins, keys and OpenRouter), set `display-name` on
  each alias row: the list shows the name of the account registered last, so the same name everywhere keeps it stable.

### Devin: real output limits, and a quota pause per model

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

### The longer prompt cache on OpenAI API keys

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

### Codex WebSocket: a full replay stays a replay

**What you get.** On the `/v1/responses` WebSocket, a client that sends its whole conversation again (without
`previous_response_id`) gets that conversation answered, not a merged one.

**Why it matters.** CLIProxyAPI treats such a request as an addition to the previous turn unless it contains
assistant or tool items. A client that re-sends its first request unchanged, for example to keep the prompt cache
warm, had it merged: previous input, previous output, then the same input again. The model saw the conversation
twice and the request was billed at about twice the input.

**How to turn it on.** Always on. A request with no `previous_response_id`, the same `model` as the previous request,
and an input that starts with every input item of the previous request, in order, replaces the transcript. An
incremental request that names no model still appends, and pending tool calls answered by a replay are kept.

### Works with Pi

Provimux works with any client CLIProxyAPI works with. For [Pi](https://pi.dev), two extensions read what
[which account serves each model](#which-account-serves-each-model-and-which-one-answered) reports:

- [pi-provimux](https://github.com/ngvoicu/pi-provimux) adds Provimux (or CLIProxyAPI) as a Pi provider, `provimux`.
  Each model's name ends with where it comes from: `sub` for a subscription login, `api` for an API key, then what it
  falls back to. Pi 1.6.0 or newer shows the name on every line of `/model`, after the id. After each answer, its
  footer names the route that answered (`answered: Anthropic sub`).
- [pi-cache-keeper](https://github.com/ngvoicu/pi-cache-keeper) keeps the prompt cache warm and compacts from it. Its
  footer shows `sub` or `api` for the account that answered last, and warns when an API key answered for a model you
  reach through a subscription.

![Pi's /model list with Provimux. Each line shows the model id, its name with where it comes from in parentheses, and [provimux]: "claude-opus-5-5 (Claude Opus 5.5 · Anthropic sub, fallback api)", selected and current; "gpt-6.2-sol (GPT-6.2-Sol · OpenAI sub, fallback api)"; "gpt-6.2-astra (GPT-6.2-Astra · OpenAI sub, fallback api)"; "gpt-6.1-terra (GPT 6.1 Terra · OpenAI sub, fallback api)"; "gpt-6.1-sol (gpt-6.1-sol · OpenAI api)"; "devin/gpt-6-astra (GPT-6 Astra (Devin) · Devin sub)"; "gpt-6-astra (gpt-6-astra · OpenAI api)"; "gpt-6-luna (gpt-6-luna · OpenAI api)"; "devin/gpt-6-luna (GPT-6 Luna (Devin) · Devin sub)"; "aion-labs/aion-2.0 (aion-labs/aion-2.0 · OpenRouter api)". Below the list: "(1/1064)".](provimux/media/pi.png)

```bash
pi install npm:pi-provimux
pi install npm:pi-cache-keeper
```

Their READMEs describe the settings.

## Requests to providers

The requests Provimux sends to providers are the same as CLIProxyAPI's, except where a feature above says otherwise:

- for OpenRouter entries: the cache markers, the cache lifetime in explicit mode, and the `X-Session-Id` header;
  Provimux also reads OpenRouter's public model list;
- on OpenAI API keys: a client's `prompt_cache_options.ttl`;
- on the Codex WebSocket: the transcript of a full replay;
- for Devin: the output limit of each model, when the real limits are in use;
- unless `models.live-lists: false`: each Claude, Codex, Meta and Devin login, and each Gemini API key on Google's own
  host, asks its provider for its model list (at startup and every 3 hours);
- unless `models.live-lists: false`, or the key says `fixed-models: true`: each API key with its own models list asks
  its service for its model list (at startup, every 3 hours and when the key is added or changed), at the host you
  configured for that key (Meta's own for a Meta key without one), with that key, its headers and its proxy: the
  request the console's "Get the list from …" button sends (`<base-url>/models` for an `openai-compatibility` key,
  once more without the key and headers after a 401 or 403; `/v1/models` for Claude, Codex, xAI and Meta keys;
  `/v1beta/models` for a Gemini key on its own host).

CLIProxyAPI itself, and so Provimux, also sends two requests from its start, whether or not you have an xAI or an
Antigravity account, that go to neither a model provider nor providers.dev:

- the Grok CLI version check: `GET https://registry.npmjs.org/@xai-official/grok/latest`, at startup and every 3
  hours, through your configured proxy; xAI requests then carry that Grok CLI version;
- the Antigravity version updater: `GET` of the latest-release manifest of Google's Antigravity updater
  (`https://antigravity-hub-auto-updater-974169037036.us-central1.run.app/manifest/latest-arm64-mac.yml`), at startup
  and every 3 hours, through your configured proxy; Antigravity requests then carry that client version.

Provimux also sends one request of its own to providers.dev, unless `models.published-lists: false` (and never in
home mode): at startup, every 6 hours and on `POST /v8/management/models/refresh`,
`GET https://providers.dev/api/v1/listings` with a fixed filter (`view=full`, `limit=40` and the same list of
providers for every user), through your configured proxy. It carries a `User-Agent` of `provimux/<version>` and the
`ETag` of the last answer providers.dev gave, and nothing else: no key, no account, no model you use and nothing
about your requests. providers.dev sees what any web server sees, the address the request comes from. The answer
only fills a model's name, window, output limit and modalities where neither the vendor's list nor the catalog
states them.

## Support

Provimux is free and stays open source (MIT). For companies that run it, providers.dev offers support plans:
**Community** (free: GitHub issues and discussions, best effort) and **Team**, **Business** and **Priority** (paid),
listed with prices and terms at
[provimux.providers.dev/support](https://provimux.providers.dev/support/). A paid plan buys a private channel and a
first reply within a stated number of business days: a reply, not a promised fix. Provimux has one
maintainer, and we say so: announced holidays pause the clock, there is no 24/7 and no uptime promise for a service
you host yourself, and the code stays MIT, so you can always fork it.

## Built on CLIProxyAPI

Provimux is built on [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) by
[Router-For.ME](https://github.com/router-for-me), started by [Luis Pater](https://github.com/luispater). Nearly
everything Provimux does is CLIProxyAPI's work: the logins, the providers, the translation between formats and the
routing. Its console, provimux-ui, is the
[CLI Proxy API Management Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center), also by
router-for-me. Provimux adds the features above on top of each CLIProxyAPI release.

This repository's GitHub **Sponsor** button goes to [the owner's GitHub Sponsors](https://github.com/sponsors/ngvoicu).
Sponsorships support Provimux and the owner's other open-source tools.

## License

MIT, like CLIProxyAPI. See [LICENSE](../LICENSE).
