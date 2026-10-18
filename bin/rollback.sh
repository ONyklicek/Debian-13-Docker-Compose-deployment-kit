#!/usr/bin/env bash
set -Eeuo pipefail
ROOT=/opt/laravel-erp
CONFIG=/etc/laravel-erp
STATE="$ROOT/state/deployment.env"
[[ $EUID -eq 0 ]] || { echo "Spusťte přes sudo/root." >&2; exit 1; }
[[ -r "$STATE" && -r "$CONFIG/stack.env" ]] || { echo "Chybí stav deploymentu nebo konfigurace." >&2; exit 1; }
ACTIVE="$(sed -n 's/^ACTIVE_SLOT=//p' "$STATE" | head -n1)"
PREVIOUS="$(sed -n 's/^PREVIOUS_SLOT=//p' "$STATE" | head -n1)"
ACTIVE_SHA="$(sed -n 's/^ACTIVE_SHA=//p' "$STATE" | head -n1)"
PREVIOUS_SHA="$(sed -n 's/^PREVIOUS_SHA=//p' "$STATE" | head -n1)"
[[ "$PREVIOUS" == "blue" || "$PREVIOUS" == "green" ]] || { echo "Není uložen předchozí slot." >&2; exit 1; }
[[ "$PREVIOUS_SHA" =~ ^[0-9a-f]{40}$ ]] || { echo "Předchozí SHA není validní." >&2; exit 1; }
docker image inspect "laravel-erp:$PREVIOUS_SHA" >/dev/null 2>&1 || { echo "Image předchozí verze chybí; rollback odmítnut." >&2; exit 1; }
set -a; source "$CONFIG/stack.env"; source "$CONFIG/secrets.env"; set +a
SERVICE="app-$PREVIOUS"
dc(){ docker compose --project-directory "$ROOT" --env-file "$CONFIG/stack.env" --env-file "$CONFIG/secrets.env" "$@"; }
dc --profile app up -d --no-build "$SERVICE"
for i in $(seq 1 60); do
  CID="$(dc ps -q "$SERVICE" 2>/dev/null || true)"
  STATUS=""
  [[ -n "$CID" ]] && STATUS="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$CID" 2>/dev/null || true)"
  [[ "$STATUS" == "healthy" ]] && break
  sleep 2
done
[[ "${STATUS:-}" == "healthy" ]] || { echo "Předchozí slot není healthy; aktivní trasa ponechána beze změny." >&2; exit 1; }

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
chmod 0640 "$TMP"; mv "$TMP" "$ROOT/traefik/dynamic/active.yml"
sleep 3
curl --insecure --fail --silent --show-error --max-time 10 --resolve "$APP_DOMAIN:443:127.0.0.1" "https://$APP_DOMAIN/up" >/dev/null || { echo "Health probe po rollbacku selhal; zkontrolujte Traefik a logy." >&2; exit 1; }
TMPSTATE="$(mktemp "$ROOT/state/.deployment.XXXXXX")"
printf 'ACTIVE_SLOT=%s\nPREVIOUS_SLOT=%s\nACTIVE_SHA=%s\nPREVIOUS_SHA=%s\n' "$PREVIOUS" "$ACTIVE" "$PREVIOUS_SHA" "$ACTIVE_SHA" > "$TMPSTATE"
chmod 0600 "$TMPSTATE"; mv "$TMPSTATE" "$STATE"
echo "Web vrácen na $PREVIOUS_SHA ($PREVIOUS). DB migrace nebyly vráceny."
