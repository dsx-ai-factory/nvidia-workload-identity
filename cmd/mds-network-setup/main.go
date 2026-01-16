package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
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

	// IPv4 header offsets (RFC 791)
	ipv4DstAddrOffset = 16 // Destination IP address offset in IPv4 header
	ipv4AddrLen       = 4  // IPv4 address length in bytes

	// Ethernet header offsets (IEEE 802.3)
	ethDstAddrOffset = 0 // Destination MAC address offset in Ethernet header
	ethAddrLen       = 6 // MAC address length in bytes
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
		slog.Debug("Container not ready yet", "pod", podKey, "error", err)
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
		return 0, fmt.Errorf("container %q not found in pod %s/%s", container, namespace, podName)
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

	// Get network namespace handle for the container
	nsHandle, err := netns.GetFromPid(pid)
	if err != nil {
		return fmt.Errorf("getting netns for pid %d: %w", pid, err)
	}
	defer nsHandle.Close()

	// Create netlink handle for this namespace
	nlHandle, err := netlink.NewHandleAt(nsHandle)
	if err != nil {
		return fmt.Errorf("creating netlink handle: %w", err)
	}
	defer nlHandle.Close()

	// Wait for bridge
	slog.Debug("Waiting for bridge", "pod", podKey, "prefix", bridgePrefix)
	bridge, err := ns.waitForBridge(ctx, nlHandle)
	if err != nil {
		return fmt.Errorf("waiting for bridge: %w", err)
	}
	slog.Info("Found bridge", "pod", podKey, "bridge", bridge.Attrs().Name)

	// Create veth pair
	vethCtr, vethVM, err := createVethPair(nlHandle, podKey)
	if err != nil {
		return err
	}

	// Attach veth to bridge
	if err := attachToBridge(nlHandle, vethVM, bridge, podKey); err != nil {
		return err
	}

	// Bring up interfaces
	if err := bringUpLink(nlHandle, vethVM, podKey); err != nil {
		return err
	}
	if err := bringUpLink(nlHandle, vethCtr, podKey); err != nil {
		return err
	}

	// Assign MDS IP
	if err := assignMDSIP(nlHandle, vethCtr, podKey); err != nil {
		return err
	}

	// Get veth MAC
	vethMAC := vethCtr.Attrs().HardwareAddr
	slog.Info("Got veth MAC", "pod", podKey, "mac", vethMAC.String())

	// Setup nftables in the namespace
	if err := setupNftablesMACRewrite(int(nsHandle), vethMAC, podKey); err != nil {
		return err
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
	slog.Info("Derived VM MAC from IP", "pod", podKey, "ip", vmIP, "mac", vmMAC.String())

	// Remove IP from eth0 to prevent martian source rejection
	if err := removeIPFromEth0(nlHandle, vmIP, podKey); err != nil {
		return err
	}

	// Add reply route for VM IP
	if err := addVMRoute(nlHandle, vethCtr, vmIP, podKey); err != nil {
		return err
	}

	// Add neighbor entry for VM MAC
	if err := addVMNeighbor(nlHandle, vethCtr, vmIP, vmMAC, podKey); err != nil {
		return err
	}

	return nil
}

func (ns *NetworkSetup) waitForBridge(ctx context.Context, nlHandle *netlink.Handle) (netlink.Link, error) {
	timeout := time.After(60 * time.Second)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout:
			return nil, fmt.Errorf("timeout waiting for bridge")
		case <-ticker.C:
			bridge, err := ns.findBridge(nlHandle)
			if err == nil && bridge != nil {
				return bridge, nil
			}
		}
	}
}

func (ns *NetworkSetup) findBridge(nlHandle *netlink.Handle) (netlink.Link, error) {
	links, err := nlHandle.LinkList()
	if err != nil {
		return nil, err
	}

	for _, link := range links {
		if link.Type() == "bridge" && strings.HasPrefix(link.Attrs().Name, bridgePrefix) {
			return link, nil
		}
	}

	return nil, fmt.Errorf("no bridge found")
}

// createVethPair creates a virtual ethernet pair for MDS connectivity.
// Equivalent to: ip link add mds-vm type veth peer name mds-ctr
func createVethPair(nlHandle *netlink.Handle, podKey string) (vethCtr, vethVM netlink.Link, err error) {
	vethCtr, err = nlHandle.LinkByName(vethContainerSide)
	if err != nil {
		slog.Debug("Creating veth pair", "pod", podKey, "veth_vm_side", vethVMSide, "veth_container_side", vethContainerSide)
		veth := &netlink.Veth{
			LinkAttrs: netlink.LinkAttrs{Name: vethVMSide},
			PeerName:  vethContainerSide,
		}
		if err := nlHandle.LinkAdd(veth); err != nil {
			return nil, nil, fmt.Errorf("creating veth pair: %w", err)
		}
		vethCtr, err = nlHandle.LinkByName(vethContainerSide)
		if err != nil {
			return nil, nil, fmt.Errorf("getting veth container side: %w", err)
		}
	} else {
		slog.Debug("Veth pair already exists", "pod", podKey)
	}

	vethVM, err = nlHandle.LinkByName(vethVMSide)
	if err != nil {
		return nil, nil, fmt.Errorf("getting veth vm side: %w", err)
	}

	return vethCtr, vethVM, nil
}

// attachToBridge attaches a link to a bridge interface.
// Equivalent to: ip link set mds-vm master <bridge>
func attachToBridge(nlHandle *netlink.Handle, link, bridge netlink.Link, podKey string) error {
	if link.Attrs().MasterIndex != bridge.Attrs().Index {
		slog.Debug("Attaching veth to bridge", "pod", podKey, "veth", link.Attrs().Name, "bridge", bridge.Attrs().Name)
		if err := nlHandle.LinkSetMaster(link, bridge); err != nil {
			return fmt.Errorf("attaching veth to bridge: %w", err)
		}
	} else {
		slog.Debug("Veth already attached to bridge", "pod", podKey)
	}
	return nil
}

// bringUpLink sets a network interface to the UP state.
// Equivalent to: ip link set <iface> up
func bringUpLink(nlHandle *netlink.Handle, link netlink.Link, podKey string) error {
	slog.Debug("Bringing up interface", "pod", podKey, "iface", link.Attrs().Name)
	if err := nlHandle.LinkSetUp(link); err != nil {
		return fmt.Errorf("bringing up %s: %w", link.Attrs().Name, err)
	}
	return nil
}

// assignMDSIP assigns the metadata service IP address to an interface.
// Equivalent to: ip addr add 169.254.169.254/32 dev mds-ctr
func assignMDSIP(nlHandle *netlink.Handle, link netlink.Link, podKey string) error {
	mdsAddr, err := netlink.ParseAddr(mdsIP + "/32")
	if err != nil {
		return fmt.Errorf("parsing MDS address: %w", err)
	}
	addrs, err := nlHandle.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("listing addresses on %s: %w", link.Attrs().Name, err)
	}
	for _, a := range addrs {
		if a.IP.Equal(mdsAddr.IP) {
			slog.Debug("MDS IP already assigned", "pod", podKey, "ip", mdsIP)
			return nil
		}
	}
	slog.Debug("Assigning MDS IP", "pod", podKey, "ip", mdsIP, "dev", link.Attrs().Name)
	if err := nlHandle.AddrAdd(link, mdsAddr); err != nil {
		return fmt.Errorf("assigning IP to %s: %w", link.Attrs().Name, err)
	}
	return nil
}

// setupNftablesMACRewrite configures nftables to rewrite destination MAC for MDS traffic.
func setupNftablesMACRewrite(nsfd int, vethMAC net.HardwareAddr, podKey string) error {
	slog.Debug("Setting up nftables bridge nat table", "pod", podKey)

	// Create nftables connection in the target namespace
	conn, err := nftables.New(nftables.WithNetNSFd(nsfd))
	if err != nil {
		return fmt.Errorf("creating nftables connection: %w", err)
	}

	// Create table
	table, err := nftAddTable(conn)
	if err != nil {
		return err
	}

	// Create chain
	chain, err := nftAddChain(conn, table)
	if err != nil {
		return err
	}

	// Add rule (idempotent)
	if err := nftAddMDSRule(conn, table, chain, vethMAC); err != nil {
		return err
	}

	slog.Debug("nftables rule added", "dst_ip", mdsIP, "dst_mac", vethMAC.String())
	return nil
}

// nftAddTable creates the bridge nat table.
// Equivalent to: nft add table bridge nat
func nftAddTable(conn *nftables.Conn) (*nftables.Table, error) {
	table := &nftables.Table{
		Family: nftables.TableFamilyBridge,
		Name:   "nat",
	}
	conn.AddTable(table)
	if err := conn.Flush(); err != nil {
		return nil, fmt.Errorf("nft add table bridge nat: %w", err)
	}
	return table, nil
}

// nftAddChain creates the PREROUTING chain in the bridge nat table.
// Equivalent to: nft add chain bridge nat PREROUTING { type filter hook prerouting priority dstnat; policy accept; }
func nftAddChain(conn *nftables.Conn, table *nftables.Table) (*nftables.Chain, error) {
	policy := nftables.ChainPolicyAccept
	chain := &nftables.Chain{
		Name:     "PREROUTING",
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookPrerouting,
		Priority: nftables.ChainPriorityNATDest,
		Policy:   &policy,
	}
	conn.AddChain(chain)
	if err := conn.Flush(); err != nil {
		return nil, fmt.Errorf("nft add chain bridge nat PREROUTING: %w", err)
	}
	return chain, nil
}

// nftAddMDSRule adds the MAC rewrite rule for MDS traffic.
// Equivalent to: nft add rule bridge nat PREROUTING ip daddr 169.254.169.254 ether daddr set <mac>
func nftAddMDSRule(conn *nftables.Conn, table *nftables.Table, chain *nftables.Chain, vethMAC net.HardwareAddr) error {
	mdsIPParsed := net.ParseIP(mdsIP).To4()
	if mdsIPParsed == nil {
		return fmt.Errorf("invalid MDS IP: %s", mdsIP)
	}

	// Check if rule already exists
	chains, err := conn.ListChainsOfTableFamily(nftables.TableFamilyBridge)
	if err != nil {
		return fmt.Errorf("listing chains: %w", err)
	}
	for _, c := range chains {
		if c.Name == "PREROUTING" && c.Table.Name == "nat" {
			rules, err := conn.GetRules(table, c)
			if err != nil {
				return fmt.Errorf("getting rules: %w", err)
			}
			for _, r := range rules {
				for _, e := range r.Exprs {
					if cmp, ok := e.(*expr.Cmp); ok {
						if len(cmp.Data) == 4 && cmp.Data[0] == mdsIPParsed[0] &&
							cmp.Data[1] == mdsIPParsed[1] && cmp.Data[2] == mdsIPParsed[2] &&
							cmp.Data[3] == mdsIPParsed[3] {
							slog.Debug("nftables rule already exists", "dst_ip", mdsIP)
							return nil
						}
					}
				}
			}
			break
		}
	}

	// Add the rule - matching shell command bytecode exactly:
	// [ meta load protocol => reg 1 ]
	// [ cmp eq reg 1 0x0800 ]  (ETH_P_IP = IPv4 ethertype)
	// [ payload load 4b @ network header + 16 => reg 1 ]
	// [ cmp eq reg 1 <mdsIP> ]
	// [ immediate reg 1 <mac> ]
	// [ payload write reg 1 => 6b @ link header + 0 ]
	conn.AddRule(&nftables.Rule{
		Table: table,
		Chain: chain,
		Exprs: []expr.Any{
			// meta load protocol => reg 1
			&expr.Meta{
				Key:      expr.MetaKeyPROTOCOL,
				Register: 1,
			},
			// cmp eq reg 1 ETH_P_IP (0x0800)
			&expr.Cmp{
				Op:       expr.CmpOpEq,
				Register: 1,
				Data:     binaryutil.BigEndian.PutUint16(unix.ETH_P_IP),
			},
			// payload load 4b @ network header + ipv4DstAddrOffset => reg 1
			&expr.Payload{
				DestRegister: 1,
				Base:         expr.PayloadBaseNetworkHeader,
				Offset:       ipv4DstAddrOffset,
				Len:          ipv4AddrLen,
			},
			// cmp eq reg 1 <mdsIP>
			&expr.Cmp{
				Op:       expr.CmpOpEq,
				Register: 1,
				Data:     mdsIPParsed,
			},
			// immediate reg 1 <mac>
			&expr.Immediate{
				Register: 1,
				Data:     vethMAC,
			},
			// payload write reg 1 => 6b @ link header + ethDstAddrOffset
			&expr.Payload{
				OperationType:  expr.PayloadWrite,
				SourceRegister: 1,
				Base:           expr.PayloadBaseLLHeader,
				Offset:         ethDstAddrOffset,
				Len:            ethAddrLen,
			},
		},
	})

	if err := conn.Flush(); err != nil {
		return fmt.Errorf("nft add rule bridge nat PREROUTING ip daddr %s ether daddr set %s: %w", mdsIP, vethMAC, err)
	}
	return nil
}

// removeIPFromEth0 removes the VM IP from eth0 to prevent martian source rejection.
// Equivalent to: ip addr del <vmIP>/<prefix> dev eth0
//
// The pod and VM share the same IP. When packets arrive from the VM with this source IP,
// the kernel would reject them as "martian source" (a local IP appearing externally).
// Alternative: `ip route del local <vmIP> table local` removes only the local route.
func removeIPFromEth0(nlHandle *netlink.Handle, vmIP, podKey string) error {
	eth0, err := nlHandle.LinkByName("eth0")
	if err != nil {
		return fmt.Errorf("getting eth0: %w", err)
	}
	eth0Addrs, err := nlHandle.AddrList(eth0, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("listing addresses on eth0: %w", err)
	}
	vmIPParsed := net.ParseIP(vmIP)
	for _, a := range eth0Addrs {
		if a.IP.Equal(vmIPParsed) {
			slog.Debug("Removing IP from eth0", "pod", podKey, "cidr", a.IPNet.String())
			if err := nlHandle.AddrDel(eth0, &a); err != nil {
				return fmt.Errorf("removing IP from eth0: %w", err)
			}
			return nil
		}
	}
	slog.Debug("IP already removed from eth0", "pod", podKey, "vm_ip", vmIP)
	return nil
}

// addVMRoute adds a host route for the VM IP via the veth interface.
// Equivalent to: ip route replace <vmIP>/32 dev mds-ctr
func addVMRoute(nlHandle *netlink.Handle, link netlink.Link, vmIP, podKey string) error {
	vmIPParsed := net.ParseIP(vmIP)
	slog.Debug("Adding explicit route for VM IP", "pod", podKey, "vm_ip", vmIP)
	vmRoute := &netlink.Route{
		Dst:       &net.IPNet{IP: vmIPParsed, Mask: net.CIDRMask(32, 32)},
		LinkIndex: link.Attrs().Index,
	}
	if err := nlHandle.RouteReplace(vmRoute); err != nil {
		return fmt.Errorf("adding route for VM IP: %w", err)
	}
	return nil
}

// addVMNeighbor adds a static ARP entry for the VM.
// Equivalent to: ip neigh replace <vmIP> lladdr <vmMAC> dev mds-ctr nud permanent
func addVMNeighbor(nlHandle *netlink.Handle, link netlink.Link, vmIP string, vmMAC net.HardwareAddr, podKey string) error {
	vmIPParsed := net.ParseIP(vmIP)
	slog.Debug("Adding neighbor entry for VM", "pod", podKey, "vm_ip", vmIP, "vm_mac", vmMAC.String())
	neigh := &netlink.Neigh{
		LinkIndex:    link.Attrs().Index,
		IP:           vmIPParsed,
		HardwareAddr: vmMAC,
		State:        netlink.NUD_PERMANENT,
	}
	if err := nlHandle.NeighSet(neigh); err != nil {
		return fmt.Errorf("adding neighbor entry: %w", err)
	}
	return nil
}

// macFromIP derives the OVN-style MAC address from an IPv4 address.
// OVN uses the format 0a:58:XX:YY:ZZ:WW where XX.YY.ZZ.WW is the IP.
func macFromIP(ip string) (net.HardwareAddr, error) {
	parsed := net.ParseIP(ip).To4()
	if parsed == nil {
		return nil, fmt.Errorf("invalid IPv4: %s", ip)
	}
	return net.HardwareAddr{0x0a, 0x58, parsed[0], parsed[1], parsed[2], parsed[3]}, nil
}
