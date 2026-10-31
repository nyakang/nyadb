#!/usr/bin/env bash
# An explicitly invoked disposable fixture, scoped to this script's own container.
set -euo pipefail
image="${NYADB_MYSQL_CORE_IMAGE:-mysql:8.4}"
case "$image" in mysql:8.0|mysql:8.4) ;; *) echo 'Only mysql:8.0 or mysql:8.4 fixtures are allowed' >&2; exit 1;; esac
container="nyadb-tunnels-it-$$"
if docker container inspect "$container" >/dev/null 2>&1; then echo 'Refusing existing container' >&2; exit 1; fi
docker run --detach --rm --name "$container" --publish 127.0.0.1::3306 --env MYSQL_ALLOW_EMPTY_PASSWORD=yes "$image" >/dev/null
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
SQL
port="$(docker port "$container" 3306/tcp | awk -F: 'NR==1 {print $NF}')"
if ! [[ "$port" =~ ^[0-9]+$ ]] || ((port<1024 || port>65535)); then echo 'Invalid loopback port' >&2; exit 1; fi
NYADB_MYSQL_CORE_IT=1 NYADB_MYSQL_CORE_PORT="$port" GOWORK=off CGO_ENABLED=1 \
 go test -race ./internal/services/mysql -run '^TestMySQL(GatewayDisposablePhysicalLeasesTransactionsAndRevocation|SSHDisposablePhysicalLeaseAndTransaction)$' -count=1 -v
