# shellcheck shell=bash
# Day-2 operations: logs, exec, database tools, backups.

cmd_logs(){
  app_load "${1:?Zadejte aplikaci}"; shift
  local tail=200 follow=() svc=()
  while [[ $# -gt 0 ]]; do
    case $1 in
      -f|--follow) follow=(-f); shift ;;
      --tail|-n) tail=$2; shift 2 ;;
      *) svc+=("$1"); shift ;;
    esac
  done
  [[ $tail =~ ^[0-9]+$ ]] || die "--tail musí být číslo."
  if [[ ${#svc[@]} -eq 0 ]]; then
    [[ -z ${S[ACTIVE_SLOT]:-} ]] || svc=("web-${S[ACTIVE_SLOT]}")
    mapfile -t -O "${#svc[@]}" svc < <(app_workers)
  fi
  dc_app --profile app logs --timestamps "--tail=$tail" "${follow[@]}" "${svc[@]}"
}

cmd_exec(){
  app_load "${1:?Zadejte aplikaci}"; shift
  require_deployed
  local target="web-${S[ACTIVE_SLOT]}" tty=()
  if [[ ${1:-} != -- && -n ${1:-} ]]; then target=$1; shift; fi
  [[ ${1:-} == -- ]] && shift
  [[ $# -gt 0 ]] || set -- sh
  [[ -t 0 && -t 1 ]] || tty=(-T)
  dc_app --profile app exec "${tty[@]}" "$target" "$@"
}

db_service(){
  local s
  s=$(app_services | grep -m1 -xE 'mariadb|postgres' || true)
  [[ -n $s ]] || die "'$APP' nemá databázovou službu (mariadb/postgres)."
  printf '%s' "$s"
}

cmd_db(){
  local sub=${1:-}; shift || true
  app_load "${1:?Zadejte aplikaci}"; shift || true
  local svc tty=(-i); svc=$(db_service)
  [[ -t 0 && -t 1 ]] && tty=(-it)
  local db user; db=$(db_name) user=$(db_user)
  case "$sub:$svc" in
    shell:mariadb) db_exec mariadb "${tty[@]}" -- mariadb -u "$user" "$db" ;;
    shell:postgres) db_exec postgres "${tty[@]}" -- psql -U "$user" "$db" ;;
    dump:*) db_dump "$svc" ;;
    import:*)
      [[ ! -t 0 ]] || die "Předejte SQL na stdin: dkit db import $APP < dump.sql"
      confirm "Importovat SQL do databáze '$APP'? Existující data mohou být přepsána." || die "Zrušeno."
      if [[ $svc == mariadb ]]; then db_exec mariadb -i -- mariadb -u "$user" "$db"
      else db_exec postgres -i -- psql -v ON_ERROR_STOP=1 -U "$user" "$db"; fi ;;
    *) die "Použití: dkit db shell|dump|import <aplikace>" ;;
  esac
}

db_dump(){
  if [[ $1 == mariadb ]]; then
    db_exec mariadb -i -- mariadb-dump --single-transaction --routines --events --triggers --hex-blob --no-tablespaces -u "$(db_user)" "$(db_name)"
  else
    db_exec postgres -i -- pg_dump --no-owner -U "$(db_user)" "$(db_name)"
  fi
}

# Adminer (MariaDB + PostgreSQL) on loopback only, attached to the app's backend network.
cmd_adminer(){
  app_load "${1:?Zadejte aplikaci}"
  local action=${2:-start} port=${3:-$(g ADMINER_PORT 8081)} name="dkit-adminer-$APP" svc
  case $action in
    stop)
      if docker rm -f "$name" >/dev/null 2>&1; then ok "Adminer zastaven."; else warn "Adminer neběžel."; fi
      return ;;
    start) ;;
    *) die "Použití: dkit adminer <aplikace> [start|stop] [port]" ;;
  esac
  svc=$(db_service)
  [[ $port =~ ^[0-9]+$ ]] || die "Neplatný port."
  local net="dkit-${APP}_backend" host=$svc
  if is_shared "$svc"; then net=$SERVICES_NETWORK host="dkit-$svc"; fi
  docker rm -f "$name" >/dev/null 2>&1 || true
  # The backend network is internal (no published ports), so publish via the edge network.
  docker run -d --name "$name" --restart no --network "$EDGE_NETWORK" \
    -p "127.0.0.1:$port:8080" -e ADMINER_DEFAULT_SERVER="$host" \
    --security-opt no-new-privileges:true "$(g ADMINER_IMAGE adminer:5)" \
    php -S 0.0.0.0:8080 -t /var/www/html >/dev/null  # the image default binds [::] only
  docker network connect "$net" "$name"
  ok "Adminer běží na http://127.0.0.1:$port (server: $host, uživatel: $(db_user), databáze: $(db_name))."
  echo "Z pracovní stanice: ssh -L $port:127.0.0.1:$port uživatel@server" >&2
  echo "Heslo: grep PASSWORD $(app_dir "$APP")/services.env    Po použití: dkit adminer $APP stop" >&2
}

backup_env_load(){
  local -A B=()
  [[ -r $DKIT_ETC/backup.env ]] || die "Chybí $DKIT_ETC/backup.env (viz backup.env.example)."
  cfg_load "$DKIT_ETC/backup.env" B
  [[ -n ${B[RESTIC_REPOSITORY]:-} && -n ${B[RESTIC_PASSWORD]:-} ]] || die "Nastavte RESTIC_REPOSITORY a RESTIC_PASSWORD v $DKIT_ETC/backup.env."
  local k
  for k in "${!B[@]}"; do export "$k=${B[$k]}"; done
  command -v restic >/dev/null 2>&1 || die "Není nainstalován restic."
}

backup_one(){
  app_load "$1"
  app_lock "$APP"
  local tmp svc paths=() p
  # Stable path (guarded by the app lock) so snapshots of one app line up for restic diff/restore.
  tmp="$DKIT_STATE/backups-tmp/$APP"
  rm -rf -- "${tmp:?}"
  install -d -m 0700 "$DKIT_STATE/backups-tmp" "$tmp"
  # shellcheck disable=SC2064
  trap "rm -rf -- '$tmp'" RETURN
  for svc in $(app_services); do
    case $svc in
      mariadb|postgres)
        if is_shared "$svc"; then
          [[ $(docker inspect -f '{{.State.Health.Status}}' "dkit-$svc" 2>/dev/null) == healthy ]] || { warn "Sdílená $svc neběží; dump $APP přeskočen."; continue; }
        else
          [[ $(container_state "$svc") == healthy ]] || { warn "$APP/$svc neběží; dump přeskočen."; continue; }
        fi
        say "$APP: dump $svc"
        db_dump "$svc" | gzip -6 > "$tmp/$svc.sql.gz"
        [[ ${PIPESTATUS[0]} -eq 0 ]] || die "$APP: dump $svc selhal."
        gzip -t "$tmp/$svc.sql.gz" ;;
    esac
  done
  paths+=("$tmp" "$(app_dir "$APP")")
  for p in ${A[PERSIST]:-}; do paths+=("$(persist_host_dir "${p%%:*}")"); done
  is_dedicated s3 && paths+=("$(app_state_dir "$APP")/s3")
  say "$APP: odesílám do $RESTIC_REPOSITORY"
  restic backup --quiet --tag "dkit:$APP" --host "${BACKUP_HOST:-${DKIT_HOSTNAME:-$(hostname)}}" "${paths[@]}"
  restic forget --quiet --tag "dkit:$APP" --group-by tags --keep-daily 7 --keep-weekly 5 --keep-monthly 12 --prune
  ok "$APP: záloha hotova."
}

# Global config and shared S3 data (per-app databases are dumped by backup_one).
backup_platform(){
  local paths=("$DKIT_ETC/dkit.env")
  [[ ! -f $DKIT_ETC/platform.env ]] || paths+=("$DKIT_ETC/platform.env")
  [[ ! -d $DKIT_ETC/presets ]] || paths+=("$DKIT_ETC/presets")
  [[ ! -d $DKIT_ETC/templates ]] || paths+=("$DKIT_ETC/templates")
  [[ ! -d $DKIT_STATE/shared/s3 ]] || paths+=("$DKIT_STATE/shared/s3")
  say "platforma: odesílám konfiguraci a sdílené S3"
  restic backup --quiet --tag dkit:_platform --host "${BACKUP_HOST:-${DKIT_HOSTNAME:-$(hostname)}}" "${paths[@]}" || return 1
  restic forget --quiet --tag dkit:_platform --group-by tags --keep-daily 7 --keep-weekly 5 --keep-monthly 12 --prune
  ok "platforma: záloha hotova."
}

cmd_backup(){
  require_root
  backup_env_load
  restic cat config >/dev/null 2>&1 || { say "Inicializuji restic repozitář"; restic init; }
  local n rc=0
  if [[ ${1:-} == --all || -z ${1:-} ]]; then
    for n in $(app_names); do ( backup_one "$n" ) || { rc=1; warn "Záloha $n selhala."; }; done
    backup_platform || rc=1
  elif [[ $1 == list ]]; then
    restic snapshots ${2:+--tag "dkit:$2"}
    return
  else
    backup_one "$1"
  fi
  [[ $rc -eq 0 ]] || die "Některé zálohy selhaly."
  echo "Pravidelně testujte obnovu (restic restore … do izolovaného prostředí)." >&2
}
