# DSX DPU NodeAttestor Server Plugin

`dsx-dpu-node-attestor-server` is the server half of SPIRE's custom `dpu_hw`
NodeAttestor. It receives the DPU serial attestation payload from the agent
plugin, validates it, and returns a reattestable SPIRE agent ID:

```text
spiffe://<trust-domain>/spire/agent/dpu_hw/<normalized-serial>
```

`Dockerfile.spire` embeds this binary and
`dsx-imds-node-attestor-server` in the DSX SPIRE server image.

SPIRE starts the binary through the plugin SDK. Configure it under
`customPlugins.nodeAttestor.dpu_hw` with `plugin_cmd` pointing at the embedded
binary path and an optional `plugin_checksum`. `plugin_data` is empty for the
current serial-based verifier. The external provisioning system owns the
serial-to-DPU association, and access to the SPIRE node-attestation service is
the boundary that controls which DPU may assert it.
