#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
ROOT=/opt/laravel-erp
CONFIG=/etc/laravel-erp
STATE="$ROOT/state/deployment.env"
LOCK=/run/lock/laravel-erp-deploy.lock
mkdir -p "$(dirname "$LOCK")"
exec 9>"$LOCK"
flock -n 9 || { echo "Deployment již běží." >&2; exit 1; }

[[ $EUID -eq 0 ]] || { echo "Spusťte přes sudo/root." >&2; exit 1; }
[[ -r "$CONFIG/stack.env" && -r "$CONFIG/secrets.env" ]] || { echo "Chybí stack.env nebo secrets.env." >&2; exit 1; }
set -a
# shellcheck disable=SC1091
source "$CONFIG/stack.env"
# shellcheck disable=SC1091
source "$CONFIG/secrets.env"
set +a

: "${APP_DOMAIN:?Nastavte APP_DOMAIN}"
: "${ACME_EMAIL:?Nastavte ACME_EMAIL}"
: "${GITHUB_REPOSITORY:?Nastavte GITHUB_REPOSITORY}"
: "${GIT_BRANCH:=main}"
: "${GIT_SSH_COMMAND:=ssh -o StrictHostKeyChecking=yes}"
[[ "$APP_DOMAIN" != *"example.com"* ]] || { echo "Nahraďte ukázkovou doménu." >&2; exit 1; }
[[ "${APP_DEBUG:-false}" != "true" ]] || { echo "APP_DEBUG=true je zakázáno." >&2; exit 1; }

mkdir -p "$ROOT/releases" "$ROOT/persistent/storage" "$ROOT/persistent/public" "$ROOT/state"
chmod 0750 "$ROOT" "$ROOT/releases" "$ROOT/persistent" "$ROOT/persistent/storage" "$ROOT/persistent/public"
[[ -f "$STATE" ]] || printf 'ACTIVE_SLOT=\nPREVIOUS_SLOT=\nACTIVE_SHA=\nPREVIOUS_SHA=\n' > "$STATE"
# State contains only controlled scalar values; avoid sourcing an untrusted file.
ACTIVE_SLOT="$(sed -n 's/^ACTIVE_SLOT=//p' "$STATE" | head -n1)"
PREVIOUS_SLOT="$(sed -n 's/^PREVIOUS_SLOT=//p' "$STATE" | head -n1)"
ACTIVE_SHA="$(sed -n 's/^ACTIVE_SHA=//p' "$STATE" | head -n1)"

if [[ "$ACTIVE_SLOT" == "blue" ]]; then TARGET=green; else TARGET=blue; fi
if [[ -z "$ACTIVE_SLOT" ]]; then TARGET=blue; fi

RELEASE="$(date -u +%Y%m%dT%H%M%SZ)-$(openssl rand -hex 3)"
CHECKOUT="$ROOT/releases/$RELEASE"
mkdir -m 0750 "$CHECKOUT"
cleanup(){ rc=$?; if [[ $rc -ne 0 ]]; then echo "Deployment selhal. Nový release: $CHECKOUT (ponechán pro diagnostiku)." >&2; fi; exit "$rc"; }
trap cleanup EXIT

echo "Stahuji repozitář: $GITHUB_REPOSITORY ($GIT_BRANCH)"
GIT_SSH_COMMAND="$GIT_SSH_COMMAND" git clone --no-checkout --filter=blob:none "$GITHUB_REPOSITORY" "$CHECKOUT"
git -C "$CHECKOUT" fetch --depth=1 origin "$GIT_BRANCH"
SHA="$(git -C "$CHECKOUT" rev-parse FETCH_HEAD)"
git -C "$CHECKOUT" checkout --detach "$SHA"
[[ -f "$CHECKOUT/artisan" && -f "$CHECKOUT/composer.json" && -f "$CHECKOUT/composer.lock" ]] || { echo "Repo neobsahuje artisan/composer.json/composer.lock." >&2; exit 1; }
[[ ! -e "$CHECKOUT/.env" ]] || { echo "Repo obsahuje .env; deployment odmítán. Secrets nesmí být součástí repozitáře." >&2; exit 1; }

# Ensure Compose can validate configuration before touching services.
export APP_BUILD_CONTEXT="$CHECKOUT"
export APP_IMAGE_BLUE="laravel-erp:$SHA-blue"
export APP_IMAGE_GREEN="laravel-erp:$SHA-green"
export APP_IMAGE_ACTIVE="laravel-erp:$SHA-active"
dc(){ docker compose --project-directory "$ROOT" --env-file "$CONFIG/stack.env" --env-file "$CONFIG/secrets.env" "$@"; }
dc config -q

# Build a single immutable local image from this exact Git SHA.
echo "Sestavuji image z $SHA na serveru..."
docker build --pull \
  --build-arg "VITE_REVERB_APP_KEY=$REVERB_APP_KEY" \
  --build-arg "VITE_REVERB_HOST=$APP_DOMAIN" \
  --build-arg VITE_REVERB_PORT=443 \
  --build-arg VITE_REVERB_SCHEME=https \
  -f "$ROOT/Dockerfile" -t "laravel-erp:$SHA" "$CHECKOUT"
# The service slot uses same immutable image. Worker services use active tag.
docker tag "laravel-erp:$SHA" "laravel-erp:$SHA-blue"
docker tag "laravel-erp:$SHA" "laravel-erp:$SHA-green"
docker tag "laravel-erp:$SHA" "laravel-erp:$SHA-active"

# Initialize shared directories without recursive chown of user data.
install -d -m 0770 "$ROOT/persistent/storage/app" "$ROOT/persistent/storage/framework/cache" "$ROOT/persistent/storage/framework/sessions" "$ROOT/persistent/storage/framework/views" "$ROOT/persistent/storage/logs" "$ROOT/persistent/public"
chown -R 33:33 "$ROOT/persistent/storage" "$ROOT/persistent/public"

# Start data services first; volumes are never removed by this script.
dc up -d db redis traefik
for i in $(seq 1 60); do
  if dc exec -T db mariadb-admin ping -h 127.0.0.1 -uroot -p"${DB_ROOT_PASSWORD}" --silent >/dev/null 2>&1 \
     && dc exec -T redis sh -c 'redis-cli -a "$REDIS_PASSWORD" ping 2>/dev/null | grep -q PONG'; then break; fi
  [[ $i -lt 60 ]] || { echo "DB/Redis nejsou healthy." >&2; exit 1; }
  sleep 2
done

# Prepare selected slot. Compose builds are disabled here: image was built explicitly above.
dc --profile app up -d --no-build "$([ "$TARGET" = blue ] && echo app-blue || echo app-green)"
SERVICE="app-$TARGET"
# Verify the new app container is healthy before traffic switch.
for i in $(seq 1 60); do
  CID="$(dc ps -q "$SERVICE" 2>/dev/null || true)"
  STATUS=""
  [[ -n "$CID" ]] && STATUS="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$CID" 2>/dev/null || true)"
  [[ "$STATUS" == "healthy" ]] && break
  if [[ "$STATUS" == "unhealthy" || "$STATUS" == "exited" ]]; then dc logs --tail=100 "$SERVICE" >&2 || true; echo "Nová instance není healthy." >&2; exit 1; fi
  sleep 2
done
[[ "${STATUS:-}" == "healthy" ]] || { dc logs --tail=100 "$SERVICE" >&2 || true; echo "Timeout při health checku nové instance." >&2; exit 1; }

# Explicit opt-in for schema changes. Migrations are not automatically reversible.
if [[ "${ALLOW_MIGRATIONS:-0}" == "1" ]]; then
  echo "Spouštím migrace (výslovně povoleno ALLOW_MIGRATIONS=1)..."
  dc exec -T "$SERVICE" php artisan migrate --force
else
  echo "Migrace přeskočeny. Pokud je změna schématu nutná, nejprve zálohujte DB a spusťte s ALLOW_MIGRATIONS=1."
fi

# Atomically replace active routing config. Save previous config for diagnosis/rollback.
DYNAMIC="$ROOT/traefik/dynamic/active.yml"
cp -a "$DYNAMIC" "$ROOT/state/active.$(date -u +%Y%m%dT%H%M%SZ).yml" 2>/dev/null || true
TMP="$(mktemp "$ROOT/traefik/dynamic/.active.XXXXXX")"
cat > "$TMP" <<EOF
http:
  routers:
    erp-web:
      rule: "Host(\`$APP_DOMAIN\`)"
      entryPoints: [websecure]
      service: erp-active
      tls:
        certResolver: le
    erp-reverb:
      rule: "Host(\`$APP_DOMAIN\`) && PathRegexp(\`^/app/[^/]+\`)"
      entryPoints: [websecure]
      service: erp-reverb
      tls:
        certResolver: le
      priority: 100
  services:
    erp-active:
      loadBalancer:
        servers:
          - url: "http://$SERVICE:8080"
    erp-reverb:
      loadBalancer:
        servers:
          - url: "http://reverb:8080"
EOF
chmod 0640 "$TMP"
mv "$TMP" "$DYNAMIC"
sleep 3
if ! curl --insecure --fail --silent --show-error --max-time 10 --resolve "$APP_DOMAIN:443:127.0.0.1" "https://$APP_DOMAIN/up" >/dev/null; then
  echo "Externí lokální health probe selhal; pokouším se vrátit předchozí trasu." >&2
  if [[ -n "$ACTIVE_SLOT" ]]; then
    OLD_SERVICE="app-$ACTIVE_SLOT"
    TMP="$(mktemp "$ROOT/traefik/dynamic/.active.XXXXXX")"
    cat > "$TMP" <<EOF
http:
  routers:
    erp-web:
      rule: "Host(\`$APP_DOMAIN\`)"
      entryPoints: [websecure]
      service: erp-active
      tls:
        certResolver: le
    erp-reverb:
      rule: "Host(\`$APP_DOMAIN\`) && PathRegexp(\`^/app/[^/]+\`)"
      entryPoints: [websecure]
      service: erp-reverb
      tls:
        certResolver: le
      priority: 100
  services:
    erp-active:
      loadBalancer:
        servers:
          - url: "http://$OLD_SERVICE:8080"
    erp-reverb:
      loadBalancer:
        servers:
          - url: "http://reverb:8080"
EOF
    chmod 0640 "$TMP"; mv "$TMP" "$DYNAMIC"
  fi
  exit 1
fi

# Switch active worker image, then restart only long-running application processes.
# This can interrupt websocket sessions; clients should reconnect.
dc up -d --no-build --force-recreate horizon reverb scheduler
# Tell Horizon to gracefully terminate before/while replacement (command uses new image).
dc exec -T horizon php artisan horizon:terminate >/dev/null 2>&1 || true

TMPSTATE="$(mktemp "$ROOT/state/.deployment.XXXXXX")"
{
  printf 'ACTIVE_SLOT=%s\n' "$TARGET"
  printf 'PREVIOUS_SLOT=%s\n' "$ACTIVE_SLOT"
  printf 'ACTIVE_SHA=%s\n' "$SHA"
  printf 'PREVIOUS_SHA=%s\n' "$ACTIVE_SHA"
} > "$TMPSTATE"
chmod 0600 "$TMPSTATE"; mv "$TMPSTATE" "$STATE"

echo "Deployment dokončen: sha=$SHA slot=$TARGET"
echo "Předchozí web slot $ACTIVE_SLOT zůstává spuštěný pro rychlé přepnutí zpět."
