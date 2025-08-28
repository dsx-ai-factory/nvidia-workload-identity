# Product-specific policy for default namespace workloads
path "secret/product/default/*" {
  capabilities = ["read", "list"]
}
