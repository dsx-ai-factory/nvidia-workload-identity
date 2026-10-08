// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	Version    = "0.1.0"
	PluginName = "csi.dsx-identity-keys.nvidia.com"
)

// Driver provides the ephemeral inline CSI service.
type Driver struct {
	csi.UnimplementedIdentityServer
	csi.UnimplementedNodeServer

	nodeID    string
	socketDir string
	mountOps  mountOps
	// Kubelet may issue duplicate publish and unpublish RPCs concurrently.
	// Serialize each external mount-state transition so retries are idempotent.
	mountTransitions sync.Mutex
}

type mountOps interface {
	Mount(string, string) error
	Unmount(string) error
	SameFile(string, string) (bool, error)
}

type systemMounts struct{}

func (systemMounts) Mount(source, target string) error {
	return bindMount(source, target)
}

func (systemMounts) Unmount(target string) error {
	return unmount(target)
}

func (systemMounts) SameFile(source, target string) (bool, error) {
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return false, err
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		return false, err
	}
	return os.SameFile(sourceInfo, targetInfo), nil
}

// Identity Server

func (d *Driver) GetPluginInfo(context.Context, *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	return &csi.GetPluginInfoResponse{
		Name:          PluginName,
		VendorVersion: Version,
	}, nil
}

func (d *Driver) GetPluginCapabilities(context.Context, *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	return &csi.GetPluginCapabilitiesResponse{}, nil
}

func (d *Driver) Probe(context.Context, *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	return &csi.ProbeResponse{}, nil
}

// Node Server

func (d *Driver) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	ephemeralMode := req.GetVolumeContext()["csi.storage.k8s.io/ephemeral"]

	slog.DebugContext(ctx, "NodePublishVolume",
		"volume_id", req.VolumeId,
		"target_path", req.TargetPath,
		"ephemeral", ephemeralMode)

	// Validate request
	switch {
	case req.VolumeId == "":
		return nil, status.Error(codes.InvalidArgument, "volume id required")
	case req.TargetPath == "":
		return nil, status.Error(codes.InvalidArgument, "target path required")
	case req.VolumeCapability == nil:
		return nil, status.Error(codes.InvalidArgument, "volume capability required")
	case !req.Readonly:
		return nil, status.Error(codes.InvalidArgument, "volume must be read-only")
	case ephemeralMode != "true":
		return nil, status.Error(codes.InvalidArgument, "only ephemeral volumes supported")
	}
	d.mountTransitions.Lock()
	defer d.mountTransitions.Unlock()

	if err := os.MkdirAll(req.TargetPath, 0755); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create target path: %v", err)
	}
	same, err := d.mountOps.SameFile(d.socketDir, req.TargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to inspect target path: %v", err)
	}
	if same {
		return &csi.NodePublishVolumeResponse{}, nil
	}

	if err := d.mountOps.Mount(d.socketDir, req.TargetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to bind mount: %v", err)
	}

	slog.InfoContext(ctx, "Volume published", "volume_id", req.VolumeId, "target_path", req.TargetPath)
	return &csi.NodePublishVolumeResponse{}, nil
}

func (d *Driver) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	slog.DebugContext(ctx, "NodeUnpublishVolume",
		"volume_id", req.VolumeId,
		"target_path", req.TargetPath)

	switch {
	case req.VolumeId == "":
		return nil, status.Error(codes.InvalidArgument, "volume id required")
	case req.TargetPath == "":
		return nil, status.Error(codes.InvalidArgument, "target path required")
	}
	d.mountTransitions.Lock()
	defer d.mountTransitions.Unlock()

	if err := d.mountOps.Unmount(req.TargetPath); err != nil && !os.IsNotExist(err) {
		return nil, status.Errorf(codes.Internal, "failed to unmount: %v", err)
	}

	if err := os.Remove(req.TargetPath); err != nil && !os.IsNotExist(err) {
		return nil, status.Errorf(codes.Internal, "failed to remove target path: %v", err)
	}

	slog.InfoContext(ctx, "Volume unpublished", "volume_id", req.VolumeId)
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (d *Driver) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_VOLUME_CONDITION,
					},
				},
			},
		},
	}, nil
}

func (d *Driver) NodeGetInfo(context.Context, *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId:            d.nodeID,
		MaxVolumesPerNode: 0,
	}, nil
}

func (d *Driver) NodeGetVolumeStats(_ context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	abnormal := false
	message := "mounted"

	if _, err := os.ReadDir(req.VolumePath); err != nil {
		abnormal = true
		message = fmt.Sprintf("failed to read volume path: %v", err)
	}

	return &csi.NodeGetVolumeStatsResponse{
		VolumeCondition: &csi.VolumeCondition{
			Abnormal: abnormal,
			Message:  message,
		},
	}, nil
}
