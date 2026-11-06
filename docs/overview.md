# NVIDIA Workload Identity

NVIDIA Workload Identity provides hardware-backed identities for bare-metal
hosts, KubeVirt VMs, Kubernetes Pods, and workloads in Kata Containers. Each
workload receives a unique identity containing its tenant, service, and resource
identity.

A workload presents a signed identity credential to a relying party, which
verifies the credential to authenticate the caller. For example, a secrets
manager can authenticate a VM's tenant identity before applying its rules for
access to a requested secret.

Identities build from the operator-owned DPU, which runs in zero trust mode. The
DPU establishes the identity of its paired bare-metal host. The attested host
provides identities to KubeVirt VMs and Kubernetes Pods. Pods running in Kata
Containers obtain their identities through a proxy that carries the host's SPIRE
Workload API across the guest VM boundary.

DSX data halls run independent SPIRE deployments, each attesting local nodes and
workloads and issuing signed identity credentials called SVIDs. NVIDIA Workload
Identity Keys exchanges local SPIRE JWT-SVIDs for DSX JWT-SVIDs and distributes
public signing keys through the DSX Event Bus, so relying parties can
authenticate workloads across data halls.

Kubernetes and DPUService Pods receive DSX JWT-SVIDs through an injected
token-fetcher sidecar. KubeVirt VMs request DSX JWT-SVIDs from DSX IMDS. Paired
bare-metal hosts use DSX JWT-SVIDs from DPU-hosted DSX IMDS to attest their SPIRE
agents. See the
[user guide](../examples/README.md) for requests and verification examples.

See [Architecture](architecture.md) for identity delivery and the
[Identity contract](identity-contract.md) for subject formats and ownership.

## Deployment

Install the [NVIDIA Workload Identity Helm chart](../charts/dsx-identity/README.md)
once per identity shard. The chart README covers prerequisites, configuration,
and optional DPF integration, including the [DSX IMDS](../charts/dsx-imds/README.md)
and [DPU token-fetcher admission](../charts/dsx-token-fetcher/README.md) child charts.

## Components

- [NVIDIA Workload Identity Controller](../cmd/dsx-identity-controller/README.md)
- [DSX IMDS Network Setup](../cmd/dsx-imds-network-setup/README.md)
- [`dsx_imds` NodeAttestor agent plugin](../cmd/dsx-imds-node-attestor-agent/README.md)
- [`dsx_imds` NodeAttestor server plugin](../cmd/dsx-imds-node-attestor-server/README.md)
- [`dpu_hw` NodeAttestor server plugin](https://github.com/NVIDIA/doca-platform/tree/545320feb894f20577fa0abf6885c6aafc4fb2eb/cmd/dpu-hw-server)
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
