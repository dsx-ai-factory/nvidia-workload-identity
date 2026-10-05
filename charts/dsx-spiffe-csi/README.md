# DSX SPIFFE CSI Helm chart

This chart renders the optional SPIFFE CSI node plugin DaemonSet, CSIDriver, and
ServiceAccount. It exposes the local DPU SPIRE agent Workload API socket to DSX
IMDS.

## Requirements and values

DPF supplies scheduling and ownership fields under `serviceDaemonSet`. Every
selected node must run the intended SPIRE agent and expose its socket at
`agentSocketPath`. The node plugin requires privileged mount access to the
kubelet Pod and plugin directories.

DPF's default integration bind mounts `/run/spire` and preserves `agent.sock`.
Keep `agentSocketPath` aligned with the host agent and the path consumed by DSX
IMDS.

| Value | Purpose |
|---|---|
| `pluginName` | Cluster-wide CSI driver name. It must match DSX IMDS and the parent chart |
| `agentSocketPath` | Canonical host path of the DPU agent socket |
| `kubeletPath` | Host kubelet state directory |
| `image` | Pinned SPIFFE CSI driver image |
| `nodeDriverRegistrar.image` | Pinned kubelet registrar image |

## Deploy

In the parent [DSX Identity chart](../dsx-identity/README.md), enable
`dpuService.spiffeCsi.enabled` only when the DPU platform does not provide
`csi.spiffe.io`. The parent chart renders the
[`DPUService`](../dsx-identity/templates/dpu-service.yaml), and DPF installs this
chart into selected DPU clusters.
