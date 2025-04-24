# Stage 1: Build the application
FROM golang:1.24.2-alpine3.21 AS builder

# Install build dependencies
RUN apk add --no-cache git

# Set the working directory
WORKDIR /app

# Copy go.mod and go.sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the source code
COPY . .

# Build the application
RUN go build -o main .

# Stage 2: Create a minimal runtime image
FROM alpine:3.21

# Install certificates for HTTPS
RUN apk add --no-cache ca-certificates

# Set the working directory
WORKDIR /root/

# Copy the built application from the builder stage
COPY --from=builder /app/main .

# Expose the port
EXPOSE 8080

# Command to run the application
CMD ["./main"]
