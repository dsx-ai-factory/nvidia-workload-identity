{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/}}

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

{{- define "dsx-identity.defaultServiceName" -}}
{{- $service := required "global.dsx.defaultServiceName is required" .Values.global.dsx.defaultServiceName -}}
{{- if not (regexMatch "^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$" $service) -}}
{{- fail "global.dsx.defaultServiceName must be a DNS label" -}}
{{- end -}}
{{- $service -}}
{{- end -}}

{{- define "dsx-identity.operatorTenant" -}}
{{- $tenant := required "global.operatorTenant is required" .Values.global.operatorTenant -}}
{{- if not (regexMatch "^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$" $tenant) -}}
{{- fail "global.operatorTenant must be a DNS label" -}}
{{- end -}}
{{- $controllerTenant := "" -}}
{{- $spireServer := index .Values.spire "spire-server" -}}
{{- range $spireServer.controllerManager.extraEnv -}}
{{- if eq .name "OPERATOR_TENANT" -}}
{{- $controllerTenant = required "spire.spire-server.controllerManager.extraEnv OPERATOR_TENANT value is required" .value -}}
{{- end -}}
{{- end -}}
{{- if ne $controllerTenant $tenant -}}
{{- fail "global.operatorTenant must match spire.spire-server.controllerManager.extraEnv OPERATOR_TENANT" -}}
{{- end -}}
{{- $tenant -}}
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
Build the environment for one first-party process. Chart-owned
scrape targets use Prometheus so their bind address and port cannot drift from
the Service. Injected processes do not open metrics listeners in shared Pods.
*/}}
{{- define "dsx-identity.observabilityEnv" -}}
{{- $root := .root -}}
{{- $defaults := dict -}}
{{- $_ := set $defaults "OTEL_SERVICE_NAME" .serviceName -}}
{{- $_ := set $defaults "OTEL_METRICS_EXPORTER" .metricsExporter -}}
{{- /* User environment settings override defaults before chart-owned metrics. */ -}}
{{- $environment := mergeOverwrite $defaults (deepCopy $root.Values.observability.env) -}}
{{- if eq .metricsExporter "prometheus" -}}
{{- $_ := set $environment "OTEL_METRICS_EXPORTER" "prometheus" -}}
{{- $_ := set $environment "OTEL_EXPORTER_PROMETHEUS_HOST" "0.0.0.0" -}}
{{- $_ := set $environment "OTEL_EXPORTER_PROMETHEUS_PORT" (toString $root.Values.observability.metrics.port) -}}
{{- else if eq .metricsExporter "none" -}}
{{- /* Native and injected processes must not start a second shared listener. */ -}}
{{- $_ := set $environment "OTEL_METRICS_EXPORTER" "none" -}}
{{- end -}}
{{- /* objectType selects typed CEL output; ordinary workloads receive YAML. */ -}}
{{- /* Stable ordering keeps rendered manifests and diffs deterministic. */ -}}
{{- if .objectType }}
[
{{- range $index, $name := keys $environment | sortAlpha }}{{ if $index }},{{ end }}
{{ $.objectType }}{
  name: {{ $name | quote }},
  value: {{ index $environment $name | quote }}
}
{{- end }}
]
{{- else }}
{{- range $name := keys $environment | sortAlpha }}
- name: {{ $name }}
  value: {{ index $environment $name | quote }}
{{- end -}}
{{- end -}}
{{- end -}}
