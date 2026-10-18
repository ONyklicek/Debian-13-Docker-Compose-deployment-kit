# shellcheck shell=bash
# Editing helpers: change settings without opening files. Every change is validated and
# rolled back automatically when the resulting configuration is invalid.

# Parses "KEY=VALUE" arguments (or lines from stdin for "-") into the arrays SETS/UNSETS.
declare -ga SET_KEYS=() SET_VALS=() UNSETS=()
parse_assignments(){
  SET_KEYS=() SET_VALS=() UNSETS=()
  local a line
  while [[ $# -gt 0 ]]; do
    case $1 in
      --unset) valid_key "$2" || die "Neplatný klíč: $2"; UNSETS+=("$2"); shift 2 ;;
      -)
        while IFS= read -r line || [[ -n $line ]]; do
          line=${line%$'\r'}
          [[ $line =~ ^[[:space:]]*(#|$) ]] && continue
          if [[ $line =~ ^-([A-Za-z_][A-Za-z0-9_]*)$ ]]; then UNSETS+=("${BASH_REMATCH[1]}"); continue; fi
          [[ $line =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]] || die "Neplatný řádek: $line"
          SET_KEYS+=("${BASH_REMATCH[1]}"); SET_VALS+=("${BASH_REMATCH[2]}")
        done
        shift ;;
      *=*)
        a=$1
        valid_key "${a%%=*}" || die "Neplatný klíč: ${a%%=*}"
        SET_KEYS+=("${a%%=*}"); SET_VALS+=("${a#*=}"); shift ;;
      *) die "Očekáváno KLÍČ=HODNOTA, --unset KLÍČ nebo '-' (stdin): $1" ;;
    esac
  done
}

# Applies SETS/UNSETS to a file; quote_fn is cfg_quote or env_quote.
apply_assignments(){
  local file=$1 quote_fn=$2 i
  for i in "${!SET_KEYS[@]}"; do cfg_set_raw "$file" "${SET_KEYS[$i]}" "$("$quote_fn" "${SET_VALS[$i]}")"; done
  for i in "${UNSETS[@]}"; do cfg_unset "$file" "$i"; done
}

# Runs a change on a file; restores the previous content if validation fails.
with_rollback(){ # with_rollback <file> <validate-fn> <change-fn> [args…]
  local file=$1 validate=$2 change=$3; shift 3
  local backup
  backup=$(mktemp "$(dirname "$file")/.bak.XXXXXX")
  cp -p "$file" "$backup"
  "$change" "$@"
  if ! ( "$validate" ); then
    mv -f "$backup" "$file"
    die "Změna neprošla kontrolou a byla vrácena."
  fi
  rm -f "$backup"
}

_validate_app(){ app_load "$APP" && render_app; }

# Re-points routes after domain/route changes when the app is running, without restarts.
refresh_routes(){
  app_load "$APP"
  [[ -n ${S[ACTIVE_SLOT]:-} && "$(container_state "web-${S[ACTIVE_SLOT]}")" =~ ^(running|healthy)$ ]] || return 0
  render_routes "${S[ACTIVE_SLOT]}"
  say "Směrování aktualizováno."
}

cmd_app_get(){
  app_load "${1:?Zadejte aplikaci}"
  local key=${2:-} k
  if [[ $key == --json ]]; then
    for k in "${!A[@]}"; do printf '%s\0%s\0' "$k" "${A[$k]}"; done \
      | jq -Rs 'split("\u0000") | .[:-1] | [range(0; length; 2) as $i | {(.[$i]): .[$i+1]}] | add // {}'
  elif [[ -n $key ]]; then
    [[ -v A[$key] ]] || die "Klíč $key není nastaven."
    printf '%s\n' "${A[$key]}"
  else
    for k in $(printf '%s\n' "${!A[@]}" | sort); do printf '%s=%s\n' "$k" "$(cfg_quote "${A[$k]}")"; done
  fi
}

cmd_app_set(){
  local n=${1:?Zadejte aplikaci}; shift
  app_load "$n"
  [[ $# -gt 0 ]] || die "Použití: dkit app set <app> KLÍČ=HODNOTA… [--unset KLÍČ] | -"
  parse_assignments "$@"
  app_lock "$APP"
  with_rollback "$(app_dir "$APP")/app.env" _validate_app apply_assignments "$(app_dir "$APP")/app.env" cfg_quote
  refresh_routes
  ok "Uloženo. Běžící kontejnery převezmou změny po 'dkit apply $APP' (build/zdroj po 'dkit deploy $APP')."
}

# --- application environment (env) -----------------------------------------------------
cmd_env(){
  local sub=${1:-list} n=${2:-}
  [[ -n $n ]] || die "Použití: dkit env list|set|unset|replace|import <app> …"
  shift 2
  app_load "$n"
  local f k; f="$(app_dir "$APP")/env"
  local -A E=()
  case $sub in
    list|ls)
      cfg_load "$f" E env
      if [[ ${1:-} == --json ]]; then
        for k in "${!E[@]}"; do printf '%s\0%s\0' "$k" "${E[$k]}"; done \
          | jq -Rs 'split("\u0000") | .[:-1] | [range(0; length; 2) as $i | {(.[$i]): .[$i+1]}] | add // {}'
        return
      fi
      for k in $(printf '%s\n' "${!E[@]}" | sort); do
        if [[ ${1:-} == --show || ! $k =~ (KEY|SECRET|PASSWORD|TOKEN|PASS)$ ]]; then printf '%s=%s\n' "$k" "${E[$k]}"
        else printf '%s=•••••• (--show)\n' "$k"; fi
      done ;;
    set)
      parse_assignments "$@"
      app_lock "$APP"
      with_rollback "$f" _validate_app apply_assignments "$f" env_quote
      ok "Uloženo. Projeví se po 'dkit apply $APP'." ;;
    unset)
      [[ $# -gt 0 ]] || die "Zadejte klíče."
      local args=(); for k in "$@"; do args+=(--unset "$k"); done
      parse_assignments "${args[@]}"
      app_lock "$APP"
      with_rollback "$f" _validate_app apply_assignments "$f" env_quote
      ok "Odstraněno. Projeví se po 'dkit apply $APP'." ;;
    replace)
      # Rewrites the file from KEY=VALUE lines on stdin (raw values, quoted here). Used by the UI.
      parse_assignments -
      app_lock "$APP"
      _write_env(){
        local i tmp; tmp=$(mktemp "$(dirname "$f")/.env.XXXXXX")
        { echo "# Proměnné prostředí aplikace $APP (spravováno přes dkit)"
          for i in "${!SET_KEYS[@]}"; do printf '%s=%s\n' "${SET_KEYS[$i]}" "$(env_quote "${SET_VALS[$i]}")"; done; } > "$tmp"
        chmod 0600 "$tmp"; mv -f "$tmp" "$f"
      }
      with_rollback "$f" _validate_app _write_env
      ok "Prostředí uloženo. Projeví se po 'dkit apply $APP'." ;;
    import)
      # Replaces the whole file with KEY=VALUE lines from stdin (values written verbatim).
      [[ ! -t 0 ]] || die "Předejte obsah na stdin: dkit env import $APP < soubor.env"
      app_lock "$APP"
      _replace_env(){ local tmp; tmp=$(mktemp "$(dirname "$f")/.env.XXXXXX"); cat > "$tmp"; chmod 0600 "$tmp"; mv -f "$tmp" "$f"; }
      with_rollback "$f" _validate_app _replace_env
      ok "Prostředí nahrazeno. Projeví se po 'dkit apply $APP'." ;;
    *) die "Použití: dkit env list|set|unset|import <app> …" ;;
  esac
}

# --- domains ----------------------------------------------------------------------------
cmd_domain(){
  local sub=${1:-list} n=${2:-} d cur
  [[ -n $n ]] || die "Použití: dkit domain list|add|remove <app> [doména…]"
  shift 2
  app_load "$n"
  cur=" $(app_domains | tr '\n' ' ')"
  case $sub in
    list|ls) app_domains; return ;;
    add) for d in "$@"; do d=${d,,}; [[ $cur == *" $d "* ]] || cur+="$d "; done ;;
    remove|rm) for d in "$@"; do cur=${cur/ ${d,,} / }; done ;;
    *) die "Použití: dkit domain list|add|remove <app> [doména…]" ;;
  esac
  cur=$(xargs <<<"$cur"); cmd_app_set "$APP" "DOMAINS=${cur// /,}"
}

# --- workers ----------------------------------------------------------------------------
cmd_worker(){
  local sub=${1:-list} n=${2:-} name=${3:-} w list
  [[ -n $n ]] || die "Použití: dkit worker list|add|remove <app> [jméno] [--cmd … --port … --path … --user …]"
  app_load "$n"
  case $sub in
    list|ls)
      for w in $(app_workers); do
        printf '%-12s %s%s%s%s\n' "$w" "${A[WORKER_${w}_CMD]}" "${A[WORKER_${w}_USER]:+  (uživatel ${A[WORKER_${w}_USER]})}" \
          "${A[WORKER_${w}_PORT]:+  port ${A[WORKER_${w}_PORT]}}" "${A[WORKER_${w}_PATH]:+  trasa ${A[WORKER_${w}_PATH]}}"
      done ;;
    add)
      shift 3
      [[ -n $name ]] || die "Zadejte jméno workeru."
      local sets=() unsets=()
      while [[ $# -gt 0 ]]; do
        case $1 in
          --cmd) sets+=("WORKER_${name}_CMD=$2"); shift 2 ;;
          --port) sets+=("WORKER_${name}_PORT=$2"); shift 2 ;;
          --path) sets+=("WORKER_${name}_PATH=$2"); shift 2 ;;
          --user) sets+=("WORKER_${name}_USER=$2"); shift 2 ;;
          *) die "Neznámý parametr: $1" ;;
        esac
      done
      list=" $(app_workers | tr '\n' ' ')"
      [[ $list == *" $name "* ]] || list+="$name"
      cmd_app_set "$APP" "WORKERS=$(xargs <<<"$list")" "${sets[@]}" "${unsets[@]}" ;;
    remove|rm)
      [[ -n $name ]] || die "Zadejte jméno workeru."
      list=" $(app_workers | tr '\n' ' ')"; list=${list/ $name / }
      cmd_app_set "$APP" "WORKERS=$(xargs <<<"$list")" --unset "WORKER_${name}_CMD" --unset "WORKER_${name}_PORT" \
        --unset "WORKER_${name}_PATH" --unset "WORKER_${name}_USER"
      docker ps -aq --filter "label=com.docker.compose.project=dkit-$APP" --filter "label=com.docker.compose.service=$name" \
        | xargs -r docker rm -f >/dev/null ;;
    *) die "Použití: dkit worker list|add|remove <app> …" ;;
  esac
}

# --- global configuration (dkit.env) ------------------------------------------------------
_validate_global(){
  global_load
  [[ -z $(g UI_DOMAIN) || $(g UI_DOMAIN) =~ $DOMAIN_RE ]] || die "Neplatné UI_DOMAIN."
  [[ $(g KEEP_IMAGES 3) =~ ^[0-9]+$ ]] || die "KEEP_IMAGES musí být číslo."
  local s; for s in $(g SHARED_SERVICES); do [[ " $SHAREABLE_SERVICES " == *" $s "* ]] || die "SHARED_SERVICES: '$s' nelze sdílet."; done
  dc_platform config -q || die "Konfigurace platformy je neplatná."
}

cmd_config(){
  local sub=${1:-get}; shift || true
  local f="$DKIT_ETC/dkit.env" k
  case $sub in
    get)
      if [[ ${1:-} == --json ]]; then
        for k in "${!G[@]}"; do printf '%s\0%s\0' "$k" "${G[$k]}"; done \
          | jq -Rs 'split("\u0000") | .[:-1] | [range(0; length; 2) as $i | {(.[$i]): .[$i+1]}] | add // {}'
      elif [[ -n ${1:-} ]]; then printf '%s\n' "${G[$1]:-}"
      else for k in $(printf '%s\n' "${!G[@]}" | sort); do printf '%s=%s\n' "$k" "$(cfg_quote "${G[$k]}")"; done; fi ;;
    set)
      parse_assignments "$@"
      with_rollback "$f" _validate_global apply_assignments "$f" cfg_quote
      ok "Uloženo. Traefik/UI převezmou změny po 'dkit platform up', aplikace po 'dkit apply'." ;;
    edit)
      local backup; backup=$(mktemp "$DKIT_ETC/.bak.XXXXXX"); cp -p "$f" "$backup"
      "${EDITOR:-nano}" "$f"
      if ! ( _validate_global ); then mv -f "$backup" "$f"; die "Neplatná konfigurace, změna vrácena."; fi
      rm -f "$backup"; ok "Uloženo." ;;
    *) die "Použití: dkit config get [KLÍČ|--json] | set KLÍČ=HODNOTA… | edit" ;;
  esac
}

# --- presets and templates ----------------------------------------------------------------
cmd_preset(){
  local sub=${1:-list} name=${2:-} from=dockerfile p
  case $sub in
    list|ls)
      if [[ ${2:-} == --json ]]; then
        for p in $(preset_names); do
          jq -cn --arg n "$p" --arg d "$(sed -n '1s/^# *//p' "$(preset_dir "$p")/app.env")" \
            --argjson local "$([[ -f $DKIT_ETC/presets/$p/app.env ]] && echo true || echo false)" '{name:$n, description:$d, local:$local}'
        done | jq -sc .
        return
      fi
      for p in $(preset_names); do
        printf '%-14s %s%s\n' "$p" "$(sed -n '1s/^# *//p' "$(preset_dir "$p")/app.env" | cut -c1-90)" "$([[ -f $DKIT_ETC/presets/$p/app.env ]] && echo '  [vlastní]')"
      done ;;
    create|edit)
      [[ $name =~ ^[a-z][a-z0-9-]{0,30}$ ]] || die "Neplatné jméno presetu."
      [[ ${3:-} != --from ]] || from=${4:?}
      if [[ ! -f $DKIT_ETC/presets/$name/app.env ]]; then
        local src; src=$(preset_dir "$name"); [[ -f $src/app.env ]] || src=$(preset_dir "$from")
        [[ -f $src/app.env ]] || die "Neznámý preset '$from'."
        install -d -m 0700 "$DKIT_ETC/presets/$name"
        cp "$src"/* "$DKIT_ETC/presets/$name/"
        ok "Vytvořen $DKIT_ETC/presets/$name (z $(basename "$src"))."
      fi
      if [[ -t 0 ]]; then "${EDITOR:-nano}" "$DKIT_ETC/presets/$name/app.env"; fi
      echo "Soubory presetu: $DKIT_ETC/presets/$name/{app.env,env}  (zástupné hodnoty: __APP__ __DOMAIN__ __DOMAINS__ __REPO__ __BRANCH__ __IMAGE__ __HEX32__ __HEX16__ __LARAVEL_KEY__)" >&2 ;;
    remove|rm)
      [[ -d $DKIT_ETC/presets/$name ]] || die "Vlastní preset '$name' neexistuje."
      rm -rf -- "${DKIT_ETC:?}/presets/${name:?}"; ok "Preset '$name' odstraněn." ;;
    *) die "Použití: dkit preset list | create <jméno> [--from preset] | edit <jméno> | remove <jméno>" ;;
  esac
}

cmd_template(){
  local sub=${1:-list} name=${2:-} t
  case $sub in
    list|ls)
      if [[ ${2:-} == --json ]]; then
        for t in $(template_names); do jq -cn --arg n "$t" --argjson l "$([[ -f $DKIT_ETC/templates/$t/Dockerfile ]] && echo true || echo false)" '{name:$n, local:$l}'; done | jq -sc .
        return
      fi
      for t in $(template_names); do printf '%-12s %s\n' "$t" "$([[ -f $DKIT_ETC/templates/$t/Dockerfile ]] && echo "[vlastní: $DKIT_ETC/templates/$t]" || echo "$DKIT_ROOT/templates/$t")"; done ;;
    edit|create)
      [[ $name =~ ^[a-z][a-z0-9_-]{0,30}$ ]] || die "Neplatné jméno šablony."
      if [[ ! -f $DKIT_ETC/templates/$name/Dockerfile ]]; then
        install -d -m 0700 "$DKIT_ETC/templates/$name"
        if [[ -d $DKIT_ROOT/templates/$name ]]; then cp "$DKIT_ROOT/templates/$name"/* "$DKIT_ETC/templates/$name/"
        else cp "$DKIT_ROOT/templates/${3:-php}"/* "$DKIT_ETC/templates/$name/"; fi
        ok "Vytvořena lokální šablona $DKIT_ETC/templates/$name (má přednost před výchozí, upgrade ji nepřepíše)."
      fi
      if [[ -t 0 ]]; then "${EDITOR:-nano}" "$DKIT_ETC/templates/$name/Dockerfile"; fi ;;
    reset)
      rm -rf -- "${DKIT_ETC:?}/templates/${name:?}"; ok "Lokální šablona '$name' odstraněna; platí výchozí." ;;
    *) die "Použití: dkit template list | edit <jméno> | reset <jméno>" ;;
  esac
}

# --- clone --------------------------------------------------------------------------------
cmd_app_clone(){
  local src=${1:?Zadejte zdrojovou aplikaci} dst=${2:?Zadejte novou aplikaci}; shift 2
  local domains="" with_env=1
  while [[ $# -gt 0 ]]; do
    case $1 in
      --domain|-d) domains+="${domains:+,}$2"; shift 2 ;;
      --no-env) with_env=0; shift ;;
      *) die "Neznámý parametr: $1" ;;
    esac
  done
  valid_app_name "$dst" || die "Neplatné jméno '$dst'."
  ! app_exists "$dst" || die "Aplikace '$dst' již existuje."
  [[ -n $domains ]] || die "Zadejte doménu nové aplikace (--domain)."
  app_load "$src"
  local sd dd k; sd=$(app_dir "$src") dd=$(app_dir "$dst")
  install -d -m 0700 "$dd" "$dd/ssh"
  cp -p "$sd/app.env" "$dd/app.env"
  [[ ! -f $sd/compose.override.yaml ]] || cp -p "$sd/compose.override.yaml" "$dd/"
  cp -p "$sd/ssh/"* "$dd/ssh/" 2>/dev/null || true
  if [[ $with_env -eq 1 ]]; then cp -p "$sd/env" "$dd/env"; else install -m 0600 /dev/null "$dd/env"; fi
  # Fresh service credentials and own database names; settings like DB_NAME are dropped.
  cfg_set "$dd/app.env" DOMAINS "${domains// /}"
  for k in DB_NAME DB_USER S3_BUCKET S3_DOMAIN; do cfg_unset "$dd/app.env" "$k"; done
  app_load "$dst"
  render_app
  ok "Aplikace '$dst' vytvořena podle '$src' (nová hesla služeb, vlastní databáze)."
  [[ $with_env -eq 0 ]] || warn "Prostředí (env) bylo zkopírováno včetně secrets — u Laravelu vygenerujte nový APP_KEY: dkit env set $dst APP_KEY=base64:\$(openssl rand -base64 32)"
  echo "Další krok: dkit deploy $dst" >&2
}

# --- bulk ---------------------------------------------------------------------------------
cmd_all(){ # cmd_all <command> [args…]: runs a per-app command for every app, continues on errors
  local cmd=$1; shift
  local n rc=0 failed=()
  for n in $(app_names); do
    # Only deploy makes sense for apps that were never deployed.
    if [[ $cmd != cmd_deploy ]] && ! grep -qE '^ACTIVE_SLOT=(blue|green)' "$(app_state_dir "$n")/state.env" 2>/dev/null; then
      say "── $n ── (nenasazeno, přeskočeno)"; continue
    fi
    say "── $n ──"
    if ! ( "$cmd" "$n" "$@" ); then rc=1; failed+=("$n"); fi
  done
  [[ $rc -eq 0 ]] || die "Selhalo pro: ${failed[*]}"
  ok "Hotovo pro všechny aplikace."
}
