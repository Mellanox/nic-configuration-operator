# Build the manager binary
FROM golang:1.24 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG GCFLAGS

WORKDIR /workspace
# Copy sources
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN --mount=type=cache,target=/go/pkg/mod/ go mod download

# Copy the go source
COPY ./ ./

# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
#RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/maintenance-manager/main.go
RUN --mount=type=cache,target=/go/pkg/mod/ GO_GCFLAGS=${GCFLAGS} make build-manager

FROM quay.io/centos/centos:stream9

RUN yum -y install mstflint && yum clean all

WORKDIR /
COPY --from=builder /workspace/build/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]

LABEL org.opencontainers.image.source=https://github.com/Mellanox/nic-configuration-operator
