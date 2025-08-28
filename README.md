# SPIFFE + OpenBao CEL Demo

Complete demonstration of SPIFFE JWT authentication with OpenBao's CEL (Common Expression Language) enabled JWT auth method.

## Prerequisites

- Docker, kubectl, Kind, Helm

## Quick Start

```bash
make install    # Complete setup
make clean      # Remove everything
make test       # Run integration tests
make help       # Show all available targets
```

## What It Creates

- **Kind cluster** (2 nodes) with trust domain `sjc-1.k8s2-control.nvidia.com`
- **SPIRE server/agent** with k8s PSAT attestation 
- **SPIFFE CSI driver** for JWT file injection
- **Demo nginx pod** with Go sidecar that:
  - Fetches JWT tokens via SPIFFE Workload API
  - Refreshes at 80% of token expiry with retries
  - Writes to `/var/run/secrets/nvidia.com/spiffe/token`
- **OpenBao** with CEL-based JWT authentication and dynamic policy assignment

## Individual Steps

```bash
make cluster      # Create cluster
make spire        # Install SPIRE  
make verify-spire # Verify SPIRE installation  
make nginx        # Deploy demo workload
make bao          # Deploy OpenBao with SPIFFE auth
```

## Configuration

- `config/kind-config.yaml` - Cluster settings
- `config/spire-server-values.yaml` - SPIRE Helm values
- `config/demo-nginx.yaml` - Demo workload with resources
- `config/bao-values.yaml` - OpenBao Helm values with local image

## Troubleshooting

```bash
make verify-spire # Run SPIRE verification

# Manual checks
kubectl get pods -n spire-server
kubectl logs demo-nginx -c spiffe-sidecar
kubectl exec demo-nginx -c nginx -- cat /var/run/secrets/nvidia.com/spiffe/token
kubectl port-forward -n bao svc/openbao 8200:8200
```

## Architecture

This demo creates a complete SPIFFE identity and authentication flow within a Kind cluster:

```mermaid
flowchart TD
    %% Kubernetes Infrastructure
    subgraph K8s["Kubernetes Kind Cluster"]
        direction TB
        subgraph SPIRE["SPIRE Server"]
            SPIRE_Server["SPIRE Server<br/>(Helm Chart)"]
            OIDC["OIDC Discovery<br/>Provider"]
        end
        
        subgraph OpenBao["OpenBao Server"]
            BAO["Bao API"]
            subgraph CEL["CEL Program Logic"]
                direction LR
                Parse["Parse SPIFFE ID<br/>spiffe://region.tenant.product.nvidia.com/resource-type/resource-id<br/>↓<br/>sjc-1.k8s2-control.default.nvidia.com/pod/demo-nginx"]
                Policies["Assign Policies<br/>spiffe-authenticated<br/>region-sjc-1<br/>tenant-k8s2-control<br/>product-default<br/>resource-type-pod<br/>resource-id-demo-nginx"]
                Parse --> Policies
            end
        end
        
        subgraph Worker["Worker Node"]
            SPIRE_Agent["SPIRE Agent<br/>(DaemonSet)"]
            
            subgraph Pod["Demo Nginx Pod"]
                Nginx["Nginx Container"]
                Sidecar["Go Sidecar<br/>(JWT Fetcher)"]
                Volume["Shared Volume<br/>/var/run/secrets/nvidia.com/spiffe/token"]
            end
        end
    end
    
    %% Authentication Flow
    SPIRE_Agent -->|"K8s PSAT<br/>Attestation"| SPIRE_Server
    SPIRE_Server -->|"Workload<br/>Registration"| SPIRE_Agent
    SPIRE_Server --> OIDC
    BAO -->|"Pull OIDC Config<br/>& JWKS"| OIDC
    
    %% JWT Flow
    Sidecar -->|"Workload API<br/>Request"| SPIRE_Agent
    SPIRE_Agent -->|"SPIFFE JWT<br/>spiffe://sjc-1.k8s2-control.nvidia.com/controlplane/pod/demo-nginx"| Sidecar
    Sidecar -->|"Write JWT<br/>(80% refresh)"| Volume
    
    %% OpenBao Authentication
    Nginx -->|"Read JWT from Volume"| Volume
    Nginx -->|"JWT Authentication<br/>/v1/auth/jwt/cel/login"| BAO
    BAO --> CEL
    CEL -->|"Bao Token"| Nginx
    Nginx -->|"Access Secrets<br/>secret/*"| BAO
    
    %% Styling
    classDef cluster fill:#e3f2fd,stroke:#1976d2,stroke-width:2px
    classDef spire fill:#f3e5f5,stroke:#7b1fa2,stroke-width:2px
    classDef app fill:#e8f5e8,stroke:#388e3c,stroke-width:2px
    classDef bao fill:#fff3e0,stroke:#f57c00,stroke-width:2px
    classDef flow fill:#fce4ec,stroke:#c2185b,stroke-width:2px
    
    class K8s,Worker cluster
    class SPIRE,SPIRE_Server,SPIRE_Agent,OIDC,CSI spire
    class Pod,Nginx,Sidecar,Volume app
    class OpenBao,BAO,CEL bao
    class Parse,Policies flow
```

### Key Features:

- **SPIFFE Identity**: Workloads get cryptographically verifiable identities
- **Dynamic Policy Assignment**: CEL program parses SPIFFE ID components (`region`, `tenant`, `product`, `resource-type`, `resource-id`) from pattern `spiffe://region.tenant.nvidia.com/product/resource-type/resource-id` and assigns policies dynamically
- **Automatic JWT Refresh**: Go sidecar refreshes tokens at 80% expiry with retry logic