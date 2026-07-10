{{- define "dsx-identity.identityControllerName" -}}
{{- "dsx-identity-controller" -}}
{{- end -}}

{{- define "dsx-identity.identityControllerFullname" -}}
{{- printf "%s-%s" .Release.Name (include "dsx-identity.identityControllerName" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dsx-identity.identityControllerLabels" -}}
app.kubernetes.io/name: {{ include "dsx-identity.identityControllerName" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "dsx-identity.labels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
{{- end -}}

{{- define "dsx-identity.selectorLabels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
{{- end -}}

{{/*
Merge user Pod annotations without mutating .Values. The DSX observability
stack owns both referenced resources, as required by its onboarding guide.
*/}}
{{- define "dsx-identity.observabilityPodAnnotations" -}}
{{- $root := .root -}}
{{- $annotations := deepCopy $root.Values.observability.podAnnotations -}}
{{- $_ := set $annotations "instrumentation.opentelemetry.io/inject-sdk" "dsx-obs/default-instrumentation" -}}
{{- $_ := set $annotations "sidecar.opentelemetry.io/inject" "dsx-obs/default-sidecar" -}}
{{- $_ := set $annotations "instrumentation.opentelemetry.io/container-names" .containerNames -}}
{{- with $annotations }}
{{ toYaml . }}
{{- end -}}
{{- end -}}

{{/*
Build the standard OTel environment for one first-party process. Chart-owned
scrape targets use Prometheus so their bind address and port cannot drift from
the Service. Injected processes do not open metrics listeners in shared Pods.
*/}}
{{- define "dsx-identity.observabilityEnv" -}}
{{- $root := .root -}}
{{- $defaults := dict -}}
{{- $_ := set $defaults "OTEL_SERVICE_NAME" .serviceName -}}
{{- $_ := set $defaults "OTEL_TRACES_EXPORTER" "otlp" -}}
{{- $_ := set $defaults "OTEL_METRICS_EXPORTER" .metricsExporter -}}
{{- $_ := set $defaults "OTEL_LOGS_EXPORTER" "none" -}}
{{- $_ := set $defaults "OTEL_EXPORTER_OTLP_PROTOCOL" "grpc" -}}
{{- /* Standard OTel settings override defaults before chart-owned metrics. */ -}}
{{- $environment := mergeOverwrite $defaults (deepCopy $root.Values.observability.env) -}}
{{- if eq .metricsExporter "prometheus" -}}
{{- $_ := set $environment "OTEL_METRICS_EXPORTER" "prometheus" -}}
{{- $_ := set $environment "OTEL_EXPORTER_PROMETHEUS_HOST" "0.0.0.0" -}}
{{- $_ := set $environment "OTEL_EXPORTER_PROMETHEUS_PORT" (toString $root.Values.observability.metrics.port) -}}
{{- else if eq .metricsExporter "none" -}}
{{- /* Native and injected processes must not start a second shared listener. */ -}}
{{- $_ := set $environment "OTEL_METRICS_EXPORTER" "none" -}}
{{- end -}}
{{- /* Stable ordering keeps rendered manifests and diffs deterministic. */ -}}
{{- range $name := keys $environment | sortAlpha }}
- name: {{ $name }}
  value: {{ index $environment $name | quote }}
{{- end -}}
{{- end -}}
