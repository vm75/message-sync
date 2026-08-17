BINARY := message-sync

.PHONY: fmt test vet build run container-build
fmt:
	gofmt -w ./cmd ./internal

test:
	go test ./...

vet:
	go vet ./...

build:
	mkdir -p bin
	go build -trimpath -o bin/$(BINARY) ./cmd/message-sync

run:
	go run ./cmd/message-sync run

container-build:
	podman build -f Containerfile -t message-sync:dev .
