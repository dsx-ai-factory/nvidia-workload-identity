# DSX token-fetcher admission Helm chart

Installs the Kubernetes admission policy that injects `dsx-token-fetcher` into
annotated DPU service Pods.

## Prerequisites

- Kubernetes 1.36 or later
- A `csi.spiffe.io` driver backed by the DPU cluster SPIRE agent
- A DSX Identity Keys exchange endpoint reachable from DPU service Pods

## Configuration

```yaml
tokenFetcher:
  identityKeysURL: https://identity-keys.example.com/v1/exchange
  spiffeCsiDriver: csi.spiffe.io
  # DPF's CSI mount preserves the DPU agent socket filename.
  spireSocketPath: /var/run/secrets/spire.io/agent.sock
```

The policy mutates Pod creation requests with this annotation:

```yaml
metadata:
  annotations:
    spiffe.nvidia.com/inject-dsx-token-fetcher: "true"
    spiffe.nvidia.com/token-audience: spiffe://ds9.dsx.nvid.id/openbao
```

The audience annotation is required on every selected Pod. It names the relying
party for the local SPIRE JWT-SVID and is preserved in the exchanged DSX JWT.

It adds the token fetcher as a native sidecar, mounts the SPIFFE Workload API
only into that sidecar, and mounts the exchanged token read-only into every
application container at `/run/secrets/nvidia.com/dsx/token`.

The parent [`dsx-identity`](../dsx-identity/README.md) chart publishes this
payload through a standalone DPF `DPUService`. Do not add it to a
`DPUDeployment`, because a policy-only service in that lifecycle prevents
`DPUNodeMaintenance` from completing.
