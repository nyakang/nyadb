#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
versions=("$@")
if ((${#versions[@]} == 0)); then versions=(10.11 11.4 11.8); fi
fixtures=()
ssl_dir=""
cleanup() {
 for fixture in "${fixtures[@]}"; do docker rm -f "$fixture" >/dev/null 2>&1 || true; done
 if [[ -n "$ssl_dir" ]]; then rm -rf "$ssl_dir"; fi
}
trap cleanup EXIT
mkdir -p build/mariadb temp/mariadb
ssl_dir=$(mktemp -d "$PWD/temp/mariadb/tls.XXXXXX")
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$ssl_dir/ca.key" -out "$ssl_dir/ca.pem" -days 1 -subj /CN=nyadb-mariadb-fixture-ca >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -keyout "$ssl_dir/server.key" -out "$ssl_dir/server.csr" -subj /CN=localhost >/dev/null 2>&1
printf '%s\n' 'subjectAltName=IP:127.0.0.1,DNS:localhost' > "$ssl_dir/server.ext"
openssl x509 -req -in "$ssl_dir/server.csr" -CA "$ssl_dir/ca.pem" -CAkey "$ssl_dir/ca.key" -CAcreateserial -out "$ssl_dir/server.pem" -days 1 -extfile "$ssl_dir/server.ext" >/dev/null 2>&1
# This temporary fixture key must be readable by the container's mysql user.
chmod 755 "$ssl_dir"
chmod 644 "$ssl_dir/server.key"
for version in "${versions[@]}"; do
 case "$version" in 10.11|11.4|11.8) ;; *) echo "Unsupported fixture version: $version" >&2; exit 1 ;; esac
 fixture="nyadb-mariadb-${version//./-}-$RANDOM-$$"
 fixtures+=("$fixture")
 docker run --rm -d --name "$fixture" -p 127.0.0.1::3306 -v "$ssl_dir:/nyadb-tls:ro" -e MARIADB_ALLOW_EMPTY_ROOT_PASSWORD=1 "mariadb:$version" --ssl-ca=/nyadb-tls/ca.pem --ssl-cert=/nyadb-tls/server.pem --ssl-key=/nyadb-tls/server.key >/dev/null
 ready=false
 for ((attempt=0; attempt<90; attempt++)); do
  if docker exec "$fixture" mariadb-admin ping --protocol=tcp -h127.0.0.1 -uroot --silent >/dev/null 2>&1; then ready=true; break; fi
  sleep 1
 done
 if [[ "$ready" != true ]]; then docker logs "$fixture" >&2; exit 1; fi
 docker exec -i "$fixture" mariadb -uroot <<'SQL'
CREATE DATABASE nyadb_maria_it CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'nyadb_maria'@'%' IDENTIFIED BY '';
ALTER USER 'nyadb_maria'@'%' REQUIRE SSL;
GRANT ALL ON nyadb_maria_it.* TO 'nyadb_maria'@'%';
SQL
 maria_port=$(docker port "$fixture" 3306/tcp)
 maria_port=${maria_port##*:}
 CGO_ENABLED=0 GOWORK=off NYADB_MARIADB_IT=1 NYADB_MARIADB_PORT="$maria_port" NYADB_MARIADB_CA_FILE="$ssl_dir/ca.pem" go test ./internal/services/dbruntime -run '^TestMariaDBWorkbenchDisposableFixture$' -count=1 -v | tee "build/mariadb/$version-integration.log"
 CGO_ENABLED=0 GOWORK=off NYADB_MARIADB_IT=1 NYADB_MARIADB_PORT="$maria_port" NYADB_MARIADB_CA_FILE="$ssl_dir/ca.pem" NYADB_MARIADB_VISUAL_DIR="$PWD/build/mariadb" go test ./internal/ui/shell -run '^TestMariaDBSessionAndSharedGridDisposableFixture$' -count=1 -v | tee "build/mariadb/$version-workbench.log"
 docker rm -f "$fixture" >/dev/null
done
CGO_ENABLED=0 NYADB_MARIADB_VISUAL_DIR="$PWD/build/mariadb" go test ./internal/ui/shell -run '^TestMariaDBChooserOpensIndependentNativeEditor$' -count=1
