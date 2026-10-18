# shellcheck shell=bash
# App registry: create, list, show, edit, remove.


ask(){ # ask <prompt> <default> -> answer on stdout
  local r
  read -r -p "$1${2:+ [$2]}: " r </dev/tty
  printf '%s' "${r:-$2}"
}

# Expands preset placeholders line by line so every random token gets its own value.
preset_expand(){
  local line
  while IFS= read -r line || [[ -n $line ]]; do
    while [[ $line == *__HEX32__* ]]; do line=${line/__HEX32__/$(rand_hex 32)}; done
    while [[ $line == *__HEX16__* ]]; do line=${line/__HEX16__/$(rand_hex 16)}; done
    while [[ $line == *__LARAVEL_KEY__* ]]; do line=${line/__LARAVEL_KEY__/base64:$(openssl rand -base64 32 | tr -d '\n')}; done
    line=${line//__APP__/$C_APP}
    line=${line//__DOMAIN__/$C_DOMAIN}
    line=${line//__DOMAINS__/$C_DOMAINS}
    line=${line//__REPO__/$C_REPO}
    line=${line//__BRANCH__/$C_BRANCH}
    line=${line//__IMAGE__/$C_IMAGE}
    printf '%s\n' "$line"
  done
}

cmd_app_create(){
  local preset="" domains="" repo="" branch="" image="" services="-" port=""
  C_APP=${1:-}; shift || true
  while [[ $# -gt 0 ]]; do
    case $1 in
      --preset|-p) preset=$2; shift 2 ;;
      --domain|-d) domains+="${domains:+,}$2"; shift 2 ;;
      --repo|-r) repo=$2; shift 2 ;;
      --branch|-b) branch=$2; shift 2 ;;
      --image|-i) image=$2; shift 2 ;;
      --services|-s) services=$2; shift 2 ;;
      --port) port=$2; shift 2 ;;
      --yes|-y) DKIT_YES=1; shift ;;
      *) die "Neznámý parametr: $1" ;;
    esac
  done
  local tty=0; [[ -t 0 && ${DKIT_YES:-0} != 1 ]] && tty=1
  [[ -n $C_APP || $tty -eq 0 ]] || C_APP=$(ask "Jméno aplikace (a-z, 0-9, -)" "")
  valid_app_name "$C_APP" || die "Neplatné jméno '$C_APP' (malá písmena, číslice, pomlčka; max 31 znaků)."
  ! app_exists "$C_APP" || die "Aplikace '$C_APP' již existuje."
  if [[ -z $preset ]]; then
    [[ $tty -eq 1 ]] || die "Zadejte --preset ($(preset_names | tr '\n' ' '))."
    preset=$(ask "Preset ($(preset_names | tr '\n' ' '))" "dockerfile")
  fi
  [[ -f "$(preset_dir "$preset")/app.env" ]] || die "Neznámý preset '$preset'. Dostupné: $(preset_names | tr '\n' ' ')"
  [[ -n $domains || $tty -eq 0 ]] || domains=$(ask "Domény (čárkou oddělené)" "")
  domains=${domains// /}
  [[ -n $domains ]] || die "Zadejte alespoň jednu doménu (--domain)."
  if [[ $preset == image ]]; then
    [[ -n $image || $tty -eq 0 ]] || image=$(ask "Docker image (např. ghcr.io/org/app:1.2.3)" "")
    [[ -n $image ]] || die "Zadejte --image."
  else
    [[ -n $repo || $tty -eq 0 ]] || repo=$(ask "Git repozitář (SSH nebo HTTPS URL)" "")
    [[ -n $repo ]] || die "Zadejte --repo."
    [[ -n $branch || $tty -eq 0 ]] || branch=$(ask "Větev" "main")
  fi
  C_DOMAINS=$domains C_DOMAIN=${domains%%,*} C_REPO=$repo C_BRANCH=${branch:-main} C_IMAGE=$image

  local dir; dir=$(app_dir "$C_APP")
  install -d -m 0700 "$DKIT_ETC/apps" "$dir" "$dir/ssh"
  local pdir; pdir=$(preset_dir "$preset")
  preset_expand < "$pdir/app.env" | atomic_write "$dir/app.env" 0640
  if [[ -f $pdir/env ]]; then
    preset_expand < "$pdir/env" | atomic_write "$dir/env" 0600
  else
    install -m 0600 /dev/null "$dir/env"
  fi
  [[ $services == - ]] || cfg_set "$dir/app.env" SERVICES "${services//,/ }"
  [[ -z $port ]] || cfg_set "$dir/app.env" WEB_PORT "$port"
  app_load "$C_APP"
  render_app
  ok "Aplikace '$C_APP' vytvořena z presetu '$preset'."
  cat >&2 <<EOF

Konfigurace:   $dir/app.env   (nastavení kitu: build, workery, služby, domény)
Prostředí:     $dir/env       (proměnné pro aplikaci, secrets; práva 0600)
Deploy klíč:   $dir/ssh/deploy_key + known_hosts (pro soukromý Git repozitář)

Další krok:    dkit deploy $C_APP
EOF
}

# All dkit containers in one docker call, as {project: [{service,state,status}]}.
all_containers_json(){
  docker ps -a --filter label=com.docker.compose.project --format '{{json .}}' 2>/dev/null | jq -sc '
    map(select(.Labels | test("com.docker.compose.project=dkit-")))
    | map({p: (.Labels | capture("com.docker.compose.project=(?<p>[^,]+)").p),
           service: (.Labels | capture("com.docker.compose.service=(?<s>[^,]+)").s), state: .State, status: .Status})
    | group_by(.p) | map({key: .[0].p, value: map(del(.p))}) | from_entries' 2>/dev/null || echo '{}'
}

app_json(){ # app_json [containers-json-for-all-apps]
  local all=${1:-}
  [[ -n $all ]] || all=$(all_containers_json)
  jq -cn \
    --arg name "$APP" --arg domains "${A[DOMAINS]:-}" --arg source "${A[SOURCE]:-git}" \
    --arg repo "${A[GIT_REPOSITORY]:-}" --arg branch "${A[GIT_BRANCH]:-main}" --arg image "${A[IMAGE]:-}" \
    --arg build "${A[BUILD]:-dockerfile}" --arg services "${A[SERVICES]:-}" --arg shared "$(for x in $(shared_list); do app_has_service "$x" && printf '%s ' "$x"; done)" \
    --arg workers "${A[WORKERS]:-}" \
    --arg slot "${S[ACTIVE_SLOT]:-}" --arg rev "${S[REVISION_ACTIVE]:-}" --arg prev "${S[REVISION_PREVIOUS]:-}" \
    --arg at "${S[DEPLOYED_AT]:-}" --argjson all "$all" \
    '{name:$name, domains:($domains|split(",")|map(select(.!=""))), source:$source, repo:$repo, branch:$branch,
      image:$image, build:$build, services:($services|split(" ")|map(select(.!=""))),
      shared:($shared|split(" ")|map(select(.!=""))),
      workers:($workers|split(" ")|map(select(.!=""))), active_slot:$slot, revision:$rev,
      previous_revision:$prev, deployed_at:$at, containers:($all["dkit-" + $name] // [])}'
}

cmd_app_list(){
  local json=0 n
  [[ ${1:-} == --json ]] && json=1
  if [[ $json -eq 1 ]]; then
    local all; all=$(all_containers_json)
    { for n in $(app_names); do ( app_load "$n" >/dev/null 2>&1 && app_json "$all" ) || jq -cn --arg n "$n" '{name:$n, error:"neplatná konfigurace"}'; done; } | jq -sc .
    return
  fi
  printf '%-20s %-32s %-7s %-14s %s\n' APLIKACE DOMÉNY SLOT REVIZE NASAZENO
  for n in $(app_names); do
    ( app_load "$n" 2>/dev/null || { printf '%-20s %s\n' "$n" "(neplatná konfigurace — dkit app check $n)"; exit 0; }
      printf '%-20s %-32s %-7s %-14s %s\n' "$n" "${A[DOMAINS]:-}" "${S[ACTIVE_SLOT]:--}" "$(st REVISION_ACTIVE | cut -c1-12)" "$(st DEPLOYED_AT -)" )
  done
}

cmd_app_show(){
  app_load "${1:?Zadejte aplikaci}"
  if [[ ${2:-} == --json ]]; then app_json; return; fi
  printf '%sAplikace:%s %s\n' "$C_B" "$C_0" "$APP"
  printf '  Domény:   %s\n  Zdroj:    %s %s\n  Build:    %s\n  Služby:   %s\n  Workery:  %s\n' \
    "${A[DOMAINS]:-}" "${A[SOURCE]:-git}" "${A[GIT_REPOSITORY]:-${A[IMAGE]:-}}${A[GIT_BRANCH]:+ (${A[GIT_BRANCH]})}" \
    "${A[BUILD]:-dockerfile}" "${A[SERVICES]:--}" "${A[WORKERS]:--}"
  printf '  Aktivní:  %s %s (nasazeno %s)\n  Předchozí: %s\n' "$(st ACTIVE_SLOT -)" "$(st REVISION_ACTIVE)" "$(st DEPLOYED_AT -)" "$(st REVISION_PREVIOUS -)"
  printf '  Soubory:  %s/{app.env,env,services.env}\n\n' "$(app_dir "$APP")"
  if [[ -f "$(app_state_dir "$APP")/compose.yaml" ]]; then dc_app ps -a 2>/dev/null || true; fi
}

cmd_app_check(){
  app_load "${1:?Zadejte aplikaci}"
  render_app
  ok "Konfigurace '$APP' je v pořádku."
}

cmd_app_edit(){
  local which=${2:-app.env} f
  [[ $which == app.env || $which == env || $which == compose.override.yaml ]] || die "Upravit lze app.env, env nebo compose.override.yaml."
  app_load "${1:?Zadejte aplikaci}"
  f="$(app_dir "$APP")/$which"
  [[ -f $f ]] || install -m 0600 /dev/null "$f"
  "${EDITOR:-nano}" "$f"
  app_load "$APP"
  render_app
  ok "Uloženo. Změny se projeví po 'dkit apply $APP' nebo dalším deployi."
}

cmd_app_remove(){
  local purge=0 n=${1:-}; shift || true
  [[ ${1:-} == --purge ]] && purge=1
  app_load "${n:?Zadejte aplikaci}"
  if [[ $purge -eq 1 ]]; then
    confirm "Smazat aplikaci '$APP' VČETNĚ databází, souborů a konfigurace? Nevratné." || die "Zrušeno."
  else
    confirm "Zastavit a odregistrovat '$APP'? Data a konfigurace zůstanou zachována." || die "Zrušeno."
  fi
  app_lock "$APP"
  rm -f "$DKIT_STATE/traefik/dynamic/$APP.yml"
  if [[ -f "$(app_state_dir "$APP")/compose.yaml" ]]; then
    if [[ $purge -eq 1 ]]; then dc_app --profile app down -v --remove-orphans; else dc_app --profile app down --remove-orphans; fi
  fi
  if [[ $purge -eq 1 ]]; then
    shared_deprovision
    docker images --format '{{.Repository}}:{{.Tag}}' "dkit/$APP" | xargs -r docker rmi >/dev/null 2>&1 || true
    rm -rf -- "$(app_state_dir "$APP")" "$(app_dir "$APP")"
    ok "Aplikace '$APP' smazána včetně dat."
  else
    local arch; arch="$DKIT_ETC/apps-removed/$APP.$(date -u +%Y%m%dT%H%M%SZ)"
    install -d -m 0700 "$DKIT_ETC/apps-removed"
    mv "$(app_dir "$APP")" "$arch"
    ok "Aplikace zastavena. Konfigurace přesunuta do $arch, data zůstala v $(app_state_dir "$APP")."
  fi
}
