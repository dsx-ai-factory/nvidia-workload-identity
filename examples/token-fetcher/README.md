# Token-fetcher injection examples

The examples support both deployment paths:

- Apply [`non-dpf-pod.yaml`](non-dpf-pod.yaml) to test an ordinary Kubernetes
  Pod.
- Apply [`dpu-service.yaml`](dpu-service.yaml) to test DPUService SPIFFE
  registration and token-fetcher injection using the
  [`dsx-token-fetcher-example`](../../charts/dsx-token-fetcher-example) chart.

Both paths set the injection and audience annotations on the Pod template. The
Pod becomes ready only after the exchanged token exists at
`/run/secrets/nvidia.com/dsx/token`.
