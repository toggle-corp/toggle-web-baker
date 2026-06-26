# Baker FrontendApp Console

A thin, **read-only** admin console for the Baker `FrontendApp` deploy platform.
It is a separate Go module (`github.com/toggle-corp/toggle-web-baker/console`)
and does not touch the repo-root module.

It reads `FrontendApp` custom resources (`baker.toggle-corp.com/v1alpha1`) via
the client-go **dynamic** client (unstructured — it never imports the
operator's Go types) and renders their `.status` as server-side HTML. There is
**zero Prometheus dependency**; everything shown comes from `.status`.

## What it does

- **List** every `FrontendApp` across all namespaces (`/`).
- **Detail** view per app (`/ns/{namespace}/app/{name}`) rendering phase,
  conditions (Ready / BuildSucceeded / IngressReady / Degraded), a STALE badge
  from `specStale`, url, nodeName, the `build` sub-status, schedule timestamps,
  `release`, `storage`, and the `lastManualTrigger` audit trail.
- **Manual rebuild** (`POST /ns/{namespace}/app/{name}/rebuild`) — the **only**
  write. It merge-patches two metadata annotations and nothing else:
  - `rebuild.baker.toggle-corp.com/requested-at` = current RFC3339 timestamp
  - `rebuild.baker.toggle-corp.com/by` = authenticated GitHub username
  The operator observes `requested-at` and decides when to build. The console
  has **no Job/Pod-create capability** — see `deploy/rbac.yaml`.
- **Health**: `/healthz`.

A `Degraded` app is made visually obvious. In particular `Ready=True` together
with `Degraded=True` renders as "serving last-good (latest build failed)".

## Routes

| Method | Path                                   | Purpose                          |
|--------|----------------------------------------|----------------------------------|
| GET    | `/`                                    | list all FrontendApps            |
| GET    | `/ns/{namespace}/app/{name}`           | detail view                      |
| POST   | `/ns/{namespace}/app/{name}/rebuild`   | annotation patch (manual rebuild)|
| GET    | `/healthz`                             | liveness/readiness              |

## Auth model

The console performs **no authentication itself**. It is reachable only behind
**oauth2-proxy**, wired as a **Traefik forward-auth** middleware:

1. Traefik forwards every request to oauth2-proxy's `/oauth2/auth`.
2. oauth2-proxy does GitHub OAuth and enforces `--github-team
   toggle-corp/platform-team`. It **fails closed**: if the GitHub membership
   API errors, the request is denied (an error is not a pass).
3. On success oauth2-proxy returns `X-Auth-Request-User` (the GitHub username),
   which the Traefik middleware copies onto the request.
4. The console **trusts** `X-Auth-Request-User` (falling back to
   `X-Forwarded-User`) and records it as the rebuild actor. If the header is
   absent the rebuild handler returns **401** and writes nothing — fail closed.

Because the console trusts that header unconditionally, it MUST NOT be exposed
without oauth2-proxy in front; the `Service` is `ClusterIP` and only the
oauth2-protected `Ingress` is public.

### Break-glass

There is deliberately **no basic-auth stopgap**. If oauth2-proxy locks the team
out (GitHub outage, OAuth app misconfig, team rename), the break-glass is
`kubectl`: operators with cluster credentials can read status and, if needed,
annotate directly:

```sh
kubectl -n <ns> annotate frontendapp <name> \
  rebuild.baker.toggle-corp.com/requested-at="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  rebuild.baker.toggle-corp.com/by="<your-username>" --overwrite
```

## Build & test

```sh
cd console
go build ./...
go vet ./...
go test ./...
```

## Local dev

Outside a cluster the client falls back to `~/.kube/config`. Run:

```sh
LISTEN_ADDR=:8080 go run ./cmd/console
```

Then send a fake user header (oauth2-proxy is not in the loop locally):

```sh
curl -H 'X-Auth-Request-User: you' localhost:8080/
```

## Deploy

The console is an opt-in component of the Helm chart at
`deploy/helm/toggle-web-baker` — enable it with `console.enabled=true`. The
chart renders the same set of objects the old `console/deploy/*.yaml` manifests
did (RBAC, console Deployment + Service, oauth2-proxy with the fail-closed
GitHub-team flags, the Traefik forward-auth Middleware, and the two Ingresses).

```bash
helm upgrade baker oci://ghcr.io/toggle-corp/toggle-web-baker-helm \
  --namespace toggle-baker-system \
  --set console.enabled=true \
  --set console.host=baker-console.example.org \
  --set console.oauth2Proxy.existingSecret=baker-oauth2-proxy
```

See the chart README for the console + oauth2-proxy values
(`console.host`, `console.oauth2Proxy.*`).

## Layout

```
console/
  cmd/console/main.go            entrypoint (net/http server)
  internal/k8s/client.go         dynamic-client list/get/patch
  internal/view/model.go         unstructured .status -> view model
  internal/view/coerce.go        defensive type coercion
  internal/server/server.go      routes + handlers
  internal/server/render.go      embedded html/template rendering
  internal/server/templates/     layout/list/detail/error templates
  deploy/                        k8s manifests (see above)
  Dockerfile
```
