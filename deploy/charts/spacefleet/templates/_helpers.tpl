{{/*
Expand the name of the chart.
*/}}
{{- define "spacefleet.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully qualified app name. Truncated at 63 chars for k8s name limits.
*/}}
{{- define "spacefleet.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "spacefleet.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels.
*/}}
{{- define "spacefleet.labels" -}}
helm.sh/chart: {{ include "spacefleet.chart" . }}
{{ include "spacefleet.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels (immutable; do not add version here).
*/}}
{{- define "spacefleet.selectorLabels" -}}
app.kubernetes.io/name: {{ include "spacefleet.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "spacefleet.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "spacefleet.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Resolved image reference. Tag falls back to the chart appVersion.
*/}}
{{- define "spacefleet.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{/*
Name of the chart-managed env Secret (DATABASE_URL / REDIS_URL).
*/}}
{{- define "spacefleet.envSecretName" -}}
{{- printf "%s-env" (include "spacefleet.fullname" .) -}}
{{- end -}}

{{/*
Names of the bundled datastore StatefulSets/Services.
*/}}
{{- define "spacefleet.postgresql.fullname" -}}
{{- printf "%s-postgresql" (include "spacefleet.fullname" .) -}}
{{- end -}}

{{- define "spacefleet.redis.fullname" -}}
{{- printf "%s-redis" (include "spacefleet.fullname" .) -}}
{{- end -}}

{{/*
DATABASE_URL resolution
------------------------
Constructed DSN for the bundled Postgres, else the user-provided DSN.
*/}}
{{- define "spacefleet.databaseURL" -}}
{{- if .Values.postgresql.enabled -}}
{{- $a := .Values.postgresql.auth -}}
{{- printf "postgres://%s:%s@%s:5432/%s?sslmode=disable" $a.username $a.password (include "spacefleet.postgresql.fullname" .) $a.database -}}
{{- else -}}
{{- .Values.externalDatabase.url -}}
{{- end -}}
{{- end -}}

{{/*
True when the chart owns the Secret entry for DATABASE_URL (bundled Postgres
or an inline external url). False when referencing a user-supplied Secret.
*/}}
{{- define "spacefleet.manageDatabaseSecret" -}}
{{- if and (not .Values.postgresql.enabled) .Values.externalDatabase.existingSecret -}}false{{- else -}}true{{- end -}}
{{- end -}}

{{/*
Secret name/key to source DATABASE_URL from, as an env entry.
*/}}
{{- define "spacefleet.databaseEnv" -}}
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
{{- if eq (include "spacefleet.manageDatabaseSecret" .) "true" }}
      name: {{ include "spacefleet.envSecretName" . }}
      key: DATABASE_URL
{{- else }}
      name: {{ .Values.externalDatabase.existingSecret }}
      key: {{ .Values.externalDatabase.existingSecretKey }}
{{- end }}
{{- end -}}

{{/*
REDIS_URL resolution (mirrors DATABASE_URL).
*/}}
{{- define "spacefleet.redisURL" -}}
{{- if .Values.redis.enabled -}}
{{- if .Values.redis.auth.enabled -}}
{{- printf "redis://:%s@%s:6379/0" .Values.redis.auth.password (include "spacefleet.redis.fullname" .) -}}
{{- else -}}
{{- printf "redis://%s:6379/0" (include "spacefleet.redis.fullname" .) -}}
{{- end -}}
{{- else -}}
{{- .Values.externalRedis.url -}}
{{- end -}}
{{- end -}}

{{- define "spacefleet.manageRedisSecret" -}}
{{- if and (not .Values.redis.enabled) .Values.externalRedis.existingSecret -}}false{{- else -}}true{{- end -}}
{{- end -}}

{{- define "spacefleet.redisEnv" -}}
- name: REDIS_URL
  valueFrom:
    secretKeyRef:
{{- if eq (include "spacefleet.manageRedisSecret" .) "true" }}
      name: {{ include "spacefleet.envSecretName" . }}
      key: REDIS_URL
{{- else }}
      name: {{ .Values.externalRedis.existingSecret }}
      key: {{ .Values.externalRedis.existingSecretKey }}
{{- end }}
{{- end -}}

{{/*
Fail fast on incoherent value combinations.
*/}}
{{- define "spacefleet.validate" -}}
{{- if and (not .Values.postgresql.enabled) (not .Values.externalDatabase.url) (not .Values.externalDatabase.existingSecret) -}}
{{- fail "Database not configured: enable the bundled postgresql, or set externalDatabase.url / externalDatabase.existingSecret." -}}
{{- end -}}
{{- if and (not .Values.redis.enabled) (not .Values.externalRedis.url) (not .Values.externalRedis.existingSecret) -}}
{{- fail "Redis not configured: enable the bundled redis, or set externalRedis.url / externalRedis.existingSecret." -}}
{{- end -}}
{{- end -}}
