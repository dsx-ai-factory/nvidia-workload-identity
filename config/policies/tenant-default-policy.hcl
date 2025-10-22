# Tenant-specific policy for default namespace
path "secret/tenant/default/*" {
  capabilities = ["read", "list"]
}

