# Service policy for DSX identities
path "secret/data/service/dsx/*" {
  capabilities = ["read"]
}
