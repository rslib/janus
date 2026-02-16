FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o /janus ./cmd/janus

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /janus /usr/local/bin/janus
RUN mkdir -p /data
VOLUME /data
EXPOSE 3000
ENTRYPOINT ["janus"]
CMD ["-config", "/etc/janus/config.yaml"]
