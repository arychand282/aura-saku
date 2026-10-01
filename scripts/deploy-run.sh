#!/usr/bin/env bash
set -euo pipefail

# Pulls the latest backend image from Docker Hub and (re)starts it as a
# standalone container, reading config from the local .env. Run this ON THE
# VM, after scripts/deploy-build-push.sh has pushed a new image.
#
# Deliberately NOT docker-compose: docker-compose.yml's backend service
# hardcodes DB_HOST=postgres / REDIS_HOST=redis for local dev's bundled
# containers, which would override .env's Supabase/Upstash values here — see
# aura-saku.plan.md's deployment notes.
#
# Usage (from ~/aura-saku on the VM, with .env already filled in):
#   DOCKERHUB_USERNAME=yourname ./scripts/deploy-run.sh
# (or edit DOCKERHUB_USERNAME's default below once, so it's not needed on
# every run)

DOCKERHUB_USERNAME="${DOCKERHUB_USERNAME:-}"
IMAGE_NAME="${IMAGE_NAME:-aura-saku-backend}"
TAG="${TAG:-latest}"
CONTAINER_NAME="${CONTAINER_NAME:-aura-saku-backend}"
APP_PORT="${APP_PORT:-8080}"

if [ -z "$DOCKERHUB_USERNAME" ]; then
  echo "Set DOCKERHUB_USERNAME (env var) or edit this script's default." >&2
  exit 1
fi

cd "$(dirname "$0")/.."

if [ ! -f .env ]; then
  echo ".env not found in $(pwd) — create it before running this." >&2
  exit 1
fi

IMAGE="${DOCKERHUB_USERNAME}/${IMAGE_NAME}:${TAG}"

docker pull "$IMAGE"

if docker ps -a --format '{{.Names}}' | grep -qx "$CONTAINER_NAME"; then
  docker stop "$CONTAINER_NAME"
  docker rm "$CONTAINER_NAME"
fi

docker run -d --name "$CONTAINER_NAME" --restart unless-stopped \
  --env-file .env -p "${APP_PORT}:${APP_PORT}" "$IMAGE"

echo "Started ${CONTAINER_NAME} from ${IMAGE}"
docker logs "$CONTAINER_NAME" --tail 30
