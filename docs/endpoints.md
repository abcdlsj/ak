# Endpoints and what an account refuses

A vendor says what it serves, one endpoint per wire protocol. An account is a
key on one vendor, so it serves what that vendor serves, and ak's two ways of
reading it — by engine, for launching an agent, and by protocol, for a request
that arrived at the service — answer from the same list.

## Saying what a vendor serves

```toml
# vendors.d/relay.toml
id = "relay"
name = "A relay of two"

[[endpoints]]
protocol = "anthropic"                 # Anthropic Messages
base_url = "https://relay.example"
model    = "claude-sonnet-5-5"
auth     = "x-api-key"                 # the default for anthropic

[[endpoints]]
protocol = "responses"                 # OpenAI Responses
base_url = "https://relay.example/v1"
model    = "gpt-6-luna"
auth     = "bearer"                    # the default for chat and responses
```

`protocol` is one of `anthropic`, `chat`, `responses`. `auth` is `bearer`
(`Authorization: Bearer …`) or `x-api-key`, and left out it follows the
protocol's habit. A vendor may serve one protocol or all three.

The older shape, one table per engine, still reads and is what every built-in
vendor uses:

```toml
[engines.claude]
base_url = "https://relay.example"
model = "claude-sonnet-5-5"
[engines.codex]
base_url = "https://relay.example/v1"
model = "gpt-6-luna"
wire_api = "responses"
```

Read as protocols, that is an `anthropic` endpoint and a `responses` one; read
as engines, a vendor that lists `endpoints` still answers Claude Code and Codex
from them, each taking the protocol it speaks. Writing a vendor with
`[[endpoints]]` is the way to say "this one serves chat too".

## What a model name reaches

`GET /v1/models` reports, for each name, how each protocol reaches it:

| Value | Means |
| --- | --- |
| `native` | a member serves that protocol as it is; only the model name is rewritten |
| `translated` | no member serves it, so the nearest one is used and the request is translated |
| `mixed` | a pool with both: some members native, some translated |
| `absent` | no member serves that protocol at all |
| `refused` | the account answered that it will not serve this model on that protocol |

`absent` used to be left out of the answer, which read the same as "unknown".
It is named now, so a client can tell the two apart before it sends anything.

A pool whose members answer with different models also lists them:

```json
{"id": "main",
 "ak": {"protocols": {"anthropic": "native", "chat": "translated", "responses": "native"},
        "models": ["deepseek-v4.1-flash", "gpt-6-luna"]}}
```

## Refusals

A vendor that answers `403 permission_error` ("this model is not on your plan"),
`404 model_not_found` or `401` to a key is not failing: it will say the same to
every request after it. ak remembers that answer for a day, by account, model
and protocol, and:

- answers the next request from what it remembers instead of asking again;
- asks the account for another protocol that serves the model, so a plan that
  has Responses but not Anthropic is translated to rather than refused;
- does not cool the member off: it serves its other models well;
- reports it under `refused` in `/v1/models`, with the reason.

A refusal is not a cooldown. A `429`, a `5xx` or a network failure still rests
the member and fails over as it did.

```console
$ ak serve after
ACCOUNT        MODEL          PROTOCOL   REASON            SEEN
command-code   claude-sonnet-5-5  anthropic  permission_error  11-14 15:02
$ ak serve after --forget          # a bought plan or a new key takes effect now
```

The record lives in `capabilities.json` next to `providers.toml`, mode 0600.
Removing it, or `--forget`, makes the next request ask again.
