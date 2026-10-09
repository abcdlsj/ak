# ak

Run multiple Claude Code, Codex and Pi providers side by side.

Instead of switching one active provider, `ak` gives every provider its own
command. Pick a provider by typing its name, not by changing global state — so
several sessions can use different providers at the same time without stepping
on each other.

```
ak-bilicodex        codex   gpt-5.6-terra    company gateway
ak-cpacodex         codex   gpt-5.6-luna     own relay
ak-stepfunclaude    claude  step-5-preview
ak-cpapi            pi      glm-5            own relay
ak-kimi-pi          pi      kimi-k2.7-code   kimi's own pi provider
```

## Why

Most provider managers model "the current provider" as a single global value
and rewrite `~/.claude/settings.json` on every switch. That model fights the way
agent work actually happens: several worktrees, several sessions, several
agents running at once. Global state means constant switching, and a switch in
one pane silently changes what every other pane talks to.

`ak` moves the choice from time (switching) to space (side by side). Nothing is
global, nothing is overwritten, and each shim is a self-contained script that
`exec`s the engine directly.

## Install

```
go install github.com/abcdlsj/ak@latest
ak add <name> --kind claude --base-url https://... --key sk-... --model ...
ak sync
```

## Usage

```
ak                          open the launcher (enter or 1-9 runs a provider)
ak list                     list providers
ak add [name]               add a provider (flags, or a form with no --base-url)
ak add pool --member a --member b --strategy rotate   a pool: balance over several providers
ak serve                    run the pool gateway (required while a pool command runs)
ak edit <name> [--model …]  change only the given fields, or open the form
ak rm <name>                remove a provider and its commands
ak rename <name> <new>      rename a provider; its command follows
ak import --from cc-switch  pick which cc-switch providers to copy over
ak import --from pi         providers from ~/.pi/agent/models.json
ak sync                     regenerate commands from the config
ak doctor                   check for anything that would break the commands
ak usage                    token usage by provider and model
ak quota                    provider balance and plan usage
ak check [name...]          send each provider a minimal real request
ak models <name>            list the models a provider serves
ak-bilicodex                run codex on that provider
ak-bilicodex high           override reasoning effort for this run
ak-stepfunclaude opus       use the opus-tier model for this run
ak-stepfunclaude -p "..."   run claude on that provider
ak-cpapi high               run pi with thinking level high
ak-cpapi -p "..."           run pi on that provider
```

The TUI opens on a launcher: the providers in a list, the cursor on the
default. `enter` launches the selected one and `1`-`9` launch a row directly,
asking for a variant first when there are any — unless the provider names a
`default_variant`, which launches it straight away; `/` filters. Launch, Manage
and Usage are tabs: `tab` / `shift+tab` cycle them, `m` and `u` jump straight
to one, and `esc` comes back to Launch:

- `m` Manage shows each provider's health, details and last 30 days of usage;
  `enter` edits (the name too, which renames the command), `a` / `d` add and
  delete, `*` sets the default and `s` syncs. In the form `ctrl+s` saves from
  any field.
- `u` Usage opens on all history and switches to 7, 30 or 90 days with `[`
  `]`; `enter` on a provider narrows the page to it and adds a tokens-over-time
  curve for the range, 5-minute points merged into round intervals to fit.

A provider whose command would fail is marked red, and the launcher names the
problem under the list before you run it.

Custom variants go under the provider:

```toml
[providers.bilicodex]
default_variant = "high"   # launch with this variant; do not ask each time

[providers.bilicodex.variants.fast]
model = "gpt-5.6-luna"
reasoning = "low"
shim = true          # also generate ak-bilicodex-fast
```

`default_variant` is a built-in thinking/reasoning level or a model tier, or
one of the provider's own variants. Without it the launcher asks, and the
command uses the engine's own default. With it, `ak-cpapi` runs at that level
unless you name another variant; the launcher's picker is skipped.

Extra environment variables go under `env`, for any kind, on the provider or
on a variant. A proxy for one provider is just that:

```toml
[providers.bilicodex.env]
HTTPS_PROXY = "http://127.0.0.1:7890"
```

A command launched from inside another ak command drops what the outer one
exported first, so one provider's proxy, config dir or key never leaks into
the next. A pool's upstream requests are made by `ak serve`, so its proxy goes
in the environment of `ak serve`.

`ak` keeps one source of truth in `~/.config/ak/providers.toml` (mode `0600`)
and derives everything else. Generated shims are marked and idempotent, so
`ak sync` rewrites only what changed and reclaims what no longer applies.

## Sharing or isolating config

By default every provider of a kind shares the engine's own directory:
`~/.claude` or `~/.codex`, with its login, sessions, plugins, skills, MCP
servers and `CLAUDE.md`. Only the endpoint, key and model differ. Two levels
of separation are available when that is too much sharing:

| Field | Kind | What is separate |
| --- | --- | --- |
| `settings` | claude | a settings layer passed as `--settings`; its values replace the shared `settings.json`'s, the rest stays shared |
| `config_dir` | claude | everything: exports `CLAUDE_CONFIG_DIR` |
| `codex_home` | codex | everything: exports `CODEX_HOME` |

```toml
[providers.stepfunclaude.settings]
effortLevel = "low"
statusLine = { type = "command", command = "~/bin/stepfun-status" }

[providers.bili-claude]
config_dir = "~/.claude-bili"     # its own login, MCP, plugins and sessions
```

Or `ak edit stepfunclaude --settings '{"effortLevel":"low"}'`,
`ak edit bili-claude --config-dir ~/.claude-bili`. A settings layer cannot
remove what the shared file sets, and it may not set the endpoint or key in
its `env`: those come from the provider. A separate directory starts empty,
so sign in, MCP servers and plugins are set up there again; `ak usage`
reads its sessions, and `ak hook install` and `ak doctor` cover its
`settings.json` too.

## Pi

A `kind = "pi"` provider runs the pi coding agent. There are two shapes:

- **A provider pi already knows** (`pi_provider`): ak writes nothing and passes
  `--provider <id>`, so a pi package or a signed-in provider is used as it is.
- **An endpoint ak registers**: ak keeps an `ak-<name>` entry in pi's
  `models.json` with the base URL and a `$AK_KEY_<NAME>` key reference, and the
  command exports the key, so no key lands in the file or on the command line.

```sh
ak add ccpi --kind pi --pi-provider commandcode --model deepseek/deepseek-v4.1-flash
ak add cpapi --kind pi --base-url https://relay.example/v1 --key sk-... \
  --model glm-5 --pi-api openai-completions
```

`--pi-api` is `anthropic-messages` (the default), `openai-completions` or
`openai-responses`; `--pi-auth-header true` sends the key as
`Authorization: Bearer` instead of `x-api-key`. The built-in thinking levels
(`off` … `max`) are variants: `ak-cpapi high`. `ak import --from pi` copies the
providers from `~/.pi/agent/models.json`, and `ak usage` counts pi's sessions.

ak writes only `ak-*` entries in `models.json`. Everything else is preserved,
and when ak's own entries have not changed the file is not rewritten at all.

## Pools: balance and aggregate providers

A pool is a provider backed by several others of the same kind, so one command
spreads over them: fail over when one is down, or rotate for load. The engine
talks to ak's own loopback gateway, which injects each member's real key and
never writes it into the command.

```sh
ak add pool --kind claude --member kimi --member cpa --strategy order
ak add pool --kind codex --member cpa --member step --strategy rotate \
  --map step=gpt-5.6-luna
ak serve            # required while a pool command runs
```

| Flag | Meaning |
| --- | --- |
| `--member` | a provider in the pool, repeatable and ordered |
| `--strategy` | `order` (failover, the default), `rotate` (round-robin) or `least-used` |
| `--map` | `member=model`: ask that member for a different model |

The gateway listens on `127.0.0.1:17877` (`settings.gateway_addr` to change it)
and is loopback-only. A member that fails is set aside for 30 seconds, doubling
to ten minutes while it keeps failing, or for the vendor's own `Retry-After`
when it is longer, and the request falls through to the next member. A busy
vendor is retried on the same member once before it is left; a refusal that is
that member's alone — a rejected key, a missing path, an unserved model, a
quota — moves the request on, while one every member would give (a conversation
too long, a missing field) reaches the agent. An event stream is held at its
opening until its first content, so an error that starts it fails over instead
of reaching the agent; one that breaks after content has begun is passed on as
it came. A conversation stays with the member that answered it (its engine's
session header, or Codex's `prompt_cache_key`) so the vendor's prompt cache is
read again rather than paid for afresh; load still spreads across conversations.
Requests, their bodies and their streams are bounded: a body over 128 MB or
unread for 60 seconds, a stream stalled for 2 minutes, each ends rather than
hangs. A member's `max_concurrency` queues extra requests instead of sending
them at once; `settings.max_inflight` (256 by default) refuses over that many
with 503 rather than queueing. Pools do not translate between protocols: every
member must speak what the engine speaks. To aggregate one relay for both
engines, register its Anthropic endpoint as a `claude` provider and its OpenAI
endpoint as a `codex` provider, then pool each kind separately. `ak doctor` warns
when a pool exists but nothing is listening on the gateway.

With the gateway running, the TUI's Manage page shows a pool as it works: a
sparkline per member of requests per minute over the last hour, scaled together
so a busier member visibly outranks a quieter one, with each member's totals and
any cooldown. The gateway answers `GET /stats` with the same data, and
`GET /healthz` with a cheaper liveness plus each member's cooling state.

The affinity table is kept on disk, so a restart keeps every conversation on the
member the vendor still has it cached at. `ak serve` reloads `providers.toml`
when it changes (or on SIGHUP), so an edited or renamed pool takes effect
without a restart; `settings.gateway_addr` still needs one. Keep it running
while a pool command is in use. Normal providers never touch the gateway: they
stay direct, with their own key in their own command.

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
id, not the API key. Put them in the provider's env (the token falls back to
the key when unset):

```toml
[providers.myrelay]
quota = "newapi"
env = { AK_QUOTA_TOKEN = "your-access-token", AK_QUOTA_USER = "42" }
```

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

Queries are opt-in per run: ak never calls a balance API on its own. A pool is
asked through its members.

## Details worth knowing

**Nothing global is rewritten.** `ak` never writes provider keys into
`~/.claude/settings.json`. That file's `env` block is applied after the shell
environment, so a leftover `ANTHROPIC_BASE_URL` there silently overrides every
shim — the command appears to work but talks to the wrong provider. `ak doctor`
flags it. Your global preferences stay in `settings.json` untouched.

**Nested launches cannot leak.** A shim explicitly unsets every provider key it
does not set, so starting one provider from inside another does not inherit the
parent's endpoint or key.

**Deletion is guarded.** `ak sync` only removes a file that matches the prefix,
is a regular file (never a symlink), is owned by you, and carries an
`ak:generated` marker. The tools in `~/.local/bin` that are not ours are left
alone.

**Codex providers are layered, not copied.** Each one gets
`~/.codex/ak-<name>.config.toml`, containing only the keys that differ from your
base config. Your `projects`, `skills` and `hooks` keep working, and codex's
`env_key` reads the key from the process environment — no per-provider
`auth.json`, no isolated `CODEX_HOME`.

**Referenced keys stay out of the commands.** With `api_key_ref`
(`env:NAME`, `cmd:...`, `keychain:...`) the command asks `ak` for the key at
launch, so no plaintext copy is written to `~/.local/bin`. A plain `api_key`
is embedded, and the command is mode `0700`.

**Usage is computed from local logs.** `~/.claude/projects` and
`~/.codex/sessions` are scanned incrementally: the first run reads the whole
history, later ones only the bytes appended since. Each request counts once —
Claude writes a message once per content block, Codex repeats token events,
and resumed or forked sessions copy history into new files. Days split at
local midnight. Claude's logs do not record which provider a session used, so
`ak hook install` adds a `SessionStart` hook that records the mapping; Codex
sessions are attributed by their `provider_id`. An id that the base
`~/.codex/config.toml` also declares is left alone and reported under the id
itself, because a log holding nothing but that id cannot say which provider
ran it; `ak doctor` names the collision. Cost comes from models.dev; models it
does not know are reported as unpriced rather than billed as zero.

**cc-switch import is selective.** `ak import --from cc-switch` reads
`~/.cc-switch/cc-switch.db` read-only, prints what it found, then opens a picker
so you choose exactly which providers to copy. Nothing is written until you
confirm, and cc-switch's own database is never modified. Providers that are
ready to use are pre-selected; ones still missing an API key are left unticked
so a half-configured provider is not imported by accident. `--all` skips the
picker for scripted use.

## Rolling back

Delete the `ak-*` commands, the `~/.codex/ak-*.config.toml` files and
`~/.config/ak/`. Nothing else was modified.
