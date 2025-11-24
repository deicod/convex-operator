# Changelog

## v0.1.0 (initial release)

- ConvexInstance CRD with defaults/validation for backend, dashboard, networking, storage, and maintenance (upgrade strategy).
- Core reconciliation: ConfigMap/Secrets/PVC/Service/StatefulSet with owner refs and status conditions.
- Dashboard Deployment/Service and Gateway API (Gateway/HTTPRoute) wiring with configurable GatewayClass (default `nginx`) and host/TLS support.
- Upgrade flows: in-place and export/import with status conditions for upgrade/export/import progress and cleanup of temporary artifacts.
- Envtest suites covering lifecycle, validation failures, dashboard toggle, Gateway/HTTPRoute creation, and upgrade job flows; unit coverage for config/secret helpers.
- CI: gofmt, govet, golangci-lint, and `make test` with module caching.
- Docs: CRD quick reference, troubleshooting, local kind/minikube guide, RBAC/security notes.
