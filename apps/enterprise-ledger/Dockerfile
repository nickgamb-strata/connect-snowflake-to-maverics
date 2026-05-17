FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum* ./
RUN go mod download

COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o enterprise-ledger .

FROM alpine:latest AS certs
RUN apk --no-cache add ca-certificates

FROM scratch

WORKDIR /app

COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/enterprise-ledger .

ENTRYPOINT ["./enterprise-ledger"]
