# ak

Run multiple Claude Code, Codex and Pi providers side by side.

```
ak kimi                 claude on kimi
ak bilicodex:high       codex on the company gateway, reasoning high
ak cpapi -p "..."       pi on your own relay, one-shot
ak                      the launcher: pick one with enter or 1-9
```

## Why

Most provider managers model "the current provider" as a single global value
and rewrite `~/.claude/settings.json` on every switch. That model fights the way
agent work actually happens: several worktrees, several sessions, several
agents running at once. Global state means constant switching, and a switch in
one pane silently changes what every other pane talks to.

`ak` moves the choice from time (switching) to space (side by side). You name
the provider when you launch; nothing global is set or overwritten, so every
pane keeps talking to what it started with.

## Get started

```
go install github.com/abcdlsj/ak@latest
ak                                       # with nothing set up, opens the add form
ak add kimi --preset kimi-coding --kind claude --key -   # or add one directly
ak kimi
```

`--key -` asks for the key, so it stays out of shell history. `ak preset list`
shows the built-in vendors; `ak import --from cc-switch` (or `--from pi`)
copies providers you already have.

## Commands

```
ak <name>[:variant] [args...]   launch; args go to the engine untouched
ak                              launcher, opens on what you last used here
ak status [name...]             health, balance and last 7 days of usage
ak list                         providers
ak add / edit / rm / rename     change providers (flags, or a form)
ak import --from cc-switch|pi   copy providers over
ak usage | quota | check | models   the parts of status, for scripts
ak doctor                       anything that would make a launch misroute
ak-<name> [args...]             the same as ak <name>, for scripts
```

## More

- [Guide](docs/guide.md): variants, the launcher, environment, presets,
  sharing or isolating config, pi, how usage is attributed.
- [Pools](docs/pools.md): one provider spread over several, with failover,
  rotation and a quota-aware strategy.
- [Health, balance and usage](docs/status.md): `ak status`, `ak check`,
  `ak quota` and its plugins.

## Rolling back

Delete the `ak-*` commands, `~/.config/ak/`, and the `ak-*` entries in pi's
`models.json` if you used pi. Nothing else was modified.
