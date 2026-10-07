#!/bin/bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

rm -rf \
    "${SCRIPT_DIR}/secret-generators/identity-k1/generated" \
    "${SCRIPT_DIR}/secret-generators/identity-k2/generated" \
    "${SCRIPT_DIR}/k2/vm/generated"
