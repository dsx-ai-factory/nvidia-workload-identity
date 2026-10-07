# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

add
| unique_by(.name, .version, .license, .source, .text)
| group_by(.text)
| .[]
| "========================================================================\n"
  + (map(.name + " " + .version + " (" + .license + ")\n" + .source) | join("\n"))
  + "\n\n" + .[0].text
