{{- define "clusterview.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "clusterview.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else if contains (include "clusterview.name" .) .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name (include "clusterview.name" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "clusterview.selectorLabels" -}}
app.kubernetes.io/name: {{ include "clusterview.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "clusterview.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{ include "clusterview.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "clusterview.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "clusterview.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
The base path as the binary uses it: "/clusterview", or "" at the root.
*/}}
{{- define "clusterview.basePath" -}}
{{- with trimAll "/" .Values.basePath }}/{{ . }}{{ end }}
{{- end }}

{{/*
The namespaces actions may touch: actions.namespaces, else the watched ones.
*/}}
{{- define "clusterview.actionNamespaces" -}}
{{- join "," (.Values.actions.namespaces | default .Values.scope.namespaces) }}
{{- end }}

{{/*
Stops the install when actions would reach too far. Kubernetes can't limit
what a patch or a new Job contains, so the rights actions need are only ever
granted in listed namespaces, never in the system ones, and with
admission-policy.yaml holding each request to what the action needs.
*/}}
{{- define "clusterview.checkActions" -}}
{{- if .Values.actions.enabled }}
{{- $namespaces := .Values.actions.namespaces | default .Values.scope.namespaces }}
{{- if empty $namespaces }}
{{- fail "actions.enabled needs actions.namespaces: the namespaces whose workloads people may restart and scale. clusterview never gets these rights cluster-wide." }}
{{- end }}
{{- range $namespaces }}
{{- if has . (list "kube-system" "kube-public" "kube-node-lease") }}
{{- fail (printf "actions.namespaces can't include %s: a Job started there could take over the cluster. Restart system components with kubectl." .) }}
{{- end }}
{{- end }}
{{- if and .Values.actions.admissionPolicy (not (semverCompare ">=1.30-0" .Capabilities.KubeVersion.Version)) }}
{{- fail "actions.admissionPolicy needs Kubernetes 1.30 or later (ValidatingAdmissionPolicy). Upgrade the cluster, or set actions.admissionPolicy=false to accept that anyone holding clusterview's service account token could change workloads in the action namespaces however they like." }}
{{- end }}
{{- end }}
{{- end }}
