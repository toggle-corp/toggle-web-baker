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

# Regenerate CRD and RBAC manifests into config/.
manifests:
    {{CONTROLLER_GEN}} rbac:roleName=manager-role crd paths="./..." output:crd:artifacts:config=config/crd output:rbac:artifacts:config=config/rbac

# Regenerate deepcopy methods.
generate:
    {{CONTROLLER_GEN}} object paths="./..."

# Build the operator container image.
docker-build TAG="dev":
    docker build -t ghcr.io/toggle-corp/toggle-web-baker-operator:{{TAG}} .

# Run the envtest (apiserver-backed) validation suite. Downloads test binaries on first run.
test-envtest:
    KUBEBUILDER_ASSETS="$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.20 use 1.32.0 -p path)" go test -tags envtest ./internal/controller/... -run Validation -count=1

# Lint the Helm chart.
helm-lint:
    helm lint deploy/helm/toggle-web-baker

# Render the Helm chart templates.
helm-template:
    helm template deploy/helm/toggle-web-baker

# Update (or --check-diff-only) Helm snapshot tests.
helm-snapshots *ARGS:
    ./deploy/helm/toggle-web-baker/update-snapshots.sh {{ARGS}}
