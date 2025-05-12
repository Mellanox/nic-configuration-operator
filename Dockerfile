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

FROM nvcr.io/nvidia/doca/doca:3.0.0-full-rt-host

ARG TARGETARCH
ENV MFT_VERSION=4.29.0-131

ARG PACKAGES="dpkg-dev=1.21.1ubuntu2.3 mstflint=4.21.0+1-1ubuntu0.1~22.04.1"

# enable deb-src repos
RUN sed -i 's/^# deb-src/deb-src/g' /etc/apt/sources.list /etc/apt/sources.list.d/*

RUN apt-get update -y
RUN apt-get install -y --no-install-recommends ${PACKAGES}
RUN apt-get source ${PACKAGES}

WORKDIR /
COPY --from=builder /workspace/build/manager .
USER 65532:65532

# Copy sources to the container
ADD . /workspace

ENTRYPOINT ["/manager"]

LABEL org.opencontainers.image.source=https://nvcr.io/nvidia/cloud-native/nic-configuration-operator
