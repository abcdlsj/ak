# ak

Run multiple Claude Code, Codex and Pi providers side by side.

```
ak kimi                 claude on kimi
ak relay:high           codex on your gateway, reasoning high
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

Needs Go 1.27.3+ and the engines you use (`claude`, `codex`, `pi`) on PATH.
Prebuilt binaries are on the [releases](https://github.com/abcdlsj/ak/releases) page,
and the service is on the registry as `ghcr.io/abcdlsj/ak` (see
[service.md](docs/service.md#deploying)).

```
go install github.com/abcdlsj/ak@latest
ak                                       # with nothing set up, opens the add form
ak login opencode-go                     # or sign in to a vendor first
ak add --account opencode-go --kind claude   # named opencode-claude
ak opencode-claude
```

`ak login` reads a sign-in another tool already has, runs a device sign-in, or
asks for the key, so it stays out of shell history.

To keep typing `claude` and `codex`, set up the shell once and bind:

```
ak install                   # zsh, bash or fish, from $SHELL; ak uninstall undoes it
ak bind kimi:high            # claude in this repository (every worktree) runs on kimi
ak default codex relay       # codex everywhere nothing is bound runs on relay
ak which                     # what claude / codex / pi run on here, and why
```

Where nothing is bound and no engine default is set, `claude` and `codex` stay
the engines' own, with their own settings. Flags every launch should get go in
`~/.config/ak/providers.toml`:

```toml
[settings]
claude_args = ['--dangerously-skip-permissions']
```

`ak vendor list` shows the vendors to sign in to; `ak import --from cc-switch` (or
`--from pi`, `--from claude-settings`, `--from codexa`) copies providers you
already have.

## Commands

```
ak <name>[:variant] [args...]   launch; args go to the engine untouched
ak                              launcher, opens on what you last used here
ak status [name...]             health, balance and last 7 days of usage
ak list                         providers
ak add / edit / rm / rename     change providers (flags, or a form)
ak login <vendor> / logout      sign in to a vendor as an account, or remove one
ak accounts                     accounts and the providers on each
ak import --from <source>       copy providers over
ak usage | quota | check | models   the parts of status, for scripts
ak quota board                  every plan and balance as cards, full screen
ak bind / unbind / bindings     what claude/codex/pi typed in a repository run on
ak default <engine> <name>      what they run on where nothing is bound
ak which                        what they run on here, and why
ak acp <engine>                 that engine's ACP server on the provider here
ak serve                        the pool gateway; with service.addr, the service for other machines
ak serve token add|list|revoke  the service's tokens, one per machine
ak remote add <name> <url>      use an ak service as a vendor and account; --model sets the providers up too
ak install / uninstall          set up the shell for the above (zsh, bash, fish)
ak doctor                       anything that would make a launch misroute
ak-<name> [args...]             the same as ak <name>, for scripts
```

## More

- [Guide](docs/guide.md): typing `claude` and `codex` directly, variants, the
  launcher, environment, accounts and vendors, sharing or isolating config, pi, how usage
  is attributed.
- [Pools](docs/pools.md): one provider spread over several, with failover,
  rotation and a quota-aware strategy.
- [ak as a service](docs/service.md): one server holding every
  subscription, one base URL and token per machine, the model name picking
  the subscription; Anthropic and OpenAI protocols translated as needed; a
  small web UI; `deploy/` has a systemd unit, a Dockerfile and proxy configs.
- [Health, balance and usage](docs/status.md): `ak status`, `ak check`,
  `ak quota` and its plugins.

## Rolling back

Run `ak uninstall` and `ak hook uninstall`, then delete the `ak-*` commands,
`~/.config/ak/`, and the `ak-*` entries in pi's `models.json` if you used pi.
Nothing else was modified.
