# Stage 1: build a fully static binary
FROM golang:1.22-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o app ./cmd/server

# Stage 2: minimal runtime image
FROM alpine:latest

# Go's TLS verification needs a CA bundle even in a static binary; alpine
# ships without one by default.
RUN apk add --no-cache ca-certificates && \
    adduser -D -H -g '' appuser

WORKDIR /app
COPY --from=builder /src/app ./app

USER appuser
EXPOSE 8080

ENTRYPOINT ["./app"]
