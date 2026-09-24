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
ak                          open the TUI (Providers / Usage, Tab to switch)
ak list                     list providers
ak add / rm / edit          manage providers
ak sync                     regenerate commands from the config
ak doctor                   check for anything that would break the commands
ak usage                    token usage by provider and model
ak-bilicodex                run codex on that provider
ak-bilicodex high           override reasoning effort for this run
ak-stepfunclaude -p "..."   run claude on that provider
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

**Usage is computed from local logs.** `~/.claude/projects` and
`~/.codex/sessions` are scanned incrementally, so the first run takes a few
seconds and later ones are near-instant. Claude's logs do not record which
provider a session used, so `ak hook install` adds a `SessionStart` hook that
records the mapping. Cost comes from models.dev; models it does not know are
reported as unpriced rather than billed as zero.

## Rolling back

Delete the `ak-*` commands, the `~/.codex/ak-*.config.toml` files and
`~/.config/ak/`. Nothing else was modified.
