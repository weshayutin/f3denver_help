#!/usr/bin/env bash
# Build, push, and optionally deploy to the Linode server.
#
# Usage:
#   ./build.sh                 # build + push :latest, then deploy
#   ./build.sh v1.2.0          # build + push a tag, then deploy
#   ./build.sh latest --no-deploy   # build + push only
#
set -euo pipefail

IMAGE="quay.io/migtools/f3denver-help"
TAG="${1:-latest}"
DEPLOY=true
LINODE_HOST="172.232.163.125"
LINODE_USER="${LINODE_USER:-root}"

for arg in "$@"; do
    [[ "$arg" == "--no-deploy" ]] && DEPLOY=false
done

echo "==> Building linux/amd64 image: ${IMAGE}:${TAG}"
podman build \
    --platform "linux/amd64" \
    -t "${IMAGE}:${TAG}" \
    -f Dockerfile \
    .

echo ""
echo "==> Pushing ${IMAGE}:${TAG} ..."
podman push "${IMAGE}:${TAG}"

if [[ "$DEPLOY" == "true" ]]; then
    echo ""
    echo "==> Deploying to ${LINODE_HOST} ..."
    ssh "${LINODE_USER}@${LINODE_HOST}" \
        "systemctl restart f3denver-help && sleep 3 && systemctl status f3denver-help --no-pager"
    echo ""
    echo "==> Deployed! App is live at http://${LINODE_HOST}:8181"
else
    echo ""
    echo "==> Push complete. Run './build.sh --no-deploy' skipped deploy."
fi

echo ""
echo "==> Done."
