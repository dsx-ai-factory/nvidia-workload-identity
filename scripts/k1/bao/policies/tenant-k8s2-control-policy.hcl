# Tenant-specific policy for k8s2-control
path "secret/tenant/k8s2-control/*" {
  capabilities = ["read", "list"]
}
