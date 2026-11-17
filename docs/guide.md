# Guide

## Typing claude, codex, pi and agy directly

`ak install` sets up shell functions named `claude`, `codex`, `pi` and `agy`. Each
asks ak what the command runs on in the current directory and launches that
provider, or the engine's own command when nothing applies. The shell comes
from `$SHELL` (or `--shell zsh|bash|fish`): fish gets
`~/.config/fish/conf.d/ak.fish`, zsh and bash a marked block in `~/.zshrc` or
`~/.bashrc` (a symlinked rc is written through the link). The block asks ak
for the functions on each shell start, so upgrading ak needs no reinstall.
Running it again changes nothing, and `ak uninstall` takes it out. To wire it
by hand instead, `ak init zsh|bash|fish` prints the functions.

What a command runs on is decided in this order, first match wins:

1. `AK=<provider[:variant]>` for one invocation: `AK=deepseek claude -p "..."`.
   `AK=native claude` runs the engine's own command.
2. A binding: `ak bind kimi:high` in a repository binds the provider's engine
   there, for every worktree of it. Outside a repository it binds the
   directory and those below it. `ak bind native` keeps the engines' own
   commands there even when a default is set (`--engine` limits it to one).
3. The engine default: `ak default claude kimi`, `ak default codex relay`.
   `ak default claude native` clears it.
4. The engine's own command, with its own settings, as if ak were not there.

`ak which` shows what each engine runs on here and which rule chose it;
`ak bindings` lists them all and `ak unbind [engine]` removes the one here.
Bindings live in `~/.config/ak/bindings.json`; nothing is written into the
repositories. A binding or default naming a provider that is gone, or one of
another kind, stops the command with the fix rather than quietly running the
engine's own; `ak rename` and `ak rm` carry them along, and `ak doctor` names
any left over.

Arguments every launch of an engine should get go in `[settings]`:
`claude_args`, `codex_args` and `pi_args`. ak puts them in front of the
typed arguments whenever it launches that engine, on a provider or natively:

```toml
[settings]
claude_args = ['--dangerously-skip-permissions']
codex_args = ['--dangerously-bypass-approvals-and-sandbox']
```

The functions exist only in interactive shells, so scripts, editors and other
tools still run the engines' own commands. `ak <name>` keeps working
alongside.

## ACP servers

`ak acp claude` starts the engine's ACP server on the provider the same rules
pick in this directory: the same `AK` override, binding and engine default as
`claude` typed here, with the provider's environment passed to the adapter. A
client that would otherwise launch `claude-agent-acp` itself (a chat pane, an
editor) runs `ak acp claude` instead and reaches the same provider ak would.
`AK_CLAUDE_ACP_BIN` overrides the adapter executable. A client that starts the
adapter under its own `HOME` sets `AK_CONFIG_DIR` to the absolute path of the
user's `~/.config/ak` so ak still finds the providers and keys. An engine whose adapter
does not read the provider environment (codex, whose provider lives in
arguments) is refused rather than pointed at the wrong provider.

## Launching

```
ak kimi                     run the provider's engine (claude, codex or pi)
ak kimi -p "..."            everything after the name goes to the engine
ak kimi:high                with a variant: a reasoning or thinking level,
ak kimi:opus                a claude model tier, or one of the provider's own
ak run kimi:high ...        the same, spelled out, for scripts
ak-kimi -p "..."            the generated command, for scripts and tools
```

A variant is only ever selected with `name:variant`. Arguments after the name
are never read by ak, so the engine's own subcommands and prompts reach it as
typed: `ak kimi mcp list`, `ak cx resume`.

`ak` with no arguments opens the launcher: the providers in a list, the cursor
on the one last launched in this directory (else in another worktree of the
same repository, else the default). `enter` launches the selected one and
`1`-`9` launch a row directly, asking for a variant first when there are any,
unless the provider names a `default_variant`; `/` filters. On a wide terminal
the selected provider's details, health and last 30 days of usage sit beside
the list. `e` edits (the name too, which renames the command), `a` / `d` add
and delete, `*` sets the default and `s` syncs. In the form `↑` / `↓` move
between fields, `←` / `→` change a choice and `ctrl+s` saves from any field.

Launch, Accounts and Usage are tabs: `tab` / `shift+tab` cycle them, `u`
jumps to Usage, and `esc` comes back to Launch:

- Accounts lists each account with its vendor, who signed in, the providers
  on it and what is left of its plan, read when the page is first opened and
  again on `b`. `a` signs in to a vendor, `enter` signs in again, `n` adds a
  provider on the account and `d` removes it.
- `u` Usage opens on all history and switches to 7, 30 or 90 days with `[`
  `]`; `enter` on a provider narrows the page to it and adds a tokens-over-time
  curve for the range, 5-minute points merged into round intervals to fit.

With no provider configured, the launcher opens on the add form.

The directory record lives in `~/.config/ak/recent.json`; nothing is
written into the directories themselves.

Before it execs the engine, ak checks the provider has a key and the engine
can be found, and says how to fix what is missing instead of letting the
engine fail with a 401.

### Variants and defaults

Custom variants go under the provider:

```toml
[providers.relay]
default_variant = "high"   # launch with this variant unless another is named

[providers.relay.variants.fast]
model = "gpt-5.6-luna"
reasoning = "low"
shim = true          # also generate ak-relay-fast
```

`default_variant` is a built-in thinking/reasoning level or a model tier, or
one of the provider's own variants. With it, `ak relay` runs at that level
unless you name another variant, and the launcher's picker is skipped.

### Environment

Extra environment variables go under `env`, for any kind, on the provider or
on a variant. A proxy for one provider is just that:

```toml
[providers.relay.env]
HTTPS_PROXY = "http://127.0.0.1:7890"
```

A launch from inside another ak launch drops what the outer one exported
first, and unsets every provider key it does not set, so one provider's proxy,
config dir, endpoint or key never leaks into the next. A pool's upstream
requests are made by `ak serve`, so its proxy goes in the environment of
`ak serve`. `ak env kimi:high` prints what a launch would inject, keys masked.

### Model numbers

Every engine carries a model's context window and reply limit in a file or a
variable of its own, and guesses badly without them: pi takes every model for a
128K one and caps every reply at 16384 tokens, Claude Code assumes a model it
has no metadata for is 1M with 32000 tokens of reply, and codex takes 272K.
ak fills them from models.dev, the same document and the same cache its usage
report prices from, and hands them to the engine at launch:

```toml
[providers.relay]
context_window = 1000000   # tokens a prompt may hold
max_tokens     = 128000    # the longest reply
```

A value here wins over models.dev's; left out (or 0), models.dev's answers, and a
model it does not list gets the engine's own default. Where several providers on
models.dev list the same model id, the vendor's own row wins over a relay's
rounded one. ak reads the catalog when it regenerates the commands (`ak add`,
`ak edit`, `ak import`, `ak sync`), never at launch: a cold cache means the
engine keeps its default rather than the launch waiting on the network, and an
unreachable models.dev keeps the catalog ak already has. What each engine gets:

- **pi**: each model ak registers in `models.json` carries `contextWindow` and
  `maxTokens`, so pi compacts at the right point and asks for a full reply.
- **claude**: `CLAUDE_CODE_MAX_CONTEXT_TOKENS` and
  `CLAUDE_CODE_MAX_OUTPUT_TOKENS`. The window is read only for a model Claude
  Code has no metadata for, and one value serves every such model, tiers
  included, so it is the smallest of the provider's. The reply limit is read for
  every model, the ones it knows included, so it is the primary model's alone: a
  tier's smaller limit would shorten the main model's replies. Claude Code caps
  a reply at 128000 whatever ak says. Both are in `ak env <provider>`, and
  `ak doctor` flags them left in `settings.json`, where they win over a launch.
- **codex**: `-c model_context_window`, unless the provider brings a
  `model_catalog` of its own. Codex clamps it to the catalog's
  `max_context_window`, 272K for a model it does not know, so a wider window
  needs that catalog; and codex asks for no reply limit at all, so `max_tokens`
  reaches pi and claude only.

`ak import --from claude-settings` and `--from pi` carry the numbers over.

### Generated commands

`ak` keeps one source of truth in `~/.config/ak/providers.toml` (mode `0600`)
and derives everything else. Each `ak-<name>` command is a two-line script
that runs `ak run <name> "$@"`: it holds no key and no launch logic, so it
never drifts from the config. Every change through ak (`add`, `edit`, `rm`,
`rename`, `import`) regenerates the commands; `ak sync` is only for repair.

## Accounts and vendors

An account is one subscription or key at a vendor ak knows. Its providers
take their endpoint from the vendor for their engine and their key from the
account, so one sign-in serves claude, codex and pi, and its balance is read
once:

```
ak vendor list [--kind claude]          id, engine, endpoint, model, how to sign in
ak login command-code                   reads the Command Code CLI's sign-in
ak login opencode-go                    reads OpenCode's, else asks for the key
ak login opendesign                     approve a code in the browser
ak login kimi-coding work --key -       a second account, named work
ak add --account command-code --kind claude      named commandcode-claude
ak add goatx --account command-code --kind codex --model gpt-6-luna
ak accounts                             accounts, who signed in, their providers
ak accounts model command-code deepseek/deepseek-v4.1-flash    the plan's model
```

A provider given no name is named `<source>-<engine>` after what it runs on:
its vendor by its short name (`commandcode`, `kimi`, `dpsk`, `sf`), the
provider pi knows, or its endpoint's host (`cpa-codex`); a taken name gets
`-2`. A provider on an account stores only what it overrides: a field it leaves
unset follows the vendor, so a vendor that moves is followed by the next ak.
`ak add kimi --vendor kimi-coding --key -` makes the account and the provider
in one go. `ak edit old --account <name>` moves a provider with its own
endpoint onto an account; its endpoint fields equal to the vendor's and its
own key are dropped. `ak login` again renews an account's credentials,
`ak logout` removes one no provider runs on, and `ak accounts rename` renames
one with its providers.

**A plan that serves one model.** When a subscription lists a single model and
refuses the rest, say so once on the account rather than in every provider on
it: `ak accounts model <account> <model>`, or `model = "…"` under
`[accounts.<name>]`. Every engine on the account then runs on it, an endpoint's
own default included, and a provider that names a model of its own keeps it;
`ak accounts model <account> -` clears it.

**Your own vendors.** A TOML file in `~/.config/ak/vendors.d/` adds a vendor,
or replaces a built-in with the same `id`, so a company gateway or a relay
gets accounts like any other:

```toml
# ~/.config/ak/vendors.d/acme.toml
id = "acme"
name = "Acme AI"
short = "acme"                 # what provider names call it
quota = "acme"                 # its balance source, from quota.d
[engines.claude]
base_url = "https://ai.acme.internal/api"
[engines.codex]
base_url = "https://ai.acme.internal/api/v1/codex"
model = "gpt-5.6-sol"
```

Then `ak login acme --key -` and `ak add --account acme --kind claude`. A file
that does not load is skipped with a warning, in `ak vendor list` and
`ak doctor`.

A provider of its own (`--base-url`, `--key`) still works as before, for a
relay or anything ak has no vendor for. The vendor data comes from cc-switch's
presets plus subscriptions; vendors that need protocol translation are left
out.

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
| `pi_dir` | pi | everything: exports `PI_CODING_AGENT_DIR` |
| `gemini_dir` | antigravity | everything, the account included: passed as `--gemini_dir` |

```toml
[providers.stepfunclaude.settings]
effortLevel = "low"
statusLine = { type = "command", command = "~/bin/stepfun-status" }

[providers.work]
config_dir = "~/.claude-work"     # its own login, MCP, plugins and sessions
```

Or `ak edit stepfunclaude --settings '{"effortLevel":"low"}'`,
`ak edit work --config-dir ~/.claude-work`. A settings layer cannot
remove what the shared file sets, and it may not set the endpoint or key in
its `env`: those come from the provider. A separate directory starts empty,
so sign in, MCP servers and plugins are set up there again; `ak usage`
reads its sessions, and `ak hook install` and `ak doctor` cover its
`settings.json` too.

To run multiple native OAuth subscriptions side by side without an API key
(`claude` Pro/Max, `codex` ChatGPT Plus/Pro, or `pi`), omit `base_url` and
isolate the directory:

```sh
ak add pro1 --kind codex --native      # isolated under ~/.local/share/ak/codex/pro1
ak login pro1                          # delegates OAuth to `codex login`
```

Each engine signs in and refreshes its own token inside its directory; native
providers do not join pools or `ak serve`.

## Antigravity

A `kind = "antigravity"` provider runs `agy`, Google's Antigravity CLI. It
takes no endpoint and no key: agy signs in through Google itself and keeps the
credential in a file inside the provider's own `gemini_dir`. So **one provider
is one account**, and providers run at the same time:

```sh
ak agy add work                  # ~/.local/share/ak/agy/work
ak agy login work                # sign in; the token lands in that directory
ak agy add personal --keychain   # or copy the account this Mac already has
ak agy models work --set gemini-3.8-flash-high
ak agy path work --json          # gemini_dir, config_dir, app_data_dir, token
```

`gemini_dir` is required, and one directory cannot serve two providers: they
would be one account. `ak doctor` checks both.

Two details are worth knowing. agy normally keeps its credential in the OS
keyring, which holds one account per user; the launch exports `SSH_CONNECTION`,
which makes agy read and write the file in `gemini_dir` instead, so the
machine's own `~/.gemini` sign-in is never touched. And because agy believes it
is on a remote session, an interactive sign-in asks for the browser's code to be
pasted back rather than calling back into the terminal.

`--gemini_dir` is not in `agy --help`; `ak doctor` asks agy whether it still
takes it, since a release that dropped it would send every provider to
`~/.gemini`.

agy reports no token usage anywhere, so `ak usage` counts what it does keep —
one row per conversation, its steps and when it was last used — and prices
none of it. The plan's allowance is agy's own, and agy answers it itself: set
`quota = "antigravity"` on the provider and `ak quota`, `ak status` and
`ak quota board` read the five-hour and weekly windows of each group of models
by running `agy -p "/usage" --output-format json` in the provider's own
environment. It is named rather than detected because agy needs a few seconds to
start; `ak doctor` says so for a provider that does not have it. agy's credits
(`agy -p "/credits"`) are not read, and nothing else is: ak calls no Antigravity
endpoint and keeps no client of its own.

## Pi

A `kind = "pi"` provider runs the pi coding agent. There are two shapes:

- **A provider pi already knows** (`pi_provider`): ak writes nothing and passes
  `--provider <id>`, so a pi package or a signed-in provider is used as it is.
- **An endpoint ak registers**: ak keeps an `ak-<name>` entry in pi's
  `models.json` with the base URL and a `$AK_KEY_<NAME>` key reference, and the
  launch exports the key, so no key lands in the file or on the command line.

```sh
ak add ccpi --kind pi --pi-provider commandcode --model deepseek/deepseek-v4.1-flash
ak add cpapi --kind pi --base-url https://relay.example/v1 --key sk-... \
  --model glm-5 --pi-api openai-completions
```

`--pi-api` is `anthropic-messages` (the default), `openai-completions` or
`openai-responses`; `--pi-auth-header true` sends the key as
`Authorization: Bearer` instead of `x-api-key`. The built-in thinking levels
(`off` … `max`) are variants: `ak cpapi:high`. `ak import --from pi` copies the
providers from `~/.pi/agent/models.json`, and `ak usage` counts pi's sessions.

ak writes only `ak-*` entries in `models.json`. Everything else is preserved,
and when ak's own entries have not changed the file is not rewritten at all.
Each entry carries the model's `contextWindow` and `maxTokens` when ak knows
them (see [Model numbers](#model-numbers)).

## Details worth knowing

**Nothing global is rewritten.** `ak` never writes provider keys into
`~/.claude/settings.json`. That file's `env` block is applied after the shell
environment, so a leftover `ANTHROPIC_BASE_URL` there silently overrides every
launch — it appears to work but talks to the wrong provider. `ak doctor`
flags it. Your global preferences stay in `settings.json` untouched.

**Deletion is guarded.** `ak sync` only removes a file that matches the prefix,
is a regular file (never a symlink), is owned by you, and carries an
`ak:generated` marker. The tools in `~/.local/bin` that are not ours are left
alone.

**Codex providers are layered, not copied.** A launch passes the provider,
endpoint and model as `-c` overrides on top of your base `~/.codex/config.toml`,
so your `projects`, `skills` and `hooks` keep working, and codex's `env_key`
reads the key from the process environment: no per-provider `auth.json`, no
isolated `CODEX_HOME`. A provider whose model codex has no metadata for can set
`model_catalog` to a JSON catalog, passed as `-c model_catalog_json`. Codex
replaces its whole built-in catalog with that file, so set it only on the
provider whose model needs it. Without one, ak passes what it knows as
`-c model_context_window` (see [Model numbers](#model-numbers)).

**Keys live in their own file.** `providers.toml` holds no key: a provider
names its entry with `auth_key` (its own name by default), and the key is in
`~/.config/ak/auth.json` (mode `0600`), which ak rewrites whole on every change,
dropping entries nothing names. Providers on one account can name the same
entry. Keys are resolved when ak launches, never written to `~/.local/bin`.
With `api_key_ref` (`env:NAME`, `cmd:...`, `keychain:...`) no plaintext copy is
on disk at all.

**Usage is computed from local logs.** `~/.claude/projects` and
`~/.codex/sessions` are scanned incrementally: the first run reads the whole
history, later ones only the bytes appended since. Each request counts once —
Claude writes a message once per content block, Codex repeats token events,
and resumed or forked sessions copy history into new files. Days split at
local midnight. Claude's logs do not record which provider a session used, so
ak starts each new claude session with its own `--session-id` and records the
mapping; `ak hook install` adds a `SessionStart` hook that also covers
sessions ak did not name, such as a fork. Codex
sessions are attributed by their `provider_id`. An id that the base
`~/.codex/config.toml` also declares is left alone and reported under the id
itself, because a log holding nothing but that id cannot say which provider
ran it; `ak doctor` names the collision, and dropping the provider's
`provider_id` clears it — the provider's own name is already unique and is
what codex falls back to. Cost comes from models.dev; models it
does not know are reported as unpriced rather than billed as zero. Usage that
is not an ak provider's (an engine's own login, another tool's provider id)
is listed apart, under "not in ak"; `--ak-only` leaves it out.

**cc-switch import is selective.** `ak import --from cc-switch` reads
`~/.cc-switch/cc-switch.db` read-only, prints what it found, then opens a picker
so you choose exactly which providers to copy. Nothing is written until you
confirm, and cc-switch's own database is never modified. Providers that are
ready to use are pre-selected; ones still missing an API key are left unticked
so a half-configured provider is not imported by accident. `--all` skips the
picker for scripted use.
