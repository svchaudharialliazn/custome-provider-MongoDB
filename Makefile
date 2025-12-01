# Project Setup
PROJECT_NAME := swapnil-provider-mongodb
PROJECT_REPO := github.com/svchaudhari/Swap-Provider-MongoDB

# Docker & Registry
DOCKERHUB_ORG ?= svchaudharialliazn
REGISTRY ?= docker.io
IMAGE_NAME := $(REGISTRY)/$(DOCKERHUB_ORG)/$(PROJECT_NAME)

# Versioning
VERSION ?= $(shell git describe --tags --always --dirty)
BRANCH_NAME ?= $(shell git rev-parse --abbrev-ref HEAD)

# Build directories
BUILD_DIR := ./_output
DIST_DIR := $(BUILD_DIR)/dist
PACKAGE_DIR := ./config
CRD_DIR := $(PACKAGE_DIR)/crd

# Go settings
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
GO_LDFLAGS := -w -s
GO_BUILD_FLAGS := -ldflags "$(GO_LDFLAGS)"

# Docker platforms
PLATFORMS ?= linux/amd64,linux/arm64
BUILDX_BUILDER_NAME ?= crossplane-builder

# Tools
CONTROLLER_GEN_VERSION := v0.19.0
CROSSPLANE_TOOLS_VERSION := v0.22.0

# Paths
TOOLS_HOST_DIR := $(abspath ./.tools)
CONTROLLER_GEN := $(TOOLS_HOST_DIR)/controller-gen-$(CONTROLLER_GEN_VERSION)

.PHONY: help
help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: submodules
submodules: ## Update git submodules
	git submodule sync
	git submodule update --init --recursive

.PHONY: vendor
vendor: ## Update go vendor dependencies
	go mod tidy
	go mod vendor
	go mod verify

.PHONY: generate
generate: #$(CONTROLLER_GEN) ## Generate code (deepcopy, CRDs, etc.)
	@echo "Generating deepcopy methods..."
	$(CONTROLLER_GEN) object:headerFile="./hack/boilerplate.go.txt" paths="./apis/..."
	@echo "Generating CRDs..."
	$(CONTROLLER_GEN) crd:allowDangerousTypes=true paths="./apis/..." output:crd:artifacts:config=$(CRD_DIR)
	@echo "Cleaning up generated CRDs..."
	@find $(CRD_DIR) -type f -name '*.yaml' -exec sed -i.bak 's/storedVersions: null/storedVersions: []/g' {} \; -exec rm {}.bak \;

.PHONY: manifests
manifests: generate ## Generate manifests
	@echo "Manifests generated in $(CRD_DIR)"

##@ Build

.PHONY: build
build: vendor generate ## Build provider binary
	@mkdir -p $(BUILD_DIR)/bin
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(GO_BUILD_FLAGS) \
		-o $(BUILD_DIR)/bin/provider \
		./cmd/provider

.PHONY: build-all
build-all: ## Build for all platforms
	@for platform in "linux/amd64" "linux/arm64" "darwin/amd64" "darwin/arm64"; do \
		export GOOS=$$(echo $$platform | cut -d/ -f1); \
		export GOARCH=$$(echo $$platform | cut -d/ -f2); \
		echo "Building for $$GOOS/$$GOARCH..."; \
		$(MAKE) build; \
		mv $(BUILD_DIR)/bin/provider $(BUILD_DIR)/bin/provider-$$GOOS-$$GOARCH; \
	done

##@ Docker

.PHONY: docker-build
docker-build: generate ## Build Docker image
	docker build \
		--platform linux/amd64 \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(shell git rev-parse HEAD) \
		--build-arg BUILD_DATE=$(shell date -u +'%Y-%m-%dT%H:%M:%SZ') \
		-t $(IMAGE_NAME):$(VERSION) \
		-t $(IMAGE_NAME):latest \
		-f Dockerfile .

.PHONY: docker-build-multiarch
docker-build-multiarch: generate ## Build multi-architecture image
	@if ! docker buildx ls | grep -q $(BUILDX_BUILDER_NAME); then \
		docker buildx create --name $(BUILDX_BUILDER_NAME) --use; \
	fi
	docker buildx build \
		--platform $(PLATFORMS) \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(shell git rev-parse HEAD) \
		--build-arg BUILD_DATE=$(shell date -u +'%Y-%m-%dT%H:%M:%SZ') \
		-t $(IMAGE_NAME):$(VERSION) \
		-t $(IMAGE_NAME):latest \
		--push \
		-f Dockerfile .

.PHONY: docker-push
docker-push: ## Push Docker image
	docker push $(IMAGE_NAME):$(VERSION)
	docker push $(IMAGE_NAME):latest

##@ Package

.PHONY: build.xpkg
build.xpkg: generate ## Build Crossplane package
	@echo "Building Crossplane package..."
	@mkdir -p $(DIST_DIR)
	crossplane xpkg build \
		--package-root=$(PACKAGE_DIR) \
		--examples-root=./examples \
		--ignore=".github/*,.git/*,_output/*" \
		--package-file=$(DIST_DIR)/$(PROJECT_NAME)-$(VERSION).xpkg \
		--verbose

.PHONY: push.xpkg
push.xpkg: build.xpkg ## Push Crossplane package
	@echo "Pushing Crossplane package to $(REGISTRY)/$(DOCKERHUB_ORG)/$(PROJECT_NAME):$(VERSION)"
	crossplane xpkg push \
		$(REGISTRY)/$(DOCKERHUB_ORG)/$(PROJECT_NAME):$(VERSION) \
		-f $(DIST_DIR)/$(PROJECT_NAME)-$(VERSION).xpkg

##@ Testing

.PHONY: test
test: generate ## Run unit tests
	go test -v -race -cover ./...

.PHONY: test-coverage
test-coverage: generate ## Test with coverage
	go test -v -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

.PHONY: test-integration
test-integration:
	@echo "Running integration tests..."
	go test -v -tags=integration ./test/integration/...

.PHONY: e2e
e2e:
	@echo "Running E2E tests..."
	./test/e2e/run.sh

##@ Quality

.PHONY: lint
lint:
	golangci-lint run --timeout=10m ./...

.PHONY: fmt
fmt:
	go fmt ./...
	goimports -w -local $(PROJECT_REPO) .

.PHONY: vet
vet:
	go vet ./...

.PHONY: reviewable
reviewable: vendor generate lint test
	@echo "Code is reviewable!"

##@ Local Development

.PHONY: dev-kind-create
dev-kind-create:
	kind create cluster --name crossplane-dev --config hack/kind-config.yaml

.PHONY: dev-kind-delete
dev-kind-delete:
	kind delete cluster --name crossplane-dev

.PHONY: dev-install-crossplane
dev-install-crossplane:
	helm repo add crossplane-stable https://charts.crossplane.io/stable
	helm repo update
	helm install crossplane crossplane-stable/crossplane \
		--namespace crossplane-system \
		--create-namespace \
		--wait

.PHONY: dev-deploy
dev-deploy: docker-build
	kind load docker-image $(IMAGE_NAME):$(VERSION) --name crossplane-dev
	kubectl apply -f examples/provider.yaml

.PHONY: dev-run
dev-run:
	go run ./cmd/provider/main.go --debug

##@ Cleanup

.PHONY: clean
clean:
	rm -rf $(BUILD_DIR)
	rm -rf vendor/
	rm -f coverage.out coverage.html

.PHONY: clean-all
clean-all: clean
	rm -rf $(CRD_DIR)/*.yaml
	find ./apis -name 'zz_generated*' -delete

##@ Tools

$(CONTROLLER_GEN):
	@mkdir -p $(TOOLS_HOST_DIR)
	@echo "Installing controller-gen $(CONTROLLER_GEN_VERSION)..."
	@GOBIN=$(TOOLS_HOST_DIR) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)
	@mv $(TOOLS_HOST_DIR)/controller-gen $(CONTROLLER_GEN)

.PHONY: tools
tools: $(CONTROLLER_GEN)
	@echo "All tools installed"

##@ Release

.PHONY: release
release: reviewable build.xpkg docker-build-multiarch push.xpkg
	@echo "Release $(VERSION) complete!"
	@echo "Docker image: $(IMAGE_NAME):$(VERSION)"
	@echo "Package: $(DIST_DIR)/$(PROJECT_NAME)-$(VERSION).xpkg"

.PHONY: release-notes
release-notes:
	@echo "Generating release notes for $(VERSION)..."
	@git log --pretty=format:"- %s (%h)" $(shell git describe --tags --abbrev=0)..HEAD > RELEASE_NOTES.md
	@echo "Release notes written to RELEASE_NOTES.md"

##@ All-in-One

.PHONY: all
all: clean vendor generate test build docker-build build.xpkg
	@echo "Build complete!"
