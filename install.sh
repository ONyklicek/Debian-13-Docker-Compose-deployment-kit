#!/usr/bin/env bash
# Installs or upgrades dkit on Debian 13. Re-runnable: kit code in /opt/dkit is replaced,
# configuration in /etc/dkit and data in /var/lib/dkit are never overwritten or deleted.
set -Eeuo pipefail
umask 022

say(){ printf '\n==> %s\n' "$*"; }
die(){ printf 'CHYBA: %s\n' "$*" >&2; exit 1; }

DKIT_ROOT="${DKIT_ROOT:-/opt/dkit}"
DKIT_ETC="${DKIT_ETC:-/etc/dkit}"
DKIT_STATE="${DKIT_STATE:-/var/lib/dkit}"
SOURCE_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ACME_EMAIL_ARG="" START=0 WITH_UI=0

usage(){
  cat <<EOF
Použití: sudo ./install.sh [--email admin@firma.cz] [--start] [--ui]
  --email   nastaví ACME_EMAIL v $DKIT_ETC/dkit.env (jen při prvním vytvoření)
  --start   po instalaci spustí Traefik (dkit platform up)
  --ui      po instalaci spustí web UI na 127.0.0.1 (dkit ui start)
EOF
}
while [[ $# -gt 0 ]]; do
  case $1 in
    --email) ACME_EMAIL_ARG=$2; shift 2 ;;
    --start) START=1; shift ;;
    --ui) WITH_UI=1; START=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "Neznámý parametr: $1" ;;
  esac
done

[[ $EUID -eq 0 ]] || die "Spusťte přes sudo/root."
[[ -r /etc/os-release ]] || die "Nelze zjistit distribuci."
. /etc/os-release
[[ "${ID:-}" == "debian" && "${VERSION_ID:-}" == "13" ]] || die "Podporován je Debian 13 Trixie; nalezeno ${PRETTY_NAME:-neznámé OS}."
ARCH="$(dpkg --print-architecture)"
[[ $ARCH == amd64 || $ARCH == arm64 ]] || die "Podporováno je amd64 nebo arm64."

say "Kontrola místa na disku"
install -d -m 0755 "$(dirname "$DKIT_STATE")"
avail_kb="$(df -Pk "$(dirname "$DKIT_STATE")" | awk 'NR==2 {print $4}')"
[[ "${avail_kb:-0}" -ge 10485760 ]] || die "Na $(dirname "$DKIT_STATE") je méně než 10 GiB volného místa."

say "Kontrola portů 80/443 (bez změny firewallu)"
if ! docker inspect dkit-traefik >/dev/null 2>&1; then
  for port in 80 443; do
    if command -v ss >/dev/null 2>&1 && ss -H -ltn "sport = :$port" | grep -q .; then
      die "Port $port již používá jiná služba. Zastavte ji ručně; instalátor ji nebude měnit."
    fi
  done
fi

say "Instalace základních nástrojů"
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y ca-certificates curl git openssl jq rsync restic openssh-client iproute2 util-linux gzip

docker_ready=0
if command -v docker >/dev/null 2>&1; then
  if ! docker info >/dev/null 2>&1 && command -v systemctl >/dev/null 2>&1; then
    systemctl start docker >/dev/null 2>&1 || true
  fi
  if docker info >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 && docker buildx version >/dev/null 2>&1; then
    docker_ready=1
  fi
fi

if [[ "$docker_ready" -eq 0 ]]; then
  say "Instalace Docker Engine, Compose a Buildx z oficiálního repozitáře Dockeru"
  install -d -m 0755 /etc/apt/keyrings
  if [[ ! -s /etc/apt/keyrings/docker.asc ]]; then
    docker_key_tmp="$(mktemp)"
    if ! curl -fsSL https://download.docker.com/linux/debian/gpg -o "$docker_key_tmp"; then
      rm -f "$docker_key_tmp"
      die "Nepodařilo se stáhnout oficiální podpisový klíč Dockeru."
    fi
    install -m 0644 "$docker_key_tmp" /etc/apt/keyrings/docker.asc
    rm -f "$docker_key_tmp"
  fi
  if ! grep -Rqs --include='*.list' --include='*.sources' 'download.docker.com/linux/debian' /etc/apt/sources.list /etc/apt/sources.list.d 2>/dev/null; then
    docker_source="/etc/apt/sources.list.d/docker.sources"
    [[ ! -e "$docker_source" ]] || die "$docker_source již existuje; instalátor jej nepřepíše."
    cat > "$docker_source" <<EOF
Types: deb
URIs: https://download.docker.com/linux/debian
Suites: $VERSION_CODENAME
Components: stable
Architectures: $ARCH
Signed-By: /etc/apt/keyrings/docker.asc
EOF
    chmod 0644 "$docker_source"
  fi
  apt-get update
  apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  command -v systemctl >/dev/null 2>&1 && systemctl enable --now docker
fi
docker info >/dev/null 2>&1 || die "Docker daemon neběží."
docker compose version >/dev/null 2>&1 || die "Chybí Docker Compose plugin."
docker buildx version >/dev/null 2>&1 || die "Chybí Docker Buildx plugin."

say "Instalace kitu do $DKIT_ROOT"
for d in bin lib platform templates presets ui; do [[ -d "$SOURCE_DIR/$d" ]] || die "V balíčku chybí adresář $d."; done
install -d -m 0755 "$DKIT_ROOT"
# Kit code is owned by the kit: replace it so upgrades are clean. Local template overrides belong
# to $DKIT_ETC/templates/<name>/Dockerfile and survive upgrades.
rsync -a --delete --exclude '.git' \
  --include '/bin/***' --include '/lib/***' --include '/platform/***' --include '/templates/***' \
  --include '/presets/***' --include '/ui/***' --include '/examples/***' \
  --include '/VERSION' --include '/README.md' --include '/SECURITY-CHECKLIST.md' \
  --include '/dkit.env.example' --include '/backup.env.example' --exclude '*' \
  "$SOURCE_DIR/" "$DKIT_ROOT/"
chown -R root:root "$DKIT_ROOT"
chmod -R go-w "$DKIT_ROOT"
chmod 0755 "$DKIT_ROOT/bin/dkit"
ln -sfn "$DKIT_ROOT/bin/dkit" /usr/local/bin/dkit

say "Příprava konfigurace a dat (existující soubory se nemění)"
install -d -m 0700 "$DKIT_ETC" "$DKIT_ETC/apps" "$DKIT_ETC/templates"
install -d -m 0751 "$DKIT_STATE"
install -d -m 0700 "$DKIT_STATE/apps" "$DKIT_STATE/locks" "$DKIT_STATE/jobs"
install -d -m 0755 "$DKIT_STATE/traefik" "$DKIT_STATE/traefik/dynamic"

if [[ ! -e "$DKIT_ETC/dkit.env" ]]; then
  install -m 0600 "$SOURCE_DIR/dkit.env.example" "$DKIT_ETC/dkit.env"
  if [[ -n $ACME_EMAIL_ARG ]]; then
    [[ $ACME_EMAIL_ARG =~ ^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$ ]] || die "Neplatný e-mail."
    sed -i "s|^ACME_EMAIL=.*|ACME_EMAIL=$ACME_EMAIL_ARG|" "$DKIT_ETC/dkit.env"
  fi
  say "Vytvořen $DKIT_ETC/dkit.env"
else
  say "Existující $DKIT_ETC/dkit.env ponechán."
fi
[[ -e "$DKIT_ETC/backup.env" ]] || install -m 0600 "$SOURCE_DIR/backup.env.example" "$DKIT_ETC/backup.env.example"
if [[ ! -s "$DKIT_ETC/ui.token" ]]; then
  openssl rand -hex 24 > "$DKIT_ETC/ui.token"
  chmod 0600 "$DKIT_ETC/ui.token"
fi

if [[ $START -eq 1 ]]; then
  dkit platform up
  [[ $WITH_UI -eq 0 ]] || dkit ui start
fi

say "Instalace dokončena ($(cat "$DKIT_ROOT/VERSION"))"
cat <<EOF
Další kroky:
  1. Nastavte ACME_EMAIL v $DKIT_ETC/dkit.env a spusťte platformu:  dkit platform up
  2. Přidejte aplikaci, např.:
       dkit app create shop --preset laravel --domain shop.firma.cz --repo git@github.com:firma/shop.git
     (presety: laravel, php, node, static, dockerfile, image)
  3. Pro soukromý repozitář vložte deploy key do $DKIT_ETC/apps/<app>/ssh/deploy_key
     a ověřený host key do $DKIT_ETC/apps/<app>/ssh/known_hosts.
  4. Nasaďte:  dkit deploy <app>
  5. Web UI (volitelně):  dkit ui start   → ssh -L 9000:127.0.0.1:9000 uživatel@server
  6. Zálohy: cp $DKIT_ETC/backup.env.example $DKIT_ETC/backup.env, vyplňte, pak dkit backup --all

Instalátor neměnil firewall, nesmazal žádná data a nepřepsal existující konfiguraci.
EOF
