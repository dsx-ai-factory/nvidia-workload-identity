# Service-specific policy for internal workloads
path "secret/service/internal/*" {
  capabilities = ["read", "list"]
}
