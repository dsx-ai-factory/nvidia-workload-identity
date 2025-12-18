package main

import (
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
)

func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		name      string
		endpoint  string
		wantProto string
		wantAddr  string
		wantErr   bool
	}{
		{
			name:      "unix socket",
			endpoint:  "unix:///csi/csi.sock",
			wantProto: "unix",
			wantAddr:  "/csi/csi.sock",
			wantErr:   false,
		},
		{
			name:      "tcp endpoint",
			endpoint:  "tcp://127.0.0.1:9000",
			wantProto: "tcp",
			wantAddr:  "127.0.0.1:9000",
			wantErr:   false,
		},
		{
			name:      "invalid format no scheme",
			endpoint:  "/csi/csi.sock",
			wantProto: "",
			wantAddr:  "",
			wantErr:   true,
		},
		{
			name:      "invalid format single colon",
			endpoint:  "unix:/csi/csi.sock",
			wantProto: "",
			wantAddr:  "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto, addr, err := parseEndpoint(tt.endpoint)
			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if proto != tt.wantProto {
				t.Errorf("proto = %q, want %q", proto, tt.wantProto)
			}
			if addr != tt.wantAddr {
				t.Errorf("addr = %q, want %q", addr, tt.wantAddr)
			}
		})
	}
}

func TestExtractPodUIDFromPath(t *testing.T) {
	tests := []struct {
		name       string
		targetPath string
		wantUID    string
		wantErr    bool
	}{
		{
			name:       "valid kubelet path",
			targetPath: "/var/lib/kubelet/pods/abc-123-def/volumes/kubernetes.io~csi/my-volume/mount",
			wantUID:    "abc-123-def",
			wantErr:    false,
		},
		{
			name:       "valid path with uuid",
			targetPath: "/var/lib/kubelet/pods/12345678-1234-1234-1234-123456789abc/volumes/kubernetes.io~csi/test/mount",
			wantUID:    "12345678-1234-1234-1234-123456789abc",
			wantErr:    false,
		},
		{
			name:       "path without pods",
			targetPath: "/var/lib/kubelet/volumes/kubernetes.io~csi/my-volume/mount",
			wantUID:    "",
			wantErr:    true,
		},
		{
			name:       "short path",
			targetPath: "/var/lib",
			wantUID:    "",
			wantErr:    true,
		},
		{
			name:       "empty path",
			targetPath: "",
			wantUID:    "",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uid, err := extractPodUIDFromPath(tt.targetPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if uid != tt.wantUID {
				t.Errorf("uid = %q, want %q", uid, tt.wantUID)
			}
		})
	}
}

func TestIsVolumeCapabilityPlainMount(t *testing.T) {
	tests := []struct {
		name       string
		capability *csi.VolumeCapability
		want       bool
	}{
		{
			name: "plain mount",
			capability: &csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{},
				},
			},
			want: true,
		},
		{
			name: "mount with fs type",
			capability: &csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{
						FsType: "ext4",
					},
				},
			},
			want: false,
		},
		{
			name: "mount with flags",
			capability: &csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{
						MountFlags: []string{"ro"},
					},
				},
			},
			want: false,
		},
		{
			name: "block volume",
			capability: &csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Block{
					Block: &csi.VolumeCapability_BlockVolume{},
				},
			},
			want: false,
		},
		{
			name:       "nil capability",
			capability: &csi.VolumeCapability{},
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isVolumeCapabilityPlainMount(tt.capability)
			if got != tt.want {
				t.Errorf("got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDriverConstants(t *testing.T) {
	if driverName == "" {
		t.Error("driverName should not be empty")
	}
	if driverVersion == "" {
		t.Error("driverVersion should not be empty")
	}
}

func TestNewDriver(t *testing.T) {
	driver := NewDriver("test-node", "/tmp/sockets")

	if driver.nodeID != "test-node" {
		t.Errorf("nodeID = %q, want %q", driver.nodeID, "test-node")
	}
	if driver.socketsDir != "/tmp/sockets" {
		t.Errorf("socketsDir = %q, want %q", driver.socketsDir, "/tmp/sockets")
	}
}

func TestNewServer(t *testing.T) {
	driver := NewDriver("test-node", "/tmp/sockets")
	server := NewServer("unix:///test.sock", driver)

	if server.endpoint != "unix:///test.sock" {
		t.Errorf("endpoint = %q, want %q", server.endpoint, "unix:///test.sock")
	}
	if server.driver != driver {
		t.Error("driver should be the same instance")
	}
}
