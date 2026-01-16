# MDS Network Setup

A privileged DaemonSet that configures network routing within virt-launcher pods to enable VMs to reach the metadata service at `169.254.169.254`.

KubeVirt VMs run inside virt-launcher pods. VM traffic passes through a Linux bridge (`k6t-*`). This DaemonSet uses native Go libraries (`vishvananda/netlink`, `vishvananda/netns`, `google/nftables`) to configure routing so packets destined to `169.254.169.254` reach the metadata-service sidecar.

## Configuration Steps

### 1. Create veth pair

```bash
ip link add mds-vm type veth peer name mds-ctr
```

Creates a virtual ethernet pair. `mds-vm` will be attached to the bridge to receive VM traffic. `mds-ctr` will host the MDS IP address and is where the metadata service listens.

### 2. Attach to bridge

```bash
ip link set mds-vm master k6t-eth0
```

Connects `mds-vm` to the KubeVirt bridge. This allows packets forwarded by the bridge to reach the veth pair.

### 3. Bring up interfaces

```bash
ip link set mds-vm up
ip link set mds-ctr up
```

Enables both ends of the veth pair.

### 4. Assign MDS IP

```bash
ip addr add 169.254.169.254/32 dev mds-ctr
```

Assigns the link-local metadata service IP to `mds-ctr`. The metadata service container binds to this address.

### 5. nftables bridge MAC rewrite

```bash
nft add table bridge nat
nft add chain bridge nat PREROUTING { type filter hook prerouting priority dstnat; policy accept; }
nft add rule bridge nat PREROUTING ip daddr 169.254.169.254 ether daddr set <mds-ctr-MAC>
```

When the VM sends a packet to `169.254.169.254`, it uses its default gateway's MAC as the destination (standard L3 routing). The bridge would normally forward this to the uplink. This nftables rule intercepts packets destined to `169.254.169.254` and rewrites the destination MAC to `mds-ctr`'s MAC, causing the bridge to forward them to the veth pair instead.

### 6. Remove IP from eth0

```bash
ip addr del <pod-IP>/<prefix> dev eth0
```

The virt-launcher pod and VM share the same IP address (assigned by OVN). When packets arrive from the VM with `src=<pod-IP>`, the kernel checks if the source IP is local. Since the pod has this IP configured, the kernel rejects the packet as "martian source" (a local IP appearing as an external source). Removing the IP from eth0 prevents this rejection.

Alternative: `ip route del local <pod-IP> table local` would remove only the local route while keeping the IP on eth0. Either approach works here since eth0 is DOWN in KubeVirt bridge mode and the pod has no cluster network access (UDN isolation).

### 7. Add reply route

```bash
ip route replace <pod-IP>/32 dev mds-ctr
```

After removing the local route, the kernel no longer knows how to reach the VM's IP. This adds an explicit route directing reply packets to the VM via `mds-ctr`. The packet flows: `mds-ctr` → `mds-vm` → bridge → `tap0` → VM.

### 8. Add neighbor entry

```bash
ip neigh replace <pod-IP> lladdr <vm-MAC> dev mds-ctr nud permanent
```

Static ARP entry mapping the VM's IP to its MAC address. Required for the kernel to build Ethernet frames when sending reply packets. The MAC is derived from the IP using OVN's convention: `0a:58:XX:YY:ZZ:WW` where `XX.YY.ZZ.WW` is the IP address.

## Traffic Flow

**Request path**: VM sends to `169.254.169.254` → `tap0` → bridge → nftables rewrites dst MAC → `mds-vm` → `mds-ctr` → metadata-service

**Reply path**: metadata-service → `mds-ctr` → (route + neighbor entry) → `mds-vm` → bridge → `tap0` → VM
