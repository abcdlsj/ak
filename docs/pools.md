# Pools: balance and aggregate providers

A pool is a provider backed by several others of the same kind, so one launch
spreads over them: fail over when one is down, or rotate for load. The engine
talks to ak's own loopback gateway, which injects each member's real key, so
the engine never sees one.

```sh
ak add pool --kind claude --member kimi --member cpa --strategy order
ak add pool --kind codex --member cpa --member step --strategy rotate \
  --map step=gpt-5.6-luna
ak serve            # optional: launching a pool starts it in the background
```

| Flag | Meaning |
| --- | --- |
| `--member` | a provider in the pool, or another pool; repeatable and ordered |
| `--strategy` | `order` (failover, the default), `rotate` (round-robin), `least-used` or `smart` |
| `--map` | `member=model`: ask that member for a different model |

**Smart** reads each member's quota (the same sources as `ak quota`) when the
gateway starts, every five minutes and on reload, and puts first the member
with allowance left whose plan window resets soonest, so the least of it is
lost at the reset. Members with nothing to reset (a balance) or no quota
source come next, in order, and members with nothing left come last by when
they are back. Only smart pools make the gateway call a balance API, and
`ak doctor` names the members a smart pool cannot read.

**Nesting.** A member may itself be a pool of the same kind, which picks among
its own members by its own strategy, up to four levels deep and never leading
back to itself. A `--map` deeper down wins over one above it. Health, cooldown
and conversation affinity are kept per concrete provider of the pool the
request came to:

```sh
ak add kimis --kind claude --member kimi-a --member kimi-b --strategy smart
ak add main  --kind claude --member kimis --member deepseek   # kimis first, then deepseek
```

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
endpoint as a `codex` provider, then pool each kind separately. Launching a pool
starts `ak serve` in the background when nothing is listening, logging to
`~/.local/share/ak/gateway.log`; `ak doctor` warns when a pool exists but
nothing is listening on the gateway.

With the gateway running, the TUI's Manage page shows a pool as it works: a
sparkline per member of requests per minute over the last hour, scaled together
so a busier member visibly outranks a quieter one, with each member's totals and
any cooldown. The gateway answers `GET /stats` with the same data, and
`GET /healthz` with a cheaper liveness plus each member's cooling state.

The affinity table is kept on disk, so a restart keeps every conversation on the
member the vendor still has it cached at. `ak serve` reloads `providers.toml`
when it changes (or on SIGHUP), so an edited or renamed pool takes effect
without a restart; `settings.gateway_addr` still needs one.
Normal providers never touch the gateway: they
stay direct, with their own key.
