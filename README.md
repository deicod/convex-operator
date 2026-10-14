# convex-operator
Kubernetes operator that manages self-hosted Convex deployments through the `ConvexInstance` CustomResource. Built with Kubebuilder v4 (cli 4.13.0), Go 1.26.9, and controller-runtime v0.25.2.

Current release: [`v0.12.4`](https://github.com/deicod/convex-operator/releases/tag/v0.12.4). Image: `ghcr.io/deicod/convex-operator:v0.12.4` (`linux/amd64` and `linux/arm64`). `config/manager/kustomization.yaml` pins tag `main`.

## Versions

Pins on `main` (same commit as `v0.12.4`):

- Go `1.26.9` (`go` directive in `go.mod`). The image build uses `golang:1.27`.
- Kubernetes client libraries `k8s.io/*` `v0.37.1` (Kubernetes 1.37). Envtest uses 1.37.
- `sigs.k8s.io/controller-runtime` `v0.25.2`, controller-tools `v0.20.1`, kustomize `v5.8.1`.
- Gateway API module `sigs.k8s.io/gateway-api` `v1.6.3`. `make test-gateway-api-compat` still runs against standard CRD bundles `v1.3.0`, `v1.4.1`, and `v1.5.1`.
- Sample Convex release `1.0.0` (`ghcr.io/get-convex/convex-backend:1.0.0`). CRD image defaults are `ghcr.io/get-convex/convex-backend:latest` and `ghcr.io/get-convex/convex-dashboard:latest`.

## Description
- Reconciles a single backend StatefulSet (one replica) and optional dashboard Deployment per `ConvexInstance`.
- Wires managed Postgres/MySQL/SQLite URLs and S3-compatible storage via Secret references.
- Exposes traffic through Gateway API/ingress using the provided host, optional TLS secret, and configurable GatewayClass (default `nginx`).
- Tracks lifecycle through status phase/conditions and upgrade strategy (in-place or export/import).

## Referral
[Double your free quota by applying a referral for the Convex cloud service](https://convex.dev/referral/DARKOL3521)

[Don't have a Kubernetes Cluster yet? Set one up at Hetzner](https://hetzner.cloud/?ref=p8DSeX0ZVH63)

## Getting Started

#### Hello World

Here is a minimal example of a Convex instance using SQLite for local development:

```yaml
apiVersion: convex.icod.de/v1alpha1
kind: ConvexInstance
metadata:
  name: convex-sample
  namespace: default
spec:
  environment: dev
  version: "1.0.0"
  backend:
    db:
      engine: sqlite
    storage:
      pvc:
        enabled: true
        size: 1Gi
  networking:
    host: convex.local # Ensure this resolves to your Gateway/Ingress IP
```

Apply it with:
```sh
kubectl apply -f convex-sample.yaml
```

### Prerequisites
- Go 1.26.9, as pinned by the `go` directive in `go.mod`.
- Docker with Buildx, if you build the image. The Dockerfile builder is `golang:1.27`. Published images are `linux/amd64` and `linux/arm64`.
- kubectl that can talk to the target cluster.
- A Kubernetes 1.37 cluster. This operator is built and envtest'd against `k8s.io/*` `v0.37.1`. Other cluster versions are not in this repo's test matrix.

### CRD Configuration Guide

**Required Fields:**
- `spec.environment`: Deployment tier (`dev` or `prod`).
- `spec.version`: Convex release tag (samples use `"1.0.0"`). That field does not select the image: `spec.backend.image` defaults to `ghcr.io/get-convex/convex-backend:latest` and `spec.dashboard.image` to `ghcr.io/get-convex/convex-dashboard:latest`. Samples pin the backend image to `ghcr.io/get-convex/convex-backend:1.0.0`.
- `spec.networking.host`: External hostname (e.g., "convex.example.com").
- `spec.backend.db.engine`: Database type (`postgres`, `mysql`, or `sqlite`).

**Backend Configuration (`spec.backend`):**
- **Database:** For `postgres` or `mysql`, you must provide `db.secretRef` and `db.urlKey`.
- **Storage:** Use `storage.mode: sqlite` with `storage.pvc.enabled: true` for local persistence.
- **S3:** Configure `s3` block with `secretRef` and keys to enable blob storage (required for prod).
- **Security:** Use `security` to override pod/container security contexts if needed.

**Dashboard Configuration (`spec.dashboard`):**
- Enabled by default. Disable with `enabled: false`.
- **Security:** Defaults to `runAsNonRoot` (UID 1001).
- **Admin Key:** Set `prefillAdminKey: true` to inject the admin key into the browser (use with caution).

**Networking (`spec.networking`):**
- **Gateway:** Defaults to creating a Gateway using `gatewayClassName: nginx`.
- **Custom Gateway:** Use `parentRefs` to attach the HTTPRoute to an existing Gateway instead of creating one (TLS/listeners on that Gateway are managed externally).
- **ListenerSet:** Use `listenerSet.parentGateway` to have the operator manage this instance's listener (hostname + TLS) on a shared Gateway via a Gateway API `ListenerSet`, without patching the Gateway or provisioning a dedicated one. Requires Gateway API 1.5+ and an implementation that supports `ListenerSet` (e.g. NGINX Gateway Fabric 2.6+). Takes precedence over `parentRefs`.
- **TLS:** Reference a TLS secret via `tlsSecretRef`.

**Maintenance (`spec.maintenance`):**
- **Upgrade Strategy:** `inPlace` (rolling update) or `exportImport` (data migration job).
- **Restarts:** `restartInterval` defaults to `168h` (7 days) to mitigate memory leaks. Set to `0s` to disable.

### Environment variables
- Backend pods receive `CONVEX_PORT` (3210), `CONVEX_ENV` (spec.environment), `CONVEX_VERSION` (spec.version), `INSTANCE_NAME` (defaults to the ConvexInstance name with `-` -> `_`, override via `spec.backend.db.databaseName`), telemetry/logging toggles when set (`DISABLE_BEACON`, `REDACT_LOGS_TO_CLIENT`), plus secret-derived `CONVEX_ADMIN_KEY`/`CONVEX_INSTANCE_SECRET`, `POSTGRES_URL` (or `MYSQL_URL` when `spec.backend.db.engine: mysql`) from the referenced `urlKey`, and S3 wiring (`AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, optional `AWS_ENDPOINT_URL`/`AWS_ENDPOINT_URL_S3` and `S3_ENDPOINT_URL` when enabled via `emitS3EndpointUrl`, `S3_STORAGE_*` buckets) when enabled. If set in the spec, `spec.networking.cloudOrigin` and `spec.networking.siteOrigin` populate `CONVEX_CLOUD_ORIGIN`/`CONVEX_SITE_ORIGIN`; otherwise they default to the external host (`scheme://spec.networking.host`). Additional env vars can be appended via `spec.backend.env` and will override earlier/operator-set entries when names overlap.
- Dashboard pods get `NEXT_PUBLIC_DEPLOYMENT_URL` (defaults to `scheme://spec.networking.host`, override with `spec.networking.deploymentUrl`), `CONVEX_CLOUD_ORIGIN`, `CONVEX_SITE_ORIGIN` (same defaults/overrides as above), and optionally `NEXT_PUBLIC_ADMIN_KEY` when `spec.dashboard.prefillAdminKey` is true. By default the admin key is not injected to avoid exposing it in the browser; users should enter it manually.

Samples live in `config/samples/` (`convex-dev`, `convex-prod`) and mirror the specification in `SPEC.md`.

Override `INSTANCE_NAME` / database name when your database name does not match the ConvexInstance:

```yaml
spec:
  backend:
    db:
      engine: postgres
      secretRef: forum-db
      urlKey: url
      databaseName: forum_prod  # sets INSTANCE_NAME
    telemetry:
      disableBeacon: true
    logging:
      redactLogsToClient: true
    env:
      - name: CUSTOM_FLAG
        value: "true"
```

### Gateway and TLS assumptions
- The operator assumes a `GatewayClass` named `nginx` by default (see `spec.networking.gatewayClassName`). Change it in the ConvexInstance spec to match your installed Gateway implementation.
- By default the operator creates a Gateway per instance using `spec.networking.gatewayClassName` and annotates it for cert-manager; set `parentRefs` to attach the HTTPRoute to an existing Gateway and skip Gateway reconciliation. TLS on the shared Gateway must then be handled externally.
- Set `spec.networking.listenerSet.parentGateway` to have the operator manage the instance's listener (hostname + TLS) on a shared Gateway via a Gateway API `ListenerSet`, instead of patching that Gateway or provisioning a dedicated one. The shared Gateway must permit attachment from the instance namespace through its `allowedListeners` field; the operator never mutates the shared Gateway. The HTTPRoute attaches to the operator-managed `ListenerSet`. This requires the standard `gateway.networking.k8s.io/v1` `ListenerSet` CRD (Gateway API 1.5+); the operator reports `GatewayReady=False` with reason `ListenerSetCRDMissing` when it is absent. `listenerSet` takes precedence over `parentRefs`.
- The controller is built against Gateway API `v1.6.3`. `make test-gateway-api-compat` runs the envtest suite against the standard CRD bundles `v1.3.0`, `v1.4.1`, and `v1.5.1` (ListenerSet specs skip automatically when that CRD is absent). Default `make test` loads CRDs from the linked module (`v1.6.3`). The `Gateway`/`HTTPRoute` modes use GA `gateway.networking.k8s.io/v1` fields; ListenerSet mode requires the standard ListenerSet CRD (Gateway API 1.5+). The compat target does not include a `v1.6.x` bundle.
- This repository does not install NGINX Gateway Fabric. If you upgrade NGF separately to `v2.4.2`, use the upstream Gateway API `v1.4.x` install and migrate any Helm/config usage of `snippetsFilters` to `snippets`. The `ListenerSet` networking mode additionally requires NGF `v2.6.0+` with the Gateway API `v1.5+` standard CRDs installed.
- If your cluster lacks the referenced GatewayClass, the `GatewayReady`/`HTTPRouteReady` conditions will stay `False`; install the class or update the spec to a class that exists.

### Status and Conditions
- `Ready`: overall readiness; `BackendReady` when the backend pod is ready, `WaitingForBackend` or `WaitingForGateway` otherwise, `ValidationFailed`/`*Error` on failures.
- `ConfigMapReady`: backend ConfigMap ensured (`Available`).
- `SecretsReady`: generated admin/instance secrets ensured; external DB/S3 secrets are validated, not owned.
- `PVCReady`: `Available` when created, `Skipped` when storage mode is external or PVC disabled.
- `ServiceReady`: backend Service ensured.
- `DashboardServiceReady`: dashboard Service ensured or `Disabled` when dashboard is off.
- `StatefulSetReady`: `Provisioning` while waiting for ready replicas; `Ready` once the backend pod is ready; error reasons on reconcile failures.
- `DashboardReady`: `Provisioning` during rollout, `Ready` when deployment ready, `Disabled` when dashboard is off.
- `GatewayReady` / `HTTPRouteReady`: reflect Gateway/HTTPRoute creation and attachment status.
- `UpgradeInProgress`: true while upgrades run; `ExportCompleted` / `ImportCompleted` track export/import strategy progress.
- Status endpoints are populated with the external host (and `/dashboard` path) once the HTTPRoute is accepted; otherwise they fall back to the internal Service URL for the API and omit the dashboard URL.

### RBAC and Security
- Writes are namespace-scoped: ConfigMaps, Secrets, Services/PVCs, Jobs, StatefulSets/Deployments, and Gateway/HTTPRoute are created only in the ConvexInstance namespace.
- Secrets are only referenced (DB/S3/TLS) and generated admin/instance secrets live in the same namespace; managed objects carry owner references for cleanup on deletion.
- Cluster-scoped access is limited to watching/listing ConvexInstances and Gateway API types via controller-runtime; no cluster-wide writes are requested.
- Backend pods no longer force `runAsNonRoot`; dashboard pods default to `runAsUser: 1001`/`runAsNonRoot: true` to satisfy the official Convex images. Override pod/container security contexts via `spec.backend.security` and `spec.dashboard.security` if your images need different UIDs or policies.
- Review `config/rbac/role.yaml` before install. Clusters with strict RBAC may require an admin to approve the manager Role/ClusterRole and bindings.
- CRDs are under `config/crd/bases/`; regenerate via `make manifests` after API changes—avoid manual edits.

### Troubleshooting quick tips
- `SecretsReady=False` with `ValidationFailed`: referenced DB/S3/TLS secret missing or key absent.
- `SecretsReady=False` with `ValidationFailed` mentioning `db urlKey is required`: set `spec.backend.db.urlKey` and ensure the key exists in the referenced Secret.
- `SecretsReady=False` with S3 key errors: ensure all S3 keys (endpoint/region/accessKeyId/secretAccessKey/bucket) are set in the Secret and referenced in the spec when S3 is enabled.
- `ConfigMapReady=False` with `ConfigMapError`: config rendering failed—check controller logs.
- `ServiceReady=False` or `StatefulSetReady=False` with `*Error`: inspect events/logs for create/update errors (e.g., invalid image, resource limits, service conflicts).
- `GatewayReady`/`HTTPRouteReady` stuck `Provisioning`: ensure GatewayClass (default `nginx`) exists, TLS Secret reference is valid, and HTTPRoute status shows `Accepted=True`.
- `Ready` stuck at `WaitingForBackend` or `StatefulSetReady=Provisioning`: backend rollout still progressing or failed; check pod events/logs, PVC binding, image pull, or DB connectivity.
- Upgrade stuck `UpgradeInProgress`: check export/import Job status and backend rollout; failed Jobs surface via `ExportCompleted`/`ImportCompleted` conditions and controller events.

#### Events reference
- `Warning ValidationFailed`: referenced secret missing/invalid.
- `Warning ConfigMapError/SecretError/PVCError/ServiceError/StatefulSetError`: reconcile step failed; check controller logs and the referenced object.
- `Normal Ready`: backend became Ready.

### To Deploy on the cluster
CI publishes `ghcr.io/deicod/convex-operator` on `v*` tags (current release `v0.12.4`, plus the commit SHA). To build and push your own image:

```sh
make docker-build docker-push IMG=ghcr.io/deicod/convex-operator:v0.12.4
```

The cluster must be able to pull that image. `make docker-push` needs push permission on the registry in `IMG`.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=ghcr.io/deicod/convex-operator:v0.12.4
```

`make deploy` runs `kustomize edit set image controller=${IMG}` and rewrites `config/manager/kustomization.yaml`. Without `IMG`, the Makefile default is `controller:latest`, which overwrites the committed `ghcr.io/deicod/convex-operator:main` pin. Pass `IMG` explicitly.

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

Build the installer for a published image:

```sh
make build-installer IMG=ghcr.io/deicod/convex-operator:v0.12.4
```

**NOTE:** That target writes `dist/install.yaml` locally. The file is not
published in this repo, so there is no raw GitHub URL to apply. The published
install path is the GHCR image above (`make deploy IMG=ghcr.io/deicod/convex-operator:v0.12.4`).

### By providing a Helm Chart

1. Build the chart using the optional helm plugin

```sh
kubebuilder edit --plugins=helm/v2-alpha
```

2. See that a chart was generated under 'dist/chart', and users
can obtain this solution from there.

**NOTE:** If you change the project, you need to update the Helm Chart
using the same command above to sync the latest changes. Furthermore,
if you create webhooks, you need to use the above command with
the '--force' flag and manually ensure that any custom configuration
previously added to 'dist/chart/values.yaml' or 'dist/chart/manager/manager.yaml'
is manually re-applied afterwards.

## Contributing
See `AGENTS.md` for contribution guidelines. Align changes with `SPEC.md` milestones and keep samples/CRD manifests regenerated (`make generate` and `make manifests`).

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2025 Darko Luketic <info@icod.de>.

This project is licensed under the GNU General Public License v3.0. See `LICENSE` for the full text and terms governing copying, distribution, and modification.
