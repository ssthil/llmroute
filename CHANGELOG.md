# Changelog

All notable changes to llmroute are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.4.1] - 2026-06-08

### Fixed
- `models add` (and the `init` local-model / custom-provider steps) is now
  idempotent: re-adding an existing model updates and **re-enables** it instead
  of failing silently on the unique id. Previously, choosing local mode in
  `init` could leave an already-known local model (e.g. `gemma3:27b`) disabled,
  so the proxy reported "0 models enabled". Recover an affected model with
  `llmroute models enable <id>`.

## [0.4.0] - 2026-06-08

### Added
- **Expanded default catalog** — seeds now include **Mistral**
  (`mistral-large-latest`, `mistral-small-latest`), **xAI** (`grok-3`), and
  **Qwen** (`qwen-max`, `qwen-plus`) alongside OpenAI/Gemini/Anthropic/DeepSeek
  (12 flagship models across 7 providers).
- **Access-mode prompt in `init`** — asks whether you use **API keys**, **local
  models**, or **both**, so subscription-only users (Claude Pro / ChatGPT Plus,
  which don't include API access) are guided to local models instead.
- **Guided local-model setup** — auto-detects installed models from a local
  server's `/models` endpoint (Ollama, LM Studio) and adds the ones you pick as
  no-key providers; falls back to manual model-id entry.
- **Multi-select `init`** — the API path shows the full numbered catalog and
  takes one selection (`all` / `none` / `1,3,5` / Enter to keep) instead of a
  prompt per model, then asks for the key of each enabled provider.
- **Bare `llmroute`** prints the model catalog with status, or — on first run —
  a modern welcome screen (boxed banner, getting-started steps, and tips).
- **Post-setup "Next" footer** after `init` pointing to `proxy` and the endpoint.

### Fixed
- Model table alignment: width padding is applied to plain text before
  colorizing, so ANSI escape bytes no longer skew the columns.

### Upgrade notes
- Upgrading from 0.3.0 is seamless. New seed providers/models are added on first
  open (existing rows, enabled flags, and keys are untouched). Seeded model ids
  are flagship picks and may drift — adjust with `llmroute models add/rm`.

## [0.3.0] - 2026-06-08

### Added
- **Data-driven providers** — provider endpoints now live in a `providers`
  table instead of being hardcoded, so any OpenAI-compatible endpoint can be
  registered without a rebuild.
- **Custom & local models** — route to local runtimes (Ollama, LM Studio,
  llama.cpp) and other clouds (Groq, Cerebras) with `--no-key` providers
  dispatched without an `Authorization` header.
- **`llmroute models`** command: `list`, `add`, `rm`, `enable`, `disable`.
- **`llmroute keys`** command: `list` (values masked), `set` (hidden prompt or
  `--stdin`), `rm` — update one provider's key without re-running the wizard.
- **Interactive "add a custom provider"** step at the end of `llmroute init`
  (name → base URL → key requirement → model → key; no flags needed).
- **Modern CLI UI** — ANSI colors and glyphs across `init`, `keys`, `models`,
  `stats`, and the `proxy` startup banner. Honors `NO_COLOR` and non-TTY output.

### Changed
- Provider keys resolve **environment-first, then the stored key store**.

### Upgrade notes
- Upgrading from 0.2.0 is seamless: opening an existing `records.db` with 0.3.0
  auto-creates and seeds the new `providers` table; existing models, the
  `enabled` flags, usage logs, and `keys.json` are preserved.

## [0.2.0] - 2026-06-07

### Added
- **Interactive `init`** wizard: choose which models to enable and enter each
  provider's API key (hidden input), stored in a `0600` `keys.json`.
- Per-model **`enabled`** flag; only enabled models participate in routing.
- `--yes/-y` flag for non-interactive setup.

### Changed
- Keys resolve environment-first, then the stored key store.

## [0.1.0] - 2026-06-07

### Added
- Initial release: native, CGO-free loopback proxy that classifies inbound
  OpenAI-style chat requests by intent (code/vision/chat) and routes them to the
  cheapest capable provider with automatic failover.
- Credential-leak interceptor, port-scan resiliency, `0700`/`0600` locked-down
  state, pure-Go SQLite usage accounting, and the `init`/`proxy`/`stats`
  commands.

[0.4.1]: https://github.com/ssthil/llmroute/releases/tag/v0.4.1
[0.4.0]: https://github.com/ssthil/llmroute/releases/tag/v0.4.0
[0.3.0]: https://github.com/ssthil/llmroute/releases/tag/v0.3.0
[0.2.0]: https://github.com/ssthil/llmroute/releases/tag/v0.2.0
[0.1.0]: https://github.com/ssthil/llmroute/releases/tag/v0.1.0
