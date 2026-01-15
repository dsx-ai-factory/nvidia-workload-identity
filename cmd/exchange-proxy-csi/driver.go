package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	Version = "0.1.0"
)

// Config is the configuration for the driver
type Config struct {
	NodeID     string
	PluginName string
	SocketDir  string
}

// Driver is the ephemeral-inline CSI driver implementation
type Driver struct {
	csi.UnimplementedIdentityServer
	csi.UnimplementedNodeServer

	nodeID     string
	pluginName string
	socketDir  string
}

// NewDriver creates a new driver with the given config
func NewDriver(config Config) (*Driver, error) {
	if config.NodeID == "" {
		return nil, errors.New("node ID is required")
	}
	if config.SocketDir == "" {
		return nil, errors.New("socket directory is required")
	}
	return &Driver{
		nodeID:     config.NodeID,
		pluginName: config.PluginName,
		socketDir:  config.SocketDir,
	}, nil
}

// Identity Server

func (d *Driver) GetPluginInfo(context.Context, *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	return &csi.GetPluginInfoResponse{
		Name:          d.pluginName,
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

func (d *Driver) NodePublishVolume(_ context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	ephemeralMode := req.GetVolumeContext()["csi.storage.k8s.io/ephemeral"]

	slog.Debug("NodePublishVolume",
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

	// Create target path
	if err := os.MkdirAll(req.TargetPath, 0755); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create target path: %v", err)
	}

	// Bind mount the socket directory
	if err := bindMount(d.socketDir, req.TargetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to bind mount: %v", err)
	}

	slog.Info("Volume published", "volume_id", req.VolumeId, "target_path", req.TargetPath)
	return &csi.NodePublishVolumeResponse{}, nil
}

func (d *Driver) NodeUnpublishVolume(_ context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	slog.Debug("NodeUnpublishVolume",
		"volume_id", req.VolumeId,
		"target_path", req.TargetPath)

	switch {
	case req.VolumeId == "":
		return nil, status.Error(codes.InvalidArgument, "volume id required")
	case req.TargetPath == "":
		return nil, status.Error(codes.InvalidArgument, "target path required")
	}

	if err := unmount(req.TargetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unmount: %v", err)
	}

	if err := os.Remove(req.TargetPath); err != nil && !os.IsNotExist(err) {
		return nil, status.Errorf(codes.Internal, "failed to remove target path: %v", err)
	}

	slog.Info("Volume unpublished", "volume_id", req.VolumeId)
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
