# Exchange Proxy CSI Driver

`exchange-proxy-csi` is a node-local CSI driver for read-only ephemeral inline
volumes. It bind mounts the directory containing the local Exchange proxy Unix
socket into a consumer Pod.

```mermaid
flowchart LR
    Proxy["Node-local Exchange proxy container"] -->|"exchange.sock"| SocketDir["Node-local socket directory"]
    Kubelet["Kubernetes kubelet process"] -->|"NodePublishVolume"| Driver["exchange-proxy-csi DaemonSet Pod"]
    Driver -->|"bind mount for read-only CSI request"| Consumer["virt-launcher metadata-service sidecar"]
    SocketDir --> Driver
    Consumer -->|"HTTP over Unix socket"| Proxy
    Proxy -->|"TCP"| Exchange["Local Exchange Service"]
```

The umbrella chart deploys the driver, node-driver-registrar, and proxy in
`charts/dsx-identity/templates/vm-support.yaml`. The VM metadata-service uses
the mounted socket to reach its local Exchange.

## Flags

| Flag | Default | Purpose |
| --- | --- | --- |
| `--node-id` | empty | Explicit CSI node ID |
| `--node-id-env` | `MY_NODE_NAME` | Environment variable used when `--node-id` is empty |
| `--csi-socket-path` | `/exchange-proxy-csi/csi.sock` | CSI gRPC Unix socket |
| `--plugin-name` | `csi.exchange.nvidia.com` | Registered CSI driver name |
| `--socket-dir` | `/run/exchange-proxy` | Directory exposed to ephemeral volumes |
| `--log-level` | `info` | JSON log level: `debug`, `info`, `warn`, or `error` |

The driver supports only read-only ephemeral volumes. It requires Linux bind
mount access to kubelet's Pod volume tree and runs privileged in the supplied
DaemonSet.

## Testing

```bash
go test ./cmd/exchange-proxy-csi
```

The focused tests cover idempotent publish and unpublish calls plus serialized
mount transitions. The KubeVirt E2E path validates CSI registration, socket
mounting, and metadata-service access to Exchange.
