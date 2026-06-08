# Security Policy

## Reporting a Vulnerability

**Do not open a public issue for security vulnerabilities.**

Email the maintainer directly: **senthil.s@ncs.com.sg**

Include in your report:
- A description of the vulnerability and its potential impact
- Steps to reproduce or a proof-of-concept
- Affected versions (check `llmroute --version`)

You should receive an acknowledgment within 48 hours. If you haven't heard back
in 72 hours, follow up to ensure the report was received.

## Disclosure Policy

Once a fix is confirmed and released, we will:
1. Publish a patched release with a `fix:` entry in [CHANGELOG.md](CHANGELOG.md)
2. Credit you in the changelog unless you prefer to remain anonymous

We ask for **90 days** before public disclosure to allow users time to upgrade.

## Security Model

`llmroute` is a loopback proxy that handles API keys. Its threat model:

**In scope:**
- Credential exfiltration — the credential-leak scanner (`internal/security`)
  blocking secrets before outbound calls
- Local privilege escalation — the config dir (`0700`), `records.db` and
  `keys.json` (`0600`) are re-asserted on every run; any bypass that lets
  another local user read these is a vulnerability
- SSRF — the proxy only binds `127.0.0.1`; any path that causes outbound
  requests to attacker-controlled hosts is in scope
- Bypass of the credential scanner regex patterns — keys that slip through
  are a vulnerability

**Out of scope:**
- Keys stored in plaintext in `keys.json` — this is documented behavior;
  users who want higher assurance should use environment variables instead
- Attacks requiring physical access to the machine
- Denial-of-service against the local proxy port

## Supported Versions

Only the latest release receives security fixes.

| Version | Supported |
| ------- | --------- |
| latest  | Yes       |
| older   | No        |
