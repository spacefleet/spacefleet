{{/*
Environment shared by the web and worker containers: non-secret config from
the ConfigMap, the DATABASE_URL secret ref, and any extraEnv.
*/}}
{{- define "spacefleet.commonEnv" -}}
- name: ADDR
  value: {{ .Values.config.addr | quote }}
- name: ENV
  value: {{ .Values.config.env | quote }}
- name: WORKER_CONCURRENCY
  value: {{ .Values.config.workerConcurrency | quote }}
- name: ALLOW_ORG_CREATION
  value: {{ .Values.config.allowOrgCreation | quote }}
{{- /* Spacefleet always authenticates against its bundled Dex; these are always set. */}}
- name: OIDC_ISSUER
  value: {{ include "spacefleet.oidc.issuer" . | quote }}
- name: OIDC_CLIENT_ID
  value: {{ .Values.config.oidc.clientID | quote }}
- name: OIDC_JWKS_URL
  value: {{ include "spacefleet.oidc.jwksURL" . | quote }}
- name: DEX_UPSTREAM_URL
  value: {{ include "spacefleet.dex.upstreamURL" . | quote }}
{{- include "spacefleet.databaseEnv" . | nindent 0 }}
{{- /* Credential-encryption key, only when supplied inline; the envFrom path
       delivers it (and any other secret env) through the container's envFrom. */}}
{{- with (.Values.config.secrets | default dict).secretKey }}
- name: SPACEFLEET_SECRET_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "spacefleet.envSecretName" $ }}
      key: SPACEFLEET_SECRET_KEY
{{- end }}
{{- with .Values.config.extraEnv }}
{{- toYaml . | nindent 0 }}
{{- end }}
{{- end -}}
