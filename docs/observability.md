# Observability

First-party Go processes emit JSON logs to stdout or stderr. Chart-owned metric
endpoints support Prometheus discovery. First-party processes export traces
through their SDK and the sidecar collector installed by the DSX observability
stack. Structured allow, deny, and dependency events never include
credentials.

[SPIRE](https://spiffe.io/docs/latest/spire-about/) server, controller-manager, and the optional local SPIRE agent expose
their native Prometheus metrics through chart-owned monitors. SPIRE
does not produce traces. [SPIFFE CSI Driver](https://github.com/spiffe/spiffe-csi), CSI node-driver-registrar, and
the `socat` proxy expose no native application telemetry. Their health checks,
container logs, and platform container metrics remain available.

The DSX sidecar collector configuration has a trace pipeline and no metrics
pipeline. `dsx-token-fetcher`, `metadata-service`, and
`dsx-imds-network-setup` export traces and do not expose application metrics.

```yaml
observability:
  logLevel: info
  metrics:
    enabled: true
    serviceMonitor:
      enabled: true
```

Tracing adds the SDK and sidecar annotations shown below to chart-owned
application Pods and the SPIRE server Pod. Repository-owned SPIRE server
plugins inherit the injected server-container environment.

```yaml
metadata:
  annotations:
    instrumentation.opentelemetry.io/inject-sdk: "dsx-obs/default-instrumentation"
    sidecar.opentelemetry.io/inject: "dsx-obs/default-sidecar"
```

Pods that receive an identity container supply the same tracing annotations on
their Pod template. For token-fetcher injection:

```yaml
metadata:
  labels:
    spiffe.nvidia.com/service: dsx
  annotations:
    spiffe.nvidia.com/inject-dsx-token-fetcher: "true"
    spiffe.nvidia.com/token-audience: spiffe://ds9.dsx.nvid.id/openbao
    instrumentation.opentelemetry.io/inject-sdk: "dsx-obs/default-instrumentation"
    sidecar.opentelemetry.io/inject: "dsx-obs/default-sidecar"
```

The service label selects SPIRE registration. The injection annotation selects
the token fetcher through native admission policy. The audience annotation names
the relying party for the requested SPIRE JWT-SVID and exchanged DSX JWT-SVID.

For [KubeVirt](https://kubevirt.io/user-guide/) IMDS injection, put them on the VM Pod template:

```yaml
spec:
  template:
    metadata:
      annotations:
        spiffe.nvidia.com/inject-dsx-imds: "true"
        instrumentation.opentelemetry.io/inject-sdk: "dsx-obs/default-instrumentation"
        sidecar.opentelemetry.io/inject: "dsx-obs/default-sidecar"
```
