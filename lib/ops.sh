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
  local svc tty=(); svc=$(db_service)
  [[ -t 0 && -t 1 ]] || tty=(-T)
  case "$sub:$svc" in
    shell:mariadb) dc_app exec "${tty[@]}" mariadb sh -c 'exec mariadb -u root -p"$MARIADB_ROOT_PASSWORD" "$MARIADB_DATABASE"' ;;
    shell:postgres) dc_app exec "${tty[@]}" postgres sh -c 'exec psql -U "$POSTGRES_USER" "$POSTGRES_DB"' ;;
    dump:mariadb) dc_app exec -T mariadb sh -c 'exec mariadb-dump --single-transaction --routines --events --triggers --hex-blob -u root -p"$MARIADB_ROOT_PASSWORD" "$MARIADB_DATABASE"' ;;
    dump:postgres) dc_app exec -T postgres sh -c 'exec pg_dump --no-owner -U "$POSTGRES_USER" "$POSTGRES_DB"' ;;
    import:*)
      [[ ! -t 0 ]] || die "Předejte SQL na stdin: dkit db import $APP < dump.sql"
      confirm "Importovat SQL do databáze '$APP'? Existující data mohou být přepsána." || die "Zrušeno."
      if [[ $svc == mariadb ]]; then
        dc_app exec -T mariadb sh -c 'exec mariadb -u root -p"$MARIADB_ROOT_PASSWORD" "$MARIADB_DATABASE"'
      else
        dc_app exec -T postgres sh -c 'exec psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" "$POSTGRES_DB"'
      fi ;;
    *) die "Použití: dkit db shell|dump|import <aplikace>" ;;
  esac
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
  docker rm -f "$name" >/dev/null 2>&1 || true
  # The backend network is internal (no published ports), so publish via the edge network.
  docker run -d --name "$name" --restart no --network "$EDGE_NETWORK" \
    -p "127.0.0.1:$port:8080" -e ADMINER_DEFAULT_SERVER="$svc" \
    --security-opt no-new-privileges:true "$(g ADMINER_IMAGE adminer:5)" \
    php -S 0.0.0.0:8080 -t /var/www/html >/dev/null  # the image default binds [::] only
  docker network connect "dkit-${APP}_backend" "$name"
  ok "Adminer běží na http://127.0.0.1:$port (server: $svc, uživatel: $(db_user))."
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
        [[ $(container_state "$svc") == healthy ]] || { warn "$APP/$svc neběží; dump přeskočen."; continue; }
        say "$APP: dump $svc"
        if [[ $svc == mariadb ]]; then
          dc_app exec -T mariadb sh -c 'exec mariadb-dump --single-transaction --routines --events --triggers --hex-blob -u root -p"$MARIADB_ROOT_PASSWORD" "$MARIADB_DATABASE"' | gzip -6 > "$tmp/$svc.sql.gz"
        else
          dc_app exec -T postgres sh -c 'exec pg_dump --no-owner -U "$POSTGRES_USER" "$POSTGRES_DB"' | gzip -6 > "$tmp/$svc.sql.gz"
        fi
        gzip -t "$tmp/$svc.sql.gz" ;;
    esac
  done
  paths+=("$tmp" "$(app_dir "$APP")")
  for p in ${A[PERSIST]:-}; do paths+=("$(persist_host_dir "${p%%:*}")"); done
  app_has_service s3 && paths+=("$(app_state_dir "$APP")/s3")
  say "$APP: odesílám do $RESTIC_REPOSITORY"
  restic backup --quiet --tag "dkit:$APP" --host "${BACKUP_HOST:-${DKIT_HOSTNAME:-$(hostname)}}" "${paths[@]}"
  restic forget --quiet --tag "dkit:$APP" --group-by tags --keep-daily 7 --keep-weekly 5 --keep-monthly 12 --prune
  ok "$APP: záloha hotova."
}

cmd_backup(){
  require_root
  backup_env_load
  restic cat config >/dev/null 2>&1 || { say "Inicializuji restic repozitář"; restic init; }
  local n rc=0
  if [[ ${1:-} == --all || -z ${1:-} ]]; then
    for n in $(app_names); do ( backup_one "$n" ) || { rc=1; warn "Záloha $n selhala."; }; done
  elif [[ $1 == list ]]; then
    restic snapshots ${2:+--tag "dkit:$2"}
    return
  else
    backup_one "$1"
  fi
  [[ $rc -eq 0 ]] || die "Některé zálohy selhaly."
  echo "Pravidelně testujte obnovu (restic restore … do izolovaného prostředí)." >&2
}
