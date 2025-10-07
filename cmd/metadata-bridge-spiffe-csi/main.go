package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
)

const (
	driverName    = "metadata-bridge.csi.nvidia.com"
	driverVersion = "0.1.0"
)

func main() {
	var (
		endpoint   = flag.String("endpoint", "unix:///csi/csi.sock", "CSI endpoint")
		nodeID     = flag.String("node-id", "", "Node ID")
		socketsDir = flag.String("sockets-dir", "/run/metadata/metadata-bridge-sockets", "Directory for socket management")
		logLevel   = flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	)
	flag.Parse()

	// Setup structured logging
	var level slog.Level
	switch *logLevel {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})))

	if *nodeID == "" {
		slog.Error("node-id is required")
		os.Exit(1)
	}

	slog.Info("Starting Metadata Bridge SPIFFE CSI Driver",
		"driver", driverName,
		"version", driverVersion,
		"node_id", *nodeID,
		"endpoint", *endpoint,
		"sockets_dir", *socketsDir)

	// Create sockets directory
	if err := os.MkdirAll(*socketsDir, 0750); err != nil {
		slog.Error("Failed to create sockets directory", "error", err)
		os.Exit(1)
	}

	// Create driver
	driver := NewDriver(*nodeID, *socketsDir)

	// Start server
	server := NewServer(*endpoint, driver)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := server.Run(ctx); err != nil {
		slog.Error("Server failed", "error", err)
		os.Exit(1)
	}
}

// Driver implements CSI Identity and Node services
type Driver struct {
	csi.UnimplementedIdentityServer
	csi.UnimplementedNodeServer

	nodeID     string
	socketsDir string
}

func NewDriver(nodeID, socketsDir string) *Driver {
	return &Driver{
		nodeID:     nodeID,
		socketsDir: socketsDir,
	}
}

// Server manages the gRPC server
type Server struct {
	endpoint string
	driver   *Driver
}

func NewServer(endpoint string, driver *Driver) *Server {
	return &Server{
		endpoint: endpoint,
		driver:   driver,
	}
}

func (s *Server) Run(ctx context.Context) error {
	proto, addr, err := parseEndpoint(s.endpoint)
	if err != nil {
		return err
	}

	if proto == "unix" {
		if err := os.Remove(addr); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	listener, err := listen(proto, addr)
	if err != nil {
		return err
	}

	opts := []grpc.ServerOption{
		grpc.UnaryInterceptor(logGRPC),
	}
	grpcServer := grpc.NewServer(opts...)

	csi.RegisterIdentityServer(grpcServer, s.driver)
	csi.RegisterNodeServer(grpcServer, s.driver)

	slog.Info("CSI server listening", "endpoint", s.endpoint)

	errCh := make(chan error, 1)
	go func() {
		errCh <- grpcServer.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		slog.Info("Shutting down server")
		grpcServer.GracefulStop()
		return nil
	case err := <-errCh:
		return err
	}
}
