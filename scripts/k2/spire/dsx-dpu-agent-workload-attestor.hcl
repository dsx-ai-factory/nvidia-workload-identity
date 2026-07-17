# External DPU agents need path discovery for the configured token-fetcher
# executable selector. The separately supplied dsx_dpu NodeAttestor is omitted.
WorkloadAttestor "unix" {
  plugin_data {
    discover_workload_path = true
  }
}
