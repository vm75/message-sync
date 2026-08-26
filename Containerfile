FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=development
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -trimpath \
    -ldflags "-s -w -X github.com/vm75/message-sync/internal/version.Build=${VERSION}" \
    -o /out/message-sync ./cmd/message-sync

FROM docker.io/library/alpine:3.22
RUN addgroup -g 1000 message-sync \
    && adduser -u 1000 -G message-sync -D -H message-sync \
    && mkdir -p /data \
    && chown -R message-sync:message-sync /data
COPY --from=build /out/message-sync /usr/local/bin/message-sync

USER message-sync
WORKDIR /data
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/message-sync"]
CMD ["run"]
