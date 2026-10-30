# Identity contract

DSX JWT subjects use complete key/value pairs with unique keys:

```text
spiffe://<tenant>.<region>.<service>.<partition>/<key>/<value>[/<key>/<value>...]
spiffe://<operator-tenant>.<region>.<service>.<partition>/bm/<node-name>/pod/<pod-uuid>
  [/role/<role>]
```

DPU service identities use:

```text
spiffe://<operator-tenant>.<region>.<service>.<partition>/dpu/<serial>/dpuservice/<service-id>/namespace/<namespace>
```

Exchange preserves path keys and values exactly. For example:

```text
local:     spiffe://<local-domain>/tenant/<tenant>/service/dsx/cluster/cluster-1/namespace/team-a/workload/api
exchanged: spiffe://<tenant>.<region>.dsx.<partition>/cluster/cluster-1/namespace/team-a/workload/api
```

Path keys and values retain their case and order. Each segment starts with an
ASCII letter or digit, followed by ASCII letters, digits, `.`, `_`, or `-`.

Cluster-local SPIRE identities encode the same fields under the shard trust
domain. Exchange takes the region and partition from explicit configuration,
the tenant and service from the local subject, and preserves the resource path.

```text
spiffe://<local-trust-domain>/tenant/<tenant>/service/<service>/<key>/<value>[/<key>/<value>...]
```

| Resource | Tenant source | Subject resource path |
|---|---|---|
| DPU | Operator tenant in DPF identity templates | `/dpu/<normalized-serial>` resource prefix |
| DPU-paired operator bare-metal host | `global.operatorTenant` | `/bm/<dpu-machine-id>` |
| Operator bare-metal Kubernetes worker | `global.operatorTenant` | `/bm/<node-name>` |
| Kubernetes Pod | `global.operatorTenant` | `/bm/<node-name>/pod/<pod-uid>` |
| Bare-metal host with a Launch Layer source | `launchlayer.nvidia.com/org-id` annotation | `/bm/<bm-id>` |
| VM with a Launch Layer source | `launchlayer.nvidia.com/org-id` annotation | `/vm/<vm-id>` |

Every Kubernetes worker is an operator-owned bare-metal host. There is no
configured worker list and no LaunchLayer source for these hosts. The Pod
registration template derives the host ID directly from the Kubernetes node
name. The DPU source supplies the BM resource ID through its
`carbide.nvidia.com/dpu-machine-id` label.

Every DSX Kubernetes Pod supplies its service label. SPIRE uses its value for
the service path segment. The fixed parent template identifies the worker's
operator-tenant `dsx_imds` agent.

```yaml
metadata:
  labels:
    spiffe.nvidia.com/service: dsx
    spiffe.nvidia.com/role: database  # optional
```

```yaml
spire:
  spire-server:
    controllerManager:
      extraEnv:
        - name: OPERATOR_TENANT
          value: operator-1
      parentIDTemplate: 'spiffe://{{ .TrustDomain }}/spire/agent/dsx_imds/tenant/$OPERATOR_TENANT/bm/{{ .NodeMeta.Name }}'
```

The chart requires the controller-manager value to equal
`global.operatorTenant`. Because it is part of the SPIRE Pod template, changing
both values causes the StatefulSet to roll out without a restart hook.

The paired host identity is a controller-derived static entry parented to
the external `dpu_hw` agent identified by the normalized DPU serial. DPUs are
Kubernetes nodes in their Kamaji clusters. The DPU agent's Kubernetes
WorkloadAttestor identifies the IMDS Pod, and SPIFFE CSI exposes the agent's
Workload API socket to that Pod.

The [NVIDIA Workload Identity
Controller](../cmd/dsx-identity-controller/README.md) derives SPIRE registration
entries from DPU and Launch Layer sources.

```yaml
apiVersion: launchlayer.nvidia.com/v1alpha1
kind: VirtualInstance
metadata:
  annotations:
    launchlayer.nvidia.com/org-id: tenant-2
spec:
  instance_id: vm-1234
  node_name: host-1234
status:
  pod_uid: <virt-launcher-pod-uid>
```

`BareMetalInstance` uses the same tenant annotation, `spec.instance_id`, and
`status.primary_dpu.machine_id` plus `status.primary_dpu.serial_number`. The
configured operator tenant determines a VM entry's worker parent. The Identity
Controller owns these entries' SPIFFE IDs, selectors, hints, and class names.

## DPF identity configuration

DPF registers the DPU Agent process and opted-in DPUService workloads beneath
the DPU's `spiffe://<local-trust-domain>/spire/agent/dpu_hw/<normalized-serial>`
agent ID. These workload subjects use the `/dpu/<normalized-serial>` resource
prefix. The paired host's `/bm/<dpu-machine-id>` identity is a separate entry
owned by the NVIDIA Workload Identity Controller. A complete tenant
`BareMetalInstance` replaces that operator host entry with
`/bm/<spec.instance_id>` under the assigned tenant.

Configure DPF's identity templates in the external `DPFOperatorConfig` before
provisioning DPUs. The local templates must include the tenant and service
segments required by NVIDIA Workload Identity Keys. This fragment shows
operator tenant `operator-1`, service `dsx`, region `ds9`, and partition
`nvid.id`:

```yaml
spec:
  security:
    spiffe:
      dpuAgentSPIFFEIDTemplate: 'spiffe://{{ .TrustDomain }}/tenant/operator-1/service/dsx/dpu/{{ .SerialNumber }}/process/dpu-agent'
      dpuAgentExchangedSPIFFEIDTemplate: 'spiffe://operator-1.ds9.dsx.nvid.id/dpu/{{ .SerialNumber }}/process/dpu-agent'
      dpuServiceSPIFFEIDTemplate: 'spiffe://{{ .TrustDomain }}/tenant/operator-1/service/dsx/dpu/{{ .SerialNumber }}/dpuservice/{{ .ServiceID }}/namespace/{{ .Namespace }}'
      dpuServiceExchangedSPIFFEIDTemplate: 'spiffe://operator-1.ds9.dsx.nvid.id/dpu/{{ .SerialNumber }}/dpuservice/{{ .ServiceID }}/namespace/{{ .Namespace }}'
      tokenExchangeEndpoint: https://identity-keys.shard.example.com/v1/exchange
```

Match the tenant to `global.operatorTenant` and the service, region, and
partition to `global.dsx.defaultServiceName`, `global.dsx.region`, and
`global.dsx.partition`. DPF supplies the normalized serial,
namespace, and service ID. The DPU Agent's helper uses `tokenExchangeEndpoint`
before writing its Kubernetes API token. Its exchanged subject template must
match the token subject because DPF uses it for the DPU Agent's RoleBinding.
DPUService token-fetcher sidecars use the exchange URL configured in the
identity chart. DPF validates the DPUService exchanged subject template but
does not consume that subject itself.

Set the remaining DPF SPIFFE fields, provide the trust bundle, and configure
Kubernetes API authentication as described in the
[DPF SPIFFE guide](https://github.com/NVIDIA/doca-platform/blob/f1b50e85122a3df6047708e41a8203f8cdce03a1/docs/public/advanced-configuration/spiffe-identity.md).

## NVIDIA Workload Identity Keys and Event Bus

[NVIDIA Workload Identity Keys](../cmd/dsx-identity-keys/README.md) validates
local SPIRE JWT-SVIDs and issues DSX JWTs. By default it publishes public
signing keys to the Event Bus and serves aggregate JWKS. With NATS disabled it
serves only local public signing keys. The external Gateway terminates TLS and
applies source-aware request quotas. Clients configure the Gateway HTTPS
endpoint.

## DSX IMDS host and VM flows

[DSX IMDS](../cmd/metadata-service/README.md) serves the tokenless host and VM
identity API. The [`dsx_imds` NodeAttestor
agent](../cmd/dsx-imds-node-attestor-agent/README.md) and [`dsx_imds`
NodeAttestor server](../cmd/dsx-imds-node-attestor-server/README.md) bootstrap
the host SPIRE agent. The [NVIDIA Workload Identity Keys Proxy CSI
Driver](../cmd/dsx-identity-keys-proxy-csi/README.md) carries VM exchange
requests, and [DSX IMDS Network Setup](../cmd/dsx-imds-network-setup/README.md)
routes the VM's metadata traffic to `169.254.169.254` over IPv4 and
`fd00:76b9::254` over IPv6. It configures the address families present in the
primary OVN allocation, including IPv4-only, IPv6-only, and dual-stack guests.
