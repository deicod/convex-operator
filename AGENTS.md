# Repository Guidelines

Brief notes for contributors building the Convex Kubernetes operator. Keep changes small, explained, and aligned with the specification in `SPEC.md`.

## Project Structure & Module Organization
- Expect Kubebuilder layout: `api/v1alpha1/` for `ConvexInstance` types and validation markers, `controllers/` for reconcilers, `config/` for CRDs/RBAC/manager/kustomize overlays, and `config/samples/` for example manifests (dev/prod).
- Generated assets live under `config/crd/bases/`; avoid manual edits. Place reusable fixtures under `controllers/testdata/`. Design docs stay in `SPEC.md` (plan) and any future `docs/` folder (how-to notes).

## Build, Test, and Development Commands
- `make fmt` / `make vet` — gofmt and vet the tree; run before commits.
- Only run `gofmt` against `.go` files to avoid touching generated or non-Go assets.
- `make generate` — regenerate deepcopy code and markers; required after API changes.
- `make manifests` — rebuild CRD/RBAC YAMLs; commit updates when API changes.
- `make test` — run unit+envtest suites locally (controller-runtime’s envtest).
- `make run` — run the controller against the current kubeconfig; `make install`/`make uninstall` apply/remove CRDs; `make deploy`/`make undeploy` manage in-cluster install.

## Coding Style & Naming Conventions
- Go 1.24+, idiomatic Go: tabs, gofmt, lower_snake package dirs, exported symbols commented. Prefer small, pure helpers and idempotent reconciliation steps.
- CRD fields use lowerCamelCase JSON tags mirroring the spec (e.g., `upgradeStrategy`, `tlsSecretRef`). Use Kubebuilder validation/default markers for enums, required refs, and quantity formats.
- Images default to `ghcr.io/get-convex/convex-backend:<version>` and `.../convex-dashboard:<version>`; keep defaults centralized near API defaults.

## Testing Guidelines
- Tests live in `_test.go`; prefer table-driven cases. For reconciler flows, use envtest or fake clients to assert created/updated objects and status conditions.
- Name tests `TestReconcile<Feature>` or `TestDefaulting<Feature>`; include sample manifests from `config/samples/` to cover dev/prod/storage/S3 variations.
- Aim to cover status transitions (Ready/Upgrading/Error) and owner reference cleanup paths; add fixtures instead of inlined YAML when reusable.

## Commit & Pull Request Guidelines
- Follow Conventional Commit style when possible (`feat: add convexinstance types`, `fix: handle missing db secret`). Keep API/manifests regenerated in the same commit.
- PRs should include: short summary, linked issue/milestone (M1–M5), test evidence (`make fmt vet test`), and note any spec alignment or deviations. Add log snippets or screenshots when touching status/UX.
- Avoid bundling unrelated changes; prefer small, reviewable diffs.

## Security & Configuration Tips
- Never commit secrets; sample manifests must use placeholder names for Secret refs, hosts, and storage sizes. TLS Secrets are referenced only, not managed.
- RBAC should stay minimal: namespace-scoped writes, cluster-scoped watch as required. Document any added permissions in `config/rbac/`.
