# Copyright 2025 NVIDIA CORPORATION & AFFILIATES
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# SPDX-License-Identifier: Apache-2.0

ARG BASE_IMAGE_DOCA_FULL_RT_HOST

# Build the manager binary
ARG BASE_IMAGE_GO_BUILDER=golang:1.27
FROM ${BASE_IMAGE_GO_BUILDER} AS builder
ARG TARGETOS
ARG TARGETARCH
ARG GCFLAGS
ARG GOPROXY
ENV GOPROXY=$GOPROXY

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

# Download the custom DMS package using the existing Go builder image.
FROM --platform=${BUILDPLATFORM} ${BASE_IMAGE_GO_BUILDER} AS doca-dms
ARG TARGETARCH
# {arch} is replaced by amd/arm. An empty URL keeps the base image's DMS.
ARG DOCA_DMS_URL=""
ARG DOCA_DMS_SHA256_AMD64=""
ARG DOCA_DMS_SHA256_ARM64=""
RUN set -eu; \
    mkdir /debs; \
    if [ -n "${DOCA_DMS_URL}" ]; then \
        case "${TARGETARCH}" in \
            amd64) checksum="${DOCA_DMS_SHA256_AMD64}" ;; \
            arm64) checksum="${DOCA_DMS_SHA256_ARM64}" ;; \
            *) echo "Unsupported DMS package architecture: ${TARGETARCH}" >&2; exit 1 ;; \
        esac; \
        : "${checksum:?Set the DMS SHA256 for the target architecture}"; \
        url=$(printf '%s' "${DOCA_DMS_URL}" | sed "s|{arch}|${TARGETARCH%64}|g"); \
        curl -fsSL --retry 3 "${url}" -o /debs/doca-dms.deb; \
        printf '%s  /debs/doca-dms.deb\n' "${checksum}" | sha256sum -c -; \
    fi

FROM ${BASE_IMAGE_DOCA_FULL_RT_HOST:-nvcr.io/nvstaging/doca/doca:full-rt-3.6.0-ubuntu24.04-host-dev}

ARG TARGETARCH

ARG PACKAGES="dpkg-dev"

# enable deb-src repos
RUN if [ -f /etc/apt/sources.list.d/ubuntu.sources ]; then \
    sed -i 's/^Types: deb$/Types: deb deb-src/' /etc/apt/sources.list.d/ubuntu.sources; \
    elif [ -f /etc/apt/sources.list ]; then \
    sed -i 's/^#\s*deb-src/deb-src/' /etc/apt/sources.list; \
    fi

RUN apt-get update -y
RUN apt-get install -y --no-install-recommends ${PACKAGES}
RUN apt-get source ${PACKAGES}

RUN --mount=type=bind,from=doca-dms,source=/debs,target=/tmp/doca-dms \
    if [ -f /tmp/doca-dms/doca-dms.deb ]; then \
        dpkg -i /tmp/doca-dms/doca-dms.deb; \
    fi

WORKDIR /
COPY --from=builder /workspace/build/manager .
USER 65532:65532

# Copy sources to the container
ADD . /workspace

ENTRYPOINT ["/manager"]

LABEL org.opencontainers.image.source=https://nvcr.io/nvidia/cloud-native/nic-configuration-operator
