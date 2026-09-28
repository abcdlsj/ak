# ak

Run multiple Claude Code and Codex providers side by side.

Instead of switching one active provider, `ak` gives every provider its own
command. Pick a provider by typing its name, not by changing global state — so
several sessions can use different providers at the same time without stepping
on each other.

```
ak-bilicodex        codex   gpt-5.6-terra    company gateway
ak-cpacodex         codex   gpt-5.6-luna     own relay
ak-stepfunclaude    claude  step-5-preview
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
ak edit <name> [--model …]  change only the given fields, or open the form
ak rm <name>                remove a provider and its commands
ak import --from cc-switch  pick which cc-switch providers to copy over
ak sync                     regenerate commands from the config
ak doctor                   check for anything that would break the commands
ak usage                    token usage by provider and model
ak-bilicodex                run codex on that provider
ak-bilicodex high           override reasoning effort for this run
ak-stepfunclaude opus       use the opus-tier model for this run
ak-stepfunclaude -p "..."   run claude on that provider
```

The TUI opens on a launcher: the providers in a list, the cursor on the
default. `enter` launches the selected one and `1`-`9` launch a row directly,
asking for a variant first when there are any; `/` filters. Launch, Manage
and Usage are tabs: `tab` / `shift+tab` cycle them, `m` and `u` jump straight
to one, and `esc` comes back to Launch:

- `m` Manage shows each provider's health, details and last 30 days of usage;
  `enter` edits, `a` / `d` add and delete, `*` sets the default and `s` syncs.
- `u` Usage opens on all history and switches to 7, 30 or 90 days with `[`
  `]`; `enter` on a provider narrows the page to it.

A provider whose command would fail is marked red, and the launcher names the
problem under the list before you run it.

Custom variants go under the provider:

```toml
[providers.bilicodex.variants.fast]
model = "gpt-5.6-luna"
reasoning = "low"
shim = true          # also generate ak-bilicodex-fast
```

`ak` keeps one source of truth in `~/.config/ak/providers.toml` (mode `0600`)
and derives everything else. Generated shims are marked and idempotent, so
`ak sync` rewrites only what changed and reclaims what no longer applies.

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
sessions are attributed by their `provider_id`. Cost comes from models.dev;
models it does not know are reported as unpriced rather than billed as zero.

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
