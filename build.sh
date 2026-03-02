#!/usr/bin/env bash
set -euo pipefail

IMAGE="quay.io/migtools/f3denver-help"
TAG="${1:-latest}"
MANIFEST="${IMAGE}:${TAG}"

echo "==> Building linux/amd64 image: ${MANIFEST}"

podman build \
    --platform "linux/amd64" \
    -t "${MANIFEST}" \
    -f Dockerfile \
    .

echo ""
echo "==> Pushing ${MANIFEST}..."
podman push "${MANIFEST}"

echo ""
echo "==> Done. Pushed ${MANIFEST} (amd64)"
