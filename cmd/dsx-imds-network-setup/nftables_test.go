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

// Bytecode for: ip daddr 169.254.169.254 ether daddr set <mac>.
func testIMDSRule(mac net.HardwareAddr) *nftables.Rule {
	return &nftables.Rule{Handle: 7, Exprs: []expr.Any{
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
	for _, test := range []struct {
		name                 string
		existing             []*nftables.Rule
		add, replace, delete int
	}{
		{name: "fresh", add: 1},
		{name: "correct MAC reused", existing: []*nftables.Rule{testIMDSRule(mac)}},
		{name: "stale MAC replaced", existing: []*nftables.Rule{testIMDSRule(oldMAC)}, replace: 1},
		{name: "duplicate removed", existing: []*nftables.Rule{testIMDSRule(mac), testIMDSRule(oldMAC)}, delete: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			rules := &testNFTRules{existing: test.existing}
			if err := nftAddIMDSRule(rules, nil, nil, mac); err != nil {
				t.Fatal(err)
			}
			if len(rules.added) != test.add || len(rules.replaced) != test.replace || len(rules.deleted) != test.delete || rules.flushes != 1 {
				t.Fatalf("operations: add=%d replace=%d delete=%d flush=%d", len(rules.added), len(rules.replaced), len(rules.deleted), rules.flushes)
			}
			for _, rule := range append(rules.added, rules.replaced...) {
				rewrite := rule.Exprs[4].(*expr.Immediate)
				if !bytes.Equal(rewrite.Data, mac) {
					t.Fatalf("rewrite MAC = %s, want %s", net.HardwareAddr(rewrite.Data), mac)
				}
			}
			if test.replace != 0 && rules.replaced[0].Handle != 7 {
				t.Fatal("replacement lost the existing rule handle")
			}
			if test.delete != 0 && rules.deleted[0] != test.existing[1] {
				t.Fatal("deleted the retained rule instead of its duplicate")
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
		{"same IP comparison alone", func(rule *nftables.Rule) { rule.Exprs = rule.Exprs[3:4] }},
		{"wrong protocol", func(rule *nftables.Rule) {
			rule.Exprs[1].(*expr.Cmp).Data = binaryutil.BigEndian.PutUint16(unix.ETH_P_IPV6)
		}},
		{"wrong match", func(rule *nftables.Rule) { rule.Exprs[3].(*expr.Cmp).Op = expr.CmpOpNeq }},
		{"wrong IP", func(rule *nftables.Rule) { rule.Exprs[3].(*expr.Cmp).Data = net.ParseIP("10.1.0.7").To4() }},
		{"wrong IP offset", func(rule *nftables.Rule) { rule.Exprs[2].(*expr.Payload).Offset = 12 }},
		{"wrong action", func(rule *nftables.Rule) { rule.Exprs[5].(*expr.Payload).OperationType = expr.PayloadLoad }},
		{"wrong write register", func(rule *nftables.Rule) { rule.Exprs[5].(*expr.Payload).SourceRegister = 2 }},
		{"wrong MAC register", func(rule *nftables.Rule) { rule.Exprs[4].(*expr.Immediate).Register = 2 }},
		{"invalid MAC length", func(rule *nftables.Rule) { rule.Exprs[4].(*expr.Immediate).Data = []byte{2, 0, 0, 0, 0} }},
		{"other expression", func(rule *nftables.Rule) { rule.Exprs[4] = &expr.Counter{} }},
		{"extra expression", func(rule *nftables.Rule) { rule.Exprs = append(rule.Exprs, &expr.Counter{}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			foreign := testIMDSRule(mac)
			test.change(foreign)
			rules := &testNFTRules{existing: []*nftables.Rule{foreign}}
			if err := nftAddIMDSRule(rules, nil, nil, mac); err != nil {
				t.Fatal(err)
			}
			if len(rules.added) != 1 || len(rules.replaced) != 0 || len(rules.deleted) != 0 || rules.flushes != 1 {
				t.Fatalf("foreign rule suppressed setup or was modified: add=%d replace=%d delete=%d flush=%d", len(rules.added), len(rules.replaced), len(rules.deleted), rules.flushes)
			}
		})
	}
}
