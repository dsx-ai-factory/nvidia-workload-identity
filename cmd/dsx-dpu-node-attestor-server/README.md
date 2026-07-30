# DSX DPU NodeAttestor Server Plugin

`dsx-dpu-node-attestor-server` is the server half of SPIRE's custom `dpu_hw`
NodeAttestor. It receives the DPU serial attestation payload from the agent
plugin, validates it, and returns a reattestable SPIRE agent ID:

```text
spiffe://<trust-domain>/spire/agent/dpu_hw/<normalized-serial>
```

This binary is built as a plugin-only image (`Dockerfile.plugin`) and copied
into the SPIRE Server pod via an initContainer, the same pattern as
`dsx-imds-node-attestor-server`.

SPIRE starts the binary through the plugin SDK. Configure it under
`customPlugins.nodeAttestor.dpu_hw` with `plugin_cmd` pointing at the copied
binary path and an optional `plugin_checksum`. `plugin_data` is empty for the
current plaintext verifier.
