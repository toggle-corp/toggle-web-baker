# Sentry error reporting

The operator and console report errors to one shared Sentry project, with
events tagged `component=operator` or `component=console`. Sentry is never
injected into the baked frontend apps — this is platform-binary telemetry
only.

## Enabling

Set the chart values:

```yaml
sentry:
  dsn: "https://<key>@<host>/<project>"
  environment: "production"
```

This renders `SENTRY_DSN`, `SENTRY_ENVIRONMENT`, and `SENTRY_RELEASE` (the
per-binary image tag, falling back to the chart `appVersion`) on both
Deployments. An empty `dsn` (the default) renders no env vars and both SDKs
initialize as no-ops — local/kind runs need nothing.

## What gets reported

Operator (`internal/observability`):

- Error-level zap logs, via a core teed into the controller-runtime logger.
  Known-transient noise is dropped before sending: optimistic-lock conflicts,
  wrapped `context.Canceled`, and leader-election churn. Repeats of the same
  fingerprint are limited to ~1 event/hour.
- Terminal build failures that are the **platform's fault**
  (`internal/controller/platformfault.go`): `ConfigError`, `BuildFailed`
  in platform-owned steps — `copier`, `release`, or an unattributed step
  (e.g. the shim-install init container) — and `OOMKilled` in the copier
  (which has no user-settable memory limit). Events carry app, namespace,
  step, and reason tags, fingerprinted by `[namespace, app, reason]`.

Console (`console/internal/sentryhttp`):

- Recovered panics (recovery is active even with Sentry disabled).
- Any 5xx response, with the request, the authenticated user
  (`X-Forwarded-User` / `X-Auth-Request-User`), and the underlying error
  attached by `renderError`.

## What is deliberately NOT reported

User-caused failures stay in the console and App conditions only:

- `BuildFailed` in the user-owned steps (`clone`, `setup`, `fetch`, `build`)
- `OOMKilled` in user-owned steps (the user's own memory limit)
- Spec rejections: `InvalidSpec`, `ImageNotAllowed`, `UnknownNodeVersion`,
  `InvalidStorage`, `InvalidStorageClass`, `MissingTLSSecret`

If Sentry fires, the platform is broken — not a user's repo.
