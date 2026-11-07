# DSX IMDS Helm chart

This chart renders the DaemonSet, Service, and ServiceAccount for [DSX
IMDS](../../cmd/metadata-service/README.md). The Service uses node-local traffic
so an identity request cannot be answered by another DPU node.

With the release name `dsx-imds` in the `dsx-identity` namespace, the stable
node-local Service endpoint is:

```text
http://dsx-imds.dsx-identity.svc.cluster.local
```

## Requirements and values

The parent [NVIDIA Workload Identity chart](../dsx-identity/README.md)
supplies the release version, image, selectors, and exchange URL. [DPF](https://github.com/NVIDIA/doca-platform) supplies
scheduling and ownership fields under `serviceDaemonSet`. A registered
`csi.spiffe.io` driver must expose the local DPU agent Workload API socket.

| Value | Purpose |
|---|---|
| `metadataService.identityKeysURL` | HTTPS exchange endpoint reachable from the DPU cluster |
| `metadataService.image` | Released metadata-service image |
| `metadataService.spireSocketPath` | Socket path mounted through [SPIFFE CSI](https://github.com/spiffe/spiffe-csi) |
| `spiffeCsiDriver` | Name of the existing SPIFFE CSI driver |
| `workload` | ServiceAccount and container names shared with controller-derived selectors |

Keep the `workload` values aligned with the parent chart. Changing them without
updating the controller selectors prevents DSX IMDS from selecting its
registered identity.

## Deploy

In the parent [NVIDIA Workload Identity chart](../dsx-identity/README.md),
`dpuService.imds.enabled` defaults to `true`. The parent chart renders the
[`DPUService`](../dsx-identity/templates/dpu-service.yaml), and DPF installs
this chart into selected DPU clusters.
