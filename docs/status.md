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

`ak quota` asks each provider's balance API what is left. The source is
detected from the endpoint host, so most vendors work with nothing extra:

```sh
ak quota                 # every provider
ak quota deepseek        # one
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
| `zhipu` | `open.bigmodel.cn`, `api.z.ai` (GLM Coding Plan) | 5h and weekly windows |
| `minimax` | `api.minimaxi.com`, `api.minimax.cn` (Coding Plan) | 5h and weekly windows |
| `minimax-intl` | `api.minimax.io` (Coding Plan) | 5h and weekly windows |
| `newapi` | never detected; set `quota = "newapi"` | balance |

For anything else, name a source with `--quota`, turn the query off with
`--quota off`, write a plugin, or point `quota_cmd` at a script.

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

**Scripts.** The script prints the answer as JSON (a balance, or plan windows:
`{"windows":[{"name":"5h","used":40}]}`) or a bare number, and is asked with
`AK_QUOTA_KEY` and `AK_QUOTA_BASE_URL` in the environment:

```toml
[providers.myrelay]
quota_cmd = "my-balance-check --json"
```

`ak quota wait <name>` blocks until the provider has allowance again (for a
pool, any member), reading the quota again at the reset time the vendor gave,
or every `--every` (a minute) when there is none; `--timeout` gives up with
exit 1. `ak quota wait kimi && ak kimi -p "..."` runs a job the moment the
window resets.

Queries are opt-in per run: ak never calls a balance API on its own. A pool is
asked through its members, and providers sharing one key on one host are asked
once. A provider no source serves shows `-`, with one hint under the table.
