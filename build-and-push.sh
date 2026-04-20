#!/bin/bash
set -e

# Build and push the gRPC ext-authz server to DockerHub.
# Usage: ./build-and-push.sh [version]
# Example: ./build-and-push.sh 0.0.2

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

IMAGE_NAME="ably7/grpc-ext-authz"
DEFAULT_VERSION="0.0.1"

if [ -n "$1" ]; then
  VERSION="$1"
else
  VERSION="$DEFAULT_VERSION"
fi

IMAGE="${IMAGE_NAME}:${VERSION}"
echo "=== Building and pushing ${IMAGE} ==="

# Build and push multi-arch image
docker buildx build --builder ly-builder \
  --platform linux/amd64,linux/arm64 \
  -t "$IMAGE" \
  -t "${IMAGE_NAME}:latest" \
  --push .

echo ""
echo "=== Pushed ${IMAGE} ==="
echo "=== Pushed ${IMAGE_NAME}:latest ==="
