• # Convex Operator – Specification and Plan

  ## SPEC

  - Goals
      - Automate lifecycle of self-hosted Convex deployments (dev/prod) on Kubernetes via ConvexInstance CRD.
      - Ensure one backend process per deployment, vertical scaling only, optional dashboard scaling.
      - Standardize wiring to managed DB (Postgres/MySQL/SQLite URL) and S3-compatible storage via Secrets.
      - Expose backend and dashboard via Gateway API (prefer NGINX Gateway Fabric) with TLS.
      - Provide clear status/conditions, owner refs, and safe upgrades (in-place or export/import).
  - Non-goals
      - No horizontal backend scaling or splitting internal Convex services (function runners/sync workers).
      - No provisioning of DB/S3/Certs/External DNS; only consumption via existing Secrets.
      - No cloud-provider specific LB management beyond Gateway API objects.
  - Constraints
      - Kubebuilder/controller-runtime, Go 1.24+, idiomatic patterns (finalizers, conditions).
      - Multi-namespace support; operator cluster-scoped for watch, but reconciles namespace-scoped resources with owner refs.
      - Images: backend ghcr.io/get-convex/convex-backend:<tag>, dashboard ghcr.io/get-convex/convex-dashboard:<tag>.
      - Ports/env derive from Convex self-hosting docs; use example values only when needed and label them.
  - CRD: convex.icod.de/v1alpha1, Kind ConvexInstance

    apiVersion: convex.icod.de/v1alpha1
    kind: ConvexInstance
    spec:
      environment: string               # "dev" or "prod"
      version: string                   # shared version/tag for backend+dashboard
      backend:
        image: string                   # default convex-backend:<version>
        resources:
          requests: { cpu: string, memory: string }
          limits:   { cpu: string, memory: string }
        db:
          engine: string                # "postgres" | "mysql" | "sqlite"
          secretRef: string             # Secret name containing DB URL
          urlKey: string                # key in Secret for DB URL
        storage:
          mode: string                  # "sqlite" | "external"
          pvc:
            enabled: bool
            storageClassName: string
            size: string                # example: "100Gi"
        s3:
          enabled: bool
          secretRef: string
          endpointKey: string
          accessKeyIdKey: string
          secretAccessKeyKey: string
          bucketKey: string
      dashboard:
        enabled: bool
        image: string                   # default convex-dashboard:<version>
        replicas: int32
        resources:
          requests: { cpu: string, memory: string }
          limits:   { cpu: string, memory: string }
      networking:
        host: string                    # example: "convex.example.com"
        tlsSecretRef: string            # TLS Secret in same namespace
      scale:
        backend:
          cpuTargetUtilization: int32   # example: 70
          maxMemory: string             # example: "8Gi"
      maintenance:
        upgradeStrategy: string         # "inPlace" | "exportImport"
    status:
      phase: string                     # "Pending" | "Ready" | "Error" | "Upgrading"
      conditions: []metav1.Condition
      observedGeneration: int64
      endpoints:
        apiUrl: string
        dashboardUrl: string
  - Architecture
      - Controllers:
          - ConvexInstanceReconciler handles full desired state, owner refs, finalizer for cleanup.
          - Supporting packages for config rendering (ConfigMap), secrets wiring, Gateway API resources.
      - Managed resources per instance:
          - ConfigMap for backend non-secret config (ports, feature flags, example defaults).
          - Secrets: Convex instance secret + admin key (generated if absent), plus existing DB/S3 Secret refs mounted/consumed.
          - PVC (if storage.pvc.enabled).
          - StatefulSet (backend, replicas=1) with probes, envs for DB URL, S3 creds, instance/admin secrets, storage mounts.
          - Service (backend) exposing Convex API and HTTP action ports.
          - Deployment (dashboard, optional) with Service; env points to backend Service.
          - Gateway API Gateway (bound to existing gatewayClassName, example "nginx") and HTTPRoute routing host to backend/dashboard paths.
      - Interactions:
          - DB: consume DB_URL (actual key from urlKey) via projected Secret; no schema management.
          - S3: consume endpoint/access/secret/bucket keys when enabled; set Convex env vars accordingly.
          - Storage: PVC mounted to backend pod for SQLite/local disk when required.
  - Reconciliation flows
      - Create/Initial
          - Add finalizer; validate spec (env values, required refs).
          - Generate instance/admin secrets if missing; read DB/S3 refs; render ConfigMap; ensure PVC if enabled.
          - Create/patch backend Service, StatefulSet (replicas=1), dashboard Deployment/Service if enabled.
          - Create/patch Gateway + HTTPRoute (host from spec.networking.host, TLS from tlsSecretRef, routes: /, /api/*, /sync, /http_action/* -> backend; /dashboard* -> dashboard).
          - Update status: observedGeneration, endpoints (from Services/Routes), conditions (Available/Progressing), phase Ready when probes pass.
      - Update
          - Detect spec changes; if version/image/resources changed: patch StatefulSet/Deployment (respect upgradeStrategy).
          - inPlace: roll images; set phase Upgrading during rollout; revert to Ready on success; set Error on failure.
          - exportImport: orchestrate Jobs (export -> roll -> import) using Convex CLI (example command convex export --url $API_URL --admin-key $KEY --output /tmp/export.tar.gz); mark phase/conditions through
            steps; clean Jobs after success/failure.
          - Handle dashboard enable/disable transitions (create/delete Deployment/Service/route rules).
          - Maintain status endpoints from Service/Route host.
      - Delete
          - On deletion timestamp: ensure Gateway/HTTPRoute, Deployments, StatefulSet, PVC (if policy to delete), Services, ConfigMap, generated Secrets removed respecting owner refs.
          - Preserve external Secrets (DB/S3) and TLS Secret; clear finalizer after children gone.
  - Configuration & secrets
      - ConfigMap: backend ports (example: CONVEX_PORT=3210 as example), feature toggles per docs, logging level, Env-appropriate defaults.
      - Secrets: instance secret, admin key (used by dashboard/CLI), projected DB URL, S3 creds; handle regeneration policy (only create if absent, no rotation unless spec indicates).
      - TLS Secret referenced only; not managed.
  - Networking
      - Gateway API preferred; Gateway refers to provided gatewayClassName (example: nginx). HTTPRoute attaches host, TLS, path rules to backend/dashboard Services.
      - Service ports align with Convex defaults; annotate if NGINX Gateway Fabric specifics needed (e.g., backend protocol hints).
  - Scaling behavior
      - Backend replicas fixed to 1; vertical resources from spec; optional HPA-like hooks via cpuTargetUtilization for future (documented but not creating HPA unless explicitly decided).
      - Dashboard replicas per spec; standard Deployment rolling strategy.
  - Status/observability
      - Conditions: Available, Progressing, Degraded, UpgradeInProgress, with reasons/messages.
      - Metrics: controller-runtime default metrics, reconcile durations, condition flips; structured logging with instance key.
      - Events on failures (Secret missing, DB unreachable, rollout timeout).
  - Multi-namespace
      - Operator cluster-scoped watch on ConvexInstance; reconciles namespace-local resources; owner refs for garbage collection; RBAC allows cross-namespace watch but resource writes are namespace-scoped to
        instance namespace.
  - RBAC/Security
      - Minimal verbs on core/Apps/Gateway API resources; Secret read limited to same namespace; no cluster-wide secret access beyond watched namespaces; optionally separate service account per instance via
        projected tokens if needed.

  ## Milestones

  - M1: Project Bootstrap & CRD Design
      - Scope: Kubebuilder init, module setup, CRD types with validations/defaults, sample manifests draft.
      - Non-scope: Controller logic, Gateway objects, upgrades.
      - Dependencies: Go toolchain, Kubebuilder installed.
      - Acceptance: CRD compiles, CRD YAML generated with validation, sample ConvexInstance manifests committed.
  - M2: Core Reconciler (Backend + Storage + Secrets)
      - Scope: Reconcile ConfigMap, generated secrets (instance/admin), PVC, backend Service/StatefulSet with probes/env wiring, status basics.
      - Non-scope: Dashboard, Gateway, upgrade strategies, export/import jobs.
      - Dependencies: M1 CRD available.
      - Acceptance: Applying sample creates backend pod ready in envtest/kind; status Ready when pod ready; owner refs verified.
  - M3: Dashboard & Gateway API Integration
      - Scope: Dashboard Deployment/Service, Gateway+HTTPRoute for host/TLS routing, status endpoints population.
      - Non-scope: Upgrade strategy orchestration.
      - Dependencies: M2 backend service ready.
      - Acceptance: Host routes traffic to backend/dashboard in test cluster; dashboard disabled case handled gracefully.
  - M4: Upgrade Strategies & Maintenance
      - Scope: maintenance.upgradeStrategy handling; in-place rollout flow; export/import Job orchestration (example commands), phase/conditions transitions.
      - Non-scope: Full disaster recovery tooling.
      - Dependencies: M3 routing in place.
      - Acceptance: Changing spec.version triggers correct flow; status reflects Upgrading then Ready; errors surface in conditions.
  - M5: Testing, CI, Docs
      - Scope: Unit/envtest coverage, example manifests for dev/prod, README/operator guide, CI with lint/fmt/vet/tests.
      - Non-scope: Performance/load testing.
      - Dependencies: M1–M4 complete.
      - Acceptance: CI green; docs present with examples; envtest suite covers main reconcile paths.

  ## Tasks

### M1: Project Bootstrap & CRD Design

  - [x] Initialize Kubebuilder project (module path set to repo), ensuring Go 1.24 toolchain is configured to satisfy future compatibility and aligning with controller-runtime version.
  - [x] Define ConvexInstance API types with fields from spec, including nested structs for backend/dashboard/networking/scale/maintenance to mirror desired CRD shape.
  - [x] Add validation markers (enum for environment and upgradeStrategy, required fields like version, format for resource quantities) to enforce spec constraints at admission.
  - [x] Set defaulting markers for images (backend/dashboard using version), dashboard enabled default (true/false decision documented), and replica defaults to ensure consistent behavior.
  - [x] Generate CRD manifests (make generate && make manifests) and inspect produced CRD YAML for correct validation schemas and defaults.
  - [x] Draft sample ConvexInstance manifests for dev (SQLite + PVC) and prod (Postgres/MySQL + S3) with clearly marked example values for host/storage sizes.
  - [x] Document CRD fields and assumptions in README/design doc to align team understanding before coding controllers.

  ### M2: Core Reconciler (Backend + Storage + Secrets)

  - [ ] Implement ConvexInstanceReconciler scaffold with finalizer logic to ensure child cleanup on deletion.
  - [ ] Add reconciliation step to validate referenced Secrets (DB/S3) presence and surface missing references via status conditions and events for operator visibility.
  - [ ] Implement ConfigMap rendering for backend non-secret config (ports, logging, feature flags) based on Convex docs; ensure idempotent hashing for rollout triggers.
  - [ ] Implement secret management for instance secret and admin key: create if absent, avoid overwriting existing, annotate for ownership, and mount via env to backend and dashboard.
  - [ ] Implement PVC reconciliation when storage.pvc.enabled is true, wiring storageClass/size; skip PVC when disabled or external storage mode selected.
  - [ ] Implement backend Service reconciliation exposing Convex API and HTTP action ports per Convex defaults, ensuring stable cluster DNS name for dashboard usage.
  - [ ] Implement backend StatefulSet reconciliation (replicas=1) with env wiring: DB URL from Secret, S3 creds when enabled, instance/admin secrets, config from ConfigMap; include liveness/readiness probes to
    Convex HTTP endpoints.
  - [ ] Add resource requests/limits from spec and set pod security context defaults (non-root if compatible) while respecting Convex image needs.
  - [ ] Populate status (observedGeneration, phase, conditions) based on StatefulSet readiness and pod probe success; set endpoints.apiUrl using Service DNS + port.
  - [ ] Verify owner references on all managed objects for automatic garbage collection and validate deletion flow via envtest/kind smoke test.

  ### M3: Dashboard & Gateway API Integration

  - [ ] Implement dashboard Deployment reconciliation gated by dashboard.enabled, wiring image, replicas, resources, and env pointing to backend Service URL and admin key usage per Convex dashboard requirements.
  - [ ] Implement dashboard Service reconciliation for HTTP access, ensuring port alignment with dashboard defaults.
  - [ ] Create Gateway API integration: reconcile Gateway bound to configurable gatewayClassName (example "nginx") in instance namespace or shared (document choice), referencing TLS Secret provided.
  - [ ] Create HTTPRoute with host from spec.networking.host, TLS termination, and path rules routing /, /api/*, /sync, /http_action/* to backend Service and /dashboard* to dashboard Service, ensuring backend-
    only routing when dashboard disabled.
  - [ ] Update status endpoints.dashboardUrl using host + dashboard path and ensure status conditions reflect route attachment health (e.g., Accepted condition from HTTPRoute if available).
  - [ ] Add annotations/labels required by NGINX Gateway Fabric (if any) after reviewing its docs; document assumptions for other Gateway classes.
  - [ ] Extend envtest/integration tests to cover dashboard enabled/disabled and Gateway/HTTPRoute creation with correct backend/dash bindings.

  ### M4: Upgrade Strategies & Maintenance

  - [ ] Implement detection of spec.version or image changes and branch on maintenance.upgradeStrategy.
  - [ ] For inPlace, orchestrate StatefulSet/Deployment rolling updates, set phase Upgrading, track rollout status, and revert to Ready or Error conditions with meaningful reasons.
  - [ ] For exportImport, design Job templates using Convex CLI (example commands with placeholders) to export data before rollout and import after; ensure Jobs mount admin key and connect to API endpoint
    securely.
  - [ ] Add sequencing: set Upgrading, run export Job, wait completion, roll backend image, run import Job, then set Ready; handle failures with Degraded condition and avoid infinite retries (backoff and requeue
    with events).
  - [ ] Ensure cleanup of temporary Jobs/ConfigMaps/PVCs used for export artifacts (if any) to avoid resource leaks post-upgrade.
  - [ ] Add safeguards preventing multiple concurrent upgrade flows per instance; serialize by finalizer/condition locks.
  - [ ] Extend status/conditions to reflect upgrade progress (e.g., UpgradeInProgress, ExportCompleted, ImportCompleted) with timestamps/messages for observability.
  - [ ] Add tests simulating version bumps for both strategies, verifying state transitions and resource mutations without breaking singleton backend constraint.

  ### M5: Testing, CI, Docs

  - [ ] Add unit tests for config/secrets rendering helpers, ensuring env var construction matches Convex expectations.
  - [ ] Add envtest suites covering create/update/delete happy paths, missing Secret failures, dashboard toggle, Gateway/HTTPRoute reconciliation, and upgrade flows (mocked Jobs).
  - [ ] Integrate golangci-lint, go fmt, go vet, and make test targets into CI (GitHub Actions or equivalent), with caching to speed runs.
  - [ ] Write developer guide for local kind/minikube deployment, including how to apply sample dev/prod ConvexInstance manifests and verify readiness via kubectl and curl against Gateway host (with example
    values).
  - [ ] Document RBAC rules and security posture (namespace scoping for Secrets, owner refs) in README, noting any permissions that may need cluster-admin approval.
  - [ ] Provide sample GatewayClass assumption (example nginx), Gateway, and TLS Secret expectations in docs, clarifying operator does not manage certificates.
  - [ ] Add troubleshooting section (common errors: missing DB URL, S3 keys, GatewayClass absent, rollout hangs) with guidance on interpreting status conditions/events.
  - [ ] Final doc pass to ensure SPEC alignment; tag initial release version and changelog summarizing milestones delivered.
