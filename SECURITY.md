# Security policy

## Reporting a vulnerability

Please do not open a public issue, pull request or discussion for a security
problem. Email **security@fiten.eu** instead, with:

- the affected version or commit,
- what an attacker needs (unauthenticated, logged-in user, admin, an MQTT
  observer, a radio node in range),
- steps or a test that reproduces it.

You will get an acknowledgement within 7 days. Once a fix is merged and
released, the issue is described in the release notes. Tell us if you want to
be credited.

## Supported versions

Fixes go into `master` and ship in the next release. Only the latest release
receives security fixes.

## Scope

In scope: the Go server (`cmd/server/`), the MQTT ingestor (`cmd/ingestor/`),
the shared packages in `internal/`, and the frontend in `public/`.

Out of scope: the MeshCore firmware and radio protocol themselves (report those
to [MeshCore](https://github.com/meshcore-dev/MeshCore)), and issues that need
an attacker who already controls the operator's `config.json` or host.
