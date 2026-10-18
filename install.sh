#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

say(){ printf '\n==> %s\n' "$*"; }
die(){ printf 'CHYBA: %s\n' "$*" >&2; exit 1; }
need(){ command -v "$1" >/dev/null 2>&1 || die "Chybí příkaz: $1"; }

[[ $EUID -eq 0 ]] || die "Spusťte přes sudo/root."
[[ -r /etc/os-release ]] || die "Nelze zjistit distribuci."
. /etc/os-release
[[ "${ID:-}" == "debian" && "${VERSION_ID:-}" == "13" ]] || die "Podporován je Debian 13 Trixie; nalezeno ${PRETTY_NAME:-neznámé OS}."
[[ "$(dpkg --print-architecture)" == "amd64" || "$(dpkg --print-architecture)" == "arm64" ]] || die "Podporováno je amd64 nebo arm64."
need install; need id; need df; need awk; need sed; need grep; need mkdir; need chmod; need cp; need date; need openssl

INSTALL_DIR="/opt/laravel-erp"
CONFIG_DIR="/etc/laravel-erp"
SOURCE_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

say "Kontrola místa na disku"
avail_kb="$(df -Pk /opt | awk 'NR==2 {print $4}')"
[[ "${avail_kb:-0}" -ge 10485760 ]] || die "Na /opt je méně než 10 GiB volného místa. Uvolněte místo a spusťte znovu."

say "Kontrola portů (bez změny firewallu)"
for port in 80 443; do
  if command -v ss >/dev/null 2>&1 && ss -H -ltn "sport = :$port" | grep -q .; then
    die "Port $port již naslouchá. Zastavte/slučte službu ručně; instalátor ji nebude měnit."
  fi
done

say "Instalace základních nástrojů"
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y ca-certificates curl git openssl jq rsync restic openssh-client iproute2

if ! command -v docker >/dev/null 2>&1; then
  die "Docker Engine není nainstalován. Nainstalujte Docker Engine + Compose plugin podle oficiální dokumentace Dockeru a spusťte instalátor znovu. Instalátor záměrně nepřepisuje repozitáře APT ani Docker konfiguraci."
fi
docker info >/dev/null 2>&1 || die "Docker daemon neběží nebo k němu nemáte přístup."
docker compose version >/dev/null 2>&1 || die "Chybí Docker Compose plugin."
[[ "$(docker compose version --short | sed 's/^v//;s/[^0-9.].*$//')" != "" ]] || die "Nelze ověřit verzi Compose."

say "Příprava adresářů (bez mazání existujících dat)"
install -d -m 0750 "$INSTALL_DIR" "$INSTALL_DIR/bin" "$INSTALL_DIR/traefik" "$INSTALL_DIR/traefik/dynamic" "$INSTALL_DIR/state" "$INSTALL_DIR/backups-tmp" "$CONFIG_DIR" "$CONFIG_DIR/ssh"
chown root:root "$CONFIG_DIR" "$CONFIG_DIR/ssh"
chmod 0700 "$CONFIG_DIR" "$CONFIG_DIR/ssh"

# Copy only project-owned deployment files. Never overwrite existing configuration/secrets.
for f in compose.yaml Dockerfile nginx.conf supervisord.conf .dockerignore traefik/traefik.yml traefik/dynamic/active.yml bin/deploy.sh bin/rollback.sh bin/backup.sh site_manager.py sites.json; do
  src="$SOURCE_DIR/$f"
  [[ -f "$src" ]] || die "V balíčku chybí $f"
  dest="$INSTALL_DIR/$f"
  install -d -m 0750 "$(dirname "$dest")"
  if [[ -e "$dest" ]]; then
    if ! cmp -s "$src" "$dest"; then
      printf 'PONECHÁNO: %s již existuje a liší se; ručně porovnejte s instalačním balíčkem.\n' "$dest"
    fi
  else
    install -m 0640 "$src" "$dest"
  fi
done

if [[ ! -e "$CONFIG_DIR/stack.env" ]]; then
  install -m 0600 "$SOURCE_DIR/stack.env.example" "$CONFIG_DIR/stack.env"
  say "Vytvořen výchozí $CONFIG_DIR/stack.env"
else
  say "Existující stack.env ponechán beze změny."
fi

if [[ ! -e "$CONFIG_DIR/secrets.env" ]]; then
  install -m 0600 "$SOURCE_DIR/secrets.env.example" "$CONFIG_DIR/secrets.env"
  # Replace placeholders with independent random values, once only.
  tmp="$(mktemp)"
  while IFS= read -r line || [[ -n "$line" ]]; do
    case "$line" in
      DB_ROOT_PASSWORD=CHANGE_ME_DB_ROOT) line="DB_ROOT_PASSWORD=$(openssl rand -hex 32)" ;;
      DB_PASSWORD=CHANGE_ME_DB_APP) line="DB_PASSWORD=$(openssl rand -hex 32)" ;;
      APP_KEY=CHANGE_ME_APP_KEY) line="APP_KEY=base64:$(openssl rand -base64 32 | tr -d '\n')" ;;
      REDIS_PASSWORD=CHANGE_ME_REDIS) line="REDIS_PASSWORD=$(openssl rand -hex 32)" ;;
      REVERB_APP_KEY=CHANGE_ME_REVERB_KEY) line="REVERB_APP_KEY=$(openssl rand -hex 16)" ;;
      REVERB_APP_SECRET=CHANGE_ME_REVERB_SECRET) line="REVERB_APP_SECRET=$(openssl rand -hex 32)" ;;
      PMA_BLOWFISH_SECRET=CHANGE_ME_PMA) line="PMA_BLOWFISH_SECRET=$(openssl rand -hex 32)" ;;
    esac
    printf '%s\n' "$line" >> "$tmp"
  done < "$CONFIG_DIR/secrets.env"
  install -m 0600 "$tmp" "$CONFIG_DIR/secrets.env"
  rm -f "$tmp"
  say "Vytvořeny secrets s náhodnými hodnotami; uložte bezpečnou zálohu APP_KEY a hesel."
else
  say "Existující secrets.env ponechán beze změny."
fi

for f in backup.env.example .github/workflows/deploy.yml.example; do
  if [[ -f "$SOURCE_DIR/$f" ]]; then
    dest="$INSTALL_DIR/$f"
    install -d -m 0750 "$(dirname "$dest")"
    [[ -e "$dest" ]] || install -m 0640 "$SOURCE_DIR/$f" "$dest"
  fi
done

# State file is only initialized if absent.
if [[ ! -e "$INSTALL_DIR/state/deployment.env" ]]; then
  printf 'ACTIVE_SLOT=\nPREVIOUS_SLOT=\nACTIVE_SHA=\nPREVIOUS_SHA=\n' > "$INSTALL_DIR/state/deployment.env"
  chmod 0600 "$INSTALL_DIR/state/deployment.env"
fi

say "Instalace dokončena"
cat <<EOF
Další kroky:
  1. Upravte $CONFIG_DIR/stack.env (domény, GitHub repo, větev, ACME email).
  2. Zkontrolujte $CONFIG_DIR/secrets.env a bezpečně uchovejte APP_KEY.
  3. Nakonfigurujte Git SSH deploy key a ověřené known_hosts v $CONFIG_DIR/ssh/.
  4. Proveďte první nasazení: sudo $INSTALL_DIR/bin/deploy.sh
  5. Zprovozněte a otestujte externí zálohy podle README.md.

Instalátor neměnil firewall, neotevíral porty, nespustil stack a nesmazal žádná data.
EOF
