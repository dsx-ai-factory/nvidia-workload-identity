{{- define "dsx-spiffe-csi.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{ include "dsx-spiffe-csi.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "dsx-spiffe-csi.selectorLabels" -}}
app.kubernetes.io/name: dsx-spiffe-csi
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "dsx-spiffe-csi.resources" -}}
{{- if .serviceDaemonSet }}
limits:
  {{- toYaml .serviceDaemonSet | nindent 2 }}
requests:
  {{- toYaml .serviceDaemonSet | nindent 2 }}
{{- else }}
{{- toYaml .defaults }}
{{- end }}
{{- end }}
