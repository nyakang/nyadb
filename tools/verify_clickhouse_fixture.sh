#!/usr/bin/env bash
set -euo pipefail
fixture_repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd -- "$fixture_repo"
test_options=()
fixture_cgo=0
while [[ $# -gt 0 ]]; do
 case "$1" in
  --race) test_options+=(-race); fixture_cgo=1; shift ;;
  *) exit 2 ;;
 esac
done
umask 077
mkdir -p build/verify
evidence_dir=$(mktemp -d "$(pwd)/build/verify/clickhouse-XXXXXX")
fixture_run="nyadb-ch-$$-$RANDOM-$(date +%s)"
fixture_id=""
test_exit=1
cleanup() {
 fixture_cleanup=false
 if [[ -n "$fixture_id" ]]; then
  fixture_label=$(docker inspect --format '{{index .Config.Labels "org.nyadb.clickhouse-fixture"}}' "$fixture_id" 2>/dev/null || true)
  if [[ "$fixture_label" == "$fixture_run" ]]; then
   docker rm -f "$fixture_id" >/dev/null 2>&1 || true
  fi
  if fixture_remaining=$(docker ps --all --no-trunc --filter "id=$fixture_id" --format '{{.ID}}' 2>/dev/null); then
   if [[ -z "$fixture_remaining" ]]; then fixture_cleanup=true; fi
  fi
 fi
 rm -f -- "$evidence_dir/credentials.env"
 printf 'run=%s\ncontainer_id=%s\nowned_container_removed=%s\ntest_exit=%s\n' "$fixture_run" "$fixture_id" "$fixture_cleanup" "$test_exit" >> "$evidence_dir/resource.txt"
 printf 'Evidence: %s\n' "$evidence_dir"
}
trap cleanup EXIT
trap 'test_exit=130; exit 130' INT
trap 'test_exit=143; exit 143' TERM
fixture_password=$(python3 -c 'import secrets; print(secrets.token_hex(32))')
printf 'CLICKHOUSE_USER=nyadb_it\nCLICKHOUSE_PASSWORD=%s\nCLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1\n' "$fixture_password" > "$evidence_dir/credentials.env"
# A new, empty server per run. Secrets are supplied via a private env file.
fixture_id=$(docker run --rm -d --name "$fixture_run" --label "org.nyadb.clickhouse-fixture=$fixture_run" -p 127.0.0.1::9000 --env-file "$evidence_dir/credentials.env" --ulimit nofile=262144:262144 clickhouse/clickhouse-server:25.8)
printf 'run=%s\ncontainer_id=%s\n' "$fixture_run" "$fixture_id" > "$evidence_dir/resource.txt"
fixture_ready=false
for attempt in {1..90}; do
 if docker exec "$fixture_id" bash -c 'exec 3<>/dev/tcp/127.0.0.1/9000' >/dev/null 2>&1; then fixture_ready=true; break; fi
 sleep 1
done
printf 'fixture_ready=%s\n' "$fixture_ready" >> "$evidence_dir/resource.txt"
if [[ "$fixture_ready" != true ]]; then exit 1; fi
fixture_port=$(docker port "$fixture_id" 9000/tcp)
fixture_port=${fixture_port##*:}
go version > "$evidence_dir/versions.txt"
docker exec "$fixture_id" clickhouse-server --version >> "$evidence_dir/versions.txt"
docker inspect --format '{{.Image}}' "$fixture_id" >> "$evidence_dir/versions.txt"
export NYADB_CLICKHOUSE_IT=1 NYADB_CLICKHOUSE_PORT="$fixture_port" NYADB_CLICKHOUSE_USER=nyadb_it NYADB_CLICKHOUSE_PASSWORD="$fixture_password"
set +e
GOWORK=off CGO_ENABLED="$fixture_cgo" go test "${test_options[@]}" ./internal/services/clickhouse ./internal/ui/shell -run 'TestClickHouseNative.*Fixture' -count=1 -v -timeout=20m 2>&1 | tee "$evidence_dir/tests.log"
test_exit=${PIPESTATUS[0]}
set -e
exit "$test_exit"
