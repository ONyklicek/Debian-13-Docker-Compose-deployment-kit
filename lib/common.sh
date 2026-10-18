# shellcheck shell=bash
# Shared helpers: paths, logging, config parsing, locking, Docker wrappers.

DKIT_ROOT="${DKIT_ROOT:-/opt/dkit}"     # kit code (replaced on upgrade)
DKIT_ETC="${DKIT_ETC:-/etc/dkit}"       # configuration and secrets (never overwritten)
DKIT_STATE="${DKIT_STATE:-/var/lib/dkit}" # generated files, releases, persistent data
export DKIT_ROOT DKIT_ETC DKIT_STATE

EDGE_NETWORK=dkit_edge
TRAEFIK_CONTAINER=dkit-traefik
APP_NAME_RE='^[a-z][a-z0-9-]{0,30}$'
DOMAIN_RE='^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$'
KNOWN_SERVICES="mariadb postgres redis s3"
SHAREABLE_SERVICES="mariadb postgres s3"
SERVICES_NETWORK=dkit_services

if [[ -t 2 ]]; then C_B=$'\e[1m' C_R=$'\e[31m' C_Y=$'\e[33m' C_G=$'\e[32m' C_0=$'\e[0m'; else C_B="" C_R="" C_Y="" C_G="" C_0=""; fi
say(){ printf '%s==>%s %s\n' "$C_B" "$C_0" "$*" >&2; }
ok(){ printf '%s✓%s %s\n' "$C_G" "$C_0" "$*" >&2; }
warn(){ printf '%sVAROVÁNÍ:%s %s\n' "$C_Y" "$C_0" "$*" >&2; }
die(){ printf '%sCHYBA:%s %s\n' "$C_R" "$C_0" "$*" >&2; exit 1; }

require_root(){ [[ $EUID -eq 0 ]] || die "Spusťte přes sudo/root."; }

# Reads a KEY=VALUE file into the associative array named by $2 without any shell evaluation.
# Semantics follow Docker env files: optional quotes, ' #' starts a comment in unquoted values.
cfg_load(){ # cfg_load <file> <assoc-array> [env]  ("env": Docker Compose env_file unescaping)
  local file=$1 line key val mode=${3:-}
  local -n _cfg=$2
  [[ -r $file ]] || return 0
  while IFS= read -r line || [[ -n $line ]]; do
    line=${line%$'\r'}
    [[ $line =~ ^[[:space:]]*(#|$) ]] && continue
    [[ $line =~ ^[[:space:]]*(export[[:space:]]+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]] || die "Neplatný řádek v $file: $line"
    key=${BASH_REMATCH[2]} val=${BASH_REMATCH[3]}
    if [[ $val =~ ^\"(.*)\"[[:space:]]*$ ]]; then
      val=${BASH_REMATCH[1]}; val=${val//\\\"/\"}
      if [[ $mode == env ]]; then val=${val//\$\$/\$}; val=${val//\\\\/\\}; fi
    elif [[ $val =~ ^\'(.*)\'[[:space:]]*$ ]]; then
      val=${BASH_REMATCH[1]}
    else
      val=${val%%[[:space:]]#*}
      val=${val%"${val##*[![:space:]]}"}
    fi
    _cfg[$key]=$val
  done < "$file"
}

# Quotes a value for our own config files (app.env, dkit.env); cfg_load reads it back verbatim.
cfg_quote(){
  local v=$1
  if [[ $v =~ ^[A-Za-z0-9_./:@,+=%-]*$ ]]; then printf '%s' "$v"
  elif [[ $v != *"'"* ]]; then printf "'%s'" "$v"
  else printf '"%s"' "${v//\"/\\\"}"; fi
}

# Quotes a value for a Docker Compose env_file ("env"): single quotes are literal there;
# inside double quotes compose interprets backslashes and $ interpolation.
env_quote(){
  local v=$1
  if [[ $v =~ ^[A-Za-z0-9_./:@,+=%-]*$ ]]; then printf '%s' "$v"
  elif [[ $v != *"'"* ]]; then printf "'%s'" "$v"
  else v=${v//\\/\\\\}; v=${v//\"/\\\"}; v=${v//\$/\$\$}; printf '"%s"' "$v"; fi
}

# Rewrites (or appends) one KEY=VALUE line in a file atomically, keeping comments and order.
# The value is written as given; callers quote it with cfg_quote/env_quote.
cfg_set_raw(){
  local file=$1 key=$2 val=$3 tmp found=0 line
  tmp=$(mktemp "$(dirname "$file")/.cfg.XXXXXX")
  if [[ -f $file ]]; then
    while IFS= read -r line || [[ -n $line ]]; do
      if [[ $line =~ ^[[:space:]]*(export[[:space:]]+)?${key}= ]]; then
        [[ $found -eq 1 ]] || printf '%s=%s\n' "$key" "$val"
        found=1
      else printf '%s\n' "$line"; fi
    done < "$file" > "$tmp"
  fi
  [[ $found -eq 1 ]] || printf '%s=%s\n' "$key" "$val" >> "$tmp"
  chmod --reference="$file" "$tmp" 2>/dev/null || chmod 0600 "$tmp"
  mv -f "$tmp" "$file"
}
cfg_set(){ cfg_set_raw "$1" "$2" "$(cfg_quote "$3")"; }

cfg_unset(){
  local file=$1 key=$2 tmp line
  [[ -f $file ]] || return 0
  tmp=$(mktemp "$(dirname "$file")/.cfg.XXXXXX")
  while IFS= read -r line || [[ -n $line ]]; do
    [[ $line =~ ^[[:space:]]*(export[[:space:]]+)?${key}= ]] || printf '%s\n' "$line"
  done < "$file" > "$tmp"
  chmod --reference="$file" "$tmp" 2>/dev/null || chmod 0600 "$tmp"
  mv -f "$tmp" "$file"
}

valid_key(){ [[ $1 =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; }

declare -gA G=()
global_load(){
  G=()
  cfg_load "$DKIT_ETC/dkit.env" G
}
g(){ printf '%s' "${G[$1]:-${2:-}}"; }

app_dir(){ printf '%s/apps/%s' "$DKIT_ETC" "$1"; }
app_state_dir(){ printf '%s/apps/%s' "$DKIT_STATE" "$1"; }

valid_app_name(){ [[ $1 =~ $APP_NAME_RE ]]; }

app_exists(){ [[ -f "$(app_dir "$1")/app.env" ]]; }

app_names(){
  local d
  for d in "$DKIT_ETC"/apps/*/app.env; do
    [[ -f $d ]] || continue
    d=${d%/app.env}; printf '%s\n' "${d##*/}"
  done
}

# Loads app config into A (kit settings), S (state) and SV (service credentials).
declare -gA A=() S=() SV=()
APP=""
app_load(){
  APP=$1
  valid_app_name "$APP" || die "Neplatné jméno aplikace: $APP"
  app_exists "$APP" || die "Aplikace '$APP' neexistuje ($(app_dir "$APP")/app.env)."
  A=() S=() SV=()
  cfg_load "$(app_dir "$APP")/app.env" A
  cfg_load "$(app_state_dir "$APP")/state.env" S
  cfg_load "$(app_dir "$APP")/services.env" SV
  app_validate
}
a(){ printf '%s' "${A[$1]:-${2:-}}"; }
st(){ printf '%s' "${S[$1]:-${2:-}}"; }

# Image for a service: app.env override, then dkit.env, then built-in default.
service_image(){
  local key=$1 def=$2
  printf '%s' "${A[$key]:-${G[$key]:-$def}}"
}

app_domains(){ local d; for d in ${A[DOMAINS]//,/ }; do printf '%s\n' "$d"; done; }
app_services(){ local s; for s in ${A[SERVICES]//,/ }; do printf '%s\n' "$s"; done; }
app_has_service(){ [[ " ${A[SERVICES]//,/ } " == *" $1 "* ]]; }
app_workers(){ local w; for w in ${A[WORKERS]//,/ }; do printf '%s\n' "$w"; done; }

# Services served by the shared platform instance instead of per-app containers.
# app.env SHARED_SERVICES (pinned when the app is created) wins; dkit.env is the default.
shared_list(){ if [[ -v A[SHARED_SERVICES] ]]; then printf '%s' "${A[SHARED_SERVICES]//,/ }"; else printf '%s' "${G[SHARED_SERVICES]//,/ }"; fi; }
is_shared(){ app_has_service "$1" && [[ " $(shared_list) " == *" $1 "* ]]; }
is_dedicated(){ app_has_service "$1" && ! is_shared "$1"; }
app_any_shared(){ local s; for s in $SHAREABLE_SERVICES; do is_shared "$s" && return 0; done; return 1; }
dedicated_services(){ local s; for s in $(app_services); do is_dedicated "$s" && printf '%s\n' "$s"; done; return 0; }
# Hostname of a service as seen by the app.
svc_host(){ if is_shared "$1"; then printf 'dkit-%s' "$1"; else printf '%s' "$1"; fi; }

# Default from dkit.env for per-app settings that also exist globally.
setting(){ printf '%s' "${A[$1]:-${G[$1]:-${2:-}}}"; }

app_validate(){
  local d s w p v
  [[ -n ${A[DOMAINS]:-} || ${A[PUBLIC]:-1} == 0 ]] || die "$APP: nastavte DOMAINS v app.env (nebo PUBLIC=0)."
  for d in $(app_domains); do [[ $d =~ $DOMAIN_RE ]] || die "$APP: neplatná doména '$d'."; done
  case ${A[SOURCE]:-git} in
    git) [[ -n ${A[GIT_REPOSITORY]:-} ]] || die "$APP: nastavte GIT_REPOSITORY." ;;
    image) [[ -n ${A[IMAGE]:-} ]] || die "$APP: nastavte IMAGE." ;;
    *) die "$APP: SOURCE musí být git nebo image." ;;
  esac
  v=${A[BUILD]:-dockerfile}
  [[ $v =~ ^[a-z0-9_-]+$ ]] || die "$APP: neplatné BUILD='$v'."
  [[ ${A[WEB_PORT]:-8080} =~ ^[0-9]{2,5}$ ]] || die "$APP: WEB_PORT musí být číslo."
  [[ ${A[HEALTH_PATH]:-/} =~ ^/[A-Za-z0-9._~/?=\&-]*$ ]] || die "$APP: neplatné HEALTH_PATH."
  for s in $(app_services); do [[ " $KNOWN_SERVICES " == *" $s "* ]] || die "$APP: neznámá služba '$s' (dostupné: $KNOWN_SERVICES)."; done
  for w in $(app_workers); do
    [[ $w =~ ^[a-z][a-z0-9]{0,20}$ ]] || die "$APP: neplatné jméno workeru '$w' (malá písmena a číslice)."
    [[ $w != web && " $KNOWN_SERVICES " != *" $w "* ]] || die "$APP: worker se nesmí jmenovat '$w'."
    [[ -n ${A[WORKER_${w}_CMD]:-} ]] || die "$APP: chybí WORKER_${w}_CMD."
    p=${A[WORKER_${w}_PORT]:-}
    [[ -z $p || $p =~ ^[0-9]{2,5}$ ]] || die "$APP: WORKER_${w}_PORT musí být číslo."
    v=${A[WORKER_${w}_PATH]:-}
    [[ -z $v || $v =~ ^/[A-Za-z0-9._/-]*$ ]] || die "$APP: neplatné WORKER_${w}_PATH."
    [[ -z $v || -n $p ]] || die "$APP: WORKER_${w}_PATH vyžaduje WORKER_${w}_PORT."
    v=${A[WORKER_${w}_USER]:-}
    [[ -z $v || $v =~ ^[a-z0-9_-]+(:[a-z0-9_-]+)?$ ]] || die "$APP: neplatné WORKER_${w}_USER."
  done
  for p in ${A[PERSIST]:-}; do
    [[ $p =~ ^[a-z0-9_-]+:/[^:]*$ ]] || die "$APP: neplatná položka PERSIST '$p' (formát jmeno:/cesta/v/kontejneru)."
  done
  v=${A[MIGRATE_USER]:-}
  [[ -z $v || $v =~ ^[a-z0-9_-]+(:[a-z0-9_-]+)?$ ]] || die "$APP: neplatné MIGRATE_USER."
  v=${A[PERSIST_OWNER]:-}
  [[ -z $v || $v =~ ^[0-9]+(:[0-9]+)?$ ]] || die "$APP: PERSIST_OWNER musí být uid nebo uid:gid."
  for s in $(shared_list); do [[ " $SHAREABLE_SERVICES " == *" $s "* ]] || die "$APP: službu '$s' nelze sdílet (sdílet lze: $SHAREABLE_SERVICES)."; done
  for v in "${A[DB_NAME]:-}" "${A[DB_USER]:-}"; do [[ -z $v || $v =~ ^[a-z][a-z0-9_]{0,62}$ ]] || die "$APP: DB_NAME/DB_USER: malá písmena, číslice, podtržítko."; done
  v=${A[S3_BUCKET]:-}
  [[ -z $v || $v =~ ^[a-z0-9][a-z0-9.-]{2,62}$ ]] || die "$APP: neplatné S3_BUCKET."
  v=${A[S3_DOMAIN]:-}
  [[ -z $v || $v =~ $DOMAIN_RE ]] || die "$APP: neplatné S3_DOMAIN."
  [[ -z $v ]] || app_has_service s3 || die "$APP: S3_DOMAIN vyžaduje službu s3."
  v=${A[MEMORY_LIMIT]:-}
  [[ -z $v || $v =~ ^[0-9]+[kmgKMG]?$ ]] || die "$APP: MEMORY_LIMIT např. 512m nebo 2g."
  return 0
}

# Exclusive per-app lock so CLI and web UI never deploy the same app concurrently.
app_lock(){
  install -d -m 0700 "$DKIT_STATE/locks"
  exec 9>"$DKIT_STATE/locks/$1.lock"
  flock -n 9 || die "Pro '$1' již běží jiná operace (deploy/rollback/backup)."
}

# JSON string encoding; output is valid YAML scalar too.
json_str(){ jq -cn --arg s "$1" '$s'; }
# JSON string for docker-compose files: '$' must be doubled to avoid interpolation.
compose_str(){ json_str "${1//\$/\$\$}"; }

rand_hex(){ openssl rand -hex "${1:-32}"; }

atomic_write(){ # atomic_write <dest> <mode>  (content on stdin)
  local dest=$1 mode=$2 tmp
  install -d -m 0750 "$(dirname "$dest")"
  tmp=$(mktemp "$(dirname "$dest")/.tmp.XXXXXX")
  cat > "$tmp"
  chmod "$mode" "$tmp"
  mv -f "$tmp" "$dest"
}

state_save(){
  local k
  {
    for k in ACTIVE_SLOT PREVIOUS_SLOT IMAGE_ACTIVE IMAGE_PREVIOUS IMAGE_BLUE IMAGE_GREEN REVISION_ACTIVE REVISION_PREVIOUS DEPLOYED_AT; do
      printf '%s=%s\n' "$k" "${S[$k]:-}"
    done
  } | atomic_write "$(app_state_dir "$APP")/state.env" 0600
}

# docker compose for the platform (Traefik, UI).
dc_platform(){
  export DKIT_HOSTNAME="${DKIT_HOSTNAME:-$(hostname)}"
  local envs=(--env-file "$DKIT_ETC/dkit.env")
  [[ ! -f $DKIT_ETC/platform.env ]] || envs+=(--env-file "$DKIT_ETC/platform.env")
  docker compose -p dkit -f "$DKIT_ROOT/platform/compose.yaml" "${envs[@]}" "$@"
}

# docker compose for the loaded app. Image tags come from state via the environment.
dc_app(){
  IMAGE_BLUE="${S[IMAGE_BLUE]:-}" IMAGE_GREEN="${S[IMAGE_GREEN]:-}" IMAGE_ACTIVE="${S[IMAGE_ACTIVE]:-}" \
    docker compose -p "dkit-$APP" -f "$(app_state_dir "$APP")/compose.yaml" \
    --env-file "$(app_dir "$APP")/services.env" "$@"
}

container_state(){ # container_state <service> -> healthy|running|exited|...
  local cid
  cid=$(dc_app ps -aq "$1" 2>/dev/null | head -n1)
  [[ -n $cid ]] || { printf 'missing'; return; }
  docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$cid" 2>/dev/null || printf 'missing'
}

traefik_running(){ [[ "$(docker inspect -f '{{.State.Running}}' "$TRAEFIK_CONTAINER" 2>/dev/null)" == true ]]; }

# HTTP probe from inside the edge network (Traefik image ships busybox wget).
probe_http(){ docker exec "$TRAEFIK_CONTAINER" wget -q -T 5 -O /dev/null "$1" >/dev/null 2>&1; }
# True once Traefik has loaded a route for the app that points at the given URL.
traefik_serves(){ # traefik_serves <service> <url>
  docker exec "$TRAEFIK_CONTAINER" wget -q -T 3 -O - "http://127.0.0.1:8080/api/http/services/$1@file" 2>/dev/null | grep -qF "\"$2\""
}
probe_via_traefik(){ # probe_via_traefik <domain> <path>
  docker exec "$TRAEFIK_CONTAINER" wget -q -T 8 --no-check-certificate --header "Host: $1" -O /dev/null "https://127.0.0.1$2" >/dev/null 2>&1
}

# Local presets/templates in /etc/dkit override (or extend) the bundled ones.
preset_dir(){ if [[ -f $DKIT_ETC/presets/$1/app.env ]]; then printf '%s' "$DKIT_ETC/presets/$1"; else printf '%s' "$DKIT_ROOT/presets/$1"; fi; }
preset_names(){
  local p
  for p in "$DKIT_ETC"/presets/*/app.env "$DKIT_ROOT"/presets/*/app.env; do
    [[ -f $p ]] || continue; p=${p%/app.env}; printf '%s\n' "${p##*/}"
  done | sort -u
}
template_names(){
  local t
  for t in "$DKIT_ETC"/templates/*/Dockerfile "$DKIT_ROOT"/templates/*/Dockerfile; do
    [[ -f $t ]] || continue; t=${t%/Dockerfile}; printf '%s\n' "${t##*/}"
  done | sort -u
}

confirm(){ # confirm <question>; honours DKIT_YES=1
  [[ ${DKIT_YES:-0} == 1 ]] && return 0
  [[ -t 0 ]] || die "Operace vyžaduje potvrzení; použijte --yes."
  local r; read -r -p "$1 [y/N] " r; [[ $r =~ ^[yYaA] ]]
}
