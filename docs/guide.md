# Guide

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
unless the provider names a `default_variant`; `/` filters. Launch, Manage and
Usage are tabs: `tab` / `shift+tab` cycle them, `m` and `u` jump straight to
one, and `esc` comes back to Launch:

- `m` Manage shows each provider's health, details and last 30 days of usage;
  `enter` edits (the name too, which renames the command), `a` / `d` add and
  delete, `*` sets the default and `s` syncs. In the form `ctrl+s` saves from
  any field.
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
[providers.bilicodex]
default_variant = "high"   # launch with this variant unless another is named

[providers.bilicodex.variants.fast]
model = "gpt-5.6-luna"
reasoning = "low"
shim = true          # also generate ak-bilicodex-fast
```

`default_variant` is a built-in thinking/reasoning level or a model tier, or
one of the provider's own variants. With it, `ak bilicodex` runs at that level
unless you name another variant, and the launcher's picker is skipped.

### Environment

Extra environment variables go under `env`, for any kind, on the provider or
on a variant. A proxy for one provider is just that:

```toml
[providers.bilicodex.env]
HTTPS_PROXY = "http://127.0.0.1:7890"
```

A launch from inside another ak launch drops what the outer one exported
first, and unsets every provider key it does not set, so one provider's proxy,
config dir, endpoint or key never leaks into the next. A pool's upstream
requests are made by `ak serve`, so its proxy goes in the environment of
`ak serve`. `ak env kimi:high` prints what a launch would inject, keys masked.

### Generated commands

`ak` keeps one source of truth in `~/.config/ak/providers.toml` (mode `0600`)
and derives everything else. Each `ak-<name>` command is a two-line script
that runs `ak run <name> "$@"`: it holds no key and no launch logic, so it
never drifts from the config. Every change through ak (`add`, `edit`, `rm`,
`rename`, `import`) regenerates the commands; `ak sync` is only for repair.

## Presets

Known vendors come built in, so adding one needs only a name and a key:

```
ak preset list [--kind claude]        id, engine, endpoint, model, where to get a key
ak add kimi --preset kimi-coding --kind claude --key sk-...
```

`--kind` is needed only when the preset exists for more than one engine. Any
other flag overrides the preset, e.g. `--model`. With no flags, `ak add` and
the TUI ask for the engine and an optional preset first, then open the form
pre-filled. The data comes from cc-switch's presets; vendors that need OAuth or
protocol translation are left out.

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
isolated `CODEX_HOME`.

**Keys stay out of the commands.** The key is resolved when ak launches, never
written to `~/.local/bin`. With `api_key_ref` (`env:NAME`, `cmd:...`,
`keychain:...`) no plaintext copy is on disk at all.

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
ran it; `ak doctor` names the collision. Cost comes from models.dev; models it
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
