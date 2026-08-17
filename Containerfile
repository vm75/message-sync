FROM docker.io/library/golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod ./
RUN go mod download
COPY . .

ARG VERSION=development
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-s -w -X github.com/vm75/message-sync/internal/version.Build=${VERSION}" \
    -o /out/message-sync ./cmd/message-sync

FROM docker.io/library/alpine:3.22
RUN addgroup -S -g 10001 message-sync \
    && adduser -S -D -H -u 10001 -G message-sync message-sync \
    && mkdir -p /data /config \
    && chown -R 10001:10001 /data /config
COPY --from=build /out/message-sync /usr/local/bin/message-sync

USER 10001:10001
WORKDIR /data
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/message-sync"]
CMD ["run"]
