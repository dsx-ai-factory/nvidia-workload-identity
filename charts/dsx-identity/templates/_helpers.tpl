{{- define "dsx-identity.identityControllerName" -}}
{{- "dsx-identity-controller" -}}
{{- end -}}

{{- define "dsx-identity.identityControllerFullname" -}}
{{- printf "%s-%s" .Release.Name (include "dsx-identity.identityControllerName" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dsx-identity.identityControllerLabels" -}}
app.kubernetes.io/name: {{ include "dsx-identity.identityControllerName" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}
