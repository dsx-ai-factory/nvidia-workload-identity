package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadVMNetwork(t *testing.T) {
	for _, test := range []struct {
		name, iface, status, ip, mac string
		wantError                    bool
	}{
		{
			name:   "bridge",
			iface:  "eth0",
			status: `[{"interface":"eth0","ips":["10.1.0.7"],"mac":"0a:58:0a:01:00:07"}]`,
			ip:     "10.1.0.7", mac: "0a:58:0a:01:00:07",
		},
		{
			name:  "l2bridge with separate infrastructure network",
			iface: "ovn-udn1",
			status: `[{"interface":"eth0","ips":["172.16.3.16"],"mac":"0a:58:ac:10:03:10"},
			{"interface":"ovn-udn1","ips":["fd00::139","10.193.0.139"],"mac":"0a:58:0a:c1:00:8b"}]`,
			ip: "10.193.0.139", mac: "0a:58:0a:c1:00:8b",
		},
		{name: "missing guest network", iface: "ovn-udn1", status: `[{"interface":"eth0","ips":["172.16.3.16"],"mac":"0a:58:ac:10:03:10"}]`, wantError: true},
		{name: "malformed JSON", iface: "eth0", status: `[`, wantError: true},
		{name: "invalid MAC", iface: "eth0", status: `[{"interface":"eth0","ips":["10.1.0.7"],"mac":"bad"}]`, wantError: true},
		{name: "invalid IP", iface: "eth0", status: `[{"interface":"eth0","ips":["bad"],"mac":"0a:58:0a:01:00:07"}]`, wantError: true},
		{name: "IPv6 only", iface: "eth0", status: `[{"interface":"eth0","ips":["fd00::7"],"mac":"0a:58:0a:01:00:07"}]`, wantError: true},
		{name: "ambiguous IPv4", iface: "eth0", status: `[{"interface":"eth0","ips":["10.1.0.7","10.1.0.8"],"mac":"0a:58:0a:01:00:07"}]`, wantError: true},
		{name: "ambiguous interface", iface: "eth0", status: `[{"interface":"eth0","ips":["10.1.0.7"],"mac":"0a:58:0a:01:00:07"},{"interface":"eth0","ips":["10.1.0.8"],"mac":"0a:58:0a:01:00:08"}]`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "network-status")
			if err := os.WriteFile(path, []byte(test.status), 0600); err != nil {
				t.Fatal(err)
			}
			ip, mac, err := readVMNetwork(path, test.iface)
			if test.wantError {
				if err == nil {
					t.Fatalf("accepted invalid guest network: %v / %v", ip, mac)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if ip.String() != test.ip || mac.String() != test.mac {
				t.Fatalf("guest network = %s / %s, want %s / %s", ip, mac, test.ip, test.mac)
			}
		})
	}
}

func TestReadVMNetworkSeesPublishedUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network-status")
	if _, _, err := readVMNetwork(path, "ovn-udn1"); err == nil {
		t.Fatal("accepted missing network-status file")
	}
	if err := os.WriteFile(path, []byte(`[]`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readVMNetwork(path, "ovn-udn1"); err == nil {
		t.Fatal("accepted empty network status")
	}
	updated := path + ".updated"
	if err := os.WriteFile(updated, []byte(`[{"interface":"ovn-udn1","ips":["10.193.0.139"],"mac":"0a:58:0a:c1:00:8b"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(updated, path); err != nil {
		t.Fatal(err)
	}
	ip, _, err := readVMNetwork(path, "ovn-udn1")
	if err != nil || ip.String() != "10.193.0.139" {
		t.Fatalf("published update = %v, %v", ip, err)
	}
}
