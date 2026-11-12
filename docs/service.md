# ak as a service

`ak serve` can run on a server that holds every subscription's credentials.
Each machine you own then points its agents (claude, codex, pi, any
OpenAI- or Anthropic-compatible tool) at one base URL with one token, and
picks a subscription by the model name. Clients hold no upstream key.

This page describes the design and what was built from it.

## Model names

A model name picks the route. There are four forms:

| Name | Means |
| --- | --- |
| `<account>` | the account's vendor's default model, for the protocol asked |
| `<account>/<model>` | any model at that account; `<model>` goes upstream as is |
| `<alias>` | another model name, from `[service.routes]` |
| `<pool>` | several model names with failover or rotation, from `[service.routes]` |

The split is at the first `/`, so a model holding slashes of its own
works: `command-code/deepseek/deepseek-v4.1-flash` asks the account
`command-code` for `deepseek/deepseek-v4.1-flash`. Route names and account
names share one namespace and contain no `/`; a route named like an account
is refused.

With accounts `bili` (a vendor of your own in `vendors.d`, serving claude
and codex), `command-code` and `opendesign`:

```
bili                          bili's claude model on /v1/messages, its codex model on /v1/responses
bili/glm-5                    glm-5 at bili, whatever the protocol
command-code                  claude-sonnet-5-5 on /v1/messages; deepseek/deepseek-v4.1-flash on /v1/responses
opendesign/deepseek-v4-pro    deepseek-v4-pro at opendesign
main                          a pool: bili/glm-5, then command-code, smart
fast                          an alias of command-code/deepseek/deepseek-v4.1-flash
```

**Which upstream answers.** An account's vendor lists an endpoint per
engine, and each engine speaks one protocol:

| Engine | Protocol | Upstream path |
| --- | --- | --- |
| claude | Anthropic Messages | `<base>/v1/messages`, `/v1/messages/count_tokens` |
| codex, `wire_api = "responses"` | OpenAI Responses | `<base>/responses` |
| codex, `wire_api = "chat"` | OpenAI Chat Completions | `<base>/chat/completions` |
| pi, `pi_api = "anthropic-messages"` | Anthropic Messages | `<base>/v1/messages` |
| pi, `pi_api = "openai-completions"` | OpenAI Chat Completions | `<base>/chat/completions` |
| pi, `pi_api = "openai-responses"` | OpenAI Responses | `<base>/responses` |

A request goes to the vendor's first engine (claude, codex, pi) that speaks
its protocol, untranslated: only the model name is rewritten. When none
does, it is translated for the engine whose protocol is nearest: an
Anthropic request prefers Chat, then Responses; a Responses request prefers
Chat, then Anthropic; a Chat request prefers Responses, then Anthropic. The
default model of `<account>` is that engine's.

## Configuration

Everything the service adds is under `[service]` in `providers.toml`, so
`ak serve` reloads it like the rest:

```toml
[service]
addr = "127.0.0.1:8787"            # where the service listens; behind Caddy or nginx
admin_password = "$2a$12$..."      # bcrypt, set by `ak serve admin set-password`

[service.routes.main]              # a pool
members = ["bili/glm-5", "command-code", "opendesign/deepseek-v4-pro"]
strategy = "smart"                 # order (default), rotate, least-used, smart

[service.routes.fast]              # an alias
model = "command-code/deepseek/deepseek-v4.1-flash"

[service.tokens.laptop]            # written by `ak serve token add`
hash = "sha256:9f2c..."
routes = ["main", "bili"]          # omitted: every route
created = 2026-11-12T10:00:00Z
```

A pool's members are any model names, pools among them, nested up to four
deep and never leading back to themselves. `routes` on a token names routes
and accounts; an account allows `<account>` and every `<account>/<model>`.
A route allowed is allowed whole: its members need no listing.

The pool gateway's own listener (`settings.gateway_addr`, loopback, `/p/`)
is unchanged and keeps serving local pools; the service is a second
listener of the same process, started when `service.addr` is set.

## HTTP surface

On `service.addr`:

| Method and path | Auth | Answers |
| --- | --- | --- |
| `POST /v1/messages` | token | Anthropic Messages, streamed or not |
| `POST /v1/messages/count_tokens` | token | Anthropic token count; Anthropic upstreams only |
| `POST /v1/responses` | token | OpenAI Responses |
| `POST /v1/chat/completions` | token | OpenAI Chat Completions |
| `GET /v1/models` | token | the model names the token may use; Anthropic's shape when `anthropic-version` is sent |
| `GET /ak/v1/quota` | token | the quota readings of the accounts the token may use |
| `GET /ak/v1/usage?days=7` | token | the token's own usage by day, route and account |
| `GET /healthz` | none | `{"status":"ok"}`, nothing more |
| `/ui/…` | admin session | the web UI |

A token is sent as `Authorization: Bearer <token>` or `x-api-key: <token>`,
the two ways the engines send a key. Each `/v1/models` entry carries an
`ak.protocols` object saying, per protocol (`anthropic`, `responses`,
`chat`), whether the route answers it `native`, `translated`, or `mixed`
(a pool with members of both). `/v1/models` lists the routes, each account,
and each account's engines' default models as `<account>/<model>`; any other
`<account>/<model>` is accepted too.

## Translation

Translation uses github.com/maximhq/bifrost/core (Apache-2.0) behind one
package, `internal/translate`; nothing else imports bifrost. Its surface is
the protocol pair and stream or not:

```go
translate.Request(from, to, body, model) ([]byte, error)
translate.Response(from, to, body) ([]byte, error)
translate.NewStream(from, to) *Stream  // .Event(data) ([]byte, error), .Close() []byte
translate.Error(to, status, message) []byte
```

Every protocol is read into bifrost's Responses types and written out of
them, so each pair is two calls. bifrost's HTTP clients and transports are
not used: ak keeps its own forwarding, key injection, failover and
affinity. The functions called:

| Step | Anthropic Messages | OpenAI Responses | OpenAI Chat |
| --- | --- | --- | --- |
| request in | `AnthropicMessageRequest.ToBifrostResponsesRequest` | `OpenAIResponsesRequest.ToBifrostResponsesRequest` | `OpenAIChatRequest.ToBifrostChatRequest`, then `BifrostChatRequest.ToResponsesRequest` |
| request out | `anthropic.ToAnthropicResponsesRequest` | `openai.ToOpenAIResponsesRequest` | `BifrostResponsesRequest.ToChatRequest`, then `openai.ToOpenAIChatRequest` |
| reply in | `AnthropicMessageResponse.ToBifrostResponsesResponse` | decoded as `BifrostResponsesResponse` | decoded as `BifrostChatResponse`, then `.ToBifrostResponsesResponse` |
| reply out | `anthropic.ToAnthropicResponsesResponse` | encoded | `BifrostResponsesResponse.ToBifrostChatResponse` |
| stream in | `AnthropicStreamEvent.ToBifrostResponsesStream` with `AcquireAnthropicResponsesStreamState` | decoded as `BifrostResponsesStreamResponse` | `BifrostChatResponse.ToBifrostResponsesStreamResponse` with `AcquireChatToResponsesStreamState` |
| stream out | `anthropic.ToAnthropicResponsesStreamResponse` | encoded | `BifrostResponsesStreamResponse.ToBifrostChatResponse` |

Two gaps are filled in ak's package: bifrost leaves an Anthropic stream's
usage to its own accumulator, so ak carries `message_start` and
`message_delta` usage onto the completed event; and a chat upstream reports
usage in a chunk after its finish, so the completed event is held until it.
bifrost's `extra_fields` are dropped from what clients see.

The matrix, each pair tested on hand-written Claude Code, Codex and chat
bodies with a tool-call turn, a plain reply and a streamed one, checking
text, reasoning, the tool call and its arguments, and usage:

| client \ upstream | Anthropic | Responses | Chat |
| --- | --- | --- | --- |
| Anthropic | native | translated | translated |
| Responses | translated | native | translated |
| Chat | translated | translated | native |

No pair failed, so every route is offered on every protocol. A single
request bifrost cannot convert fails over to the next member like an
upstream error, and alone it is answered with 400 naming the reason.
`count_tokens` is Anthropic's own and goes only to an Anthropic upstream.

## Tokens

```
ak serve token add laptop [--route main --route bili]   prints the token once
ak serve token list                                       name, routes, created
ak serve token revoke laptop
```

A token is `ak_` and 43 characters of base64url (32 random bytes). Only
its SHA-256 is stored; the comparison hashes what was sent and looks the
hash up, so a token's characters are never compared one by one. Revoking
takes effect on the next request: the config reload drops it.

`ak serve` refuses to start the service on an address that is not loopback
while no token is defined, and a request without a known token gets 401.

## Admin login and the web UI

`ak serve admin set-password` asks for the password twice and stores its
bcrypt hash. With no password set the UI shows only how to set one.

The UI is served by `ak serve` itself under `/ui/`: Go `html/template`,
a little vanilla JS, embedded, no build step. It wears the TUI quota
board's look, so the two read as one product: on water.css (v2.1.7, MIT,
vendored and embedded) sits a small sheet of ak's own, in Avenir Next with
system fallbacks. A page is a heading with a rule, cards or a table.

Its mark is a pixel `k` on the same 5-pixel grid as the numbers, the
favicon and the header's; the login page is that mark above a quiet card.

- **Accounts**: the board's cards, one per account: its hue from the
  vendor's id, each window's share used as the board's pixel number with a
  tick bar under it, amber past 80% and red once spent, the balance below.
  Sign in to a vendor by device code or by pasting a key; remove one no
  provider, route or token uses.
- **Routes**: aliases and pools as a table, members as chips, with the
  model-name legend beside the form that adds them.
- **Tokens**: issue one (shown once), limit it to routes, revoke it.
- **Usage**: counts as tiles; the year's tokens a day as GitHub draws it, a
  column a week, the months above, four shades by quantile, the year's total
  in the pixel font and the peak day named; then requests and tokens by
  token, account and route over 7 or 30 days, each row with a bar against
  the largest.

Pages follow the system's light or dark preference. The bars' shares are the
only inline styles, and their values are numbers the server computes.

The session is a random id in a cookie `ak_session`: HttpOnly,
SameSite=Strict, Secure when the request came over TLS or with
`X-Forwarded-Proto: https`, for 12 hours, held in memory (a restart signs
out). Every form carries the session's CSRF token, and a POST whose
`Origin` is another host is refused. Five failed logins within five minutes
lock the login for the rest of those minutes; the count is global, because
behind a proxy every client has the proxy's address. Logout ends the
session. An API token never opens the UI, and the session cookie is never
accepted on `/v1/` or `/ak/v1/`.

## Clients

```
ak remote add home https://ak.example.com --token -
ak add --account home --kind claude --model main     # claude on the pool main
ak add --account home --kind codex --model bili      # codex on bili's default
ak quota                                             # home's accounts among the rest
```

`ak remote add` checks the server answers `/v1/models` with the token, then
writes the vendor `vendors.d/home.toml` (claude and pi at the URL on
Anthropic Messages, codex at `<url>/v1` on Responses, `quota = "ak"`) and the
account `home` on it with the token as its key. The vendor has no default
model, so a provider on it names its route with `--model`. Everything after that is ak's usual flow: providers, bindings, the
launcher. `ak remote list` lists them, `ak remote rm home` removes the
vendor and the account once no provider runs on it.

The quota source `ak` asks `/ak/v1/quota`, and `ak quota` and
`ak quota board` show each of the server's accounts as `home/<account>`.
The server reads each account's quota at most once a minute, whoever asks.

## Deploying

One binary, its state in `AK_CONFIG_DIR`: `providers.toml`, `auth.json`,
`service-usage.json` (366 days of usage by day, token, route and account) and
the affinity tables. `deploy/` holds a systemd unit (`ak.service`), a
`Dockerfile`, a `Caddyfile` and an `nginx.conf`. TLS is the reverse proxy's
job: ak has no ACME. Run `ak serve` with `service.addr` on loopback and let
Caddy or nginx terminate TLS and forward to it; streams need the proxy not to
buffer (Caddy: `flush_interval -1`; nginx: `proxy_buffering off`).

On the server:

```sh
ak login command-code --key -            # each subscription, as on a laptop
ak serve admin set-password
ak serve token add laptop                # prints the token once
# providers.toml: [service] addr = "127.0.0.1:8787", routes as above
systemctl enable --now ak                # deploy/ak.service
```

On each machine:

```sh
ak remote add home https://ak.example.com --token -
ak add --account home --kind claude --model main
```

Any other tool takes `https://ak.example.com` (Anthropic) or
`https://ak.example.com/v1` (OpenAI) as its base URL and the token as its key.

## Failure behaviour

| Case | Answer |
| --- | --- |
| upstream down or 5xx | the same member once more, then the next member of a pool; alone, 502 |
| spent quota (402, 429, quota words) | the member cools off; a pool fails over; smart ranks it last; alone, 502 with the upstream's reason |
| unknown model | 404 naming it, in the client's protocol, pointing at `/v1/models` |
| no token, unknown or revoked token | 401 |
| a model outside the token's routes | 403 |
| a request bifrost cannot convert | the next member; alone, 400 with the reason |
| `count_tokens` with no Anthropic upstream | 400 saying so; nothing is sent upstream |
| a translated stream that breaks | an error event in the client's protocol, then the end |

Errors are written in the client's protocol, so the agent shows the
message rather than failing to parse it.

## Security

- Upstream keys stay on the server: the client's credentials are stripped
  and each member's key set as its engine expects, as in the pool gateway.
  Keys and tokens are never logged or written in a reply.
- Tokens are stored as SHA-256, the admin password as bcrypt.
- Cookies and the client's `Authorization` and `x-api-key` never go
  upstream.
- Bodies are bounded (128 MB), as are streams that stall; in-flight
  requests are bounded by `settings.max_inflight`.
- UI responses carry `Content-Security-Policy: default-src 'self';
  style-src 'self' 'unsafe-inline'` (inline styles are the bars' widths and
  heatmap colours, never text from a request), `X-Frame-Options: DENY` and
  `Referrer-Policy: same-origin`.

## What reuses what

| Package | Reused for |
| --- | --- |
| `gateway` | forwarding, key injection, retry, cooldown, failover, affinity, the strategies and smart's quota reads, the in-flight gate, stream bounds, reload |
| `config` | accounts, vendors, `Resolve`, `Save`, `Validate`; `[service]` is a new section of the same file |
| `vendor` | engines per vendor, which say which protocol an account speaks; `vendors.d` holds remotes |
| `core` | `Holders` for the quota endpoint; account sign-in from the UI |
| `quota` | readings for `/ak/v1/quota` and the UI; the `ak` source on clients |
| `login` | device sign-in from the UI |
| `out`, `ui` | the CLI commands and `ak quota board` on clients |

## Implementation plan

Each step is one commit, with `gofmt`, `go vet` and `go test` green. All
nine are done.

1. This document.
2. `internal/translate` over bifrost, tests for every pair, plain and
   streamed, from hand-written fixtures.
3. `config`: `[service]` with routes and tokens, validation (names, cycles,
   targets, collisions with accounts), and a provider for an account on a
   given engine. Tests for each refusal.
4. `gateway`: the service listener, tokens, model resolution, native
   forwarding through the existing failover, `/v1/models`. Tests against
   httptest upstreams: each protocol native, a pool failing over, an alias,
   unknown model, revoked token, a token limited to routes.
5. `gateway`: translated forwarding, plain and streamed; usage recorded per
   token, route and account; `/ak/v1/usage` and `/ak/v1/quota`. Tests: a
   Codex request served by an Anthropic upstream and the reverse, streams
   included; usage counted.
6. `cli`: `ak serve` starts the service, refuses a public address without
   tokens; `ak serve token add|list|revoke`; `ak serve admin set-password`.
7. Clients: `ak remote add|list|rm`, the `ak` quota source, remote accounts
   in `ak quota` and the board.
8. The web UI: login, sessions, CSRF, rate limit; Accounts, Routes, Tokens,
   Usage. Tests for the security rules and each form.
9. `deploy/` (systemd unit, Dockerfile, Caddyfile) and README.
