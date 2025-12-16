# Resource type policy for pods
path "secret/resource-type/pod/*" {
  capabilities = ["read", "list"] 
}
