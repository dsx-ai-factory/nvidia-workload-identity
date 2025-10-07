package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"

	"google.golang.org/grpc"
)

// parseEndpoint parses an endpoint string into protocol and address
func parseEndpoint(endpoint string) (string, string, error) {
	parts := strings.SplitN(endpoint, "://", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid endpoint format: %s", endpoint)
	}
	return parts[0], parts[1], nil
}

// listen creates a listener for the given protocol and address
func listen(proto, addr string) (net.Listener, error) {
	return net.Listen(proto, addr)
}

// logGRPC logs gRPC requests
func logGRPC(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	slog.Debug("gRPC request", "method", info.FullMethod)
	resp, err := handler(ctx, req)
	if err != nil {
		slog.Error("gRPC request failed", "method", info.FullMethod, "error", err)
	} else {
		slog.Debug("gRPC request completed", "method", info.FullMethod)
	}
	return resp, err
}
