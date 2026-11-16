# The model ak holds

ak exists to answer one question well: **which upstream answers this request**,
and to keep that answer cheap to change. Everything else — commands, isolation,
usage, quota — hangs off the answer.

This page states the model as it is implemented today, names the places where
it strains, and proposes the consolidation it is heading for. Read it before
adding a concept; every new field so far has cost a word elsewhere.

## The four layers

```
  vendor ──endpoint(protocol, base_url, model, auth) ──┐
    ▲                                                  │ knowledge of an upstream
    │                                                  ▼
  account ──key, user, secrets, plan model ────────► sign-in at a vendor
    │
    │  (a provider either runs on an account, or names an endpoint and key)
    ▼
  provider ──kind, model, variants, env, isolation ──► one command: ak-<name>
    │  └─ pool: members, member_models, strategy ──► ak's loopback gateway
    ▼
  name ──what a client puts in "model" ─────────────► service route (ak serve)
```

| Layer | Answers | Lives in |
| --- | --- | --- |
| **vendor** | what does this upstream speak, and where | `vendors.json`, `vendors.d/*.toml` |
| **account** | who am I there, and what does my plan serve | `[accounts.<name>]` |
| **provider** | what command do I run, with what environment, and what model do clients ask it for | `[providers.<name>]` |
| **name** | what does a client ask for | the provider's `model`, or `[service.routes]` |

Three engines (`claude`, `codex`, `pi`) and three protocols (`anthropic`,
`responses`, `chat`) are orthogonal to all of it: an engine is a command ak
launches, a protocol is how bytes go on the wire. `Endpoint.Protocol` states
which protocol an endpoint speaks; `wire_api` and `pi_api` are derived from it,
never chosen separately.

## One request, resolved

```
client (claude/codex/pi)                    ak
  │  POST /v1/messages {model: "main"}       │
  ├─────────────────────────────────────────►│  name "main"
  │                                          │   ├─ route members: [command-code, opendesign]
  │                                          │   ├─ account model: deepseek/deepseek-v4.1-flash
  │                                          │   ├─ protocol preference: anthropic, chat, responses
  │                                          │   └─ refusal memory: anthropic cannot serve it → chat
  │                                          ▼
  │                                    leg(account=command-code,
  │                                        model=deepseek/deepseek-v4.1-flash,
  │                                        protocol=chat, endpoint=…/chat/completions)
  ◄──────────── translated back to anthropic ┘
```

A **leg** is the only thing that talks to an upstream: one account, one model,
one protocol, one endpoint. Routing is a search over legs; everything else is
bookkeeping about them.

## Names and models are different words

This is the distinction the UI kept losing, and the source of the last two
complaints:

| Word | Example | Meaning |
| --- | --- | --- |
| **name** | `dpsk`, `ak-hk-claude`, `command-code` | what you ask for: a route, a provider's command, an account |
| **model** | `deepseek/deepseek-v4.1-flash` | what a vendor is asked for; the only thing with a price or a context window |
| **member** | `command-code`, `opendesign/deepseek-v4-flash` | what a pool routes over: a provider, an account, or an account's model |

A name may stand for one model, several (a pool, a fallback chain), or a name
another ak service chose. `core.Serves` resolves names to models through
pools, member maps and account models; a name a remote chose is resolved from
what that service last said (`GET /v1/models`, kept in `remote-models.json`).
`ak list`, the TUI's model column and its detail panel all print the resolved
models now, with the name in front when the two differ:

```
ak-hk-claude   claude   main → deepseek-v4.1-flash +1   account hk
ak-pool3       codex    deepseek/deepseek-v4.1-flash +1 pool(order: a, b)
```

## Precedence, in one place

Which model a member is asked for, strongest first:

1. the pool's `member_models[member]`
2. the member's own `model`
3. the nearest pool above that names a model
4. the model the request carried

Protocol preference for a leg, first that answers:

1. the protocol the client speaks
2. the account's other protocols, nearest first (`anthropic → chat → responses`)
3. skipping any the account is known to refuse for this model

## Where it strains

1. ~~**Two pools.**~~ **Resolved (v0.3.7).** A route may name a provider pool
   (`[service.routes.dpsk] provider = 'dpsk'`) and a pool's member may be an
   account, so one pool is defined once and answers both as `ak-<name>` and as
   a model name. What is still two-sided is the *code path*: the listener
   translates protocols, meters usage and remembers refusals, the loopback
   gateway does not. Unifying those is the next step, and it is now a matter of
   running one relay instead of two.
2. **Accounts are the routing unit on one side, providers on the other.** "What
   does `main` do" has a different answer depending on which side asks.
3. **Four places hold a model.** A name can be overridden by a pool map, a
   provider, an account, and an endpoint. Each was added for a good reason; the
   sum is hard to predict without `ak list` and `ak list --refresh`.
4. **Isolation is per engine field** (`config_dir`, `codex_home`), so a fourth
   engine needs a fourth field and a fourth form row.

## The consolidation, in three steps

Each step is worth doing alone; none breaks a config that exists today.

**1. One pool, two front doors** *(done in v0.3.7)*. `[providers.x] members` is
the single pool definition, and `[service.routes.x] provider` names it instead
of repeating the members. A member may be an account, which is what a route
always routed to.

```toml
[providers.dpsk]
kind = 'codex'
members = ['command-code', 'opendesign']   # accounts
strategy = 'order'

[service.routes]
dpsk = { provider = 'dpsk' }
```

**2. A leg, printed.** *(Not done.)* `ak show <name>` renders the resolved legs — account,
model, protocol, endpoint, in fallback order — and `ak list --refresh` is what
the TUI reads. One screen answers "what will actually be asked", so the four
precedence rules never need to be remembered.

**3. Isolation as data.** *(Not done.)* Replace `config_dir`/`codex_home`/a future
`antigravity_home` with `[providers.x.env]` plus one `home` field per engine,
declared by the engine rather than the config (the same move `Endpoint.Protocol`
made for protocols).

## Not part of the model

Cost and usage are *observations*, not configuration: they are read from the
engines' own logs and from a service's meter, priced from models.dev, and never
steer routing. Quota is the opposite: it is read to steer `smart` pools, and
nothing else. Keeping them out of the layers above is what stops this page from
growing.
