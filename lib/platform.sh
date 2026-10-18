# shellcheck shell=bash
# Shared platform: Traefik edge proxy and the web UI.

render_platform_routes(){
  local ui_domain; ui_domain=$(g UI_DOMAIN)
  {
    echo "# Vygenerováno dkit — sdílené middleware a volitelná trasa web UI"
    echo "http:"
    echo "  middlewares:"
    echo "    dkit-headers:"
    echo "      headers:"
    echo "        stsSeconds: $(g HSTS_SECONDS 31536000)"
    echo "        contentTypeNosniff: true"
    echo "        referrerPolicy: strict-origin-when-cross-origin"
    if [[ -n $ui_domain ]]; then
      [[ $ui_domain =~ $DOMAIN_RE ]] || die "Neplatné UI_DOMAIN."
      echo "  routers:"
      echo "    dkit-ui:"
      echo "      rule: $(json_str "Host(\`$ui_domain\`)")"
      echo "      entryPoints: [websecure]"
      echo "      service: dkit-ui"
      echo "      middlewares: [dkit-headers]"
      echo "      tls: {certResolver: le}"
      echo "  services:"
      echo "    dkit-ui:"
      echo "      loadBalancer:"
      echo "        servers: [{url: \"http://dkit-ui:9000\"}]"
    fi
  } | atomic_write "$DKIT_STATE/traefik/dynamic/_dkit.yml" 0644
}

cmd_platform_up(){
  global_load
  [[ -n $(g ACME_EMAIL) && $(g ACME_EMAIL) != *@example.com ]] || die "Nastavte ACME_EMAIL v $DKIT_ETC/dkit.env."
  install -d -m 0755 "$DKIT_STATE/traefik" "$DKIT_STATE/traefik/dynamic"
  render_platform_routes
  say "Spouštím Traefik"
  dc_platform up -d --wait traefik || die "Traefik se nespustil (dkit platform logs)."
  ok "Platforma běží."
}

cmd_platform(){
  local sub=${1:-status}; shift || true
  case $sub in
    up) cmd_platform_up ;;
    status) dc_platform --profile ui ps -a ;;
    logs) dc_platform --profile ui logs --tail=200 "$@" ;;
    restart) dc_platform restart traefik ;;
    *) die "Použití: dkit platform up|status|logs|restart" ;;
  esac
}

ui_token_file(){ printf '%s/ui.token' "$DKIT_ETC"; }

ui_token_ensure(){
  local f; f=$(ui_token_file)
  [[ -s $f ]] || { rand_hex 24 | atomic_write "$f" 0600; say "Vygenerován přístupový token web UI do $f"; }
}

cmd_ui(){
  local sub=${1:-status}
  global_load
  case $sub in
    start)
      [[ ! -f /.dockerenv ]] || die "Web UI spouštějte z hostitele, ne z kontejneru."
      ui_token_ensure
      traefik_running || cmd_platform_up
      render_platform_routes
      say "Sestavuji a spouštím web UI"
      dc_platform --profile ui up -d --build ui
      ok "Web UI: http://$(g UI_BIND 127.0.0.1):$(g UI_PORT 9000)${G[UI_DOMAIN]:+  a https://${G[UI_DOMAIN]}}"
      echo "Přístup přes SSH tunel: ssh -L $(g UI_PORT 9000):127.0.0.1:$(g UI_PORT 9000) uživatel@server" >&2
      echo "Token: dkit ui token" >&2 ;;
    stop) [[ ! -f /.dockerenv ]] || die "Spouštějte z hostitele."; dc_platform --profile ui stop ui; ok "Web UI zastaveno." ;;
    token)
      ui_token_ensure
      if [[ ${2:-} == --rotate ]]; then rand_hex 24 | atomic_write "$(ui_token_file)" 0600; ok "Token změněn; přihlášení byla zneplatněna."; fi
      cat "$(ui_token_file)" ;;
    status) dc_platform --profile ui ps ui ;;
    *) die "Použití: dkit ui start|stop|status|token [--rotate]" ;;
  esac
}

cmd_doctor(){
  local rc=0
  _chk(){ if eval "$2" >/dev/null 2>&1; then ok "$1"; else warn "$1 — NE"; rc=1; fi; }
  _chk "Docker daemon" "docker info"
  _chk "Docker Compose plugin" "docker compose version"
  _chk "Docker Buildx (BuildKit)" "docker buildx version"
  _chk "git, jq, openssl, flock" "command -v git && command -v jq && command -v openssl && command -v flock"
  _chk "Konfigurace $DKIT_ETC/dkit.env" "test -r $DKIT_ETC/dkit.env"
  _chk "Síť $EDGE_NETWORK" "docker network inspect $EDGE_NETWORK"
  _chk "Traefik běží" "traefik_running"
  _chk "restic (zálohy)" "command -v restic"
  _chk "Záloha nastavena ($DKIT_ETC/backup.env)" "test -r $DKIT_ETC/backup.env"
  local n
  for n in $(app_names); do _chk "Aplikace $n: konfigurace" "( app_load $n )"; done
  unset -f _chk
  return $rc
}
