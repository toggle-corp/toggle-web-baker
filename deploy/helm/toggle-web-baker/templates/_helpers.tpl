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

{{/*
Render operator.nodeImages as the compact JSON the -node-images flag expects:
{"<major>":{"image":"repo:tag","runAsUser":N,"home":"..."}}. Each entry's image
is resolved through toggle-web-baker.image (tag defaults to appVersion). runAsUser
and home are emitted only when set. Empty map renders "{}".
Usage: include "toggle-web-baker.nodeImagesJSON" $
*/}}
{{- define "toggle-web-baker.nodeImagesJSON" -}}
{{- $root := . -}}
{{- $out := dict -}}
{{- range $major, $cfg := .Values.operator.nodeImages -}}
{{- $entry := dict "image" (include "toggle-web-baker.image" (dict "image" $cfg "root" $root)) -}}
{{- if hasKey $cfg "runAsUser" }}{{- $entry = set $entry "runAsUser" $cfg.runAsUser -}}{{- end -}}
{{- if $cfg.home }}{{- $entry = set $entry "home" $cfg.home -}}{{- end -}}
{{- $out = set $out (printf "%v" $major) $entry -}}
{{- end -}}
{{- $out | toJson -}}
{{- end -}}

{{/*
Convert a duration string like "90s", "10m" or "1h" to integer seconds (for
PromQL arithmetic, which has no duration literals in expressions). Fails the
render on anything else.
Usage: include "toggle-web-baker.durationSeconds" .Values.monitoring.alerts.stuckBuildGrace
*/}}
{{- define "toggle-web-baker.durationSeconds" -}}
{{- $d := printf "%v" . -}}
{{- if not (regexMatch "^[0-9]+[smh]$" $d) -}}
{{- fail (printf "unparsable duration %q: expected <integer><s|m|h>, e.g. 90s, 10m, 1h" $d) -}}
{{- end -}}
{{- $n := regexFind "^[0-9]+" $d | int64 -}}
{{- $unit := regexFind "[smh]$" $d -}}
{{- if eq $unit "h" -}}{{ mul $n 3600 }}{{- else if eq $unit "m" -}}{{ mul $n 60 }}{{- else -}}{{ $n }}{{- end -}}
{{- end -}}

{{/*
The three SENTRY_* env list items shared by the operator and console
containers. Callers gate on .Values.sentry.dsn and provide the per-binary
image tag for SENTRY_RELEASE (falls back to the chart appVersion).
Usage: include "toggle-web-baker.sentryEnv" (dict "root" $ "tag" .Values.operator.image.tag) | nindent 12
*/}}
{{- define "toggle-web-baker.sentryEnv" -}}
- name: SENTRY_DSN
  value: {{ .root.Values.sentry.dsn | quote }}
- name: SENTRY_ENVIRONMENT
  value: {{ .root.Values.sentry.environment | quote }}
- name: SENTRY_RELEASE
  value: {{ .tag | default .root.Chart.AppVersion | quote }}
{{- end -}}
