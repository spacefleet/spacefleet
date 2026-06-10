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
- name: ALLOW_PRIVATE_CLUSTER_ENDPOINTS
  value: {{ .Values.config.allowPrivateClusterEndpoints | quote }}
{{- /* Canonical public base URL — the single source of truth for external
       links (and the OIDC issuer/redirect below). Required. */}}
- name: EXTERNAL_URL
  value: {{ include "spacefleet.externalURL" . | quote }}
{{- /* Spacefleet always authenticates against its bundled Dex; these are always set. */}}
- name: OIDC_ISSUER
  value: {{ include "spacefleet.oidc.issuer" . | quote }}
- name: OIDC_CLIENT_ID
  value: {{ .Values.config.oidc.clientID | quote }}
- name: OIDC_JWKS_URL
  value: {{ include "spacefleet.oidc.jwksURL" . | quote }}
- name: DEX_UPSTREAM_URL
  value: {{ include "spacefleet.dex.upstreamURL" . | quote }}
{{- /* Sign-in options for the login screen, derived from dex.connectors (+ the
       password DB when enabled). Drives the per-connector login buttons. */}}
- name: LOGIN_METHODS
  value: {{ include "spacefleet.loginMethods" . | quote }}
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
{{- /* SMTP for outbound email (invitations). Non-secret settings inline; the
       password (when supplied inline) comes from the chart's env Secret. Leave
       host/from empty to disable email — invites still return a copy-able link. */}}
{{- with (.Values.config.smtp | default dict) }}
{{- with .host }}
- name: SMTP_HOST
  value: {{ . | quote }}
{{- end }}
{{- with .port }}
- name: SMTP_PORT
  value: {{ . | quote }}
{{- end }}
{{- with .username }}
- name: SMTP_USERNAME
  value: {{ . | quote }}
{{- end }}
{{- with .from }}
- name: SMTP_FROM
  value: {{ . | quote }}
{{- end }}
{{- if hasKey . "startTLS" }}
- name: SMTP_STARTTLS
  value: {{ .startTLS | quote }}
{{- end }}
{{- with .password }}
- name: SMTP_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "spacefleet.envSecretName" $ }}
      key: SMTP_PASSWORD
{{- end }}
{{- end }}
{{- /* GitHub App for pulling charts from private Git repositories. App ID,
       slug, and OAuth client ID are non-secret (inline); the private key and
       client secret (when supplied inline) come from the chart's env Secret.
       Leave unset to disable the feature. */}}
{{- with (.Values.config.github | default dict) }}
{{- with .appId }}
- name: GITHUB_APP_ID
  value: {{ . | quote }}
{{- end }}
{{- with .slug }}
- name: GITHUB_APP_SLUG
  value: {{ . | quote }}
{{- end }}
{{- with .privateKey }}
- name: GITHUB_APP_PRIVATE_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "spacefleet.envSecretName" $ }}
      key: GITHUB_APP_PRIVATE_KEY
{{- end }}
{{- with .clientId }}
- name: GITHUB_APP_CLIENT_ID
  value: {{ . | quote }}
{{- end }}
{{- with .clientSecret }}
- name: GITHUB_APP_CLIENT_SECRET
  valueFrom:
    secretKeyRef:
      name: {{ include "spacefleet.envSecretName" $ }}
      key: GITHUB_APP_CLIENT_SECRET
{{- end }}
{{- end }}
{{- with .Values.config.extraEnv }}
{{- toYaml . | nindent 0 }}
{{- end }}
{{- end -}}
