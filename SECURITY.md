# Security

## Reporting a vulnerability

Please don't open a public issue. Report it privately on GitHub instead:
[**Report a vulnerability**](https://github.com/clusterview/clusterview/security/advisories/new)
(the repository's Security tab).

Include what you found, the version (`/api/v1/version` or the chart
version), how to reproduce it, and what someone could do with it. We'll
reply there, work on a fix in a private advisory, and credit you when it's
published unless you'd rather not be named.

## Supported versions

Fixes go into the latest release. Upgrade to it before reporting, if you
can, and check whether the problem is still there.

## In scope

- The `clusterview` and `kubectl-clusterview` binaries and the container
  image
- The Helm chart and `install.yaml`: their defaults and what they grant
- Sign-in, roles and actions: anything that lets someone see or do more than
  they should
- Secrets reaching the browser, logs or an AI provider when they shouldn't

## Out of scope

- Installs that switched off a protection on purpose, such as
  `auth.mode: none` behind an ingress with `auth.allowNoAuthWhenExposed`, or
  actions with `actions.admissionPolicy: false`
- What pod logs contain: clusterview shows them to anyone allowed to view the
  dashboard
- Vulnerabilities in Kubernetes itself, or in an identity provider or AI
  provider

## How clusterview limits the damage

Read-only Kubernetes access by default, with no Secrets, ConfigMaps or exec;
actions only in listed namespaces and held to restart and scale by an
admission policy; a sign-in required before the chart exposes it; and strict
browser security headers. The README covers each:
[what it can access](README.md#what-it-can-access),
[actions](README.md#actions) and
[sign-in and roles](README.md#sign-in-and-roles).
