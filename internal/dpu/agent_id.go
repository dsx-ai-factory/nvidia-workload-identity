package dpu

import "fmt"

// PluginName is the SPIRE NodeAttestor plugin name ("dpu_hw").
// Minting may later align with the Identity Controller dsx_dpu parent contract.
const PluginName = "dpu_hw"

// MakeAgentID formats the SPIRE agent ID for a normalized serial:
//
//	spiffe://<trustDomain>/spire/agent/<PluginName>/<normalizedSerial>
//
// normalizedSerial must already be normalized via NormalizeSerial.
func MakeAgentID(trustDomain, normalizedSerial string) string {
	return fmt.Sprintf("spiffe://%s/spire/agent/%s/%s", trustDomain, PluginName, normalizedSerial)
}
