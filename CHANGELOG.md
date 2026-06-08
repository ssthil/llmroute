# Changelog

All notable changes to llmroute are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

[0.3.0]: https://github.com/ssthil/llmroute/releases/tag/v0.3.0
[0.2.0]: https://github.com/ssthil/llmroute/releases/tag/v0.2.0
[0.1.0]: https://github.com/ssthil/llmroute/releases/tag/v0.1.0
