#!/usr/bin/env bash
set -euo pipefail

IMAGE="quay.io/migtools/f3denver-help"
TAG="${1:-latest}"
PLATFORMS="linux/amd64,linux/arm64"
MANIFEST="${IMAGE}:${TAG}"

echo "==> Building multi-arch images for ${MANIFEST}"
echo "    Platforms: ${PLATFORMS}"

podman manifest rm "${MANIFEST}" 2>/dev/null || true
podman manifest create "${MANIFEST}"

for ARCH in amd64 arm64; do
    echo ""
    echo "==> Building linux/${ARCH}..."
    podman build \
        --platform "linux/${ARCH}" \
        --manifest "${MANIFEST}" \
        -f Dockerfile \
        .
done

echo ""
echo "==> Pushing manifest ${MANIFEST}..."
podman manifest push --all "${MANIFEST}" "docker://${MANIFEST}"

echo ""
echo "==> Done. Pushed ${MANIFEST} (amd64 + arm64)"
