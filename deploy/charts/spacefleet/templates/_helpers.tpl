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
Name of the chart-managed env Secret (DATABASE_URL).
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

{{/*
Name of the bundled Dex Service/Deployment. Must mirror the dexidp/dex chart's
own "dex.fullname" so we can reference its Service and so the config Secret we
render lands where the subchart mounts it.
*/}}
{{- define "spacefleet.dex.fullname" -}}
{{- $dex := .Values.dex | default dict -}}
{{- if $dex.fullnameOverride -}}
{{- $dex.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default "dex" $dex.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
OIDC issuer resolution (browser-facing). Spacefleet always bundles Dex and the
app reverse-proxies it same-origin under /dex, so the issuer is always the app's
own origin + /dex:
  1. https://<first ingress host>/dex   (ingress enabled — the normal path)
  2. http://localhost:8080/dex          (no ingress — port-forward trial; the
     app proxy makes /dex reachable on whatever origin you reach the app at)
Always non-empty.
*/}}
{{- define "spacefleet.oidc.issuer" -}}
{{- if and .Values.ingress.enabled (gt (len .Values.ingress.hosts) 0) -}}
{{- printf "https://%s/dex" (index .Values.ingress.hosts 0).host -}}
{{- else -}}
{{- "http://localhost:8080/dex" -}}
{{- end -}}
{{- end -}}

{{/*
The app's public origin + /auth/callback, the OIDC redirect URI. Mirrors the
issuer's origin so it works behind the ingress or a port-forward trial.
*/}}
{{- define "spacefleet.oidc.redirectURI" -}}
{{- if and .Values.ingress.enabled (gt (len .Values.ingress.hosts) 0) -}}
{{- printf "https://%s/auth/callback" (index .Values.ingress.hosts 0).host -}}
{{- else -}}
{{- "http://localhost:8080/auth/callback" -}}
{{- end -}}
{{- end -}}

{{/*
JWKS URL the backend uses to verify tokens, pointing at the in-cluster Dex
Service so verification never depends on the public issuer being reachable from
inside the cluster (no ingress hairpin). Path mirrors the issuer's path, since
Dex serves its routes under the issuer path prefix.
*/}}
{{- define "spacefleet.oidc.jwksURL" -}}
{{- $issuer := include "spacefleet.oidc.issuer" . -}}
{{- $path := (urlParse $issuer).path -}}
{{- printf "http://%s:%v%s/keys" (include "spacefleet.dex.fullname" .) (include "spacefleet.dex.port" .) $path -}}
{{- end -}}

{{/*
Base URL of the in-cluster Dex Service the app reverse-proxies /dex/* to
(DEX_UPSTREAM_URL). No path: Dex serves under the issuer path (/dex), which the
app forwards through unchanged.
*/}}
{{- define "spacefleet.dex.upstreamURL" -}}
{{- printf "http://%s:%v" (include "spacefleet.dex.fullname" .) (include "spacefleet.dex.port" .) -}}
{{- end -}}

{{/*
Dex Service HTTP port (subchart default 5556, overridable via dex.service.ports).
*/}}
{{- define "spacefleet.dex.port" -}}
{{- $port := 5556 -}}
{{- with .Values.dex.service }}{{ with .ports }}{{ with .http }}{{ with .port }}{{ $port = . }}{{ end }}{{ end }}{{ end }}{{ end -}}
{{- $port -}}
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
True when the chart renders its managed env Secret (<fullname>-env) at all. That
Secret carries DATABASE_URL (when the chart owns it) and/or an inline
config.secrets.secretKey. When neither applies (external DB via existingSecret
*and* the secret key supplied via config.secrets.envFrom), the chart writes no
Secret of its own.
*/}}
{{- define "spacefleet.manageEnvSecret" -}}
{{- $manageDB := eq (include "spacefleet.manageDatabaseSecret" .) "true" -}}
{{- $secretKey := (.Values.config.secrets | default dict).secretKey -}}
{{- if or $manageDB $secretKey -}}true{{- else -}}false{{- end -}}
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
Fail fast on incoherent value combinations.
*/}}
{{- define "spacefleet.validate" -}}
{{- if and (not .Values.postgresql.enabled) (not .Values.externalDatabase.url) (not .Values.externalDatabase.existingSecret) -}}
{{- fail "Database not configured: enable the bundled postgresql, or set externalDatabase.url / externalDatabase.existingSecret." -}}
{{- end -}}
{{- if and .Values.ingress.enabled (not (gt (len .Values.ingress.hosts) 0)) -}}
{{- fail "ingress.enabled=true but ingress.hosts is empty: set at least one host so the OIDC issuer (https://<host>/dex) can be derived." -}}
{{- end -}}
{{- end -}}
