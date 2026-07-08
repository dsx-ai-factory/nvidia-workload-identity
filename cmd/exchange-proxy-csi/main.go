package main

import (
	"flag"
	"log/slog"
	"os"
)

var (
	nodeID        string
	nodeIDEnv     string
	csiSocketPath string
	pluginName    string
	socketDir     string
	logLevel      slog.Level
)

func main() {
	flag.StringVar(&nodeID, "node-id", "", "Node ID (overrides -node-id-env)")
	flag.StringVar(&nodeIDEnv, "node-id-env", "MY_NODE_NAME", "Env var for node ID")
	flag.StringVar(&csiSocketPath, "csi-socket-path", "/exchange-proxy-csi/csi.sock", "CSI socket path")
	flag.StringVar(&pluginName, "plugin-name", "csi.exchange.nvidia.com", "CSI plugin name")
	flag.StringVar(&socketDir, "socket-dir", "/run/exchange-proxy", "Directory containing exchange socket")
	flag.TextVar(&logLevel, "log-level", slog.LevelInfo, "Log level (debug, info, warn, error)")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})))

	// Get node ID
	id := os.Getenv(nodeIDEnv)
	if nodeID != "" {
		id = nodeID
	}
	if id == "" {
		slog.Error("Node ID is required")
		os.Exit(1)
	}
	if socketDir == "" {
		slog.Error("Socket directory is required")
		os.Exit(1)
	}

	slog.Info("Starting exchange-proxy-csi",
		"node_id", id,
		"plugin_name", pluginName,
		"socket_dir", socketDir,
		"csi_socket", csiSocketPath)

	driver := &Driver{
		nodeID:     id,
		pluginName: pluginName,
		socketDir:  socketDir,
	}

	if err := Serve(csiSocketPath, driver); err != nil {
		slog.Error("Server failed", "error", err)
		os.Exit(1)
	}
}
