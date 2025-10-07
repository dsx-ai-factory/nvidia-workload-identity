package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NodePublishVolume mounts the volume at the target path
func (d *Driver) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (_ *csi.NodePublishVolumeResponse, err error) {
	volumeID := req.GetVolumeId()
	targetPath := req.GetTargetPath()
	volumeContext := req.GetVolumeContext()
	ephemeralMode := volumeContext["csi.storage.k8s.io/ephemeral"]

	defer func() {
		if err != nil {
			slog.Error("Failed to publish volume", "volume_id", volumeID, "target_path", targetPath, "error", err)
		}
	}()

	// Validate request
	switch {
	case volumeID == "":
		return nil, status.Error(codes.InvalidArgument, "request missing required volume id")
	case targetPath == "":
		return nil, status.Error(codes.InvalidArgument, "request missing required target path")
	case req.VolumeCapability == nil:
		return nil, status.Error(codes.InvalidArgument, "request missing required volume capability")
	case req.VolumeCapability.AccessType == nil:
		return nil, status.Error(codes.InvalidArgument, "request missing required volume capability access type")
	case !isVolumeCapabilityPlainMount(req.VolumeCapability):
		return nil, status.Error(codes.InvalidArgument, "request volume capability access type must be a simple mount")
	case req.VolumeCapability.AccessMode == nil:
		return nil, status.Error(codes.InvalidArgument, "request missing required volume capability access mode")
	case ephemeralMode != "true":
		return nil, status.Error(codes.InvalidArgument, "only ephemeral volumes are supported")
	}

	// Get pod UID from volume context (provided by podInfoOnMount: true)
	podUID := volumeContext["csi.storage.k8s.io/pod.uid"]

	if podUID == "" {
		return nil, status.Error(codes.InvalidArgument, "missing pod UID in volume context")
	}

	slog.Info("Publishing volume",
		"volume_id", volumeID,
		"pod_uid", podUID,
		"target_path", targetPath)

	// Create volume directory using pod UID
	volumeDir := filepath.Join(d.socketsDir, podUID)
	if err := os.MkdirAll(volumeDir, 0755); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create volume directory: %v", err)
	}

	// Pre-create the socket file with permissions that allow the container to recreate it
	// socat will unlink and recreate this file when it starts
	socketPath := filepath.Join(volumeDir, "metadata-bridge-spire-agent.sock")
	socketFile, err := os.Create(socketPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create socket file: %v", err)
	}
	socketFile.Close()

	// Set socket file permissions to allow container user to unlink/recreate it
	if err := os.Chmod(socketPath, 0666); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to set socket file permissions: %v", err)
	}

	slog.Debug("Pre-created socket file", "path", socketPath)

	// Create target directory (required by CSI interface)
	if err := os.Mkdir(targetPath, 0755); err != nil && !os.IsExist(err) {
		return nil, status.Errorf(codes.Internal, "unable to create target path %q: %v", targetPath, err)
	}

	// Bind mount the volume directory to target path as read-write
	// The container needs write access to unlink/recreate the socket
	if err := bindMountRW(volumeDir, targetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "unable to mount %q: %v", targetPath, err)
	}

	slog.Info("Volume published successfully", "volume_id", volumeID, "target_path", targetPath)

	return &csi.NodePublishVolumeResponse{}, nil
}

// NodeUnpublishVolume unmounts the volume
func (d *Driver) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (_ *csi.NodeUnpublishVolumeResponse, err error) {
	volumeID := req.GetVolumeId()
	targetPath := req.GetTargetPath()

	defer func() {
		if err != nil {
			slog.Error("Failed to unpublish volume", "volume_id", volumeID, "target_path", targetPath, "error", err)
		}
	}()

	// Validate request
	switch {
	case volumeID == "":
		return nil, status.Error(codes.InvalidArgument, "request missing required volume id")
	case targetPath == "":
		return nil, status.Error(codes.InvalidArgument, "request missing required target path")
	}

	// Extract pod UID from target path: /var/lib/kubelet/pods/<pod-uid>/volumes/...
	podUID, err := extractPodUIDFromPath(targetPath)
	if err != nil {
		slog.Warn("Failed to extract pod UID from target path, will skip directory cleanup", "target_path", targetPath, "error", err)
		podUID = ""
	}

	slog.Info("Unpublishing volume", "volume_id", volumeID, "pod_uid", podUID, "target_path", targetPath)

	// Check if target is a valid mount and issue unmount request
	if ok, err := isMountPoint(targetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "unable to verify mount point %q: %v", targetPath, err)
	} else if ok {
		if err := unmount(targetPath); err != nil {
			return nil, status.Errorf(codes.Internal, "unable to unmount %q: %v", targetPath, err)
		}
	}

	// Check and remove the mount path if present, report an error otherwise
	if err := os.Remove(targetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, status.Errorf(codes.Internal, "unable to remove target path %q: %v", targetPath, err)
	}

	// Clean up volume directory using pod UID
	if podUID != "" {
		volumeDir := filepath.Join(d.socketsDir, podUID)
		if err := os.RemoveAll(volumeDir); err != nil {
			slog.Warn("Failed to remove volume directory", "path", volumeDir, "error", err)
		}
	}

	slog.Info("Volume unpublished successfully", "volume_id", volumeID)

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// NodeGetCapabilities returns node capabilities
func (d *Driver) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_VOLUME_CONDITION,
					},
				},
			},
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
					},
				},
			},
		},
	}, nil
}

// NodeGetInfo returns node information
func (d *Driver) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId: d.nodeID,
	}, nil
}

// NodeStageVolume is not supported for ephemeral volumes
func (d *Driver) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "NodeStageVolume is not supported")
}

// NodeUnstageVolume is not supported for ephemeral volumes
func (d *Driver) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "NodeUnstageVolume is not supported")
}

// NodeGetVolumeStats returns volume health status
func (d *Driver) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	volumeID := req.GetVolumeId()
	volumePath := req.GetVolumePath()

	volumeConditionAbnormal := false
	volumeConditionMessage := "mounted"
	if err := d.checkMount(volumePath); err != nil {
		volumeConditionAbnormal = true
		volumeConditionMessage = err.Error()
		slog.Error("Volume is unhealthy", "volume_id", volumeID, "volume_path", volumePath, "error", err)
	} else {
		slog.Info("Volume is healthy", "volume_id", volumeID, "volume_path", volumePath)
	}

	return &csi.NodeGetVolumeStatsResponse{
		VolumeCondition: &csi.VolumeCondition{
			Abnormal: volumeConditionAbnormal,
			Message:  volumeConditionMessage,
		},
	}, nil
}

// NodeExpandVolume is not supported
func (d *Driver) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "NodeExpandVolume is not supported")
}

// checkMount verifies the mount is healthy
func (d *Driver) checkMount(volumePath string) error {
	// Check whether or not it is a mount point
	if ok, err := isMountPoint(volumePath); err != nil {
		return fmt.Errorf("failed to determine root for volume path mount: %w", err)
	} else if !ok {
		return errors.New("volume path is not mounted")
	}
	// If a mount point, try to list files... this should fail if the mount is
	// broken for whatever reason
	if _, err := os.ReadDir(volumePath); err != nil {
		return fmt.Errorf("unable to list contents of volume path: %w", err)
	}
	return nil
}

// Helper functions for mount operations
func bindMountRW(source, target string) error {
	return unix.Mount(source, target, "", unix.MS_BIND, "")
}

func unmount(target string) error {
	return unix.Unmount(target, 0)
}

func isMountPoint(path string) (bool, error) {
	// Check if path exists
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	// Get parent directory
	parent := filepath.Dir(path)

	// Get device IDs
	var pathStat, parentStat unix.Stat_t
	if err := unix.Stat(path, &pathStat); err != nil {
		return false, err
	}
	if err := unix.Stat(parent, &parentStat); err != nil {
		return false, err
	}

	// If device IDs differ, it's a mount point
	return pathStat.Dev != parentStat.Dev, nil
}

// extractPodUIDFromPath extracts the pod UID from a kubelet target path
// Path format: /var/lib/kubelet/pods/<pod-uid>/volumes/kubernetes.io~csi/<volume-name>/mount
func extractPodUIDFromPath(targetPath string) (string, error) {
	// Split path into components
	parts := strings.Split(filepath.Clean(targetPath), string(filepath.Separator))

	// Assume "pods" is always at index 4 and pod UID at index 5 in the cleaned path
	// Example: /var/lib/kubelet/pods/<pod-uid>/volumes/...
	if len(parts) > 5 && parts[4] == "pods" {
		return parts[5], nil
	}

	return "", fmt.Errorf("failed to extract pod UID from path: %s", targetPath)
}

func isVolumeCapabilityPlainMount(volumeCapability *csi.VolumeCapability) bool {
	mount := volumeCapability.GetMount()
	switch {
	case mount == nil:
		return false
	case mount.FsType != "":
		return false
	case len(mount.MountFlags) != 0:
		return false
	}
	return true
}
