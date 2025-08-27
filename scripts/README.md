# SPIRE on Kind

Deploy SPIRE server and agent on Kind using Helm charts with a demo workload.

## Prerequisites

- Docker, kubectl, Kind, Helm

## Quick Start

```bash
./install.sh    # Complete setup
./cleanup.sh    # Remove everything
```

## What It Creates

- **Kind cluster** (2 nodes) with trust domain `example.org`
- **SPIRE server/agent** with k8s PSAT attestation 
- **SPIFFE CSI driver** for JWT file injection
- **Demo nginx pod** with Go sidecar that:
  - Fetches JWT tokens via SPIFFE Workload API
  - Refreshes at 80% of token expiry with retries
  - Writes to `/var/run/secrets/nvidia.com/spiffe/token`
- **OpenBao** with SPIFFE JWT authentication configured

## Individual Steps

```bash
./01-setup-kind-cluster.sh     # Create cluster
./02-install-spire-helm.sh     # Install SPIRE
./03-verify-spire.sh           # Verify setup  
./04-demo-nginx.sh             # Deploy demo workload
./05-deploy-bao.sh             # Deploy OpenBao with SPIFFE auth
```

## Configuration

- `config/kind-config.yaml` - Cluster settings
- `config/spire-server-values.yaml` - SPIRE Helm values
- `config/demo-nginx.yaml` - Demo workload with resources
- `config/bao-values.yaml` - OpenBao Helm values with local image

## Troubleshooting

```bash
# Check status
kubectl get pods -n spire-server

# Check agent registration  
kubectl exec -n spire-server statefulset/spire-server -c spire-server -- \
  /opt/spire/bin/spire-server agent list

# Check demo workload
kubectl logs demo-nginx -c spiffe-sidecar
kubectl exec demo-nginx -c nginx -- cat /var/run/secrets/nvidia.com/spiffe/token

# Check OpenBao
kubectl get pods -n bao
kubectl port-forward -n bao svc/openbao 8200:8200
```

## Integration

Provides SPIFFE JWT tokens for Bao authentication:
```
k8s PSAT → SPIRE Agent → SPIRE Server → JWT → Bao Auth
```