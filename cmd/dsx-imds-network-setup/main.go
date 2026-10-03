package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"golang.org/x/sys/unix"

	"dsx-identity/internal/telemetry"
)

const (
	imdsIP                   = "169.254.169.254"
	vethVMSide               = "imds-vm"  // max 15 chars for ifname
	vethContainerSide        = "imds-ctr" // max 15 chars for ifname
	bridgePrefix             = "k6t-"
	defaultNetworkStatusFile = "/var/run/dsx-imds/network-status"
	defaultTapIfName         = "tap0"
	metaCIDR                 = "169.254.169.254/32"

	// IPv4 header offsets (RFC 791)
	ipv4DstAddrOffset = 16 // Destination IP address offset in IPv4 header
	ipv4AddrLen       = 4  // IPv4 address length in bytes

	// Ethernet header offsets (IEEE 802.3)
	ethDstAddrOffset = 0 // Destination MAC address offset in Ethernet header
	ethAddrLen       = 6 // MAC address length in bytes
)

var (
	logLevel          slog.Level
	networkStatusFile *string
)

func runCommand() (runErr error) {
	flag.TextVar(&logLevel, "log-level", slog.LevelInfo, "log level (debug, info, warn, error)")
	networkStatusFile = flag.String("network-status-file", defaultNetworkStatusFile, "Downward API network-status file")
	flag.Parse()

	sdk, err := telemetry.Setup(context.Background(), "dsx-imds-network-setup", os.Stdout, logLevel)
	if err != nil {
		return fmt.Errorf("configure OpenTelemetry: %w", err)
	}
	defer func() {
		runErr = errors.Join(runErr, sdk.Shutdown(context.Background()))
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return run(ctx, podNetworkSetup{})
}

func main() {
	if err := runCommand(); err != nil {
		slog.Error("UDN setup failed", "error", err)
		os.Exit(1)
	}
}

type networkSetup interface {
	Setup(context.Context) error
}

type podNetworkSetup struct{}

func (podNetworkSetup) Setup(ctx context.Context) error {
	return setupPodNetwork(ctx)
}

func run(ctx context.Context, setup networkSetup) error {
	setupCtx, span := otel.Tracer("dsx-imds-network-setup").Start(ctx, "configure-pod-network")
	err := setup.Setup(setupCtx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "network setup failed")
	}
	span.End()
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	slog.Info("UDN setup complete")
	// This binary runs as a native sidecar. Stay alive after setup so kubelet does
	// not restart it and repeat namespace mutations for the same virt-launcher Pod.
	<-ctx.Done()
	return nil
}

func setupPodNetwork(ctx context.Context) error {

	// Create netlink handle for this namespace
	nlHandle, err := netlink.NewHandle()
	if err != nil {
		return fmt.Errorf("creating netlink handle: %w", err)
	}
	defer nlHandle.Close()

	// bridge uses DHCP, while l2bridge/managedTap needs guest network configuration.
	// Both hand the CNI link to a bridge and tap, then leave a dummy behind.
	// Wait for that handoff and read guest IP/MAC from CNI, not the dummy or infra eth0.
	slog.Debug("Waiting for KubeVirt network", "bridge_prefix", bridgePrefix)
	bridgeLink, vmIP, vmMAC, err := waitForKubeVirtNetwork(ctx, nlHandle, *networkStatusFile)
	if err != nil {
		return fmt.Errorf("waiting for KubeVirt network: %w", err)
	}
	slog.Info("KubeVirt network ready", "bridge", bridgeLink.Attrs().Name)

	// Create veth pair
	vethCtr, vethVM, err := createVethPair(nlHandle)
	if err != nil {
		return err
	}

	// Attach veth to bridge
	if err := attachToBridge(nlHandle, vethVM, bridgeLink); err != nil {
		return err
	}

	// Bring up interfaces
	if err := bringUpLink(nlHandle, vethVM); err != nil {
		return err
	}
	if err := bringUpLink(nlHandle, vethCtr); err != nil {
		return err
	}

	// Assign IMDS IP
	if err := assignIMDSIP(nlHandle, vethCtr); err != nil {
		return err
	}

	// Get veth MAC
	vethMAC := vethCtr.Attrs().HardwareAddr
	slog.Info("Got veth MAC", "mac", vethMAC.String())

	// Setup nftables in the namespace
	if err := setupNftablesMACRewrite(vethMAC); err != nil {
		return err
	}

	slog.Info("Got VM IP and MAC", "ip", vmIP.String(), "mac", vmMAC.String())

	// Remove the UDN interface's local-table route so the /32 route below returns replies through the veth.
	if err := deleteLocalRouteForIP(nlHandle, vmIP); err != nil {
		return err
	}

	// Add reply route for VM IP
	if err := addVMRoute(nlHandle, vethCtr, vmIP); err != nil {
		return err
	}

	// Add neighbor entry for VM MAC
	if err := addVMNeighbor(nlHandle, vethCtr, vmIP, vmMAC); err != nil {
		return err
	}

	return nil
}

func waitForKubeVirtNetwork(ctx context.Context, nlHandle *netlink.Handle, statusFile string) (netlink.Link, net.IP, net.HardwareAddr, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	lastErr := errors.New("KubeVirt network is not ready")

	for {
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return nil, nil, nil, fmt.Errorf("timeout waiting for KubeVirt network: %w", lastErr)
			}
			return nil, nil, nil, ctx.Err()
		case <-ticker.C:
			bridge, err := findBridge(nlHandle)
			if err != nil {
				lastErr = err
				continue
			}
			tap, err := nlHandle.LinkByName(defaultTapIfName)
			if err != nil {
				lastErr = err
				continue
			}
			if tap.Attrs().MasterIndex != bridge.Attrs().Index {
				lastErr = fmt.Errorf("%s is not attached to %s", defaultTapIfName, bridge.Attrs().Name)
				continue
			}
			ifaceName := strings.TrimPrefix(bridge.Attrs().Name, bridgePrefix)
			iface, err := nlHandle.LinkByName(ifaceName)
			if err != nil {
				lastErr = err
				continue
			}
			if iface.Type() != "dummy" {
				lastErr = fmt.Errorf("waiting for KubeVirt to replace %s", ifaceName)
				continue
			}
			vmIP, vmMAC, err := readVMNetwork(statusFile, ifaceName)
			if err == nil {
				return bridge, vmIP, vmMAC, nil
			}
			lastErr = err
		}
	}
}

func findBridge(nlHandle *netlink.Handle) (netlink.Link, error) {
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

// createVethPair creates a virtual ethernet pair for IMDS connectivity.
// Equivalent to: ip link add imds-vm type veth peer name imds-ctr
func createVethPair(nlHandle *netlink.Handle) (vethCtr, vethVM netlink.Link, err error) {
	slog.Debug("Creating veth pair", "veth_vm_side", vethVMSide, "veth_container_side", vethContainerSide)
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: vethVMSide},
		PeerName:  vethContainerSide,
	}
	if err := nlHandle.LinkAdd(veth); err != nil && !errors.Is(err, unix.EEXIST) {
		return nil, nil, fmt.Errorf("creating veth pair: %w", err)
	}
	vethCtr, err = nlHandle.LinkByName(vethContainerSide)
	if err != nil {
		return nil, nil, fmt.Errorf("getting veth container side: %w", err)
	}

	vethVM, err = nlHandle.LinkByName(vethVMSide)
	if err != nil {
		return nil, nil, fmt.Errorf("getting veth vm side: %w", err)
	}

	return vethCtr, vethVM, nil
}

// attachToBridge attaches a link to a bridge interface.
// Equivalent to: ip link set imds-vm master <bridge>
func attachToBridge(nlHandle *netlink.Handle, link, bridge netlink.Link) error {
	slog.Debug("Attaching veth to bridge", "veth", link.Attrs().Name, "bridge", bridge.Attrs().Name)
	if err := nlHandle.LinkSetMaster(link, bridge); err != nil {
		return fmt.Errorf("attaching veth to bridge: %w", err)
	}
	return nil
}

// bringUpLink sets a network interface to the UP state.
// Equivalent to: ip link set <iface> up
func bringUpLink(nlHandle *netlink.Handle, link netlink.Link) error {
	slog.Debug("Bringing up interface", "iface", link.Attrs().Name)
	if err := nlHandle.LinkSetUp(link); err != nil {
		return fmt.Errorf("bringing up %s: %w", link.Attrs().Name, err)
	}
	return nil
}

// assignIMDSIP assigns the metadata service IP address to an interface.
// Equivalent to: ip addr replace 169.254.169.254/32 dev imds-ctr
func assignIMDSIP(nlHandle *netlink.Handle, link netlink.Link) error {
	imdsAddr, err := netlink.ParseAddr(metaCIDR)
	if err != nil {
		return fmt.Errorf("parsing IMDS address: %w", err)
	}
	slog.Debug("Assigning IMDS IP", "cidr", metaCIDR, "dev", link.Attrs().Name)
	if err := nlHandle.AddrReplace(link, imdsAddr); err != nil {
		return fmt.Errorf("assigning IP to %s: %w", link.Attrs().Name, err)
	}
	return nil
}

// setupNftablesMACRewrite configures nftables to rewrite destination MAC for IMDS traffic.
func setupNftablesMACRewrite(vethMAC net.HardwareAddr) error {
	slog.Debug("Setting up nftables bridge nat table")

	// Create nftables connection in the target namespace
	conn, err := nftables.New()
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
	if err := nftAddIMDSRule(conn, table, chain, vethMAC); err != nil {
		return err
	}

	slog.Debug("nftables rule added", "dst_ip", imdsIP, "dst_mac", vethMAC.String())
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

// nftAddIMDSRule adds the MAC rewrite rule for IMDS traffic.
// Equivalent to: nft add rule bridge nat PREROUTING ip daddr 169.254.169.254 ether daddr set <mac>
func nftAddIMDSRule(conn *nftables.Conn, table *nftables.Table, chain *nftables.Chain, vethMAC net.HardwareAddr) error {
	imdsIPParsed := net.ParseIP(imdsIP).To4()
	if imdsIPParsed == nil {
		return fmt.Errorf("invalid IMDS IP: %s", imdsIP)
	}

	// Check if rule already exists
	chains, err := conn.ListChainsOfTableFamily(nftables.TableFamilyBridge)
	if err != nil {
		return fmt.Errorf("listing chains: %w", err)
	}
	for _, existingChain := range chains {
		if existingChain.Name != "PREROUTING" || existingChain.Table.Name != "nat" {
			continue
		}
		rules, err := conn.GetRules(table, existingChain)
		if err != nil {
			return fmt.Errorf("getting rules: %w", err)
		}
		for _, rule := range rules {
			for _, candidate := range rule.Exprs {
				comparison, ok := candidate.(*expr.Cmp)
				if ok && bytes.Equal(comparison.Data, imdsIPParsed) {
					slog.Debug("nftables rule already exists", "dst_ip", imdsIP)
					return nil
				}
			}
		}
		break
	}

	// Add the rule - matching shell command bytecode exactly:
	// [ meta load protocol => reg 1 ]
	// [ cmp eq reg 1 0x0800 ]  (ETH_P_IP = IPv4 ethertype)
	// [ payload load 4b @ network header + 16 => reg 1 ]
	// [ cmp eq reg 1 <imdsIP> ]
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
			// cmp eq reg 1 <imdsIP>
			&expr.Cmp{
				Op:       expr.CmpOpEq,
				Register: 1,
				Data:     imdsIPParsed,
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
		return fmt.Errorf("nft add rule bridge nat PREROUTING ip daddr %s ether daddr set %s: %w", imdsIP, vethMAC, err)
	}
	return nil
}

// CNI network-status preserves the guest IP and MAC across KubeVirt's handoff.
func readVMNetwork(path, ifaceName string) (net.IP, net.HardwareAddr, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open network status: %w", err)
	}
	defer file.Close()

	var networks []struct {
		Interface string   `json:"interface"`
		IPs       []string `json:"ips"`
		MAC       string   `json:"mac"`
	}
	if err := json.NewDecoder(file).Decode(&networks); err != nil {
		return nil, nil, fmt.Errorf("decode network status: %w", err)
	}
	var vmIP net.IP
	var vmMAC net.HardwareAddr
	matched := false
	for _, network := range networks {
		if network.Interface != ifaceName {
			continue
		}
		if matched {
			return nil, nil, fmt.Errorf("multiple network-status entries for %s", ifaceName)
		}
		matched = true
		vmMAC, err = net.ParseMAC(network.MAC)
		if err != nil {
			return nil, nil, fmt.Errorf("parse guest MAC for %s: %w", ifaceName, err)
		}
		if len(vmMAC) != 6 {
			return nil, nil, fmt.Errorf("invalid guest MAC %q for %s", network.MAC, ifaceName)
		}
		for _, address := range network.IPs {
			ip := net.ParseIP(address)
			if ip == nil {
				return nil, nil, fmt.Errorf("invalid guest IP %q for %s", address, ifaceName)
			}
			if ipv4 := ip.To4(); ipv4 != nil {
				if vmIP != nil {
					return nil, nil, fmt.Errorf("multiple guest IPv4 addresses for %s", ifaceName)
				}
				vmIP = ipv4
			}
		}
	}
	if vmIP == nil {
		return nil, nil, fmt.Errorf("no guest IPv4 address for %s in network status", ifaceName)
	}
	return vmIP, vmMAC, nil
}

// deleteLocalRouteForIP deletes the local route for an IP address.
// Equivalent to: ip route del local <ip> table local
func deleteLocalRouteForIP(nl *netlink.Handle, ip net.IP) error {
	ip4 := ip.To4()
	if ip4 == nil {
		return fmt.Errorf("pod IP must be IPv4: %s", ip.String())
	}

	filter := &netlink.Route{Table: unix.RT_TABLE_LOCAL}
	routes, err := nl.RouteListFiltered(netlink.FAMILY_V4, filter, netlink.RT_FILTER_TABLE)
	if err != nil {
		return fmt.Errorf("listing local routes: %w", err)
	}

	var deleted int
	for _, r := range routes {
		if r.Dst == nil {
			continue
		}

		if r.Dst.IP == nil {
			continue
		}

		if r.Dst.IP.Equal(ip4) {
			slog.Debug("Deleting local route", "dst", r.Dst.String())
			rr := r // copy
			if err := nl.RouteDel(&rr); err != nil {
				slog.Warn("Failed to delete local route (continuing)", "dst", rr.Dst.String(), "error", err)
				continue
			}
			deleted++
		}
	}
	if deleted > 0 {
		slog.Info("Deleted local route(s)", "ip", ip4.String(), "count", deleted)
	}
	return nil
}

// addVMRoute adds a host route for the VM IP via the veth interface.
// Equivalent to: ip route replace <vmIP>/32 dev imds-ctr
func addVMRoute(nlHandle *netlink.Handle, link netlink.Link, vmIP net.IP) error {
	slog.Debug("Adding explicit route for VM IP", "vm_ip", vmIP.String())
	vmRoute := &netlink.Route{
		Dst:       &net.IPNet{IP: vmIP, Mask: net.CIDRMask(32, 32)},
		LinkIndex: link.Attrs().Index,
	}
	if err := nlHandle.RouteReplace(vmRoute); err != nil {
		return fmt.Errorf("adding route for VM IP: %w", err)
	}
	return nil
}

// addVMNeighbor adds a static ARP entry for the VM.
// Equivalent to: ip neigh replace <vmIP> lladdr <vmMAC> dev imds-ctr nud permanent
func addVMNeighbor(nlHandle *netlink.Handle, link netlink.Link, vmIP net.IP, vmMAC net.HardwareAddr) error {
	slog.Debug("Adding neighbor entry for VM", "vm_ip", vmIP.String(), "vm_mac", vmMAC.String())
	neigh := &netlink.Neigh{
		LinkIndex:    link.Attrs().Index,
		IP:           vmIP,
		HardwareAddr: vmMAC,
		State:        netlink.NUD_PERMANENT,
	}
	if err := nlHandle.NeighSet(neigh); err != nil {
		return fmt.Errorf("adding neighbor entry: %w", err)
	}
	return nil
}
