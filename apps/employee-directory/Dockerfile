FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum* ./

# Download dependencies
RUN go mod download

# Copy source code
COPY *.go ./

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o employee-directory .

# Runtime stage
FROM scratch

WORKDIR /app

# Copy CA certificates for HTTPS requests
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy binary from builder
COPY --from=builder /app/employee-directory .

# Copy OpenAPI spec
COPY openapi.yaml .

# Expose port
EXPOSE 8080

# Run the application
ENTRYPOINT ["./employee-directory"]
