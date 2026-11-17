# Health, balance and usage

`ak status [name...]` puts the three on one screen: what ak can tell locally
(key, engine, command), the vendor's balance or plan windows as `ak quota`
reports them, and the last 7 days of usage as `ak usage` counts it. It sends
no model request. The commands below answer each part on its own, for scripts.

## Health and models

`ak check` sends every provider (or the named ones) one minimal, non-streamed
request and prints status and latency: ok, slow, auth, model or fail. Each check
spends a few tokens; a pool is checked through its members, not the gateway,
and the command exits 1 when anything failed. `ak models <name>` asks the
upstream for its model list, trying `/v1/models` and `/models` on the base URL
and on its root when the base ends in a compat path such as `/anthropic`.

## Balance and quota

`ak quota` asks what is left once per account, which every provider on it
shares, and once per provider with an endpoint and key of its own. Naming a
provider asks its account. The source is the account vendor's `quota`, else
detected from the endpoint host, so most vendors work with nothing extra:

```sh
ak quota                 # every account and provider of its own
ak quota command-code    # one account
ak quota deepseek        # one provider, or its account
ak quota --json          # machine-readable
```

| Source | Endpoint host | Reports |
| --- | --- | --- |
| `deepseek` | `api.deepseek.com` | balance |
| `openrouter` | `openrouter.ai` | balance |
| `moonshot` | `api.moonshot.cn`, `api.moonshot.ai` | balance |
| `siliconflow` | `api.siliconflow.cn` | balance |
| `stepfun` | `api.stepfun.com`, `api.stepfun.ai` | balance |
| `kimi` | `api.kimi.com` (Kimi For Coding) | 5h and weekly windows |
| `commandcode` | `api.commandcode.ai` (Command Code plans) | 5h and weekly windows |
| `opencode-go` | `opencode.ai` (OpenCode Go) | 5h, weekly and monthly windows |
| `opendesign` | `open-design.ai` (Design Plan, `ak login opendesign`) | windows and balance |
| `zhipu` | `open.bigmodel.cn`, `api.z.ai` (GLM Coding Plan) | 5h and weekly windows |
| `minimax` | `api.minimaxi.com`, `api.minimax.cn` (Coding Plan) | 5h and weekly windows |
| `minimax-intl` | `api.minimax.io` (Coding Plan) | 5h and weekly windows |
| `newapi` | never detected; set `quota = "newapi"` | balance |
| `antigravity` | never detected; set `quota = "antigravity"` | 5h and weekly windows, per group of models |

For anything else, name a source with `--quota`, turn the query off with
`--quota off`, write a plugin, or point `quota_cmd` at a script.

**Antigravity.** An `antigravity` provider has no endpoint to match on, and its
allowance belongs to Google's plan rather than to a balance any vendor serves,
so ak asks the engine — named, never detected, because agy takes a few seconds
to start:

```toml
[providers.agy-work]
kind = "antigravity"
gemini_dir = "~/.local/share/ak/agy/work"
quota = "antigravity"
```

ak then runs `agy -p "/usage" --output-format json` with the provider's own
environment — its `gemini_dir`, the `SSH_CONNECTION` that keeps the credential
there — and reads the groups and windows agy reports, one window per group and
allowance, with the share used and when it resets. ak calls no endpoint of its
own and reads no keyring: what agy says is the answer, and a provider with no
credential in its directory is not asked at all. `ak quota board` and
`ak quota wait` read it the same way as any other plan; `ak doctor` points at
the setting for a provider that does not have it.

**New API relays.** `/api/user/self` wants a system access token and the user
id, not the API key. Put them in the provider's `quota_vars` (the token falls
back to the key when unset); unlike `env`, these never reach the engine:

```toml
[providers.myrelay]
quota = "newapi"
quota_vars = { token = "your-access-token", user = "42" }
```

A plugin reads them as `{{var.NAME}}`, and `quota_cmd` as
`AK_QUOTA_VAR_<NAME>`.

**Plugins.** A TOML file in `~/.config/ak/quota.d/` adds a source, or replaces
a built-in with the same `id`. It makes one request and reads the JSON reply by
dotted path (`limits.0.detail.used`; a number indexes an array):

```toml
# ~/.config/ak/quota.d/myplan.toml
id = "myplan"
match = ["api.myplan.dev"]      # endpoint host substrings; omit to name it only
detail = "{{data.plan}}"         # optional, paths into the reply
ok = { path = "success", equals = true, message = "msg" }  # optional

[request]
url = "{{root}}/v1/usage"        # {{key}} {{base}} {{root}} {{env.NAME}}, a|b falls back
headers = { Authorization = "Bearer {{key}}" }

[[windows]]
name = "5h"
used = "data.used"               # or remaining = …, percent = …, percent_left = …
limit = "data.limit"
resets = "data.reset_at"         # RFC3339, unix seconds or millis

[[windows]]                      # one window per matching array item
name = "weekly"
each = "data.limits"
where = { type = "WEEKLY" }      # a|b for alternatives
percent = "percentage"
```

A balance reads `[balance]` instead: `value`, optional `used`/`limit`,
`currency` or `currency_path`, and `scale` to divide by. The built-ins in
`internal/quota/plugins/` use the same format. A file that does not load is
skipped with a warning naming it, in `ak quota` and `ak doctor`.

**Programs.** Where one request and a few paths are not enough, any
executable in `~/.config/ak/quota.d/` is a source too, in any language: a
shell script, a Go binary. Its file name without the extension is its id, and
it is used only where named (`quota = "<id>"` on a vendor or provider). It is
run with `AK_QUOTA_KEY`, `AK_QUOTA_BASE_URL` and `AK_QUOTA_VAR_<NAME>` (the
provider's `quota_vars` and the account's sign-in secrets) in the environment,
and prints the answer as JSON or a bare number taken as a balance:

```json
{"windows": [{"name": "daily", "used": 42, "resets": "2026-11-13T00:00:00+08:00"}],
 "balance": 12.5, "currency": "USD", "detail": "Pro"}
```

`used` is the share used, 0 to 100. A non-zero exit fails the reading with
what the program wrote to stderr; it has 20 seconds.

**Scripts.** `quota_cmd` runs a shell command for one provider, with the same
environment and output:

```toml
[providers.myrelay]
quota_cmd = "my-balance-check --json"
```

`ak quota board` shows the same readings full screen, one card per account
and per provider on its own with a balance source: each allowance window's
share used as a large pixel number over a tick bar, in the card's colour,
amber past 80% and red once spent, with when it resets; money left the same
way. `r` reads them again, `q` quits.

`ak quota wait <name>` blocks until the provider has allowance again (for a
pool, any member), reading the quota again at the reset time the vendor gave,
or every `--every` (a minute) when there is none; `--timeout` gives up with
exit 1. `ak quota wait kimi && ak kimi -p "..."` runs a job the moment the
window resets.

Queries are opt-in per run: ak never calls a balance API on its own. A pool is
asked through its members, and providers sharing one key on one host are asked
once. A provider no source serves shows `-`, with one hint under the table.

## Usage

`ak usage [name]` counts tokens from the session logs the engines write:
`~/.claude/projects`, `~/.codex/sessions` and `~/.pi/agent/sessions`, plus any
`config_dir`/`codex_home` a provider sets. Rows are attributed to an ak
provider, so a provider's tokens and dollars are its own; usage that never went
through ak — an engine's own login, another tool's provider id — is listed
apart, under *Outside ak*.

```sh
ak usage                 all history
ak usage --days 7        the last seven days, today included
ak usage kimi --json     one provider, machine-readable
ak usage --ak-only       leave out engine logins and other tools
ak usage --sessions      each session's tokens and cache hit rate
ak usage --session a1b2  one session, by id or id prefix
```

Cost comes from models.dev prices, fetched once a day into
`~/.config/ak/model-meta.json` — the same file that holds the model numbers a
launch hands its engine. A provider's own `[providers.x.pricing]`
overrides them: either explicit per-million prices, or a `discount` multiplier
on the models.dev figure. A model with no price anywhere is counted in tokens
and named in one line under the total, never added to the cost as zero.

**Cache hits.** claude, codex and pi each log how much of a prompt the provider
served from its prompt cache, so a session can be asked what the cache saved
it: `ak usage --sessions` lists them, `--session <id>` (any prefix of the id)
prints one with its token breakdown, and `--json` carries the same rows. A hit
is cache reads over the whole prompt — input, cache reads and cache writes —
the one split all three agree on; a cache write counts as prompt volume, so a
session that filled a cache and never reused it reads `0%`. A session no
provider reported cache activity for shows `-`, not a miss. agy records no
cache figures, so no antigravity row has a hit rate to show.

**Antigravity.** agy records no token usage at all — its conversations hold
content, its summaries a step count — so an antigravity provider is reported in
a section of its own: the conversations last used in the window, the steps agy
recorded for them, and when it was last used. Nothing there is priced, and the
token total does not include it. `ak usage --json` carries the same rows under
`agy`.

**Routes and remotes.** A provider on a remote account asks a service for a
model name the service chose, and that name may be a route — a fallback chain
over several models, `main` for instance — rather than a model. No price
matches a route, so the service is asked what it actually served
(`GET /ak/v1/usage`), and each row is renamed to the model behind it: a route
that served one model takes that name, one that served several splits the
row's tokens in the proportions the service counted. The token total is never
changed, only its attribution, so the figure stays the same and its price
becomes exact. `--no-remote` keeps ak offline and reports the route name as
logged; a service that cannot be reached is named on stderr and its tokens
stay under the route name.

A name a route has since been renamed from is in no service's report, so the
tokens logged under it stay under the old name. `--rename old=new` relabels
them (`ak usage --rename main=dpsk`), and repeats: the engines' own logs are
never touched, only what the report is made of.
