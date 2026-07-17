# Example tenant policy
path "secret/data/tenant/operator-1/*" {
  capabilities = ["read"]
}
