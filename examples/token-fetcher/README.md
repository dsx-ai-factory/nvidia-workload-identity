# Token-fetcher user guide

The injected `dsx-token-fetcher` sidecar exchanges the workload's local SPIRE
JWT-SVID for a DSX JWT-SVID and refreshes the token file before expiry. NVIDIA
Workload Identity must have Pod token fetching enabled.

## Kubernetes Pods

Add the service label and token-fetcher annotations to the Pod template:

```yaml
spec:
  template:
    metadata:
      labels:
        spiffe.nvidia.com/service: dsx
      annotations:
        spiffe.nvidia.com/inject-dsx-token-fetcher: "true"
        spiffe.nvidia.com/token-audience: spiffe://ds9.dsx.nvid.id/openbao
```

Use the relying party's audience. For a Pod, put the labels and annotations
directly under `metadata`.

Application containers receive the token through a read-only mount:

```text
/run/secrets/nvidia.com/dsx/token
```

Apply [`non-dpf-pod.yaml`](non-dpf-pod.yaml) and wait for the example token
reader to become ready:

```bash
kubectl apply -f examples/token-fetcher/non-dpf-pod.yaml
kubectl wait --for=condition=Ready pod/dsx-token-fetcher-example --timeout=2m
kubectl exec dsx-token-fetcher-example -- \
  test -s /run/secrets/nvidia.com/dsx/token
```

## DPUService Pods

Configure the external [DPF identity templates](../../charts/dsx-identity/README.md#dpf-identity-configuration)
and opt the DPUService into SPIFFE registration. The example packages a
token-fetcher-enabled DaemonSet in the
[`dsx-token-fetcher-example`](../../charts/dsx-token-fetcher-example/README.md) chart:

```yaml
spec:
  security:
    spiffe: {}
  helmChart:
    values:
      audience: spiffe://ds9.dsx.nvid.id/openbao
```

Set the audience and pin the chart version for persistent environments, then
apply [`dpu-service.yaml`](dpu-service.yaml):

```bash
kubectl apply -f examples/token-fetcher/dpu-service.yaml
```

## Consume the token file

Cache the token and watch `/run/secrets/nvidia.com/dsx`, not the token file
inode. Refresh replaces the file atomically. Reread on directory changes and on
a polling interval in case a change event is lost:

```text
path = "/run/secrets/nvidia.com/dsx/token"
token = read(path)

loop:
  wait for a parent-directory change or polling timeout
  if next = read(path):
    atomically replace cached token with next
  else:
    clear cached token
```

The fetcher preserves an unexpired token after a failed refresh and removes it
after expiry. Treat an empty cache as an authentication failure. See [Consume a
DSX JWT-SVID](../README.md#consume-a-dsx-jwt-svid) to use the token.
