# DSX identity

DSX identity issues identities from SPIRE and exchanges them for DSX JWTs. The
local integration environment runs two Kind clusters, each with its own SPIRE
server and Exchange shard. It exercises RFC 8693 token exchange, DSX IMDS v1,
KubeVirt, and an external OpenBao relying party.

## Local E2E topology

```mermaid
flowchart TB
  subgraph K1["k1: Common Services"]
    direction TB
    I1["Identity shard k1<br/>SPIRE + Exchange"]
    P1["Kubernetes Pod"] <-->|"Workload API + token-fetcher"| I1
    BUS["DSX Event Bus<br/>retained MQTT"]
    I1 <-->|"Exchange public keys by kid"| BUS
  end

  subgraph K2["k2: Data Hall"]
    direction TB
    SRC["DPU CR + LaunchLayer CR"] --> CTRL["Identity Controller"]
    CTRL -->|"SPIRE ClusterStaticEntry"| I2["Identity shard k2<br/>SPIRE + Exchange"]
    DPU["DPU process or Pod"] <-->|"Workload API + token-fetcher<br/>hint: dsx-dpu"| I2
    HOST["Bare-metal host"] <-->|"DSX IMDS v1 via DPU metadata-service"| I2
    P2["Kubernetes Pod"] <-->|"Workload API + token-fetcher"| I2
    VM["KubeVirt VM"] <-->|"DSX IMDS v1 + Exchange Proxy CSI"| I2
  end

  I2 <-->|"Exchange public keys by kid"| BUS

```

k1 and k2 are separate SPIRE trust domains. Pods, processes, hosts, and VMs use
the SPIRE server and Exchange in their local shard. Cross-shard trust starts
with a DSX JWT and the public keys distributed through the Event Bus.

The local lifecycle places the Event Bus, OAuth2 provider, and OpenBao test
deployment in k1. The k2 Exchange reaches the OAuth2 and MQTT endpoints in k1.
The OpenBao route is used by the relying-party test.

A **SPIRE JWT-SVID** is the cluster-local credential fetched through a SPIRE
Workload API. A **DSX JWT** is the token returned by RFC 8693 Exchange.

## Flat Exchange key distribution

```mermaid
flowchart TB
  RP["External relying party"]
  E1["Exchange Deployment<br/>shard-a"]
  E2["Exchange Deployment<br/>shard-b"]
  E3["Exchange Deployment<br/>shard-c"]
  BUS["External DSX Event Bus<br/>one retained key topic space"]

  RP -->|"one aggregate JWKS"| E2
  E1 <-->|"local keys out, all public keys in"| BUS
  E2 <-->|"local keys out, all public keys in"| BUS
  E3 <-->|"local keys out, all public keys in"| BUS
```

All Exchanges attached to an Event Bus share one flat retained topic space.
Common Services and Data Hall are deployment placement labels. The protocol
topology remains flat. Every Exchange has a globally unique `exchange.shardID`
and publishes the public keys for its own shard at
`spiffe-exchange/v1/pub-keys/shard/<shard-id>/kid/<kid>`. It subscribes to
`spiffe-exchange/v1/pub-keys/shard/+/kid/+`. It retains every valid local and
remote public key in one aggregate keyed by globally unique `kid` and serves
that aggregate from its JWKS endpoint.

Each replica has a unique MQTT client ID derived from its Pod name. A relying
party configures one reachable Exchange endpoint and receives the complete key
union from that aggregate JWKS. SPIRE, the Identity Controller, IMDS, and CSI
remain local to their shard. DSX JWTs and public keys cross shard boundaries.

### Identity delivery and E2E coverage

| Consumer | SPIRE plugins | Delivery | E2E coverage |
|---|---|---|---|
| k1 and k2 Kubernetes Pods | `k8s_psat` NodeAttestor and Kubernetes WorkloadAttestor | token-fetcher fetches a local SPIRE JWT-SVID, exchanges it, and writes the DSX JWT | Both shards, including k2 login to OpenBao through the Gateway |
| DPU process or Pod | external `dsx_dpu` NodeAttestor agent and server with a Unix or Kubernetes WorkloadAttestor | token-fetcher selects the local SPIRE JWT-SVID with hint `dsx-dpu`, then exchanges it for a DSX JWT | Controller registration lifecycle |
| Bare-metal host | external `dsx_dpu` NodeAttestor agent and server attest the DPU agent. Kubernetes WorkloadAttestor serves the DPU metadata-service. Repository `dsx_imds` NodeAttestor agent and server attest the physical host agent. Unix WorkloadAttestor serves host processes | DPU metadata-service exchanges a local host SPIRE JWT-SVID and serves the DSX JWT at `169.254.169.254`. The host agent submits that JWT for node attestation, then serves host processes | The k2 Pod fixture covers the software chain. Physical DPU-to-host link-local delivery is outside E2E |
| KubeVirt VM | `k8s_psat` and Kubernetes WorkloadAttestor bootstrap an E2E-only second agent. Repository `dsx_imds` NodeAttestor agent and server attest it. Kubernetes WorkloadAttestor serves the virt-launcher metadata-service | the attested second agent and its CSI driver supply the VM identity. Metadata-service exchanges through Exchange Proxy CSI. The guest calls IMDS | Real plugins, agents, CSI drivers, sidecars, Exchange, guest IMDS, and KubeVirt layer-3 UDN |

## Quick start

Required tools are Git, Docker, Kind, kubectl, Helm, Go 1.25+, Make, `jq`, `yq`,
`curl`, and `openssl`. Full VM E2E requires a supported Linux host with
privileged Docker and loadable `openvswitch`, `vport-geneve`, `nft_socket`,
`tun`, and `vhost_net` modules. WSL is unsupported. `virtctl` v1.7.4 is
optional. OpenBao commands use a host `bao` binary when available and otherwise
use the pinned container image.

Initialize source and chart dependencies, then run static validation:

```bash
git submodule update --init --recursive
make validate
```

Run the k1 smoke path:

```bash
make identity-deploy
make identity-verify
make identity-clean
```

`identity-clean` removes the identity release, its fixture, the SPIRE CRDs, and
Kyverno. The Kind cluster, `dsx-identity` namespace, signing Secret, Event Bus,
and OAuth2 provider remain available for another smoke run. `make clean`
deletes both Kind clusters and their state.

Run the complete two-cluster lifecycle:

```bash
make -j install
make test
make status
make clean
```

`make test` covers both Exchange paths, VM metadata, controller lifecycle,
cross-shard SVID rejection, OAuth2 retained publication, aggregate JWKS,
Exchange availability, OpenBao policy enforcement,
restart recovery, and key rotation overlap. Focused Go tests advance publication
lease time deterministically and verify lease expiry without delaying E2E.

The Go suite loads keys from five shards into three aggregates, rotates one
shard, and removes only the retired key after its publication lease expires. The in-process MQTT suite covers retained
replay, virtual-time refresh, and restart. The two-cluster suite covers
issuance, isolation, routing, IMDS, and OpenBao.

## Helm chart

`charts/dsx-identity` installs the shard-local SPIRE and Exchange resources. It
depends on the official SPIRE chart and renders Pod and VM identity support and
injection policies. The k2 profile also renders the Identity Controller.

For local E2E, `make install` separately deploys the SPIRE CRD chart, DSX Event
Bus, Keycloak, Kyverno, cluster networking, OpenBao, and fixtures. The Event Bus
is its own Helm release, and Keycloak provides OAuth2.

Cluster profiles `scripts/k1-values.yaml`, `scripts/k2-values.yaml`, and
`scripts/local-values.yaml` are local E2E inputs. Each deployed shard supplies
its own SPIRE trust domain, cluster name, globally unique Exchange shard ID,
and external Event Bus and OAuth2 endpoints. VM, UDN, nginx, DPU API, and
external-client resources are E2E fixtures. `make build-images-all` builds the
application and plugin images without deploying them.

## Identity contract

DSX JWT subjects use:

```text
spiffe://nca-<16-digits>.ds9.dsx.nvid.id/<dpu|host|vm>/<resource-id>
  [/process/<name>|/pod/<uuid>]
  [/role/<role>]
```

Cluster-local SPIRE JWT-SVID and `ClusterStaticEntry` IDs encode the same
components under the local trust domain:

```text
spiffe://<local-trust-domain>/tenant/<tenant>/service/dsx/<resource-type>/<resource-id>[/...]
```

Kubernetes Pods using token-fetcher injection provide these labels:

```text
spiffe.nvidia.com/tenant=nca-<16-digits>
spiffe.nvidia.com/service=dsx
spiffe.nvidia.com/resource-type=dpu|host|vm
spiffe.nvidia.com/resource-id=<resource-id>
```

The k2 management cluster runs a DSX Identity Controller. It watches
unstructured `provisioning.dpu.nvidia.com/v1alpha1, Kind=DPU` resources and uses
`metadata.name` as the DPU ID. The controller reads host and VM inputs from the
LaunchLayer source CR. The local fixture uses `LaunchLayerSourceFixture` until
that source API is available. An optional `role` becomes the final
`/role/<role>` suffix.

The source object has this shape:

```yaml
spec:
  type: vm                     # host or vm
  tenantID: nca-0000000000000001
  resourceID: vm-1234
  parentID: host-1234          # DPU ID for host, host ID for VM
  parentTenantID: nca-0000000000000001  # required only for VM
  role: database               # optional
  holder:
    namespace: tenant-tenant1
    serviceAccount: vm-identity-client
    podUID: <pod-uuid>         # optional
```

## Exchange and Event Bus

Every DSX JWT uses issuer `spiffe://ds9.dsx.nvid.id`. `/v1/exchange` accepts a
local SPIRE JWT-SVID and uses SVID validation as its trust boundary. Exchange
preserves claims such as `aud`, `iat`, and `exp`, then replaces `sub`, `iss`,
and the signature.

Each shard runs two active Exchange replicas and loads a static signing keyset
from a local Secret containing `signing-jwks.json` and `active-kid`. Private
keys stay in that Secret. Exchange derives the signing algorithm from each JWK
and advertises the algorithms present in its aggregate. Each public key is
retained at:

```text
spiffe-exchange/v1/pub-keys/shard/<shard-id>/kid/<kid>
```

Each publication carries its lease expiry and is refreshed every quarter of
`exchange.publicKeyLeaseTTL`. This leaves three further publish opportunities
before the previous lease expires. Configure the Event Bus retained-message `maxAge` to
match that TTL and cover the maximum DSX JWT lifetime plus relying-party
leeway. Keys are retained by globally unique `kid` and served at
`/.well-known/jwks.json`. Exchange preserves configured `kid` values, and each
valid retained publication refreshes the key it addresses.

`exchange.shardID` is globally unique across the Event Bus topic space and is a
required chart value. The local `k1` and `k2` values are fixture names.

The NVIDIA DSX Event Bus is pinned to `v2.6.0` in the `dsx-exchange` submodule.
Every Exchange authenticates with OAuth2 client credentials. Its MQTT ACL is
limited to `spiffe-exchange.>` publish and subscribe access. Rolling updates and
the PodDisruptionBudget keep one Exchange replica available.

## DSX IMDS host and VM flows

Hosts and VMs use the tokenless v1 request:

```http
GET /latest/meta-data/identity?aud=<audience>
Metadata: true
```

`GET /2009-04-04/meta-data/identity` is also accepted. Both routes require the
metadata header and reject proxy headers. The JSON response contains `token`,
`expires_in`, and `spiffe_id`. `Accept: text/plain` returns the exchanged JWT.

The table above describes the bare-metal host path. For the VM E2E, the runner
uses `kubectl exec` to start the request in a tenant-network client Pod. The
sequence begins there.

```mermaid
sequenceDiagram
  participant Client as Tenant Pod
  participant VM as VM guest
  participant Metadata as virt-launcher Pod<br/>metadata-service sidecar
  participant Agent as dsx_imds-attested<br/>SPIRE agent Pod
  participant Proxy as Exchange proxy (CSI Unix socket)
  participant Exchange as k2 Exchange

  Client->>VM: SSH: request DSX IMDS identity
  Note over VM,Metadata: dsx-imds-network-setup routes IMDS through the virt-launcher bridge
  VM->>Metadata: GET 169.254.169.254/.../identity?aud=...<br/>Metadata: true
  Metadata->>Agent: Fetch SPIRE JWT-SVIDs for audience
  Agent-->>Metadata: Cluster-local SPIRE JWT-SVIDs
  Note right of Metadata: Select exactly one<br/>hint: dsx-imds-vm
  Metadata->>Proxy: RFC 8693 with selected SPIRE JWT-SVID
  Proxy->>Exchange: Forward to local service
  Note right of Exchange: Validate with<br/>the k2 SPIRE JWT bundle
  Exchange-->>Proxy: k2-signed DSX JWT
  Proxy-->>Metadata: RFC 8693 response
  Metadata-->>VM: DSX IMDS response
  VM-->>Client: Return response over SSH
```

### k2 E2E `dsx_imds` bootstrap

k2 runs two SPIRE agent DaemonSets. The ordinary shard agent uses `k8s_psat`.
The official SPIRE chart's `upstream-spire-agent` alias is also enabled in
`scripts/k2-values.yaml` with `nameOverride: dsx-imds-agent`. It renders the
E2E-only `dsx-identity-dsx-imds-agent` DaemonSet, with one Kubernetes Pod on
each k2 node. This fixture is a Kubernetes Pod, not a VM or bare-metal host.

The second agent Pod contains a native `metadata-service` sidecar listening on
`http://127.0.0.1:18080`. Its `dsx_imds` agent plugin calls that endpoint. The
`dsx-imds-host` `ClusterSPIFFEID` registers a host SPIRE JWT-SVID for the Pod,
and `bootstrap-spiffe-csi` obtains it from the ordinary agent. The sidecar sends
the SPIRE JWT-SVID through Exchange Proxy CSI to the real k2 Exchange and
returns the resulting DSX JWT. The agent plugin submits that DSX JWT to the real
`dsx_imds` server plugin, which validates it before SPIRE issues the second
agent's node SVID.

```mermaid
flowchart TB
  subgraph Node["k2 node"]
    Ordinary["Ordinary SPIRE agent Pod<br/>k8s_psat"]
    BootstrapCSI["bootstrap-spiffe-csi<br/>csi.spiffe.io"]
    subgraph AgentPod["E2E-only dsx-identity-dsx-imds-agent Pod"]
      Agent["Second SPIRE agent"]
      Plugin["dsx_imds agent plugin"]
      BootstrapMetadata["metadata-service native sidecar<br/>127.0.0.1:18080"]
    end
    AttestedCSI["upstream-spiffe-csi<br/>upstream.csi.spiffe.io"]
    VirtLauncher["KubeVirt virt-launcher Pod<br/>metadata-service sidecar"]
    Guest["KubeVirt VM guest<br/>IMDS client"]
  end

  ExchangeProxy["Exchange Proxy CSI"]
  Exchange["k2 Exchange"]
  Server["k2 SPIRE server<br/>dsx_imds server plugin"]

  Ordinary -->|"host SPIRE JWT-SVID<br/>dsx-imds-host registration"| BootstrapCSI
  BootstrapCSI --> BootstrapMetadata
  Agent -->|"invoke NodeAttestor"| Plugin
  Plugin -->|"IMDS GET over loopback"| BootstrapMetadata
  BootstrapMetadata -->|"RFC 8693 over CSI socket"| ExchangeProxy
  ExchangeProxy --> Exchange
  Exchange -->|"DSX JWT"| BootstrapMetadata
  BootstrapMetadata -->|"DSX JWT"| Plugin
  Plugin -->|"attestation data"| Agent
  Agent -->|"submit DSX JWT"| Server
  Server -->|"node SVID after validation"| Agent
  Agent --> AttestedCSI
  AttestedCSI -->|"Workload API identity"| VirtLauncher
  Guest -->|"IMDS over KubeVirt layer-3 UDN"| VirtLauncher
```

The attested second agent and `upstream-spiffe-csi` provide the parent and
Workload API identity used by the virt-launcher metadata-service. The VM guest
is an IMDS client and has no SPIRE agent. Other k2 workloads remain on the
cluster Pod network. The layer-3 UDN belongs to the KubeVirt path.

Production uses an externally provisioned SPIRE agent on the physical
bare-metal host. Its `dsx_imds` plugin points to `169.254.169.254`, where the
DPU metadata-service serves IMDS. Host-agent provisioning is outside this
repository.

The E2E path runs the real metadata-service, Exchange, both `dsx_imds` plugin
halves, node attestation, second agent, CSI drivers, virt-launcher sidecar, and
VM IMDS request. Its host bootstrap substitutes same-Pod loopback and an
ordinary-agent CSI identity for the physical DPU-to-host link-local transport.
Coverage of that physical transport requires hardware integration testing.

In local E2E, the retained Event Bus publication carries the k2 public key to
the k1 Exchange. The runner then uses the VM's DSX JWT to exercise cross-shard
OpenBao login:

```mermaid
sequenceDiagram
  participant Runner as k2 E2E Pod
  participant Gateway as k1 Envoy Gateway<br/>OpenBao route, port 8200
  participant Bao as External OpenBao<br/>E2E deployment in k1
  participant JWKS as k1 Exchange aggregate JWKS

  Runner->>Gateway: JWT CEL login with VM DSX JWT
  Gateway->>Bao: Forward login request
  Bao->>JWKS: Fetch/cache aggregate JWKS
  JWKS-->>Bao: Full public keyset including k2 kid
  Bao-->>Gateway: OpenBao client token + configured policies
  Gateway-->>Runner: Return login response
  Runner->>Gateway: Read allowed and forbidden secrets
  Gateway->>Bao: Forward secret requests
  Bao-->>Gateway: Values or 403
  Gateway-->>Runner: Return responses
```

The runner submits the KubeVirt layer-3 UDN, controller sources, client, service
account, `LaunchLayerSourceFixture`, and VM before its readiness checks. The
external host-agent configuration is in
`scripts/k2/spire/dsx-imds-agent-plugin.hcl`.

## Signing-key rotation

Each `kid` is the go-jose SHA-256 public-JWK thumbprint and remains unique after
retirement. Keep private material in a restricted directory. The following
commands rotate the local k1 E2E shard:

```bash
umask 077
KEYSET_DIR=/secure/path/k1-key-rotation
install -d -m 0700 "${KEYSET_DIR}"
go run ./cmd/generate-signing-keyset > "${KEYSET_DIR}/generated.json"
jq -c '.jwks' "${KEYSET_DIR}/generated.json" > "${KEYSET_DIR}/signing-jwks.json"
jq -jr '.active_kid' "${KEYSET_DIR}/generated.json" > "${KEYSET_DIR}/active-kid"
kubectl create secret generic spiffe-exchange-signing-keyset -n dsx-identity \
  --context kind-k1 --from-file="${KEYSET_DIR}/signing-jwks.json" \
  --from-file="${KEYSET_DIR}/active-kid" --dry-run=client -o yaml \
  | kubectl apply --context kind-k1 -f -
kubectl rollout restart deployment/spiffe-exchange -n dsx-identity --context kind-k1
kubectl rollout status deployment/spiffe-exchange -n dsx-identity --context kind-k1
```

The new key publishes under its own `kid`. Old replicas stop refreshing the old
key's publication when the rolling update replaces them, so its lease expires
after `exchange.publicKeyLeaseTTL`. Reload relying-party remote-JWKS caches before
requiring immediate rejection of tokens signed by the retired key.
