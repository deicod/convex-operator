# Developer Guide: Local kind/minikube Deployment

This guide shows how to run the Convex operator locally on a Kubernetes cluster created with kind or minikube, apply the sample ConvexInstance manifests (dev/prod), and verify readiness via `kubectl` and `curl` against the Gateway host.

## Prerequisites
- Docker and either:
  - kind v0.22+ **or**
  - minikube v1.32+ with a working driver (e.g., Docker)
- kubectl v1.29+ configured to talk to your local cluster
- Go toolchain (for `make` targets)
- Optional: curl for endpoint checks

## 1) Create a local cluster

### kind
```bash
kind create cluster --name convex-local --image kindest/node:v1.29.4
kubectl cluster-info --context kind-convex-local
```

### minikube
```bash
minikube start --profile convex-local --kubernetes-version=v1.29.4
kubectl config use-context convex-local
kubectl cluster-info
```

## 2) Install CRDs and run the operator locally
From the repo root:
```bash
make install        # applies CRDs
make run            # runs controller locally against your kubeconfig
```

> Leave `make run` streaming logs in one terminal. Use a second terminal for kubectl.

## 3) Apply sample ConvexInstance manifests
Samples live under `config/samples/`:
- `config/samples/convex_v1alpha1_convexinstance_dev.yaml`
- `config/samples/convex_v1alpha1_convexinstance_prod.yaml`

Apply one (or both):
```bash
kubectl apply -f config/samples/convex_v1alpha1_convexinstance_dev.yaml
kubectl apply -f config/samples/convex_v1alpha1_convexinstance_prod.yaml
```

Watch status:
```bash
kubectl get convexinstances
kubectl describe convexinstance convexinstance-sample-dev
kubectl describe convexinstance convexinstance-sample-prod
```

## 4) Verify readiness
Check core resources:
```bash
kubectl get pods,svc,deploy,sts,gw,httproute -l app.kubernetes.io/instance=convexinstance-sample-dev
```

When ready, the `Ready` condition is `True` and the status includes endpoints:
```bash
kubectl get convexinstance convexinstance-sample-dev -o jsonpath='{.status.endpoints.apiUrl}{"\n"}'
kubectl get convexinstance convexinstance-sample-dev -o jsonpath='{.status.endpoints.dashboardUrl}{"\n"}'
```

## 5) Gateway host and curl checks
Sample manifests set `spec.networking.host` to placeholder hostnames (e.g., `dev.example.com`, `prod.example.com`). For local testing, add an entry to `/etc/hosts` pointing those hosts to your ingress/Gateway IP.

Get the Gateway/HTTPRoute address (kind/minikube with default LoadBalancer emulation may expose via NodePort or minikube tunnel):
```bash
kubectl get svc -A | grep gateway
```

If using `minikube tunnel`, start it in a terminal:
```bash
minikube tunnel --profile convex-local
```

Add to `/etc/hosts` (adjust IP from the service EXTERNAL-IP or node IP):
```
127.0.0.1 dev.example.com
127.0.0.1 prod.example.com
```

Then curl the routes:
```bash
curl -i http://dev.example.com        # backend API
curl -i http://dev.example.com/dashboard
curl -i http://prod.example.com
curl -i http://prod.example.com/dashboard
```

You should see `200` or `302` responses once services are ready. If TLS is configured, use `https://` and trust the cert or pass `-k` for self-signed testing.

## 6) Cleanup
Delete the samples and CRDs:
```bash
kubectl delete -f config/samples/convex_v1alpha1_convexinstance_dev.yaml
kubectl delete -f config/samples/convex_v1alpha1_convexinstance_prod.yaml
make uninstall
```

Remove the cluster:
- kind: `kind delete cluster --name convex-local`
- minikube: `minikube delete --profile convex-local`
