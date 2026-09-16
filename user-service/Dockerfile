FROM golang:1-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /user-service .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates && \
    adduser -D -g '' appuser

WORKDIR /app
COPY --from=builder /user-service .

USER appuser
EXPOSE 8002

CMD ["./user-service"]
