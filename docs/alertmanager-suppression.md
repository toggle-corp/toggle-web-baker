# Alertmanager / kube-prometheus-stack config: silence baker build+trigger noise

**Goal:** stop `KubeJobFailed` and `KubeContainerWaiting` firing on baker **build** and **trigger** Jobs/pods (transient DNS/clone noise), while **keeping** them for `cleanup` Jobs and `nginx` serving pods.

**Where:** the repo that owns your kube-prometheus-stack Helm values — NOT the operator repo.

---

## Key constraint (read first)

Alertmanager routing matches on **alert labels**. The default `KubeJobFailed` / `KubeContainerWaiting` alerts only carry `namespace`, `job_name`, `pod`, `container` — **not** object labels.

KSM `metricLabelsAllowlist` does NOT attach labels to `kube_job_failed`; it produces a **separate info metric** (`kube_job_labels{label_...}`). So the baker role label only reaches the alert if the **alert rule expression joins** to that info metric with `group_left`.

Therefore both approaches below require the KSM allowlist **and** a rule-expression join. There is no AM-only fix.

---

## Step 1 — KSM: surface the labels (required for both approaches)

In kube-prometheus-stack values:

```yaml
kube-state-metrics:
  metricLabelsAllowlist:
    - jobs=[app.kubernetes.io/managed-by,baker.toggle-corp.com/role]
    - pods=[app.kubernetes.io/managed-by,baker.toggle-corp.com/role]
```

Sanitized label names on the resulting `kube_job_labels` / `kube_pod_labels` series:
- `app.kubernetes.io/managed-by` → `label_app_kubernetes_io_managed_by`
- `baker.toggle-corp.com/role` → `label_baker_toggle_corp_com_role`

---

## Approach A (RECOMMENDED) — exclude in the rule expression, no AM change

Fewer moving parts. Override the two default rules to filter out baker build/trigger objects via a `group_left` join, keeping cleanup+nginx.

`KubeJobFailed` override:
```
kube_job_failed{job="kube-state-metrics"}
  * on(namespace, job_name)
  group_left(label_baker_toggle_corp_com_role, label_app_kubernetes_io_managed_by)
  kube_job_labels
  unless on(namespace, job_name) (
    kube_job_labels{
      label_app_kubernetes_io_managed_by="toggle-web-baker",
      label_baker_toggle_corp_com_role=~"build|trigger"
    }
  ) > 0
```
(Simplest form: keep the original `kube_job_failed > 0` and subtract the baker build/trigger set with `unless on(namespace, job_name) kube_job_labels{...}`.)

`KubeContainerWaiting` override — same idea, join on pod:
```
kube_pod_container_status_waiting_reason{...}
  unless on(namespace, pod) (
    kube_pod_labels{
      label_app_kubernetes_io_managed_by="toggle-web-baker",
      label_baker_toggle_corp_com_role=~"build|trigger"
    }
  ) > 0
```

Apply via kube-prometheus-stack `defaultRules.disabled` + a custom PrometheusRule, or override the rule expression in values if your chart version supports per-rule overrides:
```yaml
defaultRules:
  disabled:
    KubeJobFailed: true
    KubeContainerWaiting: true
# then ship the two rewritten rules in your own PrometheusRule
```

**Net:** cleanup + nginx keep alerting; build + trigger never fire these two alerts.

---

## Approach B — AM null-route (the originally-decided shape)

Same rule join as A (so the alert carries `label_baker_toggle_corp_com_role`), but instead of `unless`, keep the label on the alert and drop it in Alertmanager:

Rule change: add `group_left(label_baker_toggle_corp_com_role, label_app_kubernetes_io_managed_by) kube_job_labels` (and pod equivalent) so the firing alert carries the labels.

Alertmanager route:
```yaml
route:
  routes:
    - matchers:
        - label_app_kubernetes_io_managed_by="toggle-web-baker"
        - label_baker_toggle_corp_com_role=~"build|trigger"
        - alertname=~"KubeJobFailed|KubeContainerWaiting"
      receiver: "null"
      continue: false
receivers:
  - name: "null"
```

**Trade-off vs A:** more moving parts (rule join + AM route) for the same end state. Prefer A unless you want the alerts to still exist in Prometheus (visible in the UI, just not notified).

---

## Recommendation

**Approach A.** Single place (PrometheusRule), no AM coupling, and the noisy alerts never fire at all rather than firing-then-dropped.

---

## Verification

1. `kube_job_labels{label_app_kubernetes_io_managed_by="toggle-web-baker"}` returns series (KSM allowlist working).
2. Trigger a build failure (e.g. bad repo) → confirm `KubeJobFailed` does NOT fire for the `*-build-*` job.
3. Force a cleanup Job failure → confirm `KubeJobFailed` STILL fires (cleanup preserved).
4. Break an nginx image ref → confirm `KubeContainerWaiting` STILL fires (serving preserved).

## Accepted trade-off
Dropping build/trigger `KubeJobFailed`/`KubeContainerWaiting` loses kube-level visibility for failure modes baker's own metrics don't model (quota-blocked scheduling, ImagePullBackOff on baker's own build/clone/clock images). Baker's `baker_app_*` alerts are the authoritative signal for build/trigger health.
