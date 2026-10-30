# NVIDIA Workload Identity

NVIDIA Workload Identity turns shard-local SPIRE identities into DSX JWTs that
relying parties can validate across shards. An identity shard contains an
independent SPIRE deployment and a co-located NVIDIA Workload Identity Keys
deployment. NVIDIA Workload Identity Keys validates only its local SPIRE
JWT-SVIDs, issues DSX JWTs, and distributes its public signing keys through
the external DSX Event Bus.

There is no SPIRE federation between shards. Cross-shard trust starts after
token exchange, when another shard or relying party validates a DSX JWT with
the aggregate public keys served by any NVIDIA Workload Identity Keys
instance.

## User guide

See the [examples user guide](../examples/README.md) to acquire and consume DSX
JWTs through token-fetcher injection or the link-local IMDS endpoint.

Component commands and runtime configuration:

- [NVIDIA Workload Identity Controller](../cmd/dsx-identity-controller/README.md)
- [DSX IMDS Network Setup](../cmd/dsx-imds-network-setup/README.md)
- [`dsx_imds` NodeAttestor agent plugin](../cmd/dsx-imds-node-attestor-agent/README.md)
- [`dsx_imds` NodeAttestor server plugin](../cmd/dsx-imds-node-attestor-server/README.md)
- [`dpu_hw` NodeAttestor server plugin](https://github.com/dsx-ai-factory/nvidia-workload-identity/blob/master/third-party/doca-platform-foundation/cmd/dpu-hw-server)
- [DSX Token Fetcher](../cmd/dsx-token-fetcher/README.md)
- [NVIDIA Workload Identity Keys Proxy CSI Driver](../cmd/dsx-identity-keys-proxy-csi/README.md)
- [Generate Signing Keyset](../cmd/generate-signing-keyset/README.md)
- [DSX IMDS](../cmd/metadata-service/README.md)
- [NVIDIA Workload Identity Keys](../cmd/dsx-identity-keys/README.md)
- [Kata UDS Proxy](../kata-uds-proxy/README.md)

### SPIRE upgrades

Before upgrading SPIRE, check its
[upgrade requirements](https://github.com/spiffe/spire/blob/main/doc/upgrading.md)
for supported version skew, upgrade order, and configuration changes.

## Deployment

Install [`charts/dsx-identity`](../charts/dsx-identity/README.md) once per identity
shard with Helm. When enabled, the parent chart creates DPF DPUService resources
that reference the [DSX IMDS](../charts/dsx-imds/README.md), [DSX SPIFFE
CSI](../charts/dsx-spiffe-csi/README.md), and [DPU token-fetcher
admission](../charts/dsx-token-fetcher/README.md) child charts.
