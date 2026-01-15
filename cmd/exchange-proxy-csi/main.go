package main

import (
	"flag"
	"log/slog"
	"os"
	"strings"
)

var (
	nodeID        string
	nodeIDEnv     string
	csiSocketPath string
	pluginName    string
	socketDir     string
	logLevel      string
)

func main() {
	flag.StringVar(&nodeID, "node-id", "", "Node ID (overrides -node-id-env)")
	flag.StringVar(&nodeIDEnv, "node-id-env", "MY_NODE_NAME", "Env var for node ID")
	flag.StringVar(&csiSocketPath, "csi-socket-path", "/exchange-proxy-csi/csi.sock", "CSI socket path")
	flag.StringVar(&pluginName, "plugin-name", "csi.exchange.nvidia.com", "CSI plugin name")
	flag.StringVar(&socketDir, "socket-dir", "/run/exchange-proxy", "Directory containing exchange socket")
	flag.StringVar(&logLevel, "log-level", "info", "Log level (debug, info, warn, error)")
	flag.Parse()

	setupLogging(logLevel)

	// Get node ID
	id := os.Getenv(nodeIDEnv)
	if nodeID != "" {
		id = nodeID
	}
	if id == "" {
		slog.Error("Node ID is required")
		os.Exit(1)
	}

	slog.Info("Starting exchange-proxy-csi",
		"node_id", id,
		"plugin_name", pluginName,
		"socket_dir", socketDir,
		"csi_socket", csiSocketPath)

	driver, err := NewDriver(Config{
		NodeID:     id,
		PluginName: pluginName,
		SocketDir:  socketDir,
	})
	if err != nil {
		slog.Error("Failed to create driver", "error", err)
		os.Exit(1)
	}

	if err := Serve(csiSocketPath, driver); err != nil {
		slog.Error("Server failed", "error", err)
		os.Exit(1)
	}
}

func setupLogging(level string) {
	var logLevel slog.Level
	switch strings.ToLower(level) {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})))
}
