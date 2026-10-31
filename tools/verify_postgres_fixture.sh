#!/usr/bin/env bash
set -euo pipefail
fixture_name="nyadb-pg-it-$RANDOM-$$"
cleanup() { docker rm -f "$fixture_name" >/dev/null 2>&1 || true; }
trap cleanup EXIT
# Dedicated empty fixture only. Never reuse a named existing server.
docker run --rm -d --name "$fixture_name" -p 127.0.0.1::5432 -e POSTGRES_USER=nyadb_it -e POSTGRES_DB=nyadb_it -e POSTGRES_HOST_AUTH_METHOD=trust postgres:17-alpine >/dev/null
for attempt in {1..60}; do
 if docker exec "$fixture_name" pg_isready -U nyadb_it -d nyadb_it >/dev/null 2>&1; then break; fi
 sleep 1
done
fixture_port=$(docker port "$fixture_name" 5432/tcp)
fixture_port=${fixture_port##*:}
GOWORK=off CGO_ENABLED=0 NYADB_POSTGRES_IT=1 NYADB_POSTGRES_PORT="$fixture_port" go test ./internal/services/engines -run TestPostgreSQL17Fixture -count=1 -v
