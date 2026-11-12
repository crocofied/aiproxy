VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
UI_DIST := internal/controller/ui/dist

.PHONY: all build agent controller ui test lint fmt images agent-image controller-image up down e2e clean

all: lint test build

build: agent controller

agent:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/agent ./cmd/agent

# The controller embeds the web interface, so that is built first.
controller: ui
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/controller ./cmd/controller

# Builds the SvelteKit app and copies it to where go:embed picks it up.
ui:
	cd web && npm ci && npm run build
	find $(UI_DIST) -mindepth 1 ! -name .gitkeep -delete
	cp -r web/build/. $(UI_DIST)/

test:
	go test -race ./...
	cd web && npm ci && npm run check

lint:
	golangci-lint run ./...

fmt:
	golangci-lint fmt ./...

images: controller-image agent-image

controller-image:
	docker build -f deploy/controller/Dockerfile --build-arg VERSION=$(VERSION) -t lanproxy:latest .

agent-image:
	docker build -f deploy/agent/Dockerfile --build-arg VERSION=$(VERSION) -t lanproxy-agent:latest .

# Start / stop the main node (same as start.sh and start.ps1).
up:
	docker compose up -d --build

down:
	docker compose down

# Test environment with three Docker-in-Docker "hosts", see test/e2e.
e2e:
	./test/e2e/run.sh

clean:
	rm -rf bin web/build web/.svelte-kit
	find $(UI_DIST) -mindepth 1 ! -name .gitkeep -delete
