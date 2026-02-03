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
FROM golang:1.24 AS builder
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

FROM nvcr.io/nvstaging/doca/doca:3.3.0099-full-rt-host-latest

ARG TARGETARCH
ENV MFT_VERSION=4.33.0-169

ARG PACKAGES="dpkg-dev=1.22.6ubuntu6.5"

# enable deb-src repos
RUN sed -i 's/^Types: deb$/Types: deb deb-src/' /etc/apt/sources.list.d/ubuntu.sources

# DOCA repositories have a GPG issue, so we need to allow insecure repositories.
# GPG error: https://linux.mellanox.com/public/repo/doca/3.2.1/ubuntu22.04/x86_64 ./ Release: The following signatures couldn't be verified because the public key is not available: NO_PUBKEY A024F6F0E0E6D6A281
RUN apt-get update -o Acquire::AllowInsecureRepositories=true || true && \
    apt-get install -y --allow-unauthenticated --no-install-recommends ${PACKAGES} && \
    apt-get source ${PACKAGES} && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

WORKDIR /
COPY --from=builder /workspace/build/manager .
USER 65532:65532

# Copy sources to the container
ADD . /workspace

ENTRYPOINT ["/manager"]

LABEL org.opencontainers.image.source=https://nvcr.io/nvidia/cloud-native/nic-configuration-operator
