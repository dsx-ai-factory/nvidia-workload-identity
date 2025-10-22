# Product-specific policy for k8s2-control
path "secret/product/k8s2-control/*" {
  capabilities = ["read", "list"]
}

