// Command dsx-dpu-node-attestor-server is the server-side SPIRE NodeAttestor
// plugin for DPU node attestation (plugin name dpu_hw). SPIRE Server launches
// it as an external plugin.
package main

import (
	"dsx-identity/internal/dpu"

	"github.com/spiffe/spire-plugin-sdk/pluginmain"
	nodeattestorv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/plugin/server/nodeattestor/v1"
	configv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/service/common/config/v1"
)

func main() {
	plugin := dpu.New()
	pluginmain.Serve(
		nodeattestorv1.NodeAttestorPluginServer(plugin),
		configv1.ConfigServiceServer(plugin),
	)
}
