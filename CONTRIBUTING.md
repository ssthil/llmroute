# Contributing

## Prerequisites

- Go 1.22+ (see `go.mod` for the exact version)
- `make` (or run the `go` commands directly)
- No CGO required — `CGO_ENABLED=0` is the build default

## Getting started

```sh
git clone https://github.com/ssthil/llmroute
cd llmroute
make build          # produces ./bin/llmroute
./bin/llmroute --version
```

## Development workflow

```sh
make test      # go test -race ./...
make lint      # go vet + gofmt (+ golangci-lint if installed)
make cover     # run tests and open an HTML coverage report
make fmt       # auto-format with gofmt
```

CI runs `make test` and enforces `gofmt` on every push and PR. Your branch
will not merge if `gofmt -l .` reports any files.

## Project layout

```
main.go              # entrypoint; injects build version via ldflags
internal/
  cli/               # Cobra commands: init, proxy, stats, keys, models
  config/            # 0600 keys.json store
  database/          # pure-Go SQLite, migrations, model seeds
  network/           # port-scan engine + loopback reverse proxy
  router/            # intent classifier + upstream failover
  security/          # 0700/0600 lockdowns + credential-leak scanner
```

Each package has a `_test.go` file. New behaviour needs a test.

## Submitting a pull request

1. Fork and create a branch off `main`.
2. Keep changes focused — one concern per PR.
3. Run `make test` and `make lint` locally before pushing.
4. Write a clear PR description: what changed and why.

Commit style:
- Imperative mood, ≤ 72 chars: `fix: reject keys.json with wrong mode`
- Use conventional prefixes: `feat:`, `fix:`, `refactor:`, `chore:`, `docs:`
- No trailing period on the subject line

## Reporting bugs

Open an issue with:
- `llmroute --version` output
- OS and architecture (`uname -sm`)
- Steps to reproduce
- Expected vs. actual behaviour

For security issues, see [SECURITY.md](SECURITY.md) — do not open a public issue.

## Adding a model or provider

The model catalog is data-driven. To propose a new seeded model, open a PR that
adds it in `internal/database` (the migration/seed layer) with:
- provider name, base URL, model ID
- cost tier and intent tags (`chat`, `code`, `vision`)

Custom models can also be added at runtime with `llmroute models add` — no code
change needed.
