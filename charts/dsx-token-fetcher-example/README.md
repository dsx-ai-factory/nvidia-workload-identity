# DSX token-fetcher example chart

This DPUService chart deploys one service DaemonSet. It applies DPF's injected
labels, annotations, node selector, update strategy, and resources to that
DaemonSet. The Pod requests token-fetcher injection with its relying-party
audience and becomes ready after the fetcher writes a token to the shared
volume.

Apply the example DPUService in
[`examples/token-fetcher/dpu-service.yaml`](../../examples/token-fetcher/dpu-service.yaml).
