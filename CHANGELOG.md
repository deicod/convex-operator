# Changelog

## Unreleased

- Security: resolve all Dependabot/`govulncheck` findings by upgrading vulnerable transitive dependencies — `golang.org/x/net` `v0.49.0` -> `v0.58.0`, `golang.org/x/text` `v0.33.0` -> `v0.41.0`, `golang.org/x/mod` `v0.32.0` -> `v0.40.0`, `golang.org/x/sys` `v0.42.0` -> `v0.47.0`, `google.golang.org/grpc` `v1.79.3` -> `v1.83.1`, and `github.com/google/cel-go` `v0.26.0` -> `v0.30.0`.
- Security: bump the `go` directive from `1.25.6` to `1.25.14`, picking up the Go standard library fixes in `crypto/tls`, `crypto/x509`, `net/http`, `net/url`, `net/textproto`, `mime`, `encoding/asn1`, and `html/template`.
- Add `.github/dependabot.yml` covering Go modules, GitHub Actions, the Docker base image, and the dev container, with grouped weekly version updates and separately grouped security updates. Major bumps of `k8s.io/*` and `sigs.k8s.io/controller-runtime` are excluded from automation.
- Add `SECURITY.md` with supported versions, private vulnerability reporting, and the dependency/supply-chain policy (remediation SLAs, SHA-pinned actions, alert dismissal rules).
- Add a `Vulnerability Scan` workflow running `govulncheck` on push, pull request, and weekly, covering both dependencies and the pinned Go standard library.
- Fix the failing `Lint` and `Tests` workflows (red on `main` since v0.1.3): extract the Gateway/ListenerSet/HTTPRoute reconciliation out of `reconcileCoreResources` into a new `reconcileNetworking` method, bringing cyclomatic complexity back under the `gocyclo` threshold. Behaviour is unchanged.
- Align `GOLANGCI_LINT_VERSION` in the Makefile (`v2.8.0`) with the version the `Lint` workflow installs (`v2.12.2`), so `make lint` and CI agree.

## v0.1.3

- Add a third networking mode: `spec.networking.listenerSet.parentGateway` makes the operator create and manage a Gateway API `ListenerSet` (gateway.networking.k8s.io/v1) that attaches the instance's listener (hostname + TLS) to a shared Gateway, and points the HTTPRoute at that `ListenerSet` — so the shared Gateway is never patched. Takes precedence over `parentRefs`.
- Upgrade `sigs.k8s.io/gateway-api` from `v1.4.1` to `v1.5.1` (where `ListenerSet` graduated to the standard channel). The `Gateway`/`HTTPRoute` modes still use only GA fields shared by the `1.3.x`/`1.4.x`/`1.5.x` standard CRD bundles, so existing clusters remain supported.
- ListenerSet support is opt-in and auto-detected: the operator only watches/reconciles `ListenerSet` when its CRD is installed, and reports `GatewayReady=False` (reason `ListenerSetCRDMissing`) when an instance requests it on a cluster without the CRD. Requires Gateway API `1.5+` and an implementation such as NGINX Gateway Fabric `2.6+`.
- Extend `make test-gateway-api-compat` to also cover Gateway API `v1.5.1`; ListenerSet specs skip automatically under the `1.3.x`/`1.4.x` bundles.

## v0.1.2

- Upgrade `sigs.k8s.io/gateway-api` from `v1.3.0` to `v1.4.1`.
- Keep the reconciler on the GA `gateway.networking.k8s.io/v1` `Gateway`/`HTTPRoute` field set shared by Gateway API `1.3.x` and `1.4.x`, so clusters on either standard CRD bundle remain supported.
- Add `make test-gateway-api-compat` plus envtest CRD version overrides to exercise the controller against both Gateway API compatibility paths.
- Document that NGINX Gateway Fabric `2.4.x` installation changes such as `snippetsFilters` -> `snippets` apply to the NGF deployment, not to this operator.

## v0.1.1

- Add `spec.networking.gatewayAnnotations` with a default `cert-manager.io/cluster-issuer: letsencrypt-prod-rfc2136`, applied to each per-instance Gateway; users can override or disable by setting a custom map or `{}`.
- Controller now upserts Gateway annotations on reconcile while keeping owner refs and spec in sync.
- Docs refreshed (README, getting-started) to describe the per-instance Gateway behavior, cert-manager default, and how to override it in sample manifests.
- Tests updated to cover default and custom gateway annotations.

## v0.1.0 (initial release)

- ConvexInstance CRD with defaults/validation for backend, dashboard, networking, storage, and maintenance (upgrade strategy).
- Core reconciliation: ConfigMap/Secrets/PVC/Service/StatefulSet with owner refs and status conditions.
- Dashboard Deployment/Service and Gateway API (Gateway/HTTPRoute) wiring with configurable GatewayClass (default `nginx`) and host/TLS support.
- Upgrade flows: in-place and export/import with status conditions for upgrade/export/import progress and cleanup of temporary artifacts.
- Envtest suites covering lifecycle, validation failures, dashboard toggle, Gateway/HTTPRoute creation, and upgrade job flows; unit coverage for config/secret helpers.
- CI: gofmt, govet, golangci-lint, and `make test` with module caching.
- Docs: CRD quick reference, troubleshooting, local kind/minikube guide, RBAC/security notes.
