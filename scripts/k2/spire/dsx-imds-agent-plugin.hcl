# External host SPIRE agent plugin configuration. Kind's in-cluster agents keep
# using k8s_psat. This fragment is packaged with a host agent outside Kubernetes.
plugins {
  NodeAttestor "dsx_imds" {
    plugin_cmd = "/usr/local/bin/dsx-imds-node-attestor-agent"
    plugin_checksum = ""
    plugin_data {
      endpoint = "http://169.254.169.254"
      audience = "spiffe://k2.sjc-1.nvidia.com/spire/server"
    }
  }
}
