// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestReadVMNetwork(t *testing.T) {
	for _, test := range []struct {
		name, networks, ip, mac string
		wantError               bool
	}{
		{
			name:     "bridge",
			networks: `{"default":{"role":"primary","ip_addresses":["10.1.0.7/24"],"mac_address":"0a:58:0a:01:00:07"}}`,
			ip:       "[10.1.0.7]", mac: "0a:58:0a:01:00:07",
		},
		{
			name: "l2bridge with infrastructure and secondary networks",
			networks: `{"default":{"role":"infrastructure-locked","ip_addresses":["172.16.3.16/26"],"mac_address":"0a:58:ac:10:03:10"},
			"tenant/primary":{"role":"primary","ip_addresses":["fd00::139/64","10.193.0.139/29"],"mac_address":"0a:58:0a:c1:00:8b"},
			"tenant/secondary":{"role":"secondary","ip_addresses":["10.2.0.7/24"],"mac_address":"0a:58:0a:02:00:07"}}`,
			ip: "[fd00::139 10.193.0.139]", mac: "0a:58:0a:c1:00:8b",
		},
		{name: "missing primary network", networks: `{"default":{"role":"infrastructure-locked","ip_addresses":["172.16.3.16/26"],"mac_address":"0a:58:ac:10:03:10"}}`, wantError: true},
		{name: "missing role", networks: `{"default":{"ip_addresses":["10.1.0.7/24"],"mac_address":"0a:58:0a:01:00:07"}}`, wantError: true},
		{name: "malformed JSON", networks: `{`, wantError: true},
		{name: "missing MAC", networks: `{"default":{"role":"primary","ip_addresses":["10.1.0.7/24"]}}`, wantError: true},
		{name: "invalid MAC", networks: `{"default":{"role":"primary","ip_addresses":["10.1.0.7/24"],"mac_address":"bad"}}`, wantError: true},
		{name: "invalid CIDR", networks: `{"default":{"role":"primary","ip_addresses":["10.1.0.7/99"],"mac_address":"0a:58:0a:01:00:07"}}`, wantError: true},
		{name: "IPv6 only", networks: `{"default":{"role":"primary","ip_addresses":["fd00::7/64"],"mac_address":"0a:58:0a:01:00:07"}}`, ip: "[fd00::7]", mac: "0a:58:0a:01:00:07"},
		{name: "ambiguous IPv4", networks: `{"default":{"role":"primary","ip_addresses":["10.1.0.7/24","10.1.0.8/24"],"mac_address":"0a:58:0a:01:00:07"}}`, wantError: true},
		{name: "ambiguous IPv6", networks: `{"default":{"role":"primary","ip_addresses":["fd00::7/64","fd00::8/64"],"mac_address":"0a:58:0a:01:00:07"}}`, wantError: true},
		{name: "missing addresses", networks: `{"default":{"role":"primary","ip_addresses":[],"mac_address":"0a:58:0a:01:00:07"}}`, wantError: true},
		{name: "invalid IPv6", networks: `{"default":{"role":"primary","ip_addresses":["fd00::7/129"],"mac_address":"0a:58:0a:01:00:07"}}`, wantError: true},
		{name: "invalid IPv6 beside IPv4", networks: `{"default":{"role":"primary","ip_addresses":["10.1.0.7/24","fd00::7/129"],"mac_address":"0a:58:0a:01:00:07"}}`, wantError: true},
		{name: "ambiguous primary", networks: `{"default":{"role":"primary","ip_addresses":["10.1.0.7/24"],"mac_address":"0a:58:0a:01:00:07"},"tenant/primary":{"role":"primary","ip_addresses":["10.1.0.8/24"],"mac_address":"0a:58:0a:01:00:08"}}`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pod-networks")
			if err := os.WriteFile(path, []byte(test.networks), 0600); err != nil {
				t.Fatal(err)
			}
			ip, mac, err := readVMNetwork(path)
			if test.wantError {
				if err == nil {
					t.Fatalf("accepted invalid guest network: %v / %v", ip, mac)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(ip) != test.ip || mac.String() != test.mac {
				t.Fatalf("guest network = %s / %s, want %s / %s", ip, mac, test.ip, test.mac)
			}
		})
	}
}

func TestReadVMNetworkSeesPublishedUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pod-networks")
	if _, _, err := readVMNetwork(path); err == nil {
		t.Fatal("accepted missing pod-networks file")
	}
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readVMNetwork(path); err == nil {
		t.Fatal("accepted empty OVN networks")
	}
	updated := path + ".updated"
	if err := os.WriteFile(updated, []byte(`{"tenant/primary":{"role":"primary","ip_addresses":["10.193.0.139/29"],"mac_address":"0a:58:0a:c1:00:8b"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(updated, path); err != nil {
		t.Fatal(err)
	}
	ip, _, err := readVMNetwork(path)
	if err != nil || fmt.Sprint(ip) != "[10.193.0.139]" {
		t.Fatalf("published update = %v, %v", ip, err)
	}
}
