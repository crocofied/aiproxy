# Build from the repository root:
#   docker build -f deploy/agent/Dockerfile -t lanproxy-agent .

# BUILDPLATFORM lets a multi-arch build compile natively and cross-compile
# with Go instead of emulating the target CPU.
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
COPY api ./api
COPY cmd/agent ./cmd/agent
COPY internal/agent ./internal/agent
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/agent ./cmd/agent

# scratch is an empty image: the result contains the binary and the CA
# certificates (needed for an https:// controller URL), nothing else.
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/agent /agent
ENTRYPOINT ["/agent"]
