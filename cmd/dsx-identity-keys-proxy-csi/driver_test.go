// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
)

type fakeMounts struct {
	records struct {
		sync.Mutex
		mounted      map[string]string
		mountCalls   int
		unmountCalls int
	}
	mountStarted chan struct{}
	mountRelease chan struct{}
}

func newFakeMounts() *fakeMounts {
	mounts := new(fakeMounts)
	mounts.records.mounted = make(map[string]string)
	return mounts
}

func (m *fakeMounts) Mount(source, target string) error {
	if m.mountStarted != nil {
		close(m.mountStarted)
		<-m.mountRelease
	}
	m.records.Lock()
	defer m.records.Unlock()
	m.records.mounted[target] = source
	m.records.mountCalls++
	return nil
}

func (m *fakeMounts) Unmount(target string) error {
	m.records.Lock()
	defer m.records.Unlock()
	delete(m.records.mounted, target)
	m.records.unmountCalls++
	return nil
}

func (m *fakeMounts) SameFile(source, target string) (bool, error) {
	m.records.Lock()
	defer m.records.Unlock()
	return m.records.mounted[target] == source, nil
}

func (m *fakeMounts) calls() (mounts, unmounts int) {
	m.records.Lock()
	defer m.records.Unlock()
	return m.records.mountCalls, m.records.unmountCalls
}

func testDriver(t *testing.T, mounts *fakeMounts) (*Driver, *csi.NodePublishVolumeRequest) {
	t.Helper()
	socketDir := t.TempDir()
	targetPath := filepath.Join(t.TempDir(), "target")
	driver := &Driver{socketDir: socketDir, mountOps: mounts}
	request := &csi.NodePublishVolumeRequest{
		VolumeId:         "volume",
		TargetPath:       targetPath,
		VolumeCapability: &csi.VolumeCapability{},
		Readonly:         true,
		VolumeContext:    map[string]string{"csi.storage.k8s.io/ephemeral": "true"},
	}
	return driver, request
}

func TestNodePublishVolumeIsIdempotent(t *testing.T) {
	mounts := newFakeMounts()
	driver, request := testDriver(t, mounts)
	for range 2 {
		if _, err := driver.NodePublishVolume(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	mountCalls, _ := mounts.calls()
	if mountCalls != 1 {
		t.Fatalf("mount calls = %d, want 1", mountCalls)
	}
}

func TestNodeUnpublishVolumeIsIdempotent(t *testing.T) {
	mounts := newFakeMounts()
	driver, publish := testDriver(t, mounts)
	if _, err := driver.NodePublishVolume(context.Background(), publish); err != nil {
		t.Fatal(err)
	}
	unpublish := &csi.NodeUnpublishVolumeRequest{VolumeId: publish.VolumeId, TargetPath: publish.TargetPath}
	for range 2 {
		if _, err := driver.NodeUnpublishVolume(context.Background(), unpublish); err != nil {
			t.Fatal(err)
		}
	}
	_, unmountCalls := mounts.calls()
	if unmountCalls != 2 {
		t.Fatalf("unmount calls = %d, want 2", unmountCalls)
	}
	if _, err := os.Stat(publish.TargetPath); !os.IsNotExist(err) {
		t.Fatalf("target path remains after unpublish: %v", err)
	}
}

func TestNodePublishAndUnpublishAreSerialized(t *testing.T) {
	mounts := newFakeMounts()
	mounts.mountStarted = make(chan struct{})
	mounts.mountRelease = make(chan struct{})
	driver, publish := testDriver(t, mounts)
	publishDone := make(chan error, 1)
	go func() {
		_, err := driver.NodePublishVolume(context.Background(), publish)
		publishDone <- err
	}()
	<-mounts.mountStarted

	unpublishStarted := make(chan struct{})
	unpublishDone := make(chan error, 1)
	go func() {
		close(unpublishStarted)
		_, err := driver.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{
			VolumeId: publish.VolumeId, TargetPath: publish.TargetPath,
		})
		unpublishDone <- err
	}()
	<-unpublishStarted
	close(mounts.mountRelease)
	if err := <-publishDone; err != nil {
		t.Fatal(err)
	}
	if err := <-unpublishDone; err != nil {
		t.Fatal(err)
	}
	mountCalls, unmountCalls := mounts.calls()
	if mountCalls != 1 || unmountCalls != 1 {
		t.Fatalf("mount calls = %d, unmount calls = %d, want 1 each", mountCalls, unmountCalls)
	}
}
