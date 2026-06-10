{{/*
The effective Dex config (the config.yaml content), rendered from dex.* values.
Shared by two templates that must agree on it byte-for-byte:

  - dex-config.yaml      — the Secret the dexidp/dex subchart mounts
  - dex-rollout-job.yaml — hashes this to roll Dex's pods when the config
                           changes (Dex reads its config only at startup, and
                           the subchart only adds its own restart-triggering
                           checksum when *it* renders the Secret)
*/}}
{{- define "spacefleet.dex.config" -}}
{{- $issuer := include "spacefleet.oidc.issuer" . -}}
issuer: {{ $issuer | quote }}
storage:
{{- if eq .Values.dex.storage "crd" }}
  type: kubernetes
  config:
    inCluster: true
    {{- with .Values.dex.storageConfig }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
{{- else }}
  type: {{ .Values.dex.storage }}
  {{- with .Values.dex.storageConfig }}
  config:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end }}
web:
  http: 0.0.0.0:5556
  {{- with .Values.dex.web.allowedOrigins }}
  allowedOrigins:
    {{- toYaml . | nindent 4 }}
  {{- end }}
oauth2:
  {{- toYaml .Values.dex.oauth2 | nindent 2 }}
{{- with .Values.dex.expiry }}
expiry:
  {{- toYaml . | nindent 2 }}
{{- end }}
staticClients:
  - id: {{ .Values.dex.clientID | quote }}
    name: Spacefleet
    public: true
    redirectURIs:
    {{- /* The app's single public origin (config.externalURL) + /auth/callback.
           Additional origins (e.g. extra hostnames) go in dex.extraRedirectURIs. */}}
      - {{ include "spacefleet.oidc.redirectURI" . | quote }}
    {{- range .Values.dex.extraRedirectURIs }}
      - {{ . | quote }}
    {{- end }}
  {{- with .Values.dex.extraStaticClients }}
  {{- toYaml . | nindent 2 }}
  {{- end }}
{{- if .Values.dex.enablePasswordDB }}
enablePasswordDB: true
  {{- with .Values.dex.staticPasswords }}
staticPasswords:
    {{- toYaml . | nindent 2 }}
  {{- end }}
{{- end }}
{{- with .Values.dex.connectors }}
connectors:
{{- /*
  Dex's per-connector OAuth callback is always <issuer>/callback, and Dex
  rejects a connector whose config.redirectURI doesn't match it. Rather than
  make every operator hand-copy that URL (the #1 connector misconfig), derive
  it from the same issuer the app client uses and fill it in for callback-based
  connector types that didn't set one. An explicit redirectURI is always kept,
  and types that don't use one (e.g. ldap) are left untouched. Remember to
  register the SAME URL as the callback in the upstream provider (e.g. a GitHub
  OAuth App's "Authorization callback URL").
*/}}
{{- $cb := printf "%s/callback" $issuer -}}
{{- $callbackTypes := list "github" "gitlab" "google" "oidc" "oauth" "microsoft" "bitbucketcloud" "gitea" "linkedin" "atlassian-crowd" "openshift" "saml" -}}
{{- range . }}
  {{- $c := deepCopy . -}}
  {{- if and (hasKey $c "type") (has $c.type $callbackTypes) }}
    {{- $cfg := (get $c "config") | default dict -}}
    {{- if not (get $cfg "redirectURI") }}
      {{- $_ := set $cfg "redirectURI" $cb -}}
      {{- $_ := set $c "config" $cfg -}}
    {{- end }}
  {{- end }}
  - {{ toYaml $c | nindent 4 | trim }}
{{- end }}
{{- end }}
{{- end }}
