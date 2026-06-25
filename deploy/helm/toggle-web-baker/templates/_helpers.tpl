{{/*
App name (overridable). Uses a literal base, NOT .Chart.Name, so the chart's
-helm OCI suffix does not leak into resource names.
*/}}
{{- define "toggle-web-baker.name" -}}
{{- default "toggle-web-baker" .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully qualified app name. */}}
{{- define "toggle-web-baker.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default "toggle-web-baker" .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "toggle-web-baker.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Common labels. */}}
{{- define "toggle-web-baker.labels" -}}
helm.sh/chart: {{ include "toggle-web-baker.chart" . }}
{{ include "toggle-web-baker.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "toggle-web-baker.selectorLabels" -}}
app.kubernetes.io/name: {{ include "toggle-web-baker.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* Operator object name + its ServiceAccount. */}}
{{- define "toggle-web-baker.operator.fullname" -}}
{{- printf "%s-operator" (include "toggle-web-baker.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Console object name. */}}
{{- define "toggle-web-baker.console.fullname" -}}
{{- printf "%s-console" (include "toggle-web-baker.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "toggle-web-baker.oauth2.fullname" -}}
{{- printf "%s-oauth2-proxy" (include "toggle-web-baker.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Render an image ref "repository:tag", defaulting tag to the chart appVersion.
Usage: include "toggle-web-baker.image" (dict "image" .Values.x.image "root" $)
*/}}
{{- define "toggle-web-baker.image" -}}
{{- $tag := .image.tag | default .root.Chart.AppVersion -}}
{{- printf "%s:%s" .image.repository $tag -}}
{{- end -}}
