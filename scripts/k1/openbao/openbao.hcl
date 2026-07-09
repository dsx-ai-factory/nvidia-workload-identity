ui = false

listener "tcp" {
  tls_disable     = 1
  address         = "[::]:8200"
  cluster_address = "[::]:8201"
}

storage "file" {
  path = "/openbao/data"
}

seal "static" {
  current_key_id = "dsx-identity-e2e-v1"
  current_key    = "env://OPENBAO_STATIC_SEAL_KEY"
}

initialize "mounts" {
  request "enable-kv" {
    operation = "update"
    path      = "sys/mounts/secret"
    data = {
      type = "kv"
      options = {
        version = "2"
      }
    }
  }

  request "enable-jwt" {
    operation = "update"
    path      = "sys/auth/jwt"
    data = {
      type = "jwt"
    }
  }
}

initialize "policies" {
  request "spiffe-authenticated" {
    operation = "update"
    path      = "sys/policies/acl/spiffe-authenticated"
    data = {
      policy = {
        eval_type   = "string"
        eval_source = "file"
        path        = "/openbao/policies/spiffe-authenticated-policy.hcl"
      }
    }
  }

  request "region-ds9" {
    operation = "update"
    path      = "sys/policies/acl/region-ds9"
    data = {
      policy = {
        eval_type   = "string"
        eval_source = "file"
        path        = "/openbao/policies/region-ds9-policy.hcl"
      }
    }
  }

  request "tenant-nca-0000000000000001" {
    operation = "update"
    path      = "sys/policies/acl/tenant-nca-0000000000000001"
    data = {
      policy = {
        eval_type   = "string"
        eval_source = "file"
        path        = "/openbao/policies/tenant-nca-0000000000000001-policy.hcl"
      }
    }
  }

  request "service-dsx" {
    operation = "update"
    path      = "sys/policies/acl/service-dsx"
    data = {
      policy = {
        eval_type   = "string"
        eval_source = "file"
        path        = "/openbao/policies/service-dsx-policy.hcl"
      }
    }
  }

  request "resource-type-dpu" {
    operation = "update"
    path      = "sys/policies/acl/resource-type-dpu"
    data = {
      policy = {
        eval_type   = "string"
        eval_source = "file"
        path        = "/openbao/policies/resource-type-dpu-policy.hcl"
      }
    }
  }

  request "resource-type-host" {
    operation = "update"
    path      = "sys/policies/acl/resource-type-host"
    data = {
      policy = {
        eval_type   = "string"
        eval_source = "file"
        path        = "/openbao/policies/resource-type-host-policy.hcl"
      }
    }
  }

  request "resource-type-vm" {
    operation = "update"
    path      = "sys/policies/acl/resource-type-vm"
    data = {
      policy = {
        eval_type   = "string"
        eval_source = "file"
        path        = "/openbao/policies/resource-type-vm-policy.hcl"
      }
    }
  }
}

initialize "jwt" {
  request "configure-jwt" {
    operation = "update"
    path      = "auth/jwt/config"
    data = {
      jwks_url             = "http://spiffe-exchange.dsx-identity.svc.cluster.local:8080/.well-known/jwks.json"
      bound_issuer         = "spiffe://ds9.dsx.nvid.id"
      jwt_supported_algs   = ["ES256"]
      skip_jwks_validation = true
    }
  }

  request "configure-cel-role" {
    operation = "update"
    path      = "auth/jwt/cel/role/basic-spiffe"
    data = {
      name = "basic-spiffe"
      cel_program = {
        variables = [
          {
            name       = "is_spiffe"
            expression = "claims.sub.matches(r'^spiffe://nca-[0-9]{16}\\.ds9\\.dsx\\.nvid\\.id/(dpu|host|vm)/[A-Za-z0-9][A-Za-z0-9._:-]*(/(process/[A-Za-z0-9][A-Za-z0-9._:-]*|pod/[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}))?(/role/[a-z0-9][a-z0-9_-]{0,62})?$')"
          },
          {
            name       = "uri_parts"
            expression = "is_spiffe ? claims.sub.split('://') : []"
          },
          {
            name       = "authority_path"
            expression = "is_spiffe ? uri_parts[1].split('/') : []"
          },
          {
            name       = "domain_parts"
            expression = "is_spiffe ? authority_path[0].split('.') : []"
          },
          {
            name       = "tenant_id"
            expression = "is_spiffe ? domain_parts[0] : ''"
          },
          {
            name       = "region"
            expression = "is_spiffe ? domain_parts[1] : ''"
          },
          {
            name       = "service"
            expression = "is_spiffe ? domain_parts[2] : ''"
          },
          {
            name       = "resource_type"
            expression = "is_spiffe ? authority_path[1] : ''"
          },
          {
            name       = "resource_id"
            expression = "is_spiffe ? authority_path[2] : ''"
          },
          {
            name       = "role"
            expression = "!is_spiffe ? '' : size(authority_path) == 5 && authority_path[3] == 'role' ? authority_path[4] : size(authority_path) == 7 ? authority_path[6] : ''"
          },
          {
            name       = "base_policies"
            expression = "is_spiffe ? ['spiffe-authenticated', 'region-' + region, 'tenant-' + tenant_id, 'service-' + service, 'resource-type-' + resource_type] : []"
          }
        ]
        expression = "is_spiffe ? pb.Auth{policies: base_policies} : false"
      }
      expiration_leeway = 150
      not_before_leeway = 150
      clock_skew_leeway = 60
      bound_audiences   = ["spiffe://ds9.dsx.nvid.id/openbao"]
    }
  }
}

initialize "fixtures" {
  request "region-ds9" {
    operation = "update"
    path      = "secret/data/region/ds9/e2e"
    data = {
      data = { value = "region-ds9" }
    }
  }

  request "service-dsx" {
    operation = "update"
    path      = "secret/data/service/dsx/e2e"
    data = {
      data = { value = "service-dsx" }
    }
  }

  request "tenant-nca-0000000000000001" {
    operation = "update"
    path      = "secret/data/tenant/nca-0000000000000001/e2e"
    data = {
      data = { value = "tenant-1" }
    }
  }

  request "resource-type-dpu" {
    operation = "update"
    path      = "secret/data/resource-type/dpu/e2e"
    data = {
      data = { value = "resource-dpu" }
    }
  }

  request "resource-type-host" {
    operation = "update"
    path      = "secret/data/resource-type/host/e2e"
    data = {
      data = { value = "resource-host" }
    }
  }

  request "resource-type-vm" {
    operation = "update"
    path      = "secret/data/resource-type/vm/e2e"
    data = {
      data = { value = "resource-vm" }
    }
  }

  request "forbidden-admin" {
    operation = "update"
    path      = "secret/data/forbidden/admin/e2e"
    data = {
      data = { value = "forbidden" }
    }
  }
}
