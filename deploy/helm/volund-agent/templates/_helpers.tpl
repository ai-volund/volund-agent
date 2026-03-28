{{- define "volund-agent.labels" -}}
app.kubernetes.io/name: volund-agent
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "volund-agent.selectorLabels" -}}
app.kubernetes.io/name: volund-agent
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "volund-agent.fullname" -}}
{{- if contains .Chart.Name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "volund-agent.imageTag" -}}
{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}
