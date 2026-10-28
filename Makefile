VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
IMAGE   ?= ghcr.io/clusterview/clusterview
KIND_CLUSTER ?= clusterview
LDFLAGS := -s -w -X github.com/clusterview/clusterview/internal/version.Version=$(VERSION) -X github.com/clusterview/clusterview/internal/version.Commit=$(COMMIT)

.PHONY: all web build run test lint chart-lint manifests image kind-up kind-install kind-down

all: web build

web: ## Build the UI into web/dist
	cd web && npm ci && npm run build
	touch web/dist/.gitkeep

build: ## Build the binary into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/clusterview ./cmd/clusterview

run: build ## Run locally against your current kubeconfig context
	./bin/clusterview

test:
	go test -race ./...

lint:
	go vet ./...
	golangci-lint run
	cd web && npm run typecheck

chart-lint:
	helm lint charts/clusterview
	helm template clusterview charts/clusterview > /dev/null

manifests: ## Render the plain install.yaml from the chart
	@mkdir -p deploy
	helm template clusterview charts/clusterview --namespace clusterview \
	  --set image.tag=$(VERSION:v%=%) > deploy/install.yaml

image:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(IMAGE):$(VERSION) .

kind-up:
	kind create cluster --name $(KIND_CLUSTER)

kind-install: image ## Build, load into kind and install the chart
	kind load docker-image $(IMAGE):$(VERSION) --name $(KIND_CLUSTER)
	helm upgrade --install clusterview charts/clusterview -n clusterview --create-namespace \
	  --set image.tag=$(VERSION) --set image.pullPolicy=IfNotPresent --wait

kind-down:
	kind delete cluster --name $(KIND_CLUSTER)
