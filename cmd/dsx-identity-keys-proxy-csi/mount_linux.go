//go:build linux

package main

import (
	"syscall"
)

func bindMount(source, target string) error {
	return syscall.Mount(source, target, "", syscall.MS_BIND|syscall.MS_REC, "")
}

func unmount(target string) error {
	err := syscall.Unmount(target, 0)
	if err == syscall.EINVAL || err == syscall.ENOENT {
		return nil
	}
	return err
}
