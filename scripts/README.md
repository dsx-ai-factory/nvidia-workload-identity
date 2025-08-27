# SPIRE on Kind - Setup Scripts

Scripts to deploy SPIRE server and agent on a Kind cluster using the latest Helm charts.

## Prerequisites

- Docker (running)
- kubectl
- Kind
- Helm

## Quick Start

```bash
# Run complete installation
./install.sh

# Or run individual steps:
./01-setup-kind-cluster.sh     # Setup Kind cluster
./02-install-spire-helm.sh     # Install SPIRE with Helm
./03-verify-spire.sh           # Verify installation
./04-demo-nginx.sh             # Deploy demo nginx with SPIFFE access

# Cleanup when done
./cleanup.sh
```

## Files

```
scripts/
├── config/
│   ├── kind-config.yaml           # Kind cluster configuration
│   ├── spire-server-values.yaml   # SPIRE Helm values
│   └── demo-nginx.yaml            # Demo nginx pod configuration
├── 01-setup-kind-cluster.sh       # Create Kind cluster
├── 02-install-spire-helm.sh       # Install SPIRE with Helm
├── 03-verify-spire.sh             # Verify installation
├── 04-demo-nginx.sh               # Deploy demo nginx with SPIFFE access
├── install.sh                     # Complete installation script
├── cleanup.sh                     # Cleanup resources
└── common.sh                      # Shared functions
```

## Configuration

Edit configuration files directly:
- `config/kind-config.yaml` - Kind cluster settings
- `config/spire-server-values.yaml` - SPIRE Helm values including CSI driver
- `config/demo-nginx.yaml` - Demo workload pod configuration

## What It Creates

- **Kind cluster**: Multi-node cluster (spire-demo)
- **SPIRE server**: v1.12.4 running on port 8081
- **SPIRE agent**: DaemonSet on all nodes with k8s PSAT attestation
- **SPIFFE CSI driver**: For file-based token injection
- **Trust domain**: `example.org`
- **Demo workload**: Nginx pod with SPIFFE identity access via CSI

## Cleanup

```bash
./cleanup.sh                # Remove everything (cluster, demo pods)
```

## Troubleshooting

**Check pod status:**
```bash
kubectl get pods -n spire-server
```

**Check agent registration:**
```bash
kubectl exec -n spire-server statefulset/spire-server -c spire-server -- \
  /opt/spire/bin/spire-server agent list
```

**Check logs:**
```bash
kubectl logs -n spire-server statefulset/spire-server -c spire-server
kubectl logs -n spire-server daemonset/spire-agent
```

**Check CSI driver:**
```bash
kubectl get pods -n spire-server -l app.kubernetes.io/name=spiffe-csi-driver
kubectl get csidriver csi.spiffe.io
```

## Integration

This setup supports the SPIFFE/SPIRE authentication flow for Bao integration:

```
k8s PSAT → SPIRE Agent → SPIRE Server → SPIFFE JWT → Bao Auth
```

**Authentication Method**: Kubernetes Projected Service Account Token (k8s PSAT)
- SPIRE agents authenticate using Kubernetes service account tokens
- No shared secrets or pre-deployed certificates required
- Leverages Kubernetes' built-in security model

**Workload Access**: SPIFFE identities are accessible via:
- CSI driver mounted socket at `/var/run/secrets/spire.io/spire-agent.sock`
- Workload API for dynamic identity retrieval