# Copyright 2023 The Kubernetes Authors.
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

ARG GOLANG_VERSION=1.27
ARG BASE_IMAGE=undefined

# Build stage using native architecture to avoid emulation issues during Go compilation
FROM --platform=$BUILDPLATFORM golang:${GOLANG_VERSION} AS build

ARG TARGETARCH

WORKDIR /go/src/github.com/SchSeba/dra-multi-network
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN mkdir /artifacts
RUN --mount=type=cache,target=/root/.cache/go-build GOOS=linux GOARCH=${TARGETARCH} make PREFIX=/artifacts cmds

# Final stage using target architecture
FROM ${BASE_IMAGE}

LABEL org.opencontainers.image.source=https://github.com/SchSeba/dra-multi-network
LABEL org.opencontainers.image.description="Dynamic Resource Allocation (DRA) driver for secondary network topologies"
LABEL org.opencontainers.image.licenses=Apache-2.0
LABEL org.opencontainers.image.title="Dynamic Resource Allocation (DRA) driver for secondary network topologies"
LABEL org.opencontainers.image.vendor="dra-multi-network.schseba.github.io"
LABEL org.opencontainers.image.version=${VERSION}
LABEL org.opencontainers.image.release=${RELEASE}
LABEL io.k8s.display-name="Dynamic Resource Allocation (DRA) driver for secondary network topologies"
LABEL name="Dynamic Resource Allocation (DRA) driver for secondary network topologies"
LABEL vendor="dra-multi-network.schseba.github.io"
ARG VERSION="N/A"
LABEL version=${VERSION}
ARG RELEASE="N/A"
LABEL release="N/A"
LABEL summary="Dynamic Resource Allocation (DRA) driver for secondary network topologies"
LABEL description="See summary"

RUN yum -y install hwdata pciutils && yum clean all
COPY --from=build /artifacts/dra-multi-network-node /usr/bin/dra-multi-network-node
COPY --from=build /artifacts/dra-multi-network-controller /usr/bin/dra-multi-network-controller
COPY --from=build /artifacts/network-topology-compile /usr/bin/network-topology-compile
