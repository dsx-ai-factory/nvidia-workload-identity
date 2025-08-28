# Resource ID policy for demo-nginx
path "secret/resource-id/demo-nginx/*" {
  capabilities = ["read", "list"]
}
