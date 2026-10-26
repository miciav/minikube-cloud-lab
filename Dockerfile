# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/cloud ./cmd/cloud && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/lab ./cmd/lab

FROM docker:29.9.2-cli AS docker-cli

FROM debian:bookworm-slim AS base
# Take the Debian release's current security fixes rather than freezing apt package versions.
# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/* && mkdir -p /data /kube && chown 65532:65532 /data /kube
ENV KUBECONFIG=/kube/config HOME=/tmp USER=student
LABEL org.opencontainers.image.source="https://github.com/miciav/minikube-cloud-lab"

FROM base AS cloud
# curl is used by the upstream minikube library for host connectivity checks.
# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends curl && rm -rf /var/lib/apt/lists/*
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=build /out/cloud /usr/local/bin/cloud
USER 65532:65532
ENTRYPOINT ["cloud"]
CMD ["serve","--data-dir","/data"]

FROM base AS lab
COPY --from=build /out/lab /usr/local/bin/lab
USER 65532:65532
ENTRYPOINT ["lab"]
