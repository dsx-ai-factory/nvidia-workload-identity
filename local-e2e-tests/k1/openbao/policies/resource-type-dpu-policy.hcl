# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# DPU resource policy
path "secret/data/resource-type/dpu/*" {
  capabilities = ["read"]
}
