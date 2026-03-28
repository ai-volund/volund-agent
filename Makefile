.PHONY: build test lint clean docker

BINARY=bin/volund-agent
VERSION?=dev

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) ./cmd/agent

test:
	go test -race ./...

lint:
	golangci-lint run

clean:
	rm -rf bin/

docker:
	docker build -t ghcr.io/ai-volund/volund-agent:$(VERSION) .
