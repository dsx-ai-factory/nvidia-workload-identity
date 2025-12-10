# SPIFFE + OpenBao CEL Demo

Complete demonstration of SPIFFE JWT authentication with OpenBao's CEL (Common Expression Language) enabled JWT auth method, including a SPIFFE JWT metadata service at 169.254.169.254 for KubeVirt VMs.

## Prerequisites

- Docker, kubectl, Kind, Helm, OpenBao CLI, virtctl

## Quick Start

```bash
make install     # Complete setup (cluster, SPIRE, OpenBao, metadata service, VM)
make verify-vm   # Verify VM can fetch SPIFFE JWTs from metadata service
make test        # Run integration tests
make clean       # Remove everything
make help        # Show all available targets
```

## What It Creates

- **Kind cluster** (2 nodes) with trust domain `sjc-1.k8s2-control.nvidia.com`
- **SPIRE server/agent** with Kubernetes PSAT attestation
- **SPIFFE CSI driver** for Workload API socket mounting
- **KubeVirt + CDI** for virtual machine management
- **Kyverno** for policy-based sidecar injection
- **SPIFFE Exchange Service** for JWT transformation to tenant-specific trust domains
- **Demo nginx pod** with Go sidecar that fetches/refreshes JWT tokens via exchange service
- **OpenBao** with CEL-based JWT authentication and dynamic policy assignment
- **SPIFFE JWT Metadata Service** at 169.254.169.254 for VMs
- **Metadata Workload API Bridge CSI** for per-pod socket bridging
- **Ubuntu VM** with automatic SPIFFE JWT access via metadata service

## SPIFFE Exchange Service

### Overview

The SPIFFE Exchange Service transforms SPIFFE JWTs from the cluster's base trust domain into tenant-specific trust domains. Workloads with a `spiffe.nvidia.com/domain` label get SPIFFE IDs containing a `/domain/{tenant}/` path component, which the exchange service uses to create new JWTs under the tenant's trust domain.

### JWT Transformation

**Input SPIFFE ID** (from SPIRE):
```
spiffe://sjc-1.k8s2-control.nvidia.com/domain/tenant1/default/pod/demo-nginx
```

**Output SPIFFE ID** (from Exchange Service):
```
spiffe://tenant1.sjc-1.k8s2-control.nvidia.com/default/pod/demo-nginx
```

The exchange service:
1. Validates the incoming JWT against SPIRE's trust bundle
2. Extracts the tenant prefix from the `/domain/{tenant}/` path component
3. Constructs a new trust domain: `{tenant}.{base-domain}`
4. Re-signs the JWT with a tenant-specific key, updating `sub` and `iss` claims
5. Returns the new JWT with the transformed SPIFFE ID

### JWKS Endpoint

The exchange service serves a JWKS endpoint at `/.well-known/jwks.json` for each tenant domain. CoreDNS is configured with a wildcard rewrite rule so that `*.sjc-1.k8s2-control.nvidia.com` resolves to the exchange service, enabling host-based routing.

OpenBao fetches the JWKS from `http://tenant1.sjc-1.k8s2-control.nvidia.com:8080/.well-known/jwks.json` to validate exchanged JWTs.

### Configuration

- `--base-domain`: Base trust domain (e.g., `sjc-1.k8s2-control.nvidia.com`)
- `--socket`: SPIFFE Workload API socket path for validating incoming JWTs

## SPIFFE JWT Metadata Service

### Overview

HTTP metadata service at **169.254.169.254** that provides SPIFFE JWT to KubeVirt VMs via tenant-specific DaemonSet deployment. Uses OVN User Defined Networks (UDN) for tenant isolation and source IP identity verification.

VMI pods include a `socat` sidecar that proxies the SPIRE Workload API socket to a per-pod bridge socket (Metadata Workload API Bridge CSI) shared with the metadata service. This allows the metadata service to fetch JWTs by connecting to the Workload API through the sidecar.

**Why the proxy sidecar is required**: SPIRE's workload attestation is PID-based. The caller PID must be the workload being attested. A PID from outside the VM's pod would result in a different workload identity.

### Architecture

#### Block Diagram

```plantuml
@startuml SPIFFE_JWT_Metadata_Service_Architecture

package "Kubernetes Cluster" as Cluster {
  package "Tenant UDN" as TenantA {
    package "Kubernetes Node" as NodeA {
      package "VMI Pod" as VMIA {
        package "KubeVirt VM" as VMA {
          component "Customer Application" as CustomerApp
        }
        component "Workload API Proxy Sidecar\n(socat)" as Sidecar
        component "SPIRE CSI Socket" as SpireCSI
        component "Per-Pod Socket\n(Metadata Bridge CSI)" as SharedSocket
      }
      component "Metadata Service\n(DaemonSet Pod)" as MetadataService
      component "SPIRE Agent\n(DaemonSet)" as SpireAgent
      component "K8s Service\n169.254.169.254\nLocal Traffic Policy" as Service
    }
  }
  
  component "SPIFFE Exchange Service" as ExchangeService
  component "Kubernetes API Server" as K8sAPI
  component "SPIRE Server" as SpireServer
}

CustomerApp --> Service : 1. POST /v1/metadata/identity\nSource IP: VMI IP
Service --> MetadataService
MetadataService --> K8sAPI : 2. IP to Pod Lookup
MetadataService --> SharedSocket : 3. Fetch JWT SVID
SharedSocket --> Sidecar : 4. Fetch JWT SVID (passthrough)
Sidecar --> SpireCSI : 5. Fetch JWT SVID (passthrough)
SpireAgent --> SpireServer : 6. K8s Workload Attestation
SpireServer --> SpireAgent : 7. JWT SVID
SpireCSI --> SpireAgent
SharedSocket --> MetadataService
MetadataService --> ExchangeService : 8. Exchange JWT
ExchangeService --> MetadataService : 9. Exchanged JWT
MetadataService --> Service : 10. JWT Response

@enduml
```

#### Sequence Diagram

```plantuml
@startuml SPIFFE_JWT_Metadata_Service_Sequence
participant "Customer Application" as CustomerApp
participant "K8s Service\n169.254.169.254\nLocal Traffic Policy" as Service  
participant "Metadata Service\n(DaemonSet Pod)" as MetadataService
participant "SPIFFE Exchange Service" as ExchangeService
participant "Kubernetes API Server" as K8sAPI
participant "Per-Pod Socket\n(Metadata Bridge CSI)" as SharedSocket 
participant "Workload API Proxy Sidecar\n(socat)" as Sidecar
participant "SPIRE CSI Socket" as SpireCSI
participant "SPIRE Agent\n(DaemonSet)" as SpireAgent
participant "SPIRE Server" as SpireServer
participant "OpenBao Server" as Bao

CustomerApp -> Service : POST /v1/metadata/identity\nSource IP: VMI IP
Service -> MetadataService : passthrough
MetadataService -> K8sAPI : IP to Pod Lookup
K8sAPI --> MetadataService : Pod Info\n(for finding socket name)
MetadataService -> SharedSocket : Workload API Request\n(Fetch JWT SVID)
SharedSocket -> Sidecar : passthrough
Sidecar -> SpireCSI : passthrough
SpireCSI -> SpireAgent : passthrough
SpireAgent -> SpireServer : K8s Workload Attestation
SpireServer --> SpireAgent : Attestation Result
SpireAgent --> SpireCSI : Workload API Response\n(JWT SVID)
SpireCSI --> Sidecar : passthrough
Sidecar --> SharedSocket : passthrough
SharedSocket --> MetadataService : passthrough
MetadataService -> ExchangeService : Exchange JWT\n(transform to tenant domain)
ExchangeService --> MetadataService : Exchanged JWT
MetadataService --> Service : Identity Response
Service --> CustomerApp : passthrough

CustomerApp -> Bao : JWT Login
Bao -> ExchangeService : Fetch Public Keyset (tenant domain)\n(cached)
Bao --> Bao : CEL Policy
Bao --> CustomerApp : Bao Token
CustomerApp -> Bao : KV Secrets Request
Bao --> CustomerApp : KV Secrets
@enduml
```

### Components

#### Metadata Service DaemonSet

- Tenant-specific DaemonSet with node selector for tenant labels
- Attached to tenant UDN for network isolation
- Listens on port 8080, exposed at **169.254.169.254** via Kubernetes Service with `externalTrafficPolicy: Local` for source IP preservation
- Mounts Metadata Workload API Bridge CSI volume to access per-pod sockets at `/run/metadata/metadata-bridge-sockets/<pod-uid>/`
- Resolves VM IP to pod via Kubernetes API, connects to corresponding bridge socket to fetch JWT via proxied Workload API

#### Workload API Proxy Sidecar

- Injected into VMI pods via Kyverno policy (matches annotation `spiffe.nvidia.com/inject-workload-api-proxy: "true"`)
- Proxies the SPIRE Workload API socket to the Metadata Workload API Bridge CSI socket using `socat`
- Command: `socat -dd UNIX-LISTEN:/var/run/secrets/nvidia.com/metadata-bridge-spire-agent.sock,fork,unlink-early UNIX-CONNECT:/var/run/secrets/spire.io/spire-agent.sock`
- Inherits VMI pod's SPIFFE identity via SPIRE's Kubernetes workload attestation, allowing metadata service to fetch JWTs as the VM pod

#### Metadata Workload API Bridge CSI Driver

- Single CSI driver (`metadata-bridge.csi.nvidia.com`) enabling per-pod Workload API socket bridging
- Creates per-pod directories at `/run/metadata/metadata-bridge-sockets/<pod-uid>/` with 0777 permissions for `socat` to bind
- Mounts directory into VMI pod at `/var/run/secrets/nvidia.com/` and metadata service at `/run/metadata/metadata-bridge-sockets/`
- Based on [SPIFFE CSI Driver](https://github.com/spiffe/spiffe-csi) architecture

### Identity Resolution Flow

1. VM sends HTTP POST to `169.254.169.254:8080/v1/metadata/identity` with audience
2. Metadata service extracts source IP, queries Kubernetes API to find VMI pod
3. Metadata service connects to `/run/metadata/metadata-bridge-sockets/<pod-uid>/metadata-bridge-spire-agent.sock`
4. Sidecar proxies connection to SPIRE Workload API socket
5. SPIRE Agent attests sidecar's PID, resolving to VMI pod's SPIFFE identity
6. SPIRE returns JWT SVID through proxied connection
7. Metadata service returns JWT to VM via HTTP response

### HTTP API

**Request**:

```bash
curl -X POST http://169.254.169.254:8080/v1/metadata/identity \
  -H "Content-Type: application/json" \
  -d '{"audience": "spiffe://tenant1.sjc-1.k8s2-control.nvidia.com"}'
```

**Response**:

```json
{
  "token": "eyJhbGciOiJFUzI1NiIs...",
  "expires_at": "2025-09-18T15:30:00Z",
  "spiffe_id": "spiffe://tenant1.sjc-1.k8s2-control.nvidia.com/default/pod/virt-launcher-demo-ubuntu-vm-9n5ln"
}
```

### Security Model

- **Source IP Authenticity**: OVN guarantees source IP cannot be spoofed within UDN
- **Pod Identity Verification**: Kubernetes API confirms VMI pod identity from source IP
- **Workload Attestation**: SPIRE Agent attests sidecar's PID to VMI pod identity
- **Socket Isolation**: Per-pod sockets prevent cross-VM communication
- **Network Isolation**: Tenant UDNs prevent cross-tenant access

## OpenBao CEL Integration

### Architecture

```plantuml
@startuml OpenBao_CEL_Integration_Architecture

package "OpenBao Server (globally hosted)" as OpenBao {
  component "Bao API" as BAO
  package "CEL Program Logic" as CEL {
    component "Parse SPIFFE ID\nspiffe://region.tenant.product.nvidia.com/resource-type/resource-id\n(sjc-1.k8s2-control.default.nvidia.com/pod/demo-nginx)" as Parse
    component "Assign Policies\nspiffe-authenticated\nregion-sjc-1\ntenant-k8s2-control\nproduct-default\nresource-type-pod\nresource-id-demo-nginx" as Policies
  }
}

package "Kubernetes Cluster" as K8s {
  package "SPIRE Server" as SPIRE {
    component "SPIRE Server\n(Helm Chart)" as SPIRE_Server
  }
  
  component "SPIFFE Exchange Service" as ExchangeService
  
  package "Worker Node" as Worker {
    component "SPIRE Agent\n(DaemonSet)" as SPIRE_Agent
    
    package "Demo Nginx Pod" as Pod {
      component "Nginx Container" as Nginx
      component "Go Sidecar\n(JWT Fetcher)" as Sidecar
      component "Shared Volume\n/var/run/secrets/nvidia.com/spiffe/token" as Volume
    }
  }
}

' Authentication Flow
SPIRE_Agent --> SPIRE_Server : K8s PSAT\nAttestation
SPIRE_Server --> SPIRE_Agent : Workload\nRegistration
BAO --> ExchangeService : Pull JWKS\n(tenant domain)

' JWT Flow
Sidecar --> SPIRE_Agent : Workload API\nRequest
SPIRE_Agent --> Sidecar : SPIFFE JWT\nspiffe://sjc-1.k8s2-control.nvidia.com/domain/tenant1/default/pod/demo-nginx
Sidecar --> ExchangeService : Exchange JWT
ExchangeService --> Sidecar : Exchanged JWT\nspiffe://tenant1.sjc-1.k8s2-control.nvidia.com/default/pod/demo-nginx
Sidecar --> Volume : Write JWT\n(80% refresh)

' OpenBao Authentication
Nginx --> Volume : Read JWT from Volume
Nginx --> BAO : JWT Authentication\n/v1/auth/jwt/cel/login
BAO --> CEL
Parse --> Policies
CEL --> Nginx : Bao Token
Nginx --> BAO : Access Secrets\nsecret/*

@enduml
```

### Key Features

- **SPIFFE Identity**: Workloads get cryptographically verifiable identities
- **Tenant-Specific Trust Domains**: Exchange service transforms JWTs to tenant-specific trust domains (e.g., `tenant1.sjc-1.k8s2-control.nvidia.com`)
- **Dynamic Policy Assignment**: CEL program parses SPIFFE ID components (`region`, `tenant`, `product`, `resource-type`, `resource-id`) and assigns policies dynamically
- **Automatic JWT Refresh**: Go sidecar refreshes tokens at 80% expiry with retry logic

## SPIRE Multi-Cluster Key Management

SPIRE servers in k2 clusters use [intermediate signing keys](https://github.com/spiffe/spire/blob/v1.12.5/doc/plugin_server_upstreamauthority_spire.md) from k1:

- **Single JWKS Endpoint**: Only k1 serves public JWKS externally
- **Single Key Domain**: New k2 clusters use existing keyset
- **Reduced Exposure**: k2 SPIRE servers don't require public JWKS endpoints
- **Native K8s Auth**: k2 maintains local SPIRE servers for Kubernetes attestation

## Individual Steps

```bash
make cluster            # Create Kind cluster
make kubevirt           # Install KubeVirt and CDI
make kyverno            # Install Kyverno
make spire              # Install SPIRE
make verify-spire       # Verify SPIRE installation
make exchange-service   # Deploy SPIFFE exchange service
make bao                # Deploy OpenBao with SPIFFE auth
make nginx              # Deploy demo workload
make metadata-service   # Deploy metadata service
make vm                 # Deploy Ubuntu VM
make verify-vm          # Verify VM metadata service
```

## Configuration Files

- `config/kind-config.yaml` - Cluster settings
- `config/spire-server-values.yaml` - SPIRE Helm values (includes domain label in SPIFFE ID template)
- `config/spiffe-exchange-service.yaml` - Exchange service deployment
- `config/demo-nginx.yaml` - Demo workload with Go sidecar
- `config/bao-values.yaml` - OpenBao Helm values
- `config/metadata-service-daemonset.yaml.tmpl` - Metadata service deployment
- `config/metadata-service-rbac.yaml.tmpl` - Metadata service permissions
- `config/metadata-bridge-csi-daemonset.yaml` - Metadata Workload API Bridge CSI driver
- `config/workload-api-proxy-policy.yaml` - Kyverno policy for sidecar injection
- `config/vm-template.yaml.tmpl` - VM template with UDN and sidecar annotation

## Troubleshooting

### SPIRE, Exchange Service, and Metadata Service

```bash
make verify-spire       # Run SPIRE verification
make verify-vm          # Verify VM metadata service

# Manual checks
kubectl get pods -n spire-server
kubectl logs -l app=spiffe-exchange-service --tail=50
kubectl logs demo-nginx -c spiffe-sidecar
kubectl logs -l app=metadata-service --tail=50
kubectl logs -l kubevirt.io=virt-launcher -c workload-api-proxy --tail=50

# Test exchange service directly
kubectl run test --rm -it --image=curlimages/curl -- \
  curl -X POST http://spiffe-exchange-service:8080/v1/exchange \
  -H "Content-Type: application/json" \
  -d '{"jwt": "<spiffe-jwt-from-spire>"}'
```

### OpenBao

```bash
kubectl port-forward -n bao svc/openbao 8200:8200
kubectl exec demo-nginx -c nginx -- cat /var/run/secrets/nvidia.com/spiffe/token
```

### VM Access

```bash
virtctl console demo-ubuntu-vm -n default
# Username: ubuntu, Password: ubuntu

# Inside VM, test metadata service
curl -X POST http://169.254.169.254:8080/v1/metadata/identity \
  -H "Content-Type: application/json" \
  -d '{"audience": "spiffe://tenant1.sjc-1.k8s2-control.nvidia.com"}'
```

## Implementation Notes

- **CSI Driver**: Creates per-pod directories with 0777 permissions for `socat` to bind socket
- **Socket Path Pattern**: `/run/metadata/metadata-bridge-sockets/<pod-uid>/metadata-bridge-spire-agent.sock`
- **RBAC**: Metadata service requires `pods` get/list permissions for IP resolution
- **UDN Network**: VMs and metadata service share tenant-specific User Defined Network for isolation
- **Service IP**: Currently uses dynamic UDN IP; static 169.254.169.254 support planned in [ovn-kubernetes#5595](https://github.com/ovn-kubernetes/ovn-kubernetes/pull/5595)
