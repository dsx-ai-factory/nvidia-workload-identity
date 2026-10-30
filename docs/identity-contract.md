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
| DPU | `global.operatorTenant` | `/dpu/<dpu-id>` |
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

DPU identities remain separate controller-derived static entries parented to
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
Controller owns SPIFFE IDs, selectors, hints, and class names.

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
routes the VM's link-local metadata traffic.
