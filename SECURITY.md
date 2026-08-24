# Security Policy

## Supported versions

`convex-operator` is pre-1.0. Security fixes are applied to `main` and shipped in
the next tagged release; older tags are not patched retroactively.

| Version              | Supported          |
| -------------------- | ------------------ |
| Latest `v0.x` release| :white_check_mark: |
| Older `v0.x` releases| :x:                |

Run the most recent release. Container images are published to
`ghcr.io/deicod/convex-operator`.

## Reporting a vulnerability

**Do not open a public issue for a security problem.**

Report it through GitHub's private vulnerability reporting:
[Security → Report a vulnerability](https://github.com/deicod/convex-operator/security/advisories/new).
If that is unavailable to you, email <info@icod.de> instead.

Please include:

- affected version or commit,
- Kubernetes and Gateway API versions in use,
- a description of the impact,
- reproduction steps or a proof of concept, if you have one.

What to expect:

- acknowledgement within **3 business days**,
- an assessment and remediation plan within **10 business days**,
- a fix released and a GitHub Security Advisory published once a patch is
  available, crediting you unless you prefer otherwise.

Please give us a reasonable window to ship a fix before disclosing publicly.

## Dependency and supply-chain policy

Automated dependency maintenance is configured in
[`.github/dependabot.yml`](.github/dependabot.yml) and covers four ecosystems:
Go modules, GitHub Actions, the Docker base image, and the dev container.

Rules we hold ourselves to:

- **Dependabot security updates take priority.** Advisory-driven pull requests
  are reviewed and merged ahead of routine version bumps. Critical and high
  severity alerts are addressed within 7 days, moderate and low within 30 days.
- **Routine version updates are batched weekly** into a single grouped pull
  request per ecosystem, so that security pull requests remain easy to spot.
- **Kubernetes major bumps are never unattended.** Major updates to `k8s.io/*`
  and `sigs.k8s.io/controller-runtime` are ignored by Dependabot and performed
  deliberately, in lockstep, by a maintainer.
- **GitHub Actions are pinned by commit SHA**, with the human-readable version
  in a trailing comment. Dependabot updates both together. Do not replace a SHA
  pin with a floating tag.
- **Workflows request the least privilege they need** (`permissions: contents:
  read` by default) and check out with `persist-credentials: false`.
- **An alert is closed only by a fix or a documented rationale.** Dismissing an
  alert requires a note explaining why the vulnerable code path is not reachable
  from this operator.
- **`govulncheck` runs in CI** on every push and pull request, plus on a weekly
  schedule, so reachable vulnerabilities in Go dependencies and in the Go
  standard library fail the build. The Go patch version is pinned by the `go`
  directive in `go.mod`, which is bumped when a standard library advisory
  affects us — Dependabot does not cover the standard library.

To reproduce the dependency scan locally:

```sh
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```
