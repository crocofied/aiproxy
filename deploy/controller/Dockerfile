# Main node image: Traefik + controller, supervised by s6-overlay.
# Build from the repository root:
#   docker build -f deploy/controller/Dockerfile -t lanproxy .

ARG TRAEFIK_VERSION=v3.6
ARG S6_OVERLAY_VERSION=3.2.1.0

# --- 1. web interface (static files) ---
FROM --platform=$BUILDPLATFORM node:22-alpine AS ui
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# --- 2. controller binary with the web interface embedded ---
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY cmd ./cmd
COPY internal ./internal
COPY --from=ui /web/build/ ./internal/controller/ui/dist/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
    -o /out/lanproxy-controller ./cmd/controller

# --- 3. Traefik binary from the official image ---
FROM traefik:${TRAEFIK_VERSION} AS traefik

# --- 4. s6-overlay (process supervisor), unpacked into /s6 ---
FROM alpine:3.22 AS s6
ARG S6_OVERLAY_VERSION
ARG TARGETARCH
RUN apk add --no-cache xz
RUN set -eu; \
    case "$TARGETARCH" in \
        amd64) arch=x86_64 ;; \
        arm64) arch=aarch64 ;; \
        *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    mkdir /s6; \
    for part in noarch "$arch"; do \
        wget -qO- "https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}/s6-overlay-${part}.tar.xz" \
            | tar -C /s6 -Jxpf -; \
    done

# --- 5. final image ---
FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=s6 /s6/ /
COPY --from=traefik /usr/local/bin/traefik /usr/local/bin/traefik
COPY --from=build /out/lanproxy-controller /usr/local/bin/lanproxy-controller
COPY deploy/controller/rootfs/ /
# A checkout on Windows loses the executable bit.
RUN chmod +x /etc/s6-overlay/s6-rc.d/controller/run /etc/s6-overlay/s6-rc.d/traefik/run

ENV LANPROXY_DATA_DIR=/data \
    # How the controller restarts Traefik (and only Traefik).
    LANPROXY_TRAEFIK_RESTART_COMMAND="/command/s6-svc -r /run/service/traefik" \
    # Soft limit for the Go garbage collector of the controller.
    GOMEMLIMIT=40MiB \
    # Stop the container if a service cannot be started at all.
    S6_BEHAVIOUR_IF_STAGE2_FAILS=2

VOLUME /data
EXPOSE 80 443 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s \
    CMD ["/usr/local/bin/lanproxy-controller", "healthcheck"]
ENTRYPOINT ["/init"]
