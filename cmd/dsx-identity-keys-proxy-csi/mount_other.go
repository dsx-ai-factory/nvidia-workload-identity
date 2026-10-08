// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package main

import "errors"

func bindMount(source, target string) error {
	return errors.New("bind mount not supported on this platform")
}

func unmount(target string) error {
	return errors.New("unmount not supported on this platform")
}
