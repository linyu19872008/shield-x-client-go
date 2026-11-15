# Security Policy

**English** | [简体中文](SECURITY.zh-CN.md)

## Supported versions

`0.1.x` is the current preview line. Security fixes are applied to the latest release.

## Reporting a vulnerability

Please do not open a public issue for security problems. Use GitHub Security Advisories
on this repository (Security → Report a vulnerability), or contact the maintainer through
the repository profile. Include:

- affected SDK version/commit
- reproduction steps or a minimal proof of concept
- impact (token leakage, event disclosure, denial of service, etc.)

We aim to acknowledge reports within 3 business days and to publish a fix or mitigation
as soon as practical.

## Scope

In scope: token handling, TLS behavior, redirect handling, spool file permissions,
event/log disclosure, and dependency vulnerabilities.

Out of scope: gateway policy evaluation, template rendering, and deployment network
configuration.
