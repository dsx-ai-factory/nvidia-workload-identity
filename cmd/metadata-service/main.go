package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type IdentityRequest struct {
	Audience string `json:"audience"`
}

type IdentityResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	SpiffeID  string `json:"spiffe_id"`
}

type MetadataServer struct {
	k8sClient *kubernetes.Clientset
}

// findPodByIP queries the Kubernetes API to find the pod with the given IP address
// Checks both primary and secondary network IPs including UDN annotations
func (s *MetadataServer) findPodByIP(ctx context.Context, clientIP string) (*corev1.Pod, error) {
	// List all pods in all namespaces
	pods, err := s.k8sClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	slog.Debug("Searching for pod by IP", "client_ip", clientIP, "total_pods", len(pods.Items))

	targetIP := net.ParseIP(clientIP)
	if targetIP == nil {
		return nil, fmt.Errorf("invalid IP address: %s", clientIP)
	}

	// Check each pod's IPs
	for _, pod := range pods.Items {
		// Check primary network IPs (pod.Status.PodIPs)
		for _, podIP := range pod.Status.PodIPs {
			if podIP.IP == clientIP {
				slog.Debug("Found pod with primary IP", "namespace", pod.Namespace, "pod", pod.Name, "ip", clientIP)
				return &pod, nil
			}
		}

		// Check pod.Status.PodIP for older k8s versions
		if pod.Status.PodIP == clientIP {
			slog.Debug("Found pod with status IP", "namespace", pod.Namespace, "pod", pod.Name, "ip", clientIP)
			return &pod, nil
		}

		// Check secondary network IPs from OVN-Kubernetes annotations
		// The annotation key is k8s.ovn.org/pod-networks
		if podNetworks, ok := pod.Annotations["k8s.ovn.org/pod-networks"]; ok {
			if strings.Contains(podNetworks, clientIP) {
				// Parse the annotation to verify it's a valid IP match
				var networks map[string]interface{}
				if err := json.Unmarshal([]byte(podNetworks), &networks); err == nil {
					for _, netConfig := range networks {
						if netMap, ok := netConfig.(map[string]interface{}); ok {
							if ipAddrs, ok := netMap["ip_addresses"].([]interface{}); ok {
								for _, ipAddr := range ipAddrs {
									if ipStr, ok := ipAddr.(string); ok {
										// IP addresses may be in CIDR format (e.g., "10.1.1.5/24")
										ip, _, _ := net.ParseCIDR(ipStr)
										if ip == nil {
											ip = net.ParseIP(ipStr)
										}
										if ip != nil && ip.Equal(targetIP) {
											slog.Debug("Found pod with secondary network IP", "namespace", pod.Namespace, "pod", pod.Name, "ip", clientIP)
											return &pod, nil
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	return nil, fmt.Errorf("no pod found with IP address: %s", clientIP)
}

// fetchJWTForPod fetches a JWT SVID for the identified pod via the bridge socket
func (s *MetadataServer) fetchJWTForPod(ctx context.Context, pod *corev1.Pod, audience string) (*jwtsvid.SVID, error) {
	podUID := string(pod.UID)

	slog.Debug("Fetching JWT for pod via bridge socket",
		"namespace", pod.Namespace,
		"pod", pod.Name,
		"pod_uid", podUID,
		"audience", audience)

	// Construct socket path based on pod UID
	socketPath := fmt.Sprintf("/run/metadata/metadata-bridge-sockets/%s/metadata-bridge-spire-agent.sock", podUID)
	socketAddr := fmt.Sprintf("unix://%s", socketPath)

	slog.Debug("Connecting to bridge socket", "socket_path", socketPath)

	// Create a temporary JWT source connected to the pod's bridge socket
	jwtSource, err := workloadapi.NewJWTSource(ctx, workloadapi.WithClientOptions(
		workloadapi.WithAddr(socketAddr),
	))
	if err != nil {
		return nil, fmt.Errorf("failed to create JWT source for pod bridge socket: %w", err)
	}
	defer jwtSource.Close()

	// Fetch JWT SVID from the pod's workload API proxy
	jwt, err := jwtSource.FetchJWTSVID(ctx, jwtsvid.Params{
		Audience: audience,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWT from bridge socket: %w", err)
	}

	slog.Info("Fetched JWT from bridge socket",
		"namespace", pod.Namespace,
		"pod", pod.Name,
		"pod_uid", podUID,
		"spiffe_id", jwt.ID.String())

	return jwt, nil
}

// handleIdentity handles POST requests to /v1/metadata/identity
func (s *MetadataServer) handleIdentity(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, 1024*1024)
	defer body.Close()

	// Extract client IP from request
	clientIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// If SplitHostPort fails, try using RemoteAddr directly
		clientIP = r.RemoteAddr
	}

	slog.Info("Received identity request", "client_ip", clientIP)

	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		slog.Warn("Invalid content type", "client_ip", clientIP)
		http.Error(w, "Invalid content type", http.StatusUnsupportedMediaType)
		return
	}

	// Parse request body
	var req IdentityRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		slog.Warn("Failed to decode request", "error", err, "client_ip", clientIP)
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if req.Audience == "" {
		slog.Warn("Missing audience in request", "client_ip", clientIP)
		http.Error(w, "Audience is required", http.StatusBadRequest)
		return
	}

	// Find pod by IP
	ctx := r.Context()
	pod, err := s.findPodByIP(ctx, clientIP)
	if err != nil {
		slog.Warn("Failed to find pod", "client_ip", clientIP, "error", err)
		http.Error(w, fmt.Sprintf("Pod not found: %v", err), http.StatusNotFound)
		return
	}

	// Fetch JWT SVID for the pod
	jwt, err := s.fetchJWTForPod(ctx, pod, req.Audience)
	if err != nil {
		slog.Error("Failed to fetch JWT", "namespace", pod.Namespace, "pod", pod.Name, "error", err)
		http.Error(w, fmt.Sprintf("Failed to fetch JWT: %v", err), http.StatusInternalServerError)
		return
	}

	// Prepare response
	resp := IdentityResponse{
		Token:     jwt.Marshal(),
		ExpiresAt: jwt.Expiry.Format(time.RFC3339),
		SpiffeID:  jwt.ID.String(),
	}

	slog.Info("Successfully issued JWT", "namespace", pod.Namespace, "pod", pod.Name, "spiffe_id", jwt.ID.String())

	// Send response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("Failed to encode response", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
}

func main() {
	var (
		listenAddr = flag.String("listen", ":8080", "HTTP server listen address")
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

	slog.Info("Starting SPIFFE Metadata Service", "listen_addr", *listenAddr, "log_level", level.String())

	// Create in-cluster Kubernetes client
	config, err := rest.InClusterConfig()
	if err != nil {
		slog.Error("Failed to create in-cluster config", "error", err)
		panic(err)
	}

	k8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		slog.Error("Failed to create Kubernetes client", "error", err)
		panic(err)
	}

	// Create metadata server
	server := &MetadataServer{
		k8sClient: k8sClient,
	}

	// Setup HTTP handlers
	http.HandleFunc("POST /v1/metadata/identity", server.handleIdentity)
	http.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK")
	})

	// Start HTTP server
	slog.Info("Starting HTTP server", "listen_addr", *listenAddr)
	if err := http.ListenAndServe(*listenAddr, nil); err != nil {
		slog.Error("HTTP server failed", "error", err)
		panic(err)
	}
}
