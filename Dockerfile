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
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/lab ./cmd/lab && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/storage-nfs ./cmd/storage-nfs && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/portal ./cmd/portal

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
RUN apt-get update && apt-get install -y --no-install-recommends curl libcap2-bin && rm -rf /var/lib/apt/lists/*
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=build /out/cloud /usr/local/bin/cloud
RUN setcap cap_dac_override+ep /usr/local/bin/cloud
USER 65532:65532
ENTRYPOINT ["cloud"]
CMD ["serve","--data-dir","/data"]

FROM base AS lab
COPY --from=build /out/lab /usr/local/bin/lab
USER 65532:65532
ENTRYPOINT ["lab"]

FROM debian:bookworm-slim AS nfs
# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends nfs-ganesha nfs-ganesha-vfs rpcbind supervisor && \
    rm -rf /var/lib/apt/lists/* && mkdir -p /var/run/ganesha /export && chmod 0777 /export
COPY --from=build /out/storage-nfs /usr/local/bin/storage-nfs
COPY storage/supervisord.conf /etc/supervisor/conf.d/nfs.conf
LABEL org.opencontainers.image.source="https://github.com/miciav/minikube-cloud-lab"
ENTRYPOINT ["/usr/bin/supervisord", "-n", "-c", "/etc/supervisor/conf.d/nfs.conf"]

FROM --platform=$BUILDPLATFORM node:26.12.1-bookworm-slim AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM base AS portal
COPY --from=build /out/portal /usr/local/bin/portal
COPY --from=web-build /src/web/dist /app/web
USER 65532:65532
ENTRYPOINT ["portal"]
