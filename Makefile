# Version information
include Makefile.version
include make/license.mk

# Set the Operator SDK version to use. By default, what is installed on the system is used.
# This is useful for CI or a project to utilize a specific version of the operator-sdk toolkit.
OPERATOR_SDK_VERSION ?= v1.36.0
# Image URL to use all building/pushing image targets
OPERATOR_IMAGE_TAG ?= nic-configuration-operator:latest
# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.31.0

CONFIG_DAEMON_IMAGE_TAG ?= nic-configuration-daemon:latest

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Build args
TARGETOS ?= $(shell go env GOOS)
TARGETARCH ?= $(shell go env GOARCH)
GO_BUILD_OPTS ?= CGO_ENABLED=0 GOOS=$(TARGETOS) GOARCH=$(TARGETARCH)
GO_LDFLAGS ?= $(VERSION_LDFLAGS)
GO_GCFLAGS ?=
GOPROXY ?=

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

CURPATH=$(PWD)

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases
	cp -f config/crd/bases/* deployment/nic-configuration-operator-chart/crds

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

# Utilize Kind or modify the e2e tests to load the image locally, enabling compatibility with other vendors.
.PHONY: test-e2e  # Run the e2e tests against a Kind k8s instance that is spun up.
test-e2e:
	go test ./test/e2e/ -v -ginkgo.v

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter & yamllint
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	$(GOLANGCI_LINT) run --fix

.PHONY: generate-api-docs
generate-api-docs: gen-crd-api-reference-docs ## generate api documentation
	$(GEN_CRD_API_REFERENCE_DOCS) -api-dir=./api/v1alpha1 -config=${CURDIR}/hack/api-docs/config.json \
	-template-dir=${CURDIR}/hack/api-docs/templates -out-file=build/api-reference.html
	$(CONTAINER_TOOL) run --rm --volume "`pwd`:/data:Z" pandoc/minimal -f html -t markdown_strict \
	--columns 200 /data/build/api-reference.html -o /data/docs/api-reference.md
	hack/api-docs/fix_links.sh docs/api-reference.md
	chmod a+w docs/api-reference.md

.PHONY: generate-helm-docs
generate-helm-docs: helm-docs ## generate helm documentation
	cd deployment/nic-configuration-operator-chart && $(HELM_DOCS)

##@ Build

.PHONY: build
build: manifests generate fmt vet build-manager build-daemon

build-manager: ## Build manager binary.
	$(GO_BUILD_OPTS) go build -ldflags $(GO_LDFLAGS) -gcflags="$(GO_GCFLAGS)" -o build/manager cmd/manager/main.go

build-daemon: ## Build nic-configuration-daemon binary.
	go build -o build/nic-configuration-daemon cmd/nic-configuration-daemon/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build -f Dockerfile -t ${OPERATOR_IMAGE_TAG} --build-arg GOPROXY="$(GOPROXY)" ${CURPATH}
	$(CONTAINER_TOOL) build -f Dockerfile.nic-configuration-daemon -t ${CONFIG_DAEMON_IMAGE_TAG} --build-arg GOPROXY="$(GOPROXY)" ${CURPATH}

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${OPERATOR_IMAGE_TAG}
	$(CONTAINER_TOOL) push ${CONFIG_DAEMON_IMAGE_TAG}

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx OPERATOR_IMAGE_TAG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via OPERATOR_IMAGE_TAG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name project-v3-builder
	$(CONTAINER_TOOL) buildx use project-v3-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag ${OPERATOR_IMAGE_TAG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm project-v3-builder
	rm Dockerfile.cross
	# same for config-daemon
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile.nic-configuration-daemon > Dockerfile.nic-configuration-daemon.cross
	- $(CONTAINER_TOOL) buildx create --name project-v3-builder
	$(CONTAINER_TOOL) buildx use project-v3-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag ${CONFIG_DAEMON_IMAGE_TAG} -f Dockerfile.nic-configuration-daemon.cross .
	- $(CONTAINER_TOOL) buildx rm project-v3-builder
	rm Dockerfile.nic-configuration-daemon.cross

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/manager && $(KUSTOMIZE) edit set image controller=${OPERATOR_IMAGE_TAG}
	$(KUSTOMIZE) build config/default > dist/install.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${OPERATOR_IMAGE_TAG}
	$(KUSTOMIZE) build config/default | $(KUBECTL) apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUBECTL ?= kubectl
KUSTOMIZE ?= $(LOCALBIN)/kustomize-$(KUSTOMIZE_VERSION)
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen-$(CONTROLLER_TOOLS_VERSION)
ENVTEST ?= $(LOCALBIN)/setup-envtest-$(ENVTEST_VERSION)
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)

## Tool Versions
KUSTOMIZE_VERSION ?= v5.3.0
CONTROLLER_TOOLS_VERSION ?= v0.14.0
ENVTEST_VERSION ?= release-0.19
GOLANGCI_LINT_VERSION ?= v1.64.2

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

YQ := $(abspath $(LOCALBIN)/yq)
YQ_VERSION=v4.44.1
.PHONY: yq
yq: $(YQ) ## Download yq locally if necessary.
$(YQ): | $(LOCALBIN)
	@curl -fsSL -o $(YQ) https://github.com/mikefarah/yq/releases/download/$(YQ_VERSION)/yq_linux_amd64 && chmod +x $(YQ)

HELM := $(abspath $(LOCALBIN)/helm)
.PHONY: helm
helm: $(HELM) ## Download helm (last release) locally if necessary.
$(HELM): | $(LOCALBIN)
	@{ \
		curl -fsSL -o $(LOCALBIN)/get_helm.sh https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 && \
		chmod 700 $(LOCALBIN)/get_helm.sh && \
		HELM_INSTALL_DIR=$(LOCALBIN) USE_SUDO=false $(LOCALBIN)/get_helm.sh && \
		rm -f $(LOCALBIN)/get_helm.sh; \
	}

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/cmd/golangci-lint,${GOLANGCI_LINT_VERSION})

GEN_CRD_API_REFERENCE_DOCS = $(LOCALBIN)/gen-crd-api-reference-docs
.PHONY: gen-crd-api-reference-docs ## Download gen-crd-api-reference-docs locally if necessary
gen-crd-api-reference-docs: $(GEN_CRD_API_REFERENCE_DOCS)
$(GEN_CRD_API_REFERENCE_DOCS): | $(LOCALBIN)
	@ GOBIN=$(LOCALBIN) go install github.com/ahmetb/gen-crd-api-reference-docs@latest

HELM_DOCS = $(LOCALBIN)/helm-docs
HELM_DOCS_VERSION ?= v1.14.2
.PHONY: helm-docs ## Download helm-docs locally if necessary
helm-docs: $(HELM_DOCS)
$(HELM_DOCS): | $(LOCALBIN)
	@ GOBIN=$(LOCALBIN) go install github.com/norwoodj/helm-docs/cmd/helm-docs@$(HELM_DOCS_VERSION)

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary (ideally with version)
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f $(1) ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
mv "$$(echo "$(1)" | sed "s/-$(3)$$//")" $(1) ;\
}
endef

.PHONY: operator-sdk
OPERATOR_SDK ?= $(LOCALBIN)/operator-sdk
operator-sdk: ## Download operator-sdk locally if necessary.
ifeq (,$(wildcard $(OPERATOR_SDK)))
ifeq (, $(shell which operator-sdk 2>/dev/null))
	@{ \
	set -e ;\
	mkdir -p $(dir $(OPERATOR_SDK)) ;\
	OS=$(shell go env GOOS) && ARCH=$(shell go env GOARCH) && \
	curl -sSLo $(OPERATOR_SDK) https://github.com/operator-framework/operator-sdk/releases/download/$(OPERATOR_SDK_VERSION)/operator-sdk_$${OS}_$${ARCH} ;\
	chmod +x $(OPERATOR_SDK) ;\
	}
else
OPERATOR_SDK = $(shell which operator-sdk)
endif
endif

.PHONY: chart-prepare-release
chart-prepare-release: | $(YQ) ## prepare helm chart for release
	@GITHUB_TAG=$(GITHUB_TAG) GITHUB_REPO_OWNER=$(GITHUB_REPO_OWNER) hack/release/chart-update.sh

.PHONY: chart-push-release
chart-push-release: | $(HELM) ## push release helm chart
	@GITHUB_TAG=$(GITHUB_TAG) GITHUB_TOKEN=$(GITHUB_TOKEN) GITHUB_REPO_OWNER=$(GITHUB_REPO_OWNER) hack/release/chart-push.sh
