# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# External DPU agents need path discovery for the configured token-fetcher
# executable selector. The separately supplied dsx_dpu NodeAttestor is omitted.
WorkloadAttestor "unix" {
  plugin_data {
    discover_workload_path = true
  }
}
