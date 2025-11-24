# Getting Started: Deploying Convex Operator and Instances

This guide walks through installing the Convex operator onto an existing Kubernetes cluster, preparing configuration, and creating your first dev and prod `ConvexInstance` resources. It uses a Postgres database provided by a CloudNativePG cluster reachable at `pg-rw.postgres.svc.cluster.local` in the `postgres` namespace. Example hosts:
- Dev: `todo.dev.convex.icod.de`
- Prod: `todo.icod.de`

The examples use the latest Convex backend/dashboard images (`version: latest`).

## Prerequisites
- Kubernetes cluster and `kubectl` context pointing to it.
- Go toolchain (for `make` targets) and Docker permissions if building your own operator image.
- Gateway API installed with a `GatewayClass` the operator can reference (default: `nginx`).
- Postgres reachable at `pg-rw.postgres.svc.cluster.local:5432` with credentials.
- Optional: TLS Secret for your hosts if you want HTTPS.

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

If you plan to terminate TLS at the Gateway, create a TLS Secret in the same namespace (optional):
```bash
kubectl -n convex create secret tls convex-tls \
  --cert=/path/to/cert.pem --key=/path/to/key.pem
```

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
    host: todo.dev.convex.icod.de
    gatewayClassName: nginx
    # tlsSecretRef: convex-tls   # uncomment if you created a TLS secret
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
    host: todo.icod.de
    gatewayClassName: nginx
    # tlsSecretRef: convex-tls
  maintenance:
    upgradeStrategy: inPlace
```

Apply both:
```bash
kubectl apply -f convex-dev.yaml
kubectl apply -f convex-prod.yaml
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
Point your DNS (or `/etc/hosts` for testing) so `todo.dev.convex.icod.de` and `todo.icod.de` resolve to your Gateway/ingress IP. Then:
```bash
curl -i http://todo.dev.convex.icod.de
curl -i http://todo.dev.convex.icod.de/dashboard
curl -i http://todo.icod.de
curl -i http://todo.icod.de/dashboard
```
If using TLS, switch to `https://` and trust the cert accordingly.

## 7) Cleanup
```bash
kubectl delete -f convex-dev.yaml
kubectl delete -f convex-prod.yaml
make undeploy
make uninstall
```
