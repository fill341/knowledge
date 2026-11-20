# Security

kn is built to run **on your own machine**: every port is bound to `127.0.0.1`, agents reach the database only
through the API with their own scoped tokens, and tokens live in the macOS Keychain. It is not designed to be
exposed to a network or shared between people over the internet — if you do that, you are on your own.

## Reporting a vulnerability

Please report security issues privately through
[GitHub Security Advisories](https://github.com/fill341/knowledge/security/advisories/new), not in public issues.
Include steps to reproduce and the version (commit) you tested. You will get an answer within a week.

## What is in scope

- isolation between spaces (`personal` / `work` / `shared`) and token scopes;
- the secret scanner on entries;
- the web UI session (sealed cookie, CSRF guard, one-time login links);
- anything that lets an agent token do more than its scopes allow.

Details of the threat model: [docs/security.md](docs/security.md) (in Russian).
