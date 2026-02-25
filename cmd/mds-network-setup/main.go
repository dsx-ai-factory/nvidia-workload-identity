package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	mdsIP             = "169.254.169.254"
	vethVMSide        = "mds-vm"  // max 15 chars for ifname
	vethContainerSide = "mds-ctr" // max 15 chars for ifname
	bridgePrefix      = "k6t-"
	defaultUDNIfName  = "ovn-udn1"
	metaCIDR          = "169.254.169.254/32"

	// IPv4 header offsets (RFC 791)
	ipv4DstAddrOffset = 16 // Destination IP address offset in IPv4 header
	ipv4AddrLen       = 4  // IPv4 address length in bytes

	// Ethernet header offsets (IEEE 802.3)
	ethDstAddrOffset = 0 // Destination MAC address offset in Ethernet header
	ethAddrLen       = 6 // MAC address length in bytes
)

var (
	logLevel  *string
	udnIfName *string
)

func main() {
	logLevel = flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	udnIfName = flag.String("udn-if-name", defaultUDNIfName, "UDN interface name")
	flag.Parse()

	setupLogging(*logLevel)

	if err := setupPodNetwork(context.Background()); err != nil {
		slog.Error("UDN setup failed", "error", err)
		os.Exit(1)
	}

	slog.Info("UDN setup complete")
}

func setupPodNetwork(ctx context.Context) error {

	// Create netlink handle for this namespace
	nlHandle, err := netlink.NewHandle()
	if err != nil {
		return fmt.Errorf("creating netlink handle: %w", err)
	}
	defer nlHandle.Close()

	// Wait for bridge
	slog.Debug("Waiting for bridge", "prefix", bridgePrefix)
	bridgeLink, err := waitForBridge(ctx, nlHandle)
	if err != nil {
		return fmt.Errorf("waiting for bridge: %w", err)
	}
	slog.Info("Found bridge", "bridge", bridgeLink.Attrs().Name)

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

	// Assign MDS IP
	if err := assignMDSIP(nlHandle, vethCtr); err != nil {
		return err
	}

	// Get veth MAC
	vethMAC := vethCtr.Attrs().HardwareAddr
	slog.Info("Got veth MAC", "mac", vethMAC.String())

	// Setup nftables in the namespace
	if err := setupNftablesMACRewrite(vethMAC); err != nil {
		return err
	}

	// Get VM IP from pod status
	vmIP, vmMAC, err := getInterfaceIPv4AndMAC(*udnIfName)
	if err != nil {
		return fmt.Errorf("getting interface IPv4 and MAC: %w", err)
	}
	slog.Info("Got VM IP and MAC", "ip", vmIP.String(), "mac", vmMAC.String())

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

func waitForBridge(ctx context.Context, nlHandle *netlink.Handle) (netlink.Link, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return nil, fmt.Errorf("timeout waiting for bridge")
			}
			return nil, ctx.Err()
		case <-ticker.C:
			bridge, err := findBridge(nlHandle)
			if err == nil && bridge != nil {
				return bridge, nil
			}
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

// createVethPair creates a virtual ethernet pair for MDS connectivity.
// Equivalent to: ip link add mds-vm type veth peer name mds-ctr
func createVethPair(nlHandle *netlink.Handle) (vethCtr, vethVM netlink.Link, err error) {
	vethCtr, err = nlHandle.LinkByName(vethContainerSide)
	if err != nil {
		slog.Debug("Creating veth pair", "veth_vm_side", vethVMSide, "veth_container_side", vethContainerSide)
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
		slog.Debug("Veth pair already exists", "veth_vm_side", vethVMSide, "veth_container_side", vethContainerSide)
	}

	vethVM, err = nlHandle.LinkByName(vethVMSide)
	if err != nil {
		return nil, nil, fmt.Errorf("getting veth vm side: %w", err)
	}

	return vethCtr, vethVM, nil
}

// attachToBridge attaches a link to a bridge interface.
// Equivalent to: ip link set mds-vm master <bridge>
func attachToBridge(nlHandle *netlink.Handle, link, bridge netlink.Link) error {
	if link.Attrs().MasterIndex != bridge.Attrs().Index {
		slog.Debug("Attaching veth to bridge", "veth", link.Attrs().Name, "bridge", bridge.Attrs().Name)
		if err := nlHandle.LinkSetMaster(link, bridge); err != nil {
			return fmt.Errorf("attaching veth to bridge: %w", err)
		}
	} else {
		slog.Debug("Veth already attached to bridge", "veth", link.Attrs().Name, "bridge", bridge.Attrs().Name)
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

// assignMDSIP assigns the metadata service IP address to an interface.
// Equivalent to: ip addr add 169.254.169.254/32 dev mds-ctr
func assignMDSIP(nlHandle *netlink.Handle, link netlink.Link) error {
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
			slog.Debug("MDS IP already assigned", "ip", mdsIP)
			return nil
		}
	}
	slog.Debug("Assigning MDS IP", "ip", mdsIP, "dev", link.Attrs().Name)
	if err := nlHandle.AddrAdd(link, mdsAddr); err != nil {
		return fmt.Errorf("assigning IP to %s: %w", link.Attrs().Name, err)
	}
	return nil
}

// setupNftablesMACRewrite configures nftables to rewrite destination MAC for MDS traffic.
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

// getInterfaceIPv4AndMAC gets the IPv4 address and MAC address of an interface.
func getInterfaceIPv4AndMAC(ifname string) (net.IP, net.HardwareAddr, error) {
	iface, err := net.InterfaceByName(ifname)
	if err != nil {
		return nil, nil, fmt.Errorf("getting interface %q: %w", ifname, err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, nil, fmt.Errorf("listing addresses for %q: %w", ifname, err)
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP == nil {
			continue
		}
		ip4 := ipNet.IP.To4()
		if ip4 == nil {
			continue
		}
		if len(iface.HardwareAddr) != 6 {
			return nil, nil, fmt.Errorf("unexpected MAC for %q: %q", ifname, iface.HardwareAddr.String())
		}
		return ip4, iface.HardwareAddr, nil
	}
	return nil, nil, fmt.Errorf("no IPv4 address found on %q", ifname)
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
// Equivalent to: ip route replace <vmIP>/32 dev mds-ctr
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
// Equivalent to: ip neigh replace <vmIP> lladdr <vmMAC> dev mds-ctr nud permanent
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
