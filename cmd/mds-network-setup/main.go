package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

const (
	mdsIP             = "169.254.169.254"
	vethVMSide        = "mds-vm"  // max 15 chars for ifname
	vethContainerSide = "mds-ctr" // max 15 chars for ifname
	bridgePrefix      = "k6t-"
	annotationKey     = "spiffe.nvidia.com/inject-mds"
	containerName     = "compute"
	criSocket         = "/run/containerd/containerd.sock"
)

var (
	nodeName string
	logLevel string
)

type NetworkSetup struct {
	client         *kubernetes.Clientset
	criClient      runtimeapi.RuntimeServiceClient
	configuredPods sync.Map // map[string]bool
}

func main() {
	flag.StringVar(&nodeName, "node-name", os.Getenv("NODE_NAME"), "Node name to filter pods")
	flag.StringVar(&logLevel, "log-level", "info", "Log level (debug, info, warn, error)")
	flag.Parse()

	setupLogging(logLevel)

	if nodeName == "" {
		slog.Error("NODE_NAME environment variable or --node-name flag required")
		os.Exit(1)
	}

	slog.Info("Starting MDS network setup daemon", "node", nodeName)

	config, err := rest.InClusterConfig()
	if err != nil {
		slog.Error("Failed to create in-cluster config", "error", err)
		os.Exit(1)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		slog.Error("Failed to create Kubernetes client", "error", err)
		os.Exit(1)
	}

	// Connect to CRI
	criConn, err := grpc.NewClient("unix://"+criSocket,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		slog.Error("Failed to connect to CRI", "error", err)
		os.Exit(1)
	}
	defer criConn.Close()

	ns := &NetworkSetup{
		client:    client,
		criClient: runtimeapi.NewRuntimeServiceClient(criConn),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("Received shutdown signal")
		cancel()
	}()

	ns.run(ctx)
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

func (ns *NetworkSetup) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			slog.Info("Shutting down")
			return
		default:
		}

		if err := ns.watchPods(ctx); err != nil {
			slog.Error("Watch error, restarting", "error", err)
			time.Sleep(5 * time.Second)
		}
	}
}

func (ns *NetworkSetup) watchPods(ctx context.Context) error {
	// Watch virt-launcher pods on this node, filter by annotation in handlePod
	watcher, err := ns.client.CoreV1().Pods("").Watch(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", nodeName),
		LabelSelector: "kubevirt.io/domain",
	})
	if err != nil {
		return fmt.Errorf("failed to watch pods: %w", err)
	}
	defer watcher.Stop()

	slog.Info("Watching for virt-launcher pods", "node", nodeName)

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("watch channel closed")
			}

			if event.Type == watch.Error {
				return fmt.Errorf("watch error event")
			}

			pod, ok := event.Object.(*corev1.Pod)
			if !ok {
				continue
			}

			ns.handlePod(ctx, pod)
		}
	}
}

func (ns *NetworkSetup) handlePod(ctx context.Context, pod *corev1.Pod) {
	podKey := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)

	// Check annotation
	if pod.Annotations[annotationKey] != "true" {
		slog.Debug("Pod missing MDS annotation", "pod", podKey)
		return
	}

	// Check if pod is running
	if pod.Status.Phase != corev1.PodRunning {
		slog.Debug("Pod not running", "pod", podKey, "phase", pod.Status.Phase)
		return
	}

	// Skip if already configured
	if _, configured := ns.configuredPods.Load(podKey); configured {
		slog.Debug("Pod already configured", "pod", podKey)
		return
	}

	slog.Info("Processing pod", "pod", podKey)
	slog.Debug("Pod annotations", "pod", podKey, "annotations", pod.Annotations)

	// Get container PID
	pid, err := ns.getContainerPID(ctx, pod.Namespace, pod.Name, containerName)
	if err != nil {
		slog.Debug("Failed to get container PID", "pod", podKey, "error", err)
		return
	}

	if pid == 0 {
		slog.Debug("Container PID not available yet", "pod", podKey)
		return
	}
	slog.Debug("Got container PID", "pod", podKey, "pid", pid)

	// Setup network
	if err := ns.setupPodNetwork(ctx, pod, pid); err != nil {
		slog.Error("Failed to setup network", "pod", podKey, "error", err)
		return
	}

	ns.configuredPods.Store(podKey, true)
	slog.Info("Network setup complete", "pod", podKey)
}

func (ns *NetworkSetup) getContainerPID(ctx context.Context, namespace, podName, container string) (int, error) {
	// List containers matching the pod/container
	resp, err := ns.criClient.ListContainers(ctx, &runtimeapi.ListContainersRequest{
		Filter: &runtimeapi.ContainerFilter{
			LabelSelector: map[string]string{
				"io.kubernetes.pod.name":       podName,
				"io.kubernetes.pod.namespace":  namespace,
				"io.kubernetes.container.name": container,
			},
			State: &runtimeapi.ContainerStateValue{
				State: runtimeapi.ContainerState_CONTAINER_RUNNING,
			},
		},
	})
	if err != nil {
		return 0, fmt.Errorf("ListContainers failed: %w", err)
	}

	if len(resp.Containers) == 0 {
		return 0, nil
	}

	containerID := resp.Containers[0].Id

	// Get container status with info (includes PID)
	statusResp, err := ns.criClient.ContainerStatus(ctx, &runtimeapi.ContainerStatusRequest{
		ContainerId: containerID,
		Verbose:     true,
	})
	if err != nil {
		return 0, fmt.Errorf("ContainerStatus failed: %w", err)
	}

	// Parse PID from info (JSON in info["info"])
	info := statusResp.GetInfo()
	if info == nil {
		return 0, fmt.Errorf("no info in container status")
	}

	// The "info" field contains JSON with pid
	infoJSON, ok := info["info"]
	if !ok {
		return 0, fmt.Errorf("no 'info' key in status info")
	}

	// Simple extraction: look for "pid":NUMBER
	var pid int
	if _, err := fmt.Sscanf(extractPID(infoJSON), "%d", &pid); err != nil {
		return 0, fmt.Errorf("failed to parse PID from info: %w", err)
	}

	return pid, nil
}

// extractPID extracts the pid value from container info JSON
func extractPID(info string) string {
	// Look for "pid": followed by a number
	const pidKey = `"pid":`
	idx := strings.Index(info, pidKey)
	if idx < 0 {
		return "0"
	}

	start := idx + len(pidKey)
	// Skip whitespace
	for start < len(info) && (info[start] == ' ' || info[start] == '\t') {
		start++
	}

	// Read digits
	end := start
	for end < len(info) && info[end] >= '0' && info[end] <= '9' {
		end++
	}

	if end > start {
		return info[start:end]
	}
	return "0"
}

func (ns *NetworkSetup) setupPodNetwork(ctx context.Context, pod *corev1.Pod, pid int) error {
	podKey := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)

	// Wait for bridge
	slog.Debug("Waiting for bridge", "pod", podKey, "prefix", bridgePrefix)
	bridge, err := ns.waitForBridge(ctx, pid)
	if err != nil {
		return fmt.Errorf("waiting for bridge: %w", err)
	}
	slog.Info("Found bridge", "pod", podKey, "bridge", bridge)

	// Create veth pair (idempotent - check if exists first)
	if _, err := ns.nsexec(pid, "ip", "link", "show", vethContainerSide); err != nil {
		slog.Debug("Creating veth pair", "pod", podKey, "veth_vm_side", vethVMSide, "veth_container_side", vethContainerSide)
		if _, err := ns.nsexec(pid, "ip", "link", "add", vethVMSide, "type", "veth", "peer", "name", vethContainerSide); err != nil {
			return fmt.Errorf("creating veth pair: %w", err)
		}
	} else {
		slog.Debug("Veth pair already exists", "pod", podKey)
	}

	// Attach veth to bridge (idempotent - check if already attached)
	if !ns.isInterfaceAttachedToBridge(pid, vethVMSide, bridge) {
		slog.Debug("Attaching veth to bridge", "pod", podKey, "veth", vethVMSide, "bridge", bridge)
		if _, err := ns.nsexec(pid, "ip", "link", "set", vethVMSide, "master", bridge); err != nil {
			return fmt.Errorf("attaching veth to bridge: %w", err)
		}
	} else {
		slog.Debug("Veth already attached to bridge", "pod", podKey)
	}

	// Bring up interfaces (idempotent)
	slog.Debug("Bringing up interfaces", "pod", podKey)
	if _, err := ns.nsexec(pid, "ip", "link", "set", vethVMSide, "up"); err != nil {
		return fmt.Errorf("bringing up %s: %w", vethVMSide, err)
	}
	if _, err := ns.nsexec(pid, "ip", "link", "set", vethContainerSide, "up"); err != nil {
		return fmt.Errorf("bringing up %s: %w", vethContainerSide, err)
	}

	// Assign MDS IP (idempotent - check if already assigned)
	if !ns.hasIPAddress(pid, vethContainerSide, mdsIP) {
		slog.Debug("Assigning MDS IP", "pod", podKey, "ip", mdsIP, "dev", vethContainerSide)
		if _, err := ns.nsexec(pid, "ip", "addr", "add", mdsIP+"/32", "dev", vethContainerSide); err != nil {
			return fmt.Errorf("assigning IP to %s: %w", vethContainerSide, err)
		}
	} else {
		slog.Debug("MDS IP already assigned", "pod", podKey, "ip", mdsIP)
	}

	// Get veth MAC
	vethMAC, err := ns.getInterfaceMAC(pid, vethContainerSide)
	if err != nil {
		return fmt.Errorf("getting veth MAC: %w", err)
	}
	slog.Info("Got veth MAC", "pod", podKey, "mac", vethMAC)

	// Setup nftables - create table and chain if they don't exist.
	slog.Debug("Setting up nftables bridge nat table", "pod", podKey)
	if err := ns.ensureNftTable(pid, "bridge", "nat"); err != nil {
		return fmt.Errorf("creating nftables table: %w", err)
	}
	if err := ns.ensureNftChain(pid, "bridge", "nat", "PREROUTING",
		"{ type filter hook prerouting priority dstnat; policy accept; }"); err != nil {
		return fmt.Errorf("creating nftables chain: %w", err)
	}

	// Add nftables MAC rewrite rule (idempotent - check if exists first)
	if ns.hasNftRule(pid, "bridge", "nat", "PREROUTING", mdsIP) {
		slog.Debug("nftables rule already exists", "pod", podKey, "dst_ip", mdsIP)
	} else {
		slog.Debug("Adding nftables MAC rewrite rule", "pod", podKey, "dst_ip", mdsIP, "dst_mac", vethMAC)
		if _, err := ns.nsexec(pid, "nft", "add", "rule", "bridge", "nat", "PREROUTING",
			"ip", "daddr", mdsIP, "ether", "daddr", "set", vethMAC); err != nil {
			return fmt.Errorf("adding nftables rule: %w", err)
		}
	}

	// Get VM IP from pod status
	vmIP := pod.Status.PodIP
	if vmIP == "" {
		return fmt.Errorf("pod has no IP assigned yet")
	}

	// Derive VM MAC from IP (OVN uses 0a:58 prefix + IP bytes)
	vmMAC, err := macFromIP(vmIP)
	if err != nil {
		return fmt.Errorf("deriving MAC from IP: %w", err)
	}
	slog.Info("Derived VM MAC from IP", "pod", podKey, "ip", vmIP, "mac", vmMAC)

	// Setup reply path
	// The pod and VM share the same IP (vmIP). The pod has a local route for this IP,
	// which causes incoming packets from the VM to be rejected as "martian source".
	// We need to:
	// 1. Remove the IP from eth0 so incoming packets aren't rejected as martian
	// 2. Add an explicit route via mds-ctr so replies reach the VM
	//
	// Alternative: `ip route del local <vmIP> table local` would remove
	// only the local route while keeping the IP on eth0. Either approach works here
	// since eth0 is DOWN in KubeVirt bridge mode and the pod has no cluster network
	// access (UDN isolation). We use `ip addr del` as the simpler option.
	//
	// Idempotent: only remove if still present
	vmIPWithPrefix, err := ns.getIPWithPrefix(pid, "eth0", vmIP)
	if err != nil {
		slog.Debug("IP already removed from eth0 or not present", "pod", podKey, "vm_ip", vmIP)
	} else {
		slog.Debug("Removing IP from eth0", "pod", podKey, "cidr", vmIPWithPrefix)
		if _, err := ns.nsexec(pid, "ip", "addr", "del", vmIPWithPrefix, "dev", "eth0"); err != nil {
			return fmt.Errorf("removing IP from eth0: %w", err)
		}
	}

	slog.Debug("Adding explicit route for VM IP", "pod", podKey, "vm_ip", vmIP)
	if _, err := ns.nsexec(pid, "ip", "route", "replace", vmIP+"/32", "dev", vethContainerSide); err != nil {
		return fmt.Errorf("adding route for VM IP: %w", err)
	}

	// Add neighbor entry for VM MAC
	slog.Debug("Adding neighbor entry for VM", "pod", podKey, "vm_ip", vmIP, "vm_mac", vmMAC)
	if _, err := ns.nsexec(pid, "ip", "neigh", "replace", vmIP, "lladdr", vmMAC, "dev", vethContainerSide, "nud", "permanent"); err != nil {
		return fmt.Errorf("adding neighbor entry: %w", err)
	}

	// List nftables for verification (debug only, ignore errors)
	if slog.Default().Enabled(ctx, slog.LevelDebug) {
		slog.Debug("Listing nftables rules for verification", "pod", podKey)
		_, _ = ns.nsexec(pid, "nft", "list", "table", "bridge", "nat")
	}

	return nil
}

func (ns *NetworkSetup) waitForBridge(ctx context.Context, pid int) (string, error) {
	timeout := time.After(60 * time.Second)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timeout:
			return "", fmt.Errorf("timeout waiting for bridge")
		case <-ticker.C:
			bridge, err := ns.findBridge(pid)
			if err == nil && bridge != "" {
				return bridge, nil
			}
		}
	}
}

func (ns *NetworkSetup) findBridge(pid int) (string, error) {
	output, err := ns.nsexec(pid, "ip", "-o", "link", "show", "type", "bridge")
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, bridgePrefix) {
			// Extract interface name: "4: k6t-xxx: <BROADCAST..."
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				name := strings.TrimSuffix(parts[1], ":")
				if strings.HasPrefix(name, bridgePrefix) {
					return name, nil
				}
			}
		}
	}

	return "", fmt.Errorf("no bridge found")
}

func (ns *NetworkSetup) getInterfaceMAC(pid int, iface string) (string, error) {
	output, err := ns.nsexec(pid, "ip", "-o", "link", "show", iface)
	if err != nil {
		return "", err
	}

	// Parse: "5: mds-ctr: ... link/ether aa:bb:cc:dd:ee:ff ..."
	if idx := strings.Index(output, "link/ether "); idx >= 0 {
		macPart := output[idx+11:]
		if spaceIdx := strings.Index(macPart, " "); spaceIdx > 0 {
			mac := macPart[:spaceIdx]
			if _, err := net.ParseMAC(mac); err == nil {
				return mac, nil
			}
		}
	}

	return "", fmt.Errorf("MAC not found for %s", iface)
}

// nsexec runs a command in the pod's network namespace and returns combined stdout/stderr.
func (ns *NetworkSetup) nsexec(pid int, args ...string) (string, error) {
	cmdArgs := append([]string{"-t", fmt.Sprintf("%d", pid), "-n"}, args...)
	cmd := exec.Command("nsenter", cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Include output in error for debugging
		return string(output), fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

// macFromIP derives the OVN-style MAC address from an IPv4 address.
// OVN uses the format 0a:58:XX:YY:ZZ:WW where XX.YY.ZZ.WW is the IP.
func macFromIP(ip string) (string, error) {
	parsed := net.ParseIP(ip).To4()
	if parsed == nil {
		return "", fmt.Errorf("invalid IPv4: %s", ip)
	}
	return fmt.Sprintf("0a:58:%02x:%02x:%02x:%02x", parsed[0], parsed[1], parsed[2], parsed[3]), nil
}

// getIPWithPrefix looks up an IP address on an interface and returns it with its prefix length.
// For example, if eth0 has 10.100.1.7/24, returns "10.100.1.7/24".
func (ns *NetworkSetup) getIPWithPrefix(pid int, iface, ip string) (string, error) {
	output, err := ns.nsexec(pid, "ip", "-o", "addr", "show", "dev", iface)
	if err != nil {
		return "", fmt.Errorf("ip addr show failed: %w", err)
	}

	// Parse output like: "2: eth0    inet 10.100.1.7/24 brd 10.100.1.255 ..."
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "inet ") || strings.Contains(line, "inet6") {
			continue
		}
		// Find "inet X.X.X.X/NN"
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "inet" && i+1 < len(fields) {
				cidr := fields[i+1]
				// Check if this is the IP we're looking for
				if strings.HasPrefix(cidr, ip+"/") {
					return cidr, nil
				}
			}
		}
	}

	return "", fmt.Errorf("IP %s not found on %s", ip, iface)
}

// isInterfaceAttachedToBridge checks if an interface is attached to a specific bridge.
func (ns *NetworkSetup) isInterfaceAttachedToBridge(pid int, iface, bridge string) bool {
	output, err := ns.nsexec(pid, "ip", "-o", "link", "show", iface)
	if err != nil {
		return false
	}
	// Output like: "5: mds-vm: <BROADCAST...> master k6t-net1 state UP ..."
	return strings.Contains(output, "master "+bridge)
}

// hasIPAddress checks if an interface has a specific IP address assigned.
func (ns *NetworkSetup) hasIPAddress(pid int, iface, ip string) bool {
	output, err := ns.nsexec(pid, "ip", "-o", "addr", "show", "dev", iface)
	if err != nil {
		return false
	}
	// Check if the IP is in the output
	return strings.Contains(output, ip)
}

// ensureNftTable creates an nftables table if it doesn't exist.
func (ns *NetworkSetup) ensureNftTable(pid int, family, name string) error {
	// Check if table exists
	if _, err := ns.nsexec(pid, "nft", "list", "table", family, name); err == nil {
		slog.Debug("nftables table already exists", "family", family, "table", name)
		return nil
	}
	// Create it
	if _, err := ns.nsexec(pid, "nft", "add", "table", family, name); err != nil {
		return fmt.Errorf("nft add table %s %s: %w", family, name, err)
	}
	return nil
}

// hasNftRule checks if an nftables rule matching the given IP exists in the chain.
func (ns *NetworkSetup) hasNftRule(pid int, family, table, chain, ip string) bool {
	output, err := ns.nsexec(pid, "nft", "list", "chain", family, table, chain)
	if err != nil {
		return false
	}
	// Look for our rule pattern: "ip daddr 169.254.169.254"
	return strings.Contains(output, "ip daddr "+ip)
}

// ensureNftChain creates an nftables chain if it doesn't exist.
func (ns *NetworkSetup) ensureNftChain(pid int, family, table, chain, spec string) error {
	// Check if chain exists
	if _, err := ns.nsexec(pid, "nft", "list", "chain", family, table, chain); err == nil {
		slog.Debug("nftables chain already exists", "family", family, "table", table, "chain", chain)
		return nil
	}
	// Create it
	if _, err := ns.nsexec(pid, "nft", "add", "chain", family, table, chain, spec); err != nil {
		return fmt.Errorf("nft add chain %s %s %s: %w", family, table, chain, err)
	}
	return nil
}
