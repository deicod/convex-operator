# Backend Env Extensibility Plan

Track implementation work to expose all backend environment variables referenced in the upstream Convex self-hosted README.

- [ ] Inventory & scope
  - [x] Upstream README envs not covered today: `DISABLE_BEACON`, `REDACT_LOGS_TO_CLIENT`, `S3_ENDPOINT_URL` alias, and any other advanced knobs (via generic passthrough).
  - [x] Current operator coverage: ports/env/version, DB URL + `DO_NOT_REQUIRE_SSL`, `INSTANCE_NAME`, S3 creds/buckets/endpoints (`AWS_ENDPOINT_URL`/`AWS_ENDPOINT_URL_S3`), cloud/site origins, dashboard envs.
- [ ] CRD/API changes
  - [x] Add `spec.backend.telemetry.disableBeacon` -> `DISABLE_BEACON` (bool, default false).
  - [x] Add `spec.backend.logging.redactLogsToClient` -> `REDACT_LOGS_TO_CLIENT` (bool, default false).
  - [x] Extend `spec.backend.s3` to optionally emit `S3_ENDPOINT_URL` alongside existing AWS endpoint envs.
  - [x] Add generic passthrough `spec.backend.env` (`[]corev1.EnvVar`) merged after operator-managed envs, allowing overrides when necessary.
  - [x] Regenerate CRD (`make generate manifests`).
- [ ] Controller wiring
  - [x] Inject new telemetry/logging env vars.
  - [x] Emit S3 endpoint alias when configured.
  - [x] Merge passthrough envs with deterministic ordering; document/implement precedence rules.
  - [x] Ensure upgrade hash/config hash accounts for new envs to trigger rollouts.
- [ ] Tests
  - [x] Unit/envtest: assert new env vars appear with defaults/off and when set.
  - [x] Passthrough env test, including override of an operator-managed env (e.g., `CONVEX_VERSION` or `INSTANCE_NAME`) to confirm precedence.
  - [x] S3 endpoint alias test when field set.
- [ ] Samples & docs
  - [x] Update samples to demonstrate `disableBeacon` and `env` passthrough usage.
  - [x] Update README/SPEC to document new fields, precedence, and S3 endpoint alias.
- [ ] Rollup
  - [x] Run `make test`.
  - [ ] Prepare changelog/notes for PR.
