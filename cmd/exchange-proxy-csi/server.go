package main

import (
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
)

// Serve starts the CSI gRPC server
func Serve(socketPath string, driver *Driver) error {
	// Remove existing socket
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	// Create socket directory
	if err := os.MkdirAll(filepath.Dir(socketPath), 0755); err != nil {
		return err
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}

	server := grpc.NewServer()
	csi.RegisterIdentityServer(server, driver)
	csi.RegisterNodeServer(server, driver)

	slog.Info("CSI server listening", "socket", socketPath)
	return server.Serve(listener)
}
