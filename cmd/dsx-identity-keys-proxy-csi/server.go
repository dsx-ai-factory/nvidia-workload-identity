package main

import (
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
)

// Serve starts the CSI gRPC server.
func Serve(socketPath string, driver *Driver) error {
	// A terminated instance can leave its Unix socket behind. Remove that stale
	// filesystem entry before binding the replacement server.
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(socketPath), 0755); err != nil {
		return err
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	server := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()))
	csi.RegisterIdentityServer(server, driver)
	csi.RegisterNodeServer(server, driver)

	slog.Info("CSI server listening", "socket", socketPath)
	return server.Serve(listener)
}
