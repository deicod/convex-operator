# Getting Started: Deploying Convex Operator and Instances

This guide walks through installing the Convex operator onto an existing Kubernetes cluster, preparing configuration, and creating your first dev and prod `ConvexInstance` resources. It uses a Postgres database provided by a CloudNativePG cluster reachable at `pg-rw.postgres.svc.cluster.local` in the `postgres` namespace. Example hosts:
- API (dev, port 3210): `api.todo.dev.convex.icod.de`
- API (prod, port 3210): `api.todo.icod.de`
- App/HTTP actions (dev, port 3211): `todo.dev.convex.icod.de`
- App/HTTP actions (prod, port 3211): `todo.icod.de`
- Dashboard (dev, port 6791): `dash.todo.dev.convex.icod.de`
- Dashboard (prod, port 6791): `dash.todo.icod.de`

Convex exposes three HTTP ports: `3210` (API + sync), `3211` (HTTP actions/app), and `6791` (dashboard). The operator-managed Service and HTTPRoute target these ports, but the CRD only carries a single `spec.networking.host`; add extra HTTPRoutes if you want separate API/app/dashboard hostnames (example below).

The examples use the latest Convex backend/dashboard images (`version: latest`).

## Prerequisites
- Kubernetes cluster and `kubectl` context pointing to it.
- Go toolchain (for `make` targets) and Docker permissions if building your own operator image.
- Gateway API installed with a `GatewayClass` the operator can reference (default: `nginx`).
- Postgres reachable at `pg-rw.postgres.svc.cluster.local:5432` with credentials.
- Optional: TLS Secret for your hosts if you want HTTPS. The operator creates a Gateway per `ConvexInstance` and, by default, annotates it with `cert-manager.io/cluster-issuer: letsencrypt-prod-rfc2136`; set `spec.networking.gatewayAnnotations` to override (or `{}` to disable) and point `spec.networking.tlsSecretRef` at the Secret name cert-manager should populate.

## 1) Clone the repo
```bash
git clone https://github.com/deicod/convex-operator.git
cd convex-operator
```

## 2) Install CRDs and deploy the operator
If you have a published operator image, set `IMG` accordingly; otherwise the default uses `controller:latest` built locally.
```bash
make install                      # applies CRDs
make deploy IMG=<your-operator-image>   # deploys controller manager
```
Verify the controller pod:
```bash
kubectl -n convex-operator-system get pods
```

## 3) Prepare a namespace and DB secrets
Create a namespace for your Convex instances (example: `convex`):
```bash
kubectl create namespace convex
```

Create Postgres URL secrets in that namespace. Replace credentials/database names with your values; the URL must use the service in `postgres` namespace:
```bash
kubectl -n convex create secret generic convex-dev-db \
  --from-literal=url='postgres://devuser:devpass@pg-rw.postgres.svc.cluster.local:5432/convex_dev?sslmode=require'

kubectl -n convex create secret generic convex-prod-db \
  --from-literal=url='postgres://produser:prodpass@pg-rw.postgres.svc.cluster.local:5432/convex_prod?sslmode=require'
```

If you plan to terminate TLS at the Gateway, create a TLS Secret in the same namespace (optional if you let cert-manager populate it):
```bash
kubectl -n convex create secret tls convex-tls \
  --cert=/path/to/cert.pem --key=/path/to/key.pem
```
Alternatively, keep `spec.networking.tlsSecretRef` set and rely on the default Gateway annotation (`cert-manager.io/cluster-issuer: letsencrypt-prod-rfc2136`) or your own `gatewayAnnotations` to have cert-manager issue the certificate into that Secret.

## 4) Create the ConvexInstance resources
Save the following manifests (adjust hosts, secrets, and TLS as needed) and apply them. These use the Postgres engine and point to the secrets created above.

`convex-dev.yaml`:
```yaml
apiVersion: convex.icod.de/v1alpha1
kind: ConvexInstance
metadata:
  name: todo-dev
  namespace: convex
spec:
  environment: dev
  version: latest
  backend:
    image: ghcr.io/get-convex/convex-backend:latest
    db:
      engine: postgres
      secretRef: convex-dev-db
      urlKey: url
    storage:
      mode: external     # use external Postgres; PVC not required
  dashboard:
    enabled: true
    image: ghcr.io/get-convex/convex-dashboard:latest
  networking:
    host: api.todo.dev.convex.icod.de   # operator-managed host; add extra HTTPRoutes if you want app/dashboard on separate hosts
    gatewayClassName: nginx
    tlsSecretRef: convex-tls            # cert-manager will populate this by default; precreate if you are not using cert-manager
    # gatewayAnnotations:               # optional: override the default cert-manager issuer or disable
    #   cert-manager.io/cluster-issuer: letsencrypt-staging-rfc2136
  maintenance:
    upgradeStrategy: inPlace
```

`convex-prod.yaml`:
```yaml
apiVersion: convex.icod.de/v1alpha1
kind: ConvexInstance
metadata:
  name: todo-prod
  namespace: convex
spec:
  environment: prod
  version: latest
  backend:
    image: ghcr.io/get-convex/convex-backend:latest
    db:
      engine: postgres
      secretRef: convex-prod-db
      urlKey: url
    storage:
      mode: external
  dashboard:
    enabled: true
    image: ghcr.io/get-convex/convex-dashboard:latest
  networking:
    host: api.todo.icod.de   # operator-managed host; add extra HTTPRoutes if you want app/dashboard on separate hosts
    gatewayClassName: nginx
    tlsSecretRef: convex-tls
    # gatewayAnnotations:
    #   cert-manager.io/cluster-issuer: letsencrypt-staging-rfc2136
  maintenance:
    upgradeStrategy: inPlace
```

Apply both:
```bash
kubectl apply -f convex-dev.yaml
kubectl apply -f convex-prod.yaml
```

If you want distinct app and dashboard hostnames, keep `spec.networking.host` on the API domain and add extra `HTTPRoute` objects that attach to the same Gateway. Example (dev):
```yaml
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: todo-dev-app
  namespace: convex
spec:
  parentRefs:
  - name: todo-dev-gateway
    namespace: convex
  hostnames:
  - todo.dev.convex.icod.de
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: "/http_action/"
    backendRefs:
    - name: todo-dev-backend
      port: 3211
  - matches:
    - path:
        type: PathPrefix
        value: "/"
    backendRefs:
    - name: todo-dev-backend
      port: 3211
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: todo-dev-dash
  namespace: convex
spec:
  parentRefs:
  - name: todo-dev-gateway
    namespace: convex
  hostnames:
  - dash.todo.dev.convex.icod.de
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: "/"
    backendRefs:
    - name: todo-dev-dashboard
      port: 6791
```
Prod variant:
```yaml
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: todo-prod-app
  namespace: convex
spec:
  parentRefs:
  - name: todo-prod-gateway
    namespace: convex
  hostnames:
  - todo.icod.de
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: "/http_action/"
    backendRefs:
    - name: todo-prod-backend
      port: 3211
  - matches:
    - path:
        type: PathPrefix
        value: "/"
    backendRefs:
    - name: todo-prod-backend
      port: 3211
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: todo-prod-dash
  namespace: convex
spec:
  parentRefs:
  - name: todo-prod-gateway
    namespace: convex
  hostnames:
  - dash.todo.icod.de
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: "/"
    backendRefs:
    - name: todo-prod-dashboard
      port: 6791
```

## 5) Verify resources and status
Check instances and conditions:
```bash
kubectl -n convex get convexinstances
kubectl -n convex describe convexinstance todo-dev
kubectl -n convex describe convexinstance todo-prod
```

Ensure backend/dash/Gateway/HTTPRoute are created:
```bash
kubectl -n convex get pods,svc,deploy,sts,gw,httproute
```

Wait for `Ready=True`:
```bash
kubectl -n convex get convexinstance todo-dev -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}{"\n"}'
kubectl -n convex get convexinstance todo-prod -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}{"\n"}'
```

## 6) Test routing via Gateway host
Point your DNS (or `/etc/hosts` for testing) so your chosen hosts resolve to your Gateway/ingress IP. If you used the split-host setup above:
```bash
curl -i http://api.todo.dev.convex.icod.de/api
curl -i http://api.todo.icod.de/api
curl -i http://todo.dev.convex.icod.de
curl -i http://todo.icod.de
curl -i http://dash.todo.dev.convex.icod.de
curl -i http://dash.todo.icod.de
```
If you did not add the extra HTTPRoutes, curl the host you set in `spec.networking.host` (e.g., `api.todo.dev.convex.icod.de` and its `/dashboard` path).
If using TLS, switch to `https://` and trust the cert accordingly.

## 7) Cleanup
```bash
kubectl delete -f convex-dev.yaml
kubectl delete -f convex-prod.yaml
make undeploy
make uninstall
```
