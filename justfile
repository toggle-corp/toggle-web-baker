CONTROLLER_GEN := "go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.21.0"

# List available recipes.
default:
    @just --list

# Build all packages in the root module.
build:
    go build ./...

# Run tests for the root module and the console module.
test:
    go test ./...
    cd console && go test ./...

# Lint with golangci-lint if available, otherwise fall back to go vet.
lint:
    #!/usr/bin/env sh
    if command -v golangci-lint >/dev/null 2>&1; then
        golangci-lint run
    else
        echo "golangci-lint not found; falling back to go vet"
        go vet ./...
        cd console && go vet ./...
    fi

# Regenerate CRD and RBAC manifests into config/, then re-sync the chart CRD copy.
manifests:
    {{CONTROLLER_GEN}} rbac:roleName=manager-role crd paths="./..." output:crd:artifacts:config=config/crd output:rbac:artifacts:config=config/rbac
    just sync-crd

# Regenerate the chart CRD (templates/crd.yaml) from config/crd/, wrapping the
# generated CRD with the chart-specific bits (install guard, resource-policy
# annotation). Deterministic transform; keep the two in lockstep so they cannot
# drift. Folded into `just manifests`.
sync-crd:
    #!/usr/bin/env python3
    import sys

    SRC = "config/crd/baker.toggle-corp.com_frontendapps.yaml"
    DST = "deploy/helm/toggle-web-baker/templates/crd.yaml"

    with open(SRC) as f:
        lines = f.read().splitlines(keepends=True)

    # Anchor: the controller-gen version annotation. Insert the resource-policy
    # comment + annotation immediately after it, matching its indentation.
    ANCHOR = "controller-gen.kubebuilder.io/version:"
    out = []
    inserted = False
    for line in lines:
        out.append(line)
        if not inserted and ANCHOR in line:
            indent = line[: len(line) - len(line.lstrip())]
            out.append(f"{indent}# helm.sh/resource-policy keeps the CRD on `helm uninstall` so existing\n")
            out.append(f"{indent}# FrontendApp CRs are not cascade-deleted.\n")
            out.append(f"{indent}helm.sh/resource-policy: keep\n")
            inserted = True

    if not inserted:
        sys.exit(f"sync-crd: anchor {ANCHOR!r} not found in {SRC}")

    # Ensure the body ends with a single trailing newline before the guard end.
    body = "".join(out)
    if not body.endswith("\n"):
        body += "\n"

    # Wrap with the chart install guard.
    wrapped = "{{{{- if .Values.crds.install }}\n" + body + "{{{{- end }}\n"

    with open(DST, "w") as f:
        f.write(wrapped)

# Regenerate deepcopy methods.
generate:
    {{CONTROLLER_GEN}} object paths="./..."

# Build the operator container image.
docker-build TAG="dev":
    docker build -t ghcr.io/toggle-corp/toggle-web-baker-operator:{{TAG}} .

# Run the envtest (apiserver-backed) validation suite. Downloads test binaries on first run.
test-envtest:
    KUBEBUILDER_ASSETS="$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.20 use 1.32.0 -p path)" go test -tags envtest ./internal/controller/... -run Validation -count=1

# Full kind pipeline smoke test (MANUAL — needs Docker + network + several
# minutes). Builds the operator + helper images, loads them into a throwaway
# kind cluster, installs the chart, applies config/samples/frontendapp.yaml and
# waits for the build Job to complete. Do NOT run this in CI or a sandbox.
e2e-local:
    #!/usr/bin/env bash
    set -euo pipefail

    # ---- tunables (override via env) ----------------------------------------
    CLUSTER="${CLUSTER:-twb-e2e}"
    TAG="${TAG:-dev}"
    SAMPLE="${SAMPLE:-config/samples/frontendapp.yaml}"
    BUILD_TIMEOUT="${BUILD_TIMEOUT:-600s}"
    OPERATOR_TIMEOUT="${OPERATOR_TIMEOUT:-180s}"
    RELEASE="${RELEASE:-twb}"
    # kind's default pod + service CIDRs (the operator REQUIRES these excluded
    # from build-pod egress; it refuses to reconcile if clusterCIDRs is empty).
    POD_CIDR="${POD_CIDR:-10.244.0.0/16}"
    SVC_CIDR="${SVC_CIDR:-10.96.0.0/12}"
    # Allowlist prefix for the sample's build image (docker.io/cimg/node:18.20).
    REGISTRY_ALLOW="${REGISTRY_ALLOW:-docker.io/cimg/}"

    log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }

    # ---- resolve a kind runner (prefer a real binary) -----------------------
    if command -v kind >/dev/null 2>&1; then
        KIND=(kind)
    else
        log "kind binary not found; using 'go run sigs.k8s.io/kind@v0.24.0'"
        KIND=(go run sigs.k8s.io/kind@v0.24.0)
    fi

    # ---- teardown trap (KEEP_CLUSTER=1 to keep it for debugging) -------------
    cleanup() {
        if [ "${KEEP_CLUSTER:-0}" = "1" ]; then
            log "KEEP_CLUSTER=1 set — leaving cluster '${CLUSTER}' running"
            return
        fi
        log "Tearing down kind cluster '${CLUSTER}'"
        "${KIND[@]}" delete cluster --name "${CLUSTER}" || true
    }
    trap cleanup EXIT

    log "Creating kind cluster '${CLUSTER}'"
    "${KIND[@]}" create cluster --name "${CLUSTER}" --wait 120s

    log "Building operator image (tag ${TAG})"
    just docker-build "${TAG}"

    log "Building platform helper images (tag ${TAG})"
    make -C images build TAG="${TAG}"

    IMAGES=(
        "ghcr.io/toggle-corp/toggle-web-baker-operator:${TAG}"
        "ghcr.io/toggle-corp/toggle-web-baker-clone:${TAG}"
        "ghcr.io/toggle-corp/toggle-web-baker-copier:${TAG}"
        "ghcr.io/toggle-corp/toggle-web-baker-du:${TAG}"
        "ghcr.io/toggle-corp/toggle-web-baker-cleanup:${TAG}"
    )
    log "Loading ${#IMAGES[@]} images into kind"
    for img in "${IMAGES[@]}"; do
        echo "  loading ${img}"
        "${KIND[@]}" load docker-image "${img}" --name "${CLUSTER}"
    done

    log "Installing chart (release ${RELEASE})"
    helm install "${RELEASE}" deploy/helm/toggle-web-baker \
        --wait --timeout "${OPERATOR_TIMEOUT}" \
        --set "operator.image.tag=${TAG}" \
        --set "operator.image.pullPolicy=IfNotPresent" \
        --set "platformImages.clone.tag=${TAG}" \
        --set "platformImages.copier.tag=${TAG}" \
        --set "platformImages.du.tag=${TAG}" \
        --set "platformImages.cleanup.tag=${TAG}" \
        --set "console.enabled=false" \
        --set "operator.clusterCIDRs={${POD_CIDR},${SVC_CIDR}}" \
        --set "operator.registryAllowlist={${REGISTRY_ALLOW}}"

    log "Applying sample ${SAMPLE}"
    kubectl apply -f "${SAMPLE}"

    APP_NS="$(kubectl get -f "${SAMPLE}" -o jsonpath='{.metadata.namespace}')"
    APP_NS="${APP_NS:-default}"
    SELECTOR="app.kubernetes.io/instance=smoke,baker.toggle-corp.com/role=build"

    log "Waiting for a build Job to be created (selector: ${SELECTOR})"
    for _ in $(seq 1 60); do
        if [ -n "$(kubectl get jobs -n "${APP_NS}" -l "${SELECTOR}" -o name 2>/dev/null)" ]; then
            break
        fi
        sleep 2
    done

    dump_diagnostics() {
        log "Build did not complete — dumping diagnostics"
        kubectl get frontendapp -n "${APP_NS}" -o wide || true
        kubectl get jobs,pods -n "${APP_NS}" -l "${SELECTOR}" -o wide || true
        kubectl describe pods -n "${APP_NS}" -l "${SELECTOR}" || true
        for pod in $(kubectl get pods -n "${APP_NS}" -l "${SELECTOR}" -o name 2>/dev/null); do
            echo "----- logs (all containers) for ${pod} -----"
            kubectl logs -n "${APP_NS}" "${pod}" --all-containers --prefix || true
        done
    }

    log "Waiting up to ${BUILD_TIMEOUT} for the build Job to finish"
    # `kubectl wait --for=condition=complete` never returns for a FAILED Job, so
    # poll BOTH terminal conditions and fail fast instead of blocking the whole
    # timeout when a build errors out.
    deadline=$(( SECONDS + ${BUILD_TIMEOUT%s} ))
    build_status=""
    while [ "${SECONDS}" -lt "${deadline}" ]; do
        if [ "$(kubectl get job -n "${APP_NS}" -l "${SELECTOR}" \
            -o jsonpath='{.items[*].status.conditions[?(@.type=="Complete")].status}' 2>/dev/null)" = "True" ]; then
            build_status="complete"; break
        fi
        if [ "$(kubectl get job -n "${APP_NS}" -l "${SELECTOR}" \
            -o jsonpath='{.items[*].status.conditions[?(@.type=="Failed")].status}' 2>/dev/null)" = "True" ]; then
            build_status="failed"; break
        fi
        sleep 5
    done

    if [ "${build_status}" != "complete" ]; then
        dump_diagnostics
        echo
        if [ "${build_status}" = "failed" ]; then
            echo "FAIL: build Job reached Failed"
        else
            echo "FAIL: build Job did not finish within ${BUILD_TIMEOUT}"
        fi
        exit 1
    fi

    log "Build Job complete"
    kubectl get jobs,pods -n "${APP_NS}" -l "${SELECTOR}" -o wide
    echo
    echo "PASS: e2e smoke build completed successfully on kind cluster '${CLUSTER}'"

# Lint the Helm chart.
helm-lint:
    helm lint deploy/helm/toggle-web-baker

# Render the Helm chart templates.
helm-template:
    helm template deploy/helm/toggle-web-baker

# Update (or --check-diff-only) Helm snapshot tests.
helm-snapshots *ARGS:
    ./deploy/helm/toggle-web-baker/update-snapshots.sh {{ARGS}}
