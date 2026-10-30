// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

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
	"reflect"
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
	imdsIP                 = "169.254.169.254"
	imdsIPv6               = "fd00:76b9::254"
	vethVMSide             = "imds-vm"  // max 15 chars for ifname
	vethContainerSide      = "imds-ctr" // max 15 chars for ifname
	bridgePrefix           = "k6t-"
	defaultPodNetworksFile = "/var/run/dsx-imds/pod-networks"
	defaultTapIfName       = "tap0"

	// IPv4 header offsets (RFC 791)
	ipv4DstAddrOffset = 16 // Destination IP address offset in IPv4 header
	ipv4AddrLen       = 4  // IPv4 address length in bytes
	// IPv6 keeps the destination in the fixed header (RFC 8200).
	ipv6DstAddrOffset = 24

	// Ethernet header offsets (IEEE 802.3)
	ethDstAddrOffset = 0 // Destination MAC address offset in Ethernet header
	ethAddrLen       = 6 // MAC address length in bytes
)

var (
	logLevel        slog.Level
	podNetworksFile *string
)

func runCommand() (runErr error) {
	flag.TextVar(&logLevel, "log-level", slog.LevelInfo, "log level (debug, info, warn, error)")
	podNetworksFile = flag.String("pod-networks-file", defaultPodNetworksFile, "Downward API OVN pod-networks file")
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
	// Wait for QEMU to open the tap, then read guest IP/MAC from OVN.
	slog.Debug("Waiting for KubeVirt network", "bridge_prefix", bridgePrefix)
	bridgeLink, vmIPs, vmMAC, err := waitForKubeVirtNetwork(ctx, nlHandle, *podNetworksFile)
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

	for _, vmIP := range vmIPs {
		metadataIP := net.ParseIP(imdsIP).To4()
		if vmIP.To4() == nil {
			metadataIP = net.ParseIP(imdsIPv6)
		}
		if err := assignIMDSIP(nlHandle, vethCtr, metadataIP); err != nil {
			return err
		}
		if err := setupNftablesMACRewrite(vethCtr.Attrs().HardwareAddr, metadataIP); err != nil {
			return err
		}
		slog.Info("Got VM IP and MAC", "ip", vmIP.String(), "mac", vmMAC.String())

		// Remove the UDN interface's local-table route so the /32 or /128 route
		// below returns replies through the veth.
		if err := deleteLocalRouteForIP(nlHandle, vmIP); err != nil {
			return err
		}
		if err := addVMRoute(nlHandle, vethCtr, vmIP); err != nil {
			return err
		}
		// Use the OVN allocation MAC for deterministic replies without ARP or
		// IPv6 neighbor discovery. Supported guests retain this MAC rather than
		// overriding it in the VMI.
		if err := addVMNeighbor(nlHandle, vethCtr, vmIP, vmMAC); err != nil {
			return err
		}
	}

	return nil
}

func waitForKubeVirtNetwork(ctx context.Context, nlHandle *netlink.Handle, statusFile string) (netlink.Link, []net.IP, net.HardwareAddr, error) {
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
			// The dummy appears before KubeVirt finishes its address updates.
			// Carrier returns when QEMU opens the persistent tap after that setup.
			if tap.Attrs().RawFlags&unix.IFF_LOWER_UP == 0 {
				lastErr = fmt.Errorf("waiting for guest to open %s", defaultTapIfName)
				continue
			}
			vmIP, vmMAC, err := readVMNetwork(statusFile)
			if err == nil {
				return bridge, vmIP, vmMAC, nil
			}
			lastErr = err
		}
	}
}

type linkLookup interface {
	LinkByName(string) (netlink.Link, error)
	LinkByIndex(int) (netlink.Link, error)
}

func findBridge(nlHandle linkLookup) (netlink.Link, error) {
	// tap0 belongs to the primary network. Other KubeVirt bridges may exist first.
	tap, err := nlHandle.LinkByName(defaultTapIfName)
	if err != nil {
		return nil, fmt.Errorf("finding primary tap: %w", err)
	}
	if tap.Attrs().MasterIndex == 0 {
		return nil, fmt.Errorf("%s is not attached to a bridge", defaultTapIfName)
	}
	bridge, err := nlHandle.LinkByIndex(tap.Attrs().MasterIndex)
	if err != nil {
		return nil, fmt.Errorf("finding primary tap bridge: %w", err)
	}
	if bridge.Type() != "bridge" || !strings.HasPrefix(bridge.Attrs().Name, bridgePrefix) {
		return nil, fmt.Errorf("%s is not attached to a KubeVirt bridge", defaultTapIfName)
	}
	return bridge, nil
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

type addressWriter interface {
	AddrReplace(netlink.Link, *netlink.Addr) error
}

// assignIMDSIP assigns an IPv4 or IPv6 metadata service address to an interface.
// Equivalent to: ip addr replace 169.254.169.254/32 dev imds-ctr
// IPv6 equivalent: ip -6 addr replace fd00:76b9::254/128 dev imds-ctr nodad
func assignIMDSIP(nlHandle addressWriter, link netlink.Link, ip net.IP) error {
	addr := &netlink.Addr{IPNet: hostPrefix(ip)}
	if ip.To4() == nil {
		// Each VM has a separate network namespace for this fixed service address.
		// Skip DAD so the address is usable as soon as setup finishes.
		addr.Flags = unix.IFA_F_NODAD
	}
	if err := nlHandle.AddrReplace(link, addr); err != nil {
		return fmt.Errorf("assigning IMDS IP to %s: %w", link.Attrs().Name, err)
	}
	return nil
}

// setupNftablesMACRewrite configures nftables to rewrite destination MAC for IMDS traffic.
func setupNftablesMACRewrite(vethMAC net.HardwareAddr, metadataIP net.IP) error {
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
	if err := nftAddIMDSRule(conn, table, chain, vethMAC, metadataIP); err != nil {
		return err
	}

	slog.Debug("nftables rule added", "dst_ip", metadataIP.String(), "dst_mac", vethMAC.String())
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

type nftRules interface {
	GetRules(*nftables.Table, *nftables.Chain) ([]*nftables.Rule, error)
	AddRule(*nftables.Rule) *nftables.Rule
	ReplaceRule(*nftables.Rule) *nftables.Rule
	DelRule(*nftables.Rule) error
	Flush() error
}

// nftAddIMDSRule reconciles the destination MAC rewrite for one address family.
// Equivalent to: nft add rule bridge nat PREROUTING ip daddr 169.254.169.254 ether daddr set <mac>
// IPv6 equivalent: nft add rule bridge nat PREROUTING ip6 daddr fd00:76b9::254 ether daddr set <mac>
func nftAddIMDSRule(conn nftRules, table *nftables.Table, chain *nftables.Chain, vethMAC net.HardwareAddr, metadataIP net.IP) error {
	protocol, offset, length := uint16(unix.ETH_P_IP), uint32(ipv4DstAddrOffset), uint32(ipv4AddrLen)
	ip := metadataIP.To4()
	if ip == nil {
		protocol, offset, length = unix.ETH_P_IPV6, ipv6DstAddrOffset, net.IPv6len
		ip = metadataIP.To16()
	}
	if ip == nil {
		return fmt.Errorf("invalid IMDS IP: %s", metadataIP)
	}
	// Add the rule - matching shell command bytecode exactly:
	// IPv4:
	// [ meta load protocol => reg 1 ]
	// [ cmp eq reg 1 0x0800 ]  (ETH_P_IP = IPv4 ethertype)
	// [ payload load 4b @ network header + 16 => reg 1 ]
	// [ cmp eq reg 1 <imdsIP> ]
	// [ immediate reg 1 <mac> ]
	// [ payload write reg 1 => 6b @ link header + 0 ]
	// IPv6:
	// [ meta load protocol => reg 1 ]
	// [ cmp eq reg 1 0x86dd ]  (ETH_P_IPV6 = IPv6 ethertype)
	// [ payload load 16b @ network header + 24 => reg 1 ]
	// [ cmp eq reg 1 <imdsIPv6> ]
	// [ immediate reg 1 <mac> ]
	// [ payload write reg 1 => 6b @ link header + 0 ]
	rule := &nftables.Rule{
		Table: table,
		Chain: chain,
		Exprs: []expr.Any{
			// meta load protocol => reg 1
			&expr.Meta{
				Key:      expr.MetaKeyPROTOCOL,
				Register: 1,
			},
			// cmp eq reg 1 ETH_P_IP (0x0800) or ETH_P_IPV6 (0x86dd)
			&expr.Cmp{
				Op:       expr.CmpOpEq,
				Register: 1,
				Data:     binaryutil.BigEndian.PutUint16(protocol),
			},
			// payload load 4b @ network header + ipv4DstAddrOffset => reg 1 (IPv4)
			// payload load 16b @ network header + ipv6DstAddrOffset => reg 1 (IPv6)
			&expr.Payload{
				DestRegister: 1,
				Base:         expr.PayloadBaseNetworkHeader,
				Offset:       offset,
				Len:          length,
			},
			// cmp eq reg 1 <metadataIP>
			&expr.Cmp{
				Op:       expr.CmpOpEq,
				Register: 1,
				Data:     ip,
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
	}

	rules, err := conn.GetRules(table, chain)
	if err != nil {
		return fmt.Errorf("getting IMDS chain rules: %w", err)
	}
	matched := false
	for _, existing := range rules {
		// Match our complete destination test and MAC write. A comparison
		// to the IMDS address alone may belong to another rule in this chain.
		if len(existing.Exprs) != len(rule.Exprs) ||
			!reflect.DeepEqual(existing.Exprs[:4], rule.Exprs[:4]) ||
			!reflect.DeepEqual(existing.Exprs[5:], rule.Exprs[5:]) {
			continue
		}
		rewrite, ok := existing.Exprs[4].(*expr.Immediate)
		if !ok || rewrite.Register != 1 || len(rewrite.Data) != ethAddrLen {
			continue
		}
		if matched {
			if err := conn.DelRule(existing); err != nil {
				return fmt.Errorf("removing duplicate IMDS rule: %w", err)
			}
			continue
		}
		matched = true
		if !bytes.Equal(rewrite.Data, vethMAC) {
			// Recreating the veth changes its MAC. Replace the stale rule in place.
			existing.Exprs = rule.Exprs
			conn.ReplaceRule(existing)
		}
	}
	if !matched {
		conn.AddRule(rule)
	}

	if err := conn.Flush(); err != nil {
		return fmt.Errorf("reconcile IMDS MAC rewrite for %s to %s: %w", metadataIP, vethMAC, err)
	}
	return nil
}

// tap0 attaches the primary network. OVN preserves its allocation across
// KubeVirt's handoff, including when eth0 belongs to the infrastructure network.
func readVMNetwork(path string) ([]net.IP, net.HardwareAddr, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open OVN pod networks: %w", err)
	}
	defer file.Close()

	var networks map[string]struct {
		Role string   `json:"role"`
		IPs  []string `json:"ip_addresses"`
		MAC  string   `json:"mac_address"`
	}
	if err := json.NewDecoder(file).Decode(&networks); err != nil {
		return nil, nil, fmt.Errorf("decode OVN pod networks: %w", err)
	}
	var vmIPs []net.IP
	var vmMAC net.HardwareAddr
	matched := false
	for name, network := range networks {
		if network.Role != "primary" {
			continue
		}
		if matched {
			return nil, nil, fmt.Errorf("multiple primary OVN networks")
		}
		matched = true
		vmMAC, err = net.ParseMAC(network.MAC)
		if err != nil {
			return nil, nil, fmt.Errorf("parse guest MAC for %s: %w", name, err)
		}
		if len(vmMAC) != 6 {
			return nil, nil, fmt.Errorf("invalid guest MAC %q for %s", network.MAC, name)
		}
		for _, address := range network.IPs {
			ip, _, err := net.ParseCIDR(address)
			if err != nil {
				return nil, nil, fmt.Errorf("parse guest CIDR %q for %s: %w", address, name, err)
			}
			if ipv4 := ip.To4(); ipv4 != nil {
				ip = ipv4
			}
			for _, existing := range vmIPs {
				if (existing.To4() != nil) == (ip.To4() != nil) {
					return nil, nil, fmt.Errorf("multiple guest addresses in one IP family for %s", name)
				}
			}
			vmIPs = append(vmIPs, ip)
		}
	}
	if len(vmIPs) == 0 {
		return nil, nil, fmt.Errorf("no guest address in primary OVN network")
	}
	return vmIPs, vmMAC, nil
}

type routeWriter interface {
	RouteListFiltered(int, *netlink.Route, uint64) ([]netlink.Route, error)
	RouteDel(*netlink.Route) error
	RouteReplace(*netlink.Route) error
}

func hostPrefix(ip net.IP) *net.IPNet {
	bits := net.IPv6len * 8
	if ip.To4() != nil {
		ip = ip.To4()
		bits = net.IPv4len * 8
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
}

// deleteLocalRouteForIP deletes the guest's local route for one family.
// Equivalent to: ip route del local <ip> table local
// IPv6 equivalent: ip -6 route del local <ip> table local
func deleteLocalRouteForIP(nl routeWriter, ip net.IP) error {
	family := netlink.FAMILY_V6
	if ip.To4() != nil {
		family = netlink.FAMILY_V4
	}
	filter := &netlink.Route{Table: unix.RT_TABLE_LOCAL}
	routes, err := nl.RouteListFiltered(family, filter, netlink.RT_FILTER_TABLE)
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

		ones, bits := r.Dst.Mask.Size()
		if r.Type == unix.RTN_LOCAL && ones == bits && r.Dst.IP.Equal(ip) {
			slog.Debug("Deleting local route", "dst", r.Dst.String())
			rr := r // copy
			if err := nl.RouteDel(&rr); err != nil {
				return fmt.Errorf("deleting local route %s: %w", rr.Dst, err)
			}
			deleted++
		}
	}
	if deleted > 0 {
		slog.Info("Deleted local route(s)", "ip", ip.String(), "count", deleted)
	}
	return nil
}

// addVMRoute adds a host route for the VM IP via the veth interface.
// Equivalent to: ip route replace <vmIP>/32 dev imds-ctr
// IPv6 equivalent: ip -6 route replace <vmIP>/128 dev imds-ctr
func addVMRoute(nlHandle routeWriter, link netlink.Link, vmIP net.IP) error {
	slog.Debug("Adding explicit route for VM IP", "vm_ip", vmIP.String())
	vmRoute := &netlink.Route{
		Dst:       hostPrefix(vmIP),
		LinkIndex: link.Attrs().Index,
	}
	if err := nlHandle.RouteReplace(vmRoute); err != nil {
		return fmt.Errorf("adding route for VM IP: %w", err)
	}
	return nil
}

type neighborWriter interface {
	NeighSet(*netlink.Neigh) error
}

// addVMNeighbor adds a permanent ARP or IPv6 neighbor entry for the VM.
// Equivalent to: ip neigh replace <vmIP> lladdr <vmMAC> dev imds-ctr nud permanent
// IPv6 equivalent: ip -6 neigh replace <vmIP> lladdr <vmMAC> dev imds-ctr nud permanent
func addVMNeighbor(nlHandle neighborWriter, link netlink.Link, vmIP net.IP, vmMAC net.HardwareAddr) error {
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
