# llmroute

[![CI](https://github.com/ssthil/llmroute/actions/workflows/ci.yml/badge.svg)](https://github.com/ssthil/llmroute/actions/workflows/ci.yml)
[![Release](https://img.shields.io/badge/release-v0.1.0-blue)](https://github.com/ssthil/llmroute/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A standalone, native, multi-LLM routing CLI proxy written in pure Go.

`llmroute` runs a small loopback HTTP gateway that speaks the OpenAI
`/v1/chat/completions` API. It inspects each incoming request, classifies its
**intent** (code, vision, or chat), and transparently routes it to the cheapest
capable upstream provider — automatically failing over to the next candidate
when a provider is rate-limited or down.

No Node. No Python. No CGO. A single static binary and a 0600 SQLite file.

`llmroute` is provider-agnostic: it accepts the OpenAI chat-completions request
format and forwards it to each provider's OpenAI-compatible endpoint. OpenAI,
Gemini (via Google's OpenAI-compat surface), and DeepSeek expose this natively.
Anthropic's native Messages API is not OpenAI-shaped at `/v1/chat/completions`,
so routing to it assumes an OpenAI-compatible gateway in front; a native
Anthropic adapter is not yet implemented.

---

## Features

- **Intent-based routing** — a text classifier flags requests as `code`,
  `vision`, or `chat` and maps them onto the right model.
- **Cheapest-first failover** — candidates are tried in ascending cost order; a
  `429`/`502`/`503`/`504` upstream transparently rolls over to the next model.
- **Credential leak interceptor** — request bodies are scanned for live key
  signatures (OpenAI/DeepSeek `sk-…`, Anthropic `sk-ant-…`, Google `AIza…`,
  AWS `AKIA…`) and blocked *before* any outbound network call.
- **Port-scan resiliency** — binds `127.0.0.1:4040`, stepping upward
  (`4041`, `4042`, …) if the port is busy.
- **Locked-down state** — config dir is `0700`, the SQLite database is `0600`.
- **Pure-Go SQLite** — uses [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite);
  builds are fully `CGO_ENABLED=0` and cross-compile cleanly.
- **Usage accounting** — every routed call is logged for per-model token stats.

---

## Install

### Download a release binary

Pre-built, CGO-free binaries are attached to every
[GitHub release](https://github.com/ssthil/llmroute/releases). Archives are
named `llmroute_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows).

**Linux / macOS:**

```sh
VERSION=0.1.0
OS=$(uname -s | tr '[:upper:]' '[:lower:]')   # linux | darwin
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')  # amd64 | arm64

curl -sSL -o llmroute.tar.gz \
  "https://github.com/ssthil/llmroute/releases/download/v${VERSION}/llmroute_${VERSION}_${OS}_${ARCH}.tar.gz"

# (optional) verify against the published checksums
curl -sSL -O "https://github.com/ssthil/llmroute/releases/download/v${VERSION}/checksums.txt"
shasum -a 256 -c checksums.txt --ignore-missing

tar -xzf llmroute.tar.gz llmroute
sudo install -m 0755 llmroute /usr/local/bin/llmroute
llmroute --version
```

**Windows (PowerShell):** download `llmroute_<version>_windows_amd64.zip` from the
[releases page](https://github.com/ssthil/llmroute/releases), extract it, and put
`llmroute.exe` somewhere on your `PATH`.

### Install with Go

```sh
go install github.com/ssthil/llmroute@latest   # latest tagged release
```

### Build from source

```sh
make build      # produces ./bin/llmroute
```

Cross-platform release archives are produced with
[GoReleaser](https://goreleaser.com): `make snapshot`.

---

## Quick start

```sh
# 1. Initialize the 0700 config dir + 0600 SQLite db and seed the model catalog
llmroute init

# 2. Export the provider keys you have (any subset works)
export OPENAI_API_KEY=sk-...
export GEMINI_API_KEY=...
export ANTHROPIC_API_KEY=sk-ant-...
export DEEPSEEK_API_KEY=sk-...

# 3. Boot the proxy (binds 127.0.0.1:4040, scans upward if busy)
llmroute proxy

# 4. Point any OpenAI-compatible client at it
curl http://127.0.0.1:4040/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"auto","messages":[{"role":"user","content":"write a quicksort in Go"}]}'

# 5. Inspect token usage
llmroute stats
```

The `model` field in your request is treated as a hint; `llmroute` overrides it
with the model it selects. Response headers `X-LLMRoute-Model` and
`X-LLMRoute-Intent` report what actually handled the request.

---

## Usage examples

### See which model intent routing picked

A coding prompt is classified as `code` and sent to the cheapest code-capable
provider with a key configured (DeepSeek first):

```sh
curl -sD - http://127.0.0.1:4040/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"auto","messages":[{"role":"user","content":"fix this: ```go\nfunc main(){fmt.Println(x)}```"}]}' \
  | grep -i '^x-llmroute'
# x-llmroute-intent: code
# x-llmroute-model: deepseek-chat
```

A prompt carrying an image is classified as `vision` and routed to Gemini Flash:

```sh
curl -sD - http://127.0.0.1:4040/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"auto","messages":[{"role":"user","content":[
        {"type":"text","text":"describe this"},
        {"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KG..."}}
      ]}]}' \
  | grep -i '^x-llmroute'
# x-llmroute-intent: vision
# x-llmroute-model: gemini-2.5-flash
```

### Drop-in with the OpenAI SDKs

`llmroute` speaks the OpenAI API, so existing clients work by changing only the
base URL — your real provider keys stay on the proxy host, not in the client.

**Python:**

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:4040/v1", api_key="not-needed")

resp = client.chat.completions.create(
    model="auto",  # llmroute picks the model by intent
    messages=[{"role": "user", "content": "summarize the CAP theorem in 2 lines"}],
)
print(resp.choices[0].message.content)
```

**Node / TypeScript:**

```ts
import OpenAI from "openai";

const client = new OpenAI({ baseURL: "http://127.0.0.1:4040/v1", apiKey: "not-needed" });

const resp = await client.chat.completions.create({
  model: "auto",
  messages: [{ role: "user", content: "write a haiku about routers" }],
});
console.log(resp.choices[0].message.content);
```

### Inspect usage

```sh
$ llmroute stats
MODEL          REQUESTS  PROMPT  COMPLETION  TOTAL
deepseek-chat  3         412     1860        2272
gemini-2.5-flash  1      88      230         318
TOTAL          4         500     2090        2590
```

### Credential-leak guard in action

Anything resembling a live key is rejected before it leaves the host:

```sh
$ curl -s http://127.0.0.1:4040/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d '{"messages":[{"role":"user","content":"my key is sk-abcdefghij1234567890ABCD"}]}'
{"error":{"message":"request blocked: detected openai/deepseek credential signature in payload","type":"llmroute_error"}}
```

---

## Commands

| Command           | Description                                                      |
| ----------------- | ---------------------------------------------------------------- |
| `llmroute init`   | Create the config dir/db with strict perms and seed the catalog. |
| `llmroute proxy`  | Boot the loopback routing gateway. `-p/--port` sets the preferred port (default `4040`). |
| `llmroute stats`  | Print per-model request counts and token volumes.                |

---

## How routing works

1. **Screen** — the raw body is matched against credential regexes. A hit is
   rejected with HTTP `400` and nothing leaves the host.
2. **Classify** — `internal/router` scans the payload:
   - multi-modal markers (`image_url`, `data:image/…`, base64, layout/screenshot
     keywords) → **vision** → Gemini Flash first.
   - code fences, `func`/`def`/`class`, stack traces, source extensions →
     **code** → DeepSeek then Claude Sonnet.
   - everything else → **chat**.
3. **Select** — `internal/database` returns models carrying that intent tag,
   ordered cheapest-first.
4. **Dispatch & failover** — each candidate with a configured API key is tried
   in turn; retryable upstream errors roll over to the next model.

### Seeded model catalog

| Model                | Provider  | Cost× | Intents              |
| -------------------- | --------- | ----- | -------------------- |
| `deepseek-chat`      | deepseek  | 0.14  | chat, code           |
| `gemini-2.5-flash`   | gemini    | 0.30  | vision, chat         |
| `deepseek-reasoner`  | deepseek  | 0.55  | code                 |
| `claude-3-5-haiku`   | anthropic | 0.80  | chat                 |
| `gemini-2.5-pro`     | gemini    | 1.00  | vision, chat, code   |
| `gpt-4o`             | openai    | 2.50  | vision, chat, code   |
| `claude-3-5-sonnet`  | anthropic | 3.00  | code, chat           |

Provider keys are read from the environment: `OPENAI_API_KEY`,
`GEMINI_API_KEY`, `ANTHROPIC_API_KEY`, `DEEPSEEK_API_KEY`.

---

## Project layout

```
.
├── main.go                 # entrypoint; injects build version
└── internal/
    ├── cli/                # Cobra commands: init, proxy, stats
    ├── database/           # pure-Go SQLite engine, migrations, model seeds
    ├── network/            # port-scan engine + loopback reverse proxy
    ├── router/             # intent classifier + upstream hot-swap/failover
    └── security/           # 0700/0600 lockdowns + key-leak scanner
```

State lives in `~/.config/llmroute/` (honoring `XDG_CONFIG_HOME`):
`records.db` holds the `models` and `usage_logs` tables.

---

## Development

```sh
make build     # build ./bin/llmroute
make test      # go test -race ./...
make lint      # go vet + gofmt (+ golangci-lint if installed)
make cover     # HTML coverage report
make snapshot  # local cross-platform build via goreleaser
```

---

## Configuration & security notes

- The config directory (`0700`) and database file (`0600`) are created — and
  re-asserted — so other local OS users cannot read your usage data.
- The proxy binds **only** to the `127.0.0.1` loopback interface.
- The credential scanner is a guardrail, not a vault: never paste live secrets
  into prompts.

## License

[MIT](LICENSE)
