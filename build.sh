#!/bin/bash

# Exit immediately if a command exits with a non-zero status
set -e

# Variables
IMAGE_NAME="registry.meisterlala.dev/tucan-ical"
TAG="latest"
PLATFORMS="linux/amd64,linux/arm64,linux/arm/v7"

# Ensure Buildx is available
echo "Ensuring Docker Buildx is available..."
docker buildx version > /dev/null

# Create and use a Buildx builder if not already set up
echo "Setting up Docker Buildx builder..."
docker buildx create --use --name tucan-builder --driver docker-container --bootstrap 2>/dev/null || true

# Build and push the Docker image for multiple platforms
echo "Building and pushing Docker image for platforms: $PLATFORMS..."
docker buildx build --platform $PLATFORMS -t "$IMAGE_NAME:$TAG" --push .

echo "Build and push completed successfully."
