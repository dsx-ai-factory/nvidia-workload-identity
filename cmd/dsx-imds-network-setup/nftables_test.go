// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"net"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// Record native API operations without opening a privileged netlink socket.
type testNFTRules struct {
	existing                 []*nftables.Rule
	added, replaced, deleted []*nftables.Rule
	flushes                  int
}

func (rules *testNFTRules) GetRules(*nftables.Table, *nftables.Chain) ([]*nftables.Rule, error) {
	return rules.existing, nil
}

func (rules *testNFTRules) AddRule(rule *nftables.Rule) *nftables.Rule {
	rules.added = append(rules.added, rule)
	return rule
}

func (rules *testNFTRules) ReplaceRule(rule *nftables.Rule) *nftables.Rule {
	rules.replaced = append(rules.replaced, rule)
	return rule
}

func (rules *testNFTRules) DelRule(rule *nftables.Rule) error {
	rules.deleted = append(rules.deleted, rule)
	return nil
}

func (rules *testNFTRules) Flush() error {
	rules.flushes++
	return nil
}

// Bytecode for: iifname tap0 ip daddr 169.254.169.254 ether daddr set <mac>.
// nft pads interface names to 16 bytes with zeros.
func testIMDSRule(mac net.HardwareAddr) *nftables.Rule {
	return &nftables.Rule{Handle: 7, Exprs: []expr.Any{
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{'t', 'a', 'p', '0', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
		&expr.Meta{Key: expr.MetaKeyPROTOCOL, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(unix.ETH_P_IP)},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: net.ParseIP(imdsIP).To4()},
		&expr.Immediate{Register: 1, Data: mac},
		&expr.Payload{OperationType: expr.PayloadWrite, SourceRegister: 1, Base: expr.PayloadBaseLLHeader, Offset: 0, Len: 6},
	}}
}

func TestReconcileIMDSRule(t *testing.T) {
	mac := net.HardwareAddr{2, 0, 0, 0, 0, 2}
	oldMAC := net.HardwareAddr{2, 0, 0, 0, 0, 1}
	for _, address := range []string{imdsIP, imdsIPv6} {
		t.Run(address, func(t *testing.T) {
			makeRule := func(mac net.HardwareAddr) *nftables.Rule {
				rule := testIMDSRule(mac)
				if address == imdsIPv6 {
					rule.Exprs[3].(*expr.Cmp).Data = binaryutil.BigEndian.PutUint16(unix.ETH_P_IPV6)
					rule.Exprs[4].(*expr.Payload).Offset = 24
					rule.Exprs[4].(*expr.Payload).Len = 16
					rule.Exprs[5].(*expr.Cmp).Data = net.ParseIP(imdsIPv6)
				}
				return rule
			}
			legacyRule := func(mac net.HardwareAddr) *nftables.Rule {
				rule := makeRule(mac)
				rule.Exprs = rule.Exprs[2:]
				return rule
			}
			otherFamily := testIMDSRule(mac)
			if address == imdsIP {
				otherFamily.Exprs[3].(*expr.Cmp).Data = binaryutil.BigEndian.PutUint16(unix.ETH_P_IPV6)
				otherFamily.Exprs[4].(*expr.Payload).Offset = 24
				otherFamily.Exprs[4].(*expr.Payload).Len = 16
				otherFamily.Exprs[5].(*expr.Cmp).Data = net.ParseIP(imdsIPv6)
			}
			for _, test := range []struct {
				name                 string
				existing             []*nftables.Rule
				add, replace, delete int
			}{
				{name: "fresh", add: 1},
				{name: "other family preserved", existing: []*nftables.Rule{otherFamily}, add: 1},
				{name: "correct MAC reused", existing: []*nftables.Rule{makeRule(mac)}},
				{name: "stale MAC replaced", existing: []*nftables.Rule{makeRule(oldMAC)}, replace: 1},
				{name: "duplicate removed", existing: []*nftables.Rule{makeRule(mac), makeRule(oldMAC)}, delete: 1},
				{name: "legacy replaced", existing: []*nftables.Rule{legacyRule(mac)}, replace: 1},
				{name: "legacy stale MAC replaced", existing: []*nftables.Rule{legacyRule(oldMAC)}, replace: 1},
				{name: "legacy duplicate removed", existing: []*nftables.Rule{legacyRule(mac), legacyRule(oldMAC)}, replace: 1, delete: 1},
				{name: "legacy before scoped", existing: []*nftables.Rule{legacyRule(mac), makeRule(mac)}, replace: 1, delete: 1},
				{name: "legacy after scoped", existing: []*nftables.Rule{makeRule(mac), legacyRule(mac)}, delete: 1},
			} {
				t.Run(test.name, func(t *testing.T) {
					rules := &testNFTRules{existing: test.existing}
					if err := nftAddIMDSRule(rules, nil, nil, mac, net.ParseIP(address)); err != nil {
						t.Fatal(err)
					}
					if len(rules.added) != test.add || len(rules.replaced) != test.replace || len(rules.deleted) != test.delete || rules.flushes != 1 {
						t.Fatalf("operations: add=%d replace=%d delete=%d flush=%d", len(rules.added), len(rules.replaced), len(rules.deleted), rules.flushes)
					}
					for _, rule := range append(rules.added, rules.replaced...) {
						checkIMDSIngress(t, rule, address, mac)
					}
					if test.replace != 0 && rules.replaced[0].Handle != 7 {
						t.Fatal("replacement lost the existing rule handle")
					}
					if test.delete != 0 && rules.deleted[0] != test.existing[1] {
						t.Fatal("deleted the retained rule instead of its duplicate")
					}
				})
			}
		})
	}
}

func TestPreserveOtherNFTRules(t *testing.T) {
	mac := net.HardwareAddr{2, 0, 0, 0, 0, 2}
	for _, test := range []struct {
		name   string
		change func(*nftables.Rule)
	}{
		{"other ingress", func(rule *nftables.Rule) { rule.Exprs[1].(*expr.Cmp).Data = []byte("tap1\x00") }},
		{"wrong ingress match", func(rule *nftables.Rule) { rule.Exprs[1].(*expr.Cmp).Op = expr.CmpOpNeq }},
		{"same IP comparison alone", func(rule *nftables.Rule) { rule.Exprs = rule.Exprs[5:6] }},
		{"wrong protocol", func(rule *nftables.Rule) {
			rule.Exprs[3].(*expr.Cmp).Data = binaryutil.BigEndian.PutUint16(unix.ETH_P_IPV6)
		}},
		{"wrong match", func(rule *nftables.Rule) { rule.Exprs[5].(*expr.Cmp).Op = expr.CmpOpNeq }},
		{"wrong IP", func(rule *nftables.Rule) { rule.Exprs[5].(*expr.Cmp).Data = net.ParseIP("10.1.0.7").To4() }},
		{"wrong IP offset", func(rule *nftables.Rule) { rule.Exprs[4].(*expr.Payload).Offset = 12 }},
		{"wrong action", func(rule *nftables.Rule) { rule.Exprs[7].(*expr.Payload).OperationType = expr.PayloadLoad }},
		{"wrong write register", func(rule *nftables.Rule) { rule.Exprs[7].(*expr.Payload).SourceRegister = 2 }},
		{"wrong MAC register", func(rule *nftables.Rule) { rule.Exprs[6].(*expr.Immediate).Register = 2 }},
		{"invalid MAC length", func(rule *nftables.Rule) { rule.Exprs[6].(*expr.Immediate).Data = []byte{2, 0, 0, 0, 0} }},
		{"other expression", func(rule *nftables.Rule) { rule.Exprs[6] = &expr.Counter{} }},
		{"extra expression", func(rule *nftables.Rule) { rule.Exprs = append(rule.Exprs, &expr.Counter{}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			foreign := testIMDSRule(mac)
			test.change(foreign)
			rules := &testNFTRules{existing: []*nftables.Rule{foreign}}
			if err := nftAddIMDSRule(rules, nil, nil, mac, net.ParseIP(imdsIP)); err != nil {
				t.Fatal(err)
			}
			if len(rules.added) != 1 || len(rules.replaced) != 0 || len(rules.deleted) != 0 || rules.flushes != 1 {
				t.Fatalf("foreign rule suppressed setup or was modified: add=%d replace=%d delete=%d flush=%d", len(rules.added), len(rules.replaced), len(rules.deleted), rules.flushes)
			}
		})
	}
}

func TestRejectInvalidIMDSAddress(t *testing.T) {
	rules := &testNFTRules{}
	if err := nftAddIMDSRule(rules, nil, nil, net.HardwareAddr{2, 0, 0, 0, 0, 2}, nil); err == nil {
		t.Fatal("accepted missing metadata IP")
	}
	if len(rules.added) != 0 || rules.flushes != 0 {
		t.Fatal("invalid address changed rules")
	}
}

// Interpret the rule's expressions so packet tests can run without privileges.
func evaluateIMDSRule(t *testing.T, rule *nftables.Rule, ingress string, frame []byte) {
	t.Helper()
	var register [16]byte
	for _, expression := range rule.Exprs {
		switch e := expression.(type) {
		case *expr.Meta:
			if e.Register != 1 || e.SourceRegister {
				t.Fatalf("unsupported meta register: %#v", e)
			}
			clear(register[:])
			switch e.Key {
			case expr.MetaKeyIIFNAME:
				copy(register[:], ingress)
			case expr.MetaKeyPROTOCOL:
				copy(register[:], frame[12:14])
			default:
				t.Fatalf("unsupported meta key: %v", e.Key)
			}
		case *expr.Cmp:
			if e.Register != 1 || e.Op != expr.CmpOpEq {
				t.Fatalf("unsupported comparison: %#v", e)
			}
			if !bytes.Equal(register[:len(e.Data)], e.Data) {
				return
			}
		case *expr.Immediate:
			if e.Register != 1 {
				t.Fatalf("unsupported immediate register: %#v", e)
			}
			copy(register[:], e.Data)
		case *expr.Payload:
			offset := int(e.Offset)
			switch e.Base {
			case expr.PayloadBaseNetworkHeader:
				offset += 14
			case expr.PayloadBaseLLHeader:
			default:
				t.Fatalf("unsupported payload base: %v", e.Base)
			}
			payload := frame[offset : offset+int(e.Len)]
			if e.OperationType == expr.PayloadWrite && e.SourceRegister == 1 {
				copy(payload, register[:e.Len])
			} else if e.OperationType == expr.PayloadLoad && e.DestRegister == 1 {
				copy(register[:], payload)
			} else {
				t.Fatalf("unsupported payload operation: %#v", e)
			}
		default:
			t.Fatalf("unsupported expression: %T", expression)
		}
	}
}

func checkIMDSIngress(t *testing.T, rule *nftables.Rule, address string, mac net.HardwareAddr) {
	t.Helper()
	for _, ingress := range []string{"tap0", "tap1", "tap00", "eth0", "imds-vm", "k6t-net1", ""} {
		for _, destination := range []string{address, "192.0.2.1", "2001:db8::1"} {
			frame := make([]byte, 14+40)
			gateway := net.HardwareAddr{2, 0, 0, 0, 0, 99}
			copy(frame, gateway)
			ip := net.ParseIP(destination)
			if ipv4 := ip.To4(); ipv4 != nil {
				copy(frame[12:14], binaryutil.BigEndian.PutUint16(unix.ETH_P_IP))
				frame[14] = 0x45
				copy(frame[14+16:], ipv4)
			} else {
				copy(frame[12:14], binaryutil.BigEndian.PutUint16(unix.ETH_P_IPV6))
				frame[14] = 0x60
				copy(frame[14+24:], ip.To16())
			}
			want := bytes.Clone(frame)
			if ingress == "tap0" && destination == address {
				copy(want, mac)
			}
			evaluateIMDSRule(t, rule, ingress, frame)
			if !bytes.Equal(frame, want) {
				t.Fatalf("ingress %q destination %s: frame = %x, want %x", ingress, destination, frame, want)
			}
		}
	}
}
