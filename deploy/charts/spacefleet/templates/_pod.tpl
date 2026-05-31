{{/*
Environment shared by the web and worker containers: non-secret config from
the ConfigMap, the DATABASE_URL / REDIS_URL secret refs, and any extraEnv.
*/}}
{{- define "spacefleet.commonEnv" -}}
- name: ADDR
  value: {{ .Values.config.addr | quote }}
- name: ENV
  value: {{ .Values.config.env | quote }}
- name: WORKER_CONCURRENCY
  value: {{ .Values.config.workerConcurrency | quote }}
{{- $issuer := include "spacefleet.oidc.issuer" . }}
{{- if $issuer }}
- name: OIDC_ISSUER
  value: {{ $issuer | quote }}
{{- end }}
- name: OIDC_CLIENT_ID
  value: {{ .Values.config.oidc.clientID | quote }}
{{- $jwks := include "spacefleet.oidc.jwksURL" . }}
{{- if $jwks }}
- name: OIDC_JWKS_URL
  value: {{ $jwks | quote }}
{{- end }}
{{- include "spacefleet.databaseEnv" . | nindent 0 }}
{{- include "spacefleet.redisEnv" . | nindent 0 }}
{{- with .Values.config.extraEnv }}
{{- toYaml . | nindent 0 }}
{{- end }}
{{- end -}}
