# Region-specific policy for sjc-1
path "secret/region/sjc-1/*" {
  capabilities = ["read", "list"]
}
