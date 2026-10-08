# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Example tenant policy
path "secret/data/tenant/operator-1/*" {
  capabilities = ["read"]
}
