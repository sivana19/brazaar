# Stage 1: Build the Go binary
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code and build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/server cmd/api/main.go

# Stage 2: Create a minimal, secure production image
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Copy compiled binary from builder
COPY --from=builder /app/server .

# Expose port (default 8080)
EXPOSE 8080

CMD ["./server"]