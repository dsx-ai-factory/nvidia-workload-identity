package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"dsx-identity/internal/telemetry"
)

var (
	nodeID        string
	csiSocketPath string
	socketDir     string
	logLevel      slog.Level
)

func run() (runErr error) {
	flag.StringVar(&nodeID, "node-id", "", "Node ID")
	flag.StringVar(&csiSocketPath, "csi-socket-path", "/dsx-identity-keys-proxy-csi/csi.sock", "CSI socket path")
	flag.StringVar(&socketDir, "socket-dir", "/run/dsx", "Directory containing DSX Identity Keys socket")
	flag.TextVar(&logLevel, "log-level", slog.LevelInfo, "log level (debug, info, warn, error)")
	flag.Parse()

	sdk, err := telemetry.Setup(context.Background(), "dsx-identity-keys-proxy-csi", os.Stdout, logLevel)
	if err != nil {
		return fmt.Errorf("configure OpenTelemetry: %w", err)
	}
	defer func() {
		runErr = errors.Join(runErr, sdk.Shutdown(context.Background()))
	}()

	if nodeID == "" {
		return errors.New("node ID is required")
	}
	if socketDir == "" {
		return errors.New("socket directory is required")
	}

	slog.Info("Starting dsx-identity-keys-proxy-csi",
		"node_id", nodeID,
		"plugin_name", PluginName,
		"socket_dir", socketDir,
		"csi_socket", csiSocketPath)

	driver := &Driver{
		nodeID:    nodeID,
		socketDir: socketDir,
		mountOps:  systemMounts{},
	}

	return Serve(csiSocketPath, driver)
}

func main() {
	if err := run(); err != nil {
		slog.Error("DSX Identity Keys Proxy CSI failed", "error", err)
		os.Exit(1)
	}
}
