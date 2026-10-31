#!/usr/bin/env bash
# Only creates/removes its own ephemeral MySQL container. No original software.
set -euo pipefail
image="${NYADB_MYSQL_CORE_IMAGE:-mysql:8.4}"
case "$image" in mysql:8.0|mysql:8.4) ;; *) echo 'Only mysql:8.0 or mysql:8.4 fixtures are allowed' >&2; exit 1;; esac
container="nyadb-core-it-$$"
if docker container inspect "$container" >/dev/null 2>&1; then
  echo 'Refusing to reuse a container' >&2
  exit 1
fi
docker run --detach --rm --name "$container" --publish 127.0.0.1::3306 --env MYSQL_ALLOW_EMPTY_PASSWORD=yes "$image" --log-bin-trust-function-creators=1 >/dev/null
trap 'docker rm -f "$container" >/dev/null 2>&1 || true' EXIT
ready=0
for ((i=0;i<65;i++)); do
  if docker exec "$container" mysqladmin ping --protocol=tcp -h127.0.0.1 -uroot --silent >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
if [[ "$ready" != 1 ]]; then echo 'Disposable fixture did not start' >&2; exit 1; fi
docker exec -i "$container" mysql -uroot <<'SQL'
CREATE DATABASE nyadb_core_it;
CREATE USER 'nyadb_core'@'%' IDENTIFIED BY '';
GRANT ALL PRIVILEGES ON nyadb_core_it.* TO 'nyadb_core'@'%';
GRANT ALL PRIVILEGES ON nyadb_object_database.* TO 'nyadb_core'@'%';
SQL
port="$(docker port "$container" 3306/tcp | awk -F: 'NR==1 {print $NF}')"
if ! [[ "$port" =~ ^[0-9]+$ ]] || ((port<1024 || port>65535)); then echo 'Invalid disposable loopback port' >&2; exit 1; fi
NYADB_MYSQL_CORE_IT=1 NYADB_MYSQL_CORE_PORT="$port" GOWORK=off CGO_ENABLED=0 \
  go test ./internal/services/mysql -run '^(TestTableDesignDisposableMySQLRoundTrip|TestSQLPipelineDisposableTypedMultiResultsAndFullStream|TestMySQLObjectManagementCoreFixture|TestMySQLBackupRestoreCoreFixture|TestNontransactionalRestoreCoreFixture)$' -count=1 -v
