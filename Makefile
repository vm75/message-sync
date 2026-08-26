BINARY := message-sync

.PHONY: fmt test vet build run container-build container-release
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
	set -a; . ./.env; set +a; go run ./cmd/message-sync run

container-build:
	podman build -f Containerfile -t message-sync:dev .

container-release:
	podman build -f Containerfile -t vm75/message-sync:latest .
