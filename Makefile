.PHONY: build run clean docker

BINARY=janus
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/janus

run: build
	./$(BINARY) -config configs/config.yaml

clean:
	rm -f $(BINARY)

docker:
	docker build -t janus:latest .
