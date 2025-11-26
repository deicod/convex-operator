# convex-operator
Kubernetes operator that manages self-hosted Convex deployments through the `ConvexInstance` CustomResource. Built with Kubebuilder (Go 1.24+) and controller-runtime.

## Description
- Reconciles a single backend StatefulSet (one replica) and optional dashboard Deployment per `ConvexInstance`.
- Wires managed Postgres/MySQL/SQLite URLs and S3-compatible storage via Secret references.
- Exposes traffic through Gateway API/ingress using the provided host, optional TLS secret, and configurable GatewayClass (default `nginx`).
- Tracks lifecycle through status phase/conditions and upgrade strategy (in-place or export/import).

## Referral
[Double your free quota by applying a referral for the Convex cloud service](https://convex.dev/referral/DARKOL3521)

## Getting Started

### Prerequisites
- go version v1.24.6+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### CRD quick reference (convex.icod.de/v1alpha1)
- `spec.environment` (`dev|prod`) and `spec.version` (Convex image tag) are required.
- `spec.backend`: image (defaults to Convex backend), `db.engine` (`postgres|mysql|sqlite`) plus Secret/key refs, `storage.mode` (`sqlite|external`) with optional PVC, S3 secret/key wiring.
- `spec.dashboard`: enabled flag (default true), image (defaults to Convex dashboard), replicas/resources.
- `spec.networking`: hostname, `gatewayClassName` (defaults to `nginx`), optional `gatewayAnnotations` (defaults to `cert-manager.io/cluster-issuer: letsencrypt-prod-rfc2136`), and TLS secret reference. The default assumes NGINX Gateway Fabric and annotates the generated Gateway for cert-manager; set `gatewayAnnotations: {}` or provide your own map to override/disable.
- `spec.maintenance.upgradeStrategy`: `inPlace` (default) or `exportImport`; `spec.scale.backend` provides CPU target/max memory hints.

Samples live in `config/samples/` (`convex-dev`, `convex-prod`) and mirror the specification in `SPEC.md`.

### Gateway and TLS assumptions
- The operator assumes a `GatewayClass` named `nginx` by default (see `spec.networking.gatewayClassName`). Change it in the ConvexInstance spec to match your installed Gateway implementation.
- A Gateway and HTTPRoute are created per instance, using the provided `spec.networking.host` and optional `spec.networking.tlsSecretRef` for TLS. The generated Gateway is annotated by default with `cert-manager.io/cluster-issuer: letsencrypt-prod-rfc2136` to let cert-manager issue the TLS Secret you reference; override with `gatewayAnnotations` or clear it via `gatewayAnnotations: {}` if you do not want cert-manager involved.
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
- Review `config/rbac/role.yaml` before install. Clusters with strict RBAC may require an admin to approve the manager Role/ClusterRole and bindings.
- CRDs are under `config/crd/bases/`; regenerate via `make manifests` after API changes—avoid manual edits.

### Troubleshooting quick tips
- `SecretsReady=False` with `ValidationFailed`: referenced DB/S3/TLS secret missing or key absent.
- `SecretsReady=False` with `ValidationFailed` mentioning `db urlKey is required`: set `spec.backend.db.urlKey` and ensure the key exists in the referenced Secret.
- `SecretsReady=False` with S3 key errors: ensure all S3 keys (endpoint/accessKeyId/secretAccessKey/bucket) are set in the Secret and referenced in the spec when S3 is enabled.
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
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/convex-operator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/convex-operator:tag
```

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

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/convex-operator:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/convex-operator/<tag or branch>/dist/install.yaml
```

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
