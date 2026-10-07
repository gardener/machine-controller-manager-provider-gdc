# syntax=docker/dockerfile:1.26@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32
# SPDX-FileCopyrightText: 2026 Google LLC
#
# SPDX-License-Identifier: Apache-2.0

#############      builder       #############
FROM --platform=$BUILDPLATFORM golang:1.27.1@sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /build

# Copy go mod and sum files
COPY go.mod go.sum ./
# Download all dependencies. Cached via BuildKit cache mount independent of layer cache.
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH make release

############# base
FROM gcr.io/distroless/static-debian13:nonroot AS base
WORKDIR /
USER 65532:65532

#############      machine-controller-manager-provider-gdch     #############
FROM base AS machine-controller-manager-provider-gdch

COPY --from=builder /build/machine-controller-manager-provider-gdch /machine-controller

ENTRYPOINT ["/machine-controller"]
