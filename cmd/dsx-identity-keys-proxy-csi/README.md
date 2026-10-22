# NVIDIA Workload Identity Keys Proxy CSI Driver

`dsx-identity-keys-proxy-csi` is a node-local CSI driver for read-only
ephemeral inline volumes. It bind mounts the directory containing the local
NVIDIA Workload Identity Keys Proxy Unix socket into a consumer Pod.

```mermaid
flowchart LR
    Proxy["Node-local<br/>NVIDIA Workload Identity<br/>Keys Proxy container"] -->|"identity-keys.sock"| SocketDir["Node-local socket directory"]
    Kubelet["Kubernetes kubelet process"] -->|"NodePublishVolume"| Driver["dsx-identity-keys-proxy-csi DaemonSet Pod"]
    Driver -->|"bind mount for read-only CSI request"| Consumer["virt-launcher DSX IMDS sidecar"]
    SocketDir --> Driver
    Consumer -->|"HTTP over Unix socket"| Proxy
    Proxy -->|"verified TLS"| IdentityKeys["Shard-local<br/>NVIDIA Workload Identity<br/>Keys"]
```

The identity chart deploys the driver, node-driver-registrar, and proxy in
`charts/dsx-identity/templates/vm-support.yaml`. The VM DSX IMDS sidecar uses
the mounted socket to reach its local NVIDIA Workload Identity Keys.

## Flags

| Flag | Default | Purpose |
| --- | --- | --- |
| `--node-id` | empty | Explicit CSI node ID |
| `--csi-socket-path` | `/dsx-identity-keys-proxy-csi/csi.sock` | CSI gRPC Unix socket |
| `--socket-dir` | `/run/dsx` | Directory exposed to ephemeral volumes |
| `--log-level` | `info` | JSON log level: `debug`, `info`, `warn`, or `error` |

The driver supports only read-only ephemeral volumes. It requires Linux bind
mount access to kubelet's Pod volume tree and runs privileged in the supplied
DaemonSet.
