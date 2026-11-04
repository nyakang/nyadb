#!/usr/bin/env bash
set -euo pipefail
fixture_repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd -- "$fixture_repo"
test_options=()
fixture_cgo=0
fixture_pg_version=17
fixture_test_pattern='TestPostgreSQL17.*Fixture'
fixture_shard=""
fixture_native=false
fixture_test_package=./internal/services/engines
while [[ $# -gt 0 ]]; do
 case "$1" in
  --race) test_options+=(-race); fixture_cgo=1; shift ;;
 --native) fixture_native=true; shift ;;
  --run) if [[ $# -lt 2 || -z "$2" ]]; then exit 2; fi; fixture_test_pattern=$2; shift 2 ;;
  --postgres-version) if [[ $# -lt 2 ]]; then exit 2; fi; case "$2" in 14|15|16|17|18) fixture_pg_version=$2 ;; *) exit 2 ;; esac; shift 2 ;;
  --package)
   if [[ $# -lt 2 ]]; then exit 2; fi
   case "$2" in engines|dataworkspace|shell) fixture_test_package="./internal/"; if [[ "$2" == engines ]]; then fixture_test_package+="services/engines"; else fixture_test_package+="ui/$2"; fi ;; *) exit 2 ;; esac
   shift 2 ;;
  --shard)
   if [[ $# -lt 2 || ! "$2" =~ ^([1-9][0-9]*)/([1-9][0-9]*)$ ]]; then exit 2; fi
   fixture_shard_index=${BASH_REMATCH[1]}
   fixture_shard_count=${BASH_REMATCH[2]}
   if [[ ${#fixture_shard_index} -gt 2 || ${#fixture_shard_count} -gt 2 ]]; then exit 2; fi
   if (( fixture_shard_count > 64 || fixture_shard_index > fixture_shard_count )); then exit 2; fi
   fixture_shard=$2
   shift 2 ;;
  *) exit 2 ;;
 esac
done
fixture_name="nyadb-pg-it-$RANDOM-$$"
fixture_id=""
fixture_run="$fixture_name-$(date +%s)"
mkdir -p build/verify
evidence_dir=$(mktemp -d "$(pwd)/build/verify/postgres-XXXXXX")
cleanup() {
 if [[ -n "$fixture_id" ]]; then
  owned_label=$(docker inspect --format '{{index .Config.Labels "org.nyadb.postgres-fixture"}}' "$fixture_id" 2>/dev/null || true)
  if [[ "$owned_label" == "$fixture_run" ]]; then
   docker rm -f "$fixture_id" >/dev/null 2>&1 || true
  fi
  if docker inspect "$fixture_id" >/dev/null 2>&1; then
   printf 'owned_container_removed=false\n' >> "$evidence_dir/resource.txt"
  else
   printf 'owned_container_removed=true\n' >> "$evidence_dir/resource.txt"
  fi
 fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if [[ -n "$fixture_shard" ]]; then
 # Discover the entire requested inventory before creating any server. -list
 # does not run integration cases or require NYADB_POSTGRES_IT. Persist both
 # inventories so an audit can prove that the shards cover every named test.
 GOWORK=off CGO_ENABLED="$fixture_cgo" go test "$fixture_test_package" -list "$fixture_test_pattern" > "$evidence_dir/test-list.raw"
 awk '/^Test[^[:space:]]+$/ { print }' "$evidence_dir/test-list.raw" > "$evidence_dir/test-list.txt"
 awk -v shard="$fixture_shard_index" -v count="$fixture_shard_count" '(NR-1)%count==shard-1' "$evidence_dir/test-list.txt" > "$evidence_dir/selected-tests.txt"
 if [[ ! -s "$evidence_dir/selected-tests.txt" ]]; then exit 2; fi
 # Go test identifiers contain no regular-expression punctuation.
 fixture_test_pattern="^($(paste -sd '|' "$evidence_dir/selected-tests.txt"))$"
fi
# Dedicated empty fixture only. Never reuse a named existing server.
fixture_id=$(docker run --rm -d --name "$fixture_name" --label "org.nyadb.postgres-fixture=$fixture_run" -p 127.0.0.1::5432 -e POSTGRES_USER=nyadb_it -e POSTGRES_DB=nyadb_it -e POSTGRES_HOST_AUTH_METHOD=trust "postgres:${fixture_pg_version}-alpine")
printf 'run=%s\ncontainer_id=%s\ncontainer_name=%s\n' "$fixture_run" "$fixture_id" "$fixture_name" > "$evidence_dir/resource.txt"
fixture_ready=false
for attempt in {1..60}; do
 if docker exec "$fixture_id" pg_isready -h 127.0.0.1 -U nyadb_it -d nyadb_it >/dev/null 2>&1; then fixture_ready=true; break; fi
 sleep 1
done
printf 'fixture_ready=%s\n' "$fixture_ready" >> "$evidence_dir/resource.txt"
printf 'test_package=%s\n' "$fixture_test_package" >> "$evidence_dir/resource.txt"
printf 'test_pattern=%s\n' "$fixture_test_pattern" >> "$evidence_dir/resource.txt"
printf 'shard=%s\n' "$fixture_shard" >> "$evidence_dir/resource.txt"
printf 'postgres_image=%s\n' "postgres:${fixture_pg_version}-alpine" >> "$evidence_dir/resource.txt"
if [[ "$fixture_ready" != true ]]; then exit 1; fi
fixture_port=$(docker port "$fixture_id" 5432/tcp)
fixture_port=${fixture_port##*:}
go version > "$evidence_dir/versions.txt"
docker exec "$fixture_id" postgres --version >> "$evidence_dir/versions.txt"
set +e
GOWORK=off CGO_ENABLED="$fixture_cgo" NYADB_POSTGRES_IT=1 NYADB_POSTGRES_PORT="$fixture_port" go test "${test_options[@]}" "$fixture_test_package" -run "$fixture_test_pattern" -count=1 -v -timeout=20m 2>&1 | tee "$evidence_dir/tests.log"
test_exit=${PIPESTATUS[0]}
set -e
printf 'test_exit=%s\n' "$test_exit" >> "$evidence_dir/resource.txt"
if [[ "$test_exit" == 0 && "$fixture_native" == true ]]; then
 GOWORK=off CGO_ENABLED=0 go test -c -o build/native-import ./internal/ui/dataworkspace
 GOWORK=off CGO_ENABLED=0 go test -c -o build/native-maintenance ./internal/ui/objectworkspace
 set +e
 NYADB_NATIVE_POSTGRES_PORT="$fixture_port" "${NYADB_NATIVE_PYTHON:-python3}" tools/verify_multi_engine_native.py > "$evidence_dir/native.log" 2>&1
 test_exit=$?
 set -e
 printf 'native_exit=%s\n' "$test_exit" >> "$evidence_dir/resource.txt"
fi
printf 'Evidence: %s\n' "$evidence_dir"
exit "$test_exit"
