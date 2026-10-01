#!/usr/bin/env bash
set -euo pipefail

# Builds the backend image for the VM's architecture (linux/amd64 — needed
# when running this from an Apple Silicon Mac, which is arm64 by default)
# and pushes it straight to Docker Hub via buildx. Run this LOCALLY, not on
# the VM: the e2-micro VM's weak shared vCPU exhausted its burst credits
# compiling this in-process and made the whole instance briefly unreachable,
# which is why the build happens here instead.
#
# Usage:
#   DOCKERHUB_USERNAME=yourname ./scripts/deploy-build-push.sh
# (or edit DOCKERHUB_USERNAME's default below once, so it's not needed on
# every run)

DOCKERHUB_USERNAME="${DOCKERHUB_USERNAME:-}"
IMAGE_NAME="${IMAGE_NAME:-aura-saku-backend}"
TAG="${TAG:-latest}"

if [ -z "$DOCKERHUB_USERNAME" ]; then
  echo "Set DOCKERHUB_USERNAME (env var) or edit this script's default." >&2
  exit 1
fi

cd "$(dirname "$0")/.."

IMAGE="${DOCKERHUB_USERNAME}/${IMAGE_NAME}:${TAG}"

docker buildx build --platform linux/amd64 -t "$IMAGE" --push .

echo "Pushed ${IMAGE}"
