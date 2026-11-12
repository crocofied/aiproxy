VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build agent test lint fmt agent-image clean

all: lint test build

build: agent

agent:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/agent ./cmd/agent

test:
	go test -race ./...

lint:
	golangci-lint run ./...

fmt:
	golangci-lint fmt ./...

agent-image:
	docker build -f deploy/agent/Dockerfile --build-arg VERSION=$(VERSION) -t lanproxy-agent:$(VERSION) -t lanproxy-agent:latest .

clean:
	rm -rf bin
