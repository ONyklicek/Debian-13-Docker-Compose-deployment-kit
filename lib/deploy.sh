# shellcheck shell=bash
# Build, blue/green deploy, rollback, start/stop/apply.

other_slot(){ [[ $1 == blue ]] && echo green || echo blue; }

# Dockerfile for BUILD: a template (local override in /etc/dkit/templates wins) or the repo's own.
resolve_dockerfile(){
  local checkout=$1 build=${A[BUILD]:-dockerfile} f
  if [[ $build == dockerfile ]]; then
    f="$checkout/${A[DOCKERFILE]:-Dockerfile}"
    [[ $f != *..* ]] || die "DOCKERFILE nesmí obsahovat '..'."
    [[ -f $f ]] || die "Repozitář neobsahuje ${A[DOCKERFILE]:-Dockerfile}. Použijte BUILD=php|node|static nebo přidejte Dockerfile."
  else
    f="$DKIT_ETC/templates/$build/Dockerfile"
    [[ -f $f ]] || f="$DKIT_ROOT/templates/$build/Dockerfile"
    [[ -f $f ]] || die "Neznámá šablona BUILD=$build."
  fi
  printf '%s' "$f"
}

# Build-time variables: BUILD_ARGS names values taken from app env (e.g. public VITE_* keys).
build_env_file(){
  local out=$1 n
  local -A E=()
  cfg_load "$(app_state_dir "$APP")/runtime.env" E
  cfg_load "$(app_dir "$APP")/env" E
  : > "$out"; chmod 0600 "$out"
  for n in ${A[BUILD_ARGS]:-}; do
    [[ $n =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || die "Neplatné jméno v BUILD_ARGS: $n"
    printf '%s=%s\n' "$n" "${E[$n]:-}" >> "$out"
  done
}

build_from_git(){
  local rel checkout sha ref=${1:-${A[GIT_BRANCH]:-main}} dockerfile key buildenv n
  local -a args=()
  rel="$(date -u +%Y%m%dT%H%M%SZ)"
  checkout="$(app_state_dir "$APP")/releases/$rel"
  install -d -m 0750 "$(dirname "$checkout")"
  # Only failed builds leave a checkout behind; keep the three newest for diagnosis.
  local reldir; reldir=$(dirname "$checkout")
  find "$reldir" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort -r | tail -n +4 \
    | while read -r old; do rm -rf -- "${reldir:?}/${old:?}"; done
  key=${A[GIT_SSH_KEY]:-$(app_dir "$APP")/ssh/deploy_key}
  if [[ -f $key ]]; then
    local kh; kh="$(dirname "$key")/known_hosts"
    [[ -f $kh ]] || die "Chybí $kh (ověřený host key Git serveru). StrictHostKeyChecking se nevypíná."
    export GIT_SSH_COMMAND="ssh -i $key -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=$kh"
  fi
  say "Stahuji ${A[GIT_REPOSITORY]} ($ref)"
  # World-readable checkout so non-root images (nginx, node) can read the copied files;
  # the releases directory itself stays 0750.
  (
    umask 022
    git clone --quiet --no-checkout --filter=blob:none "${A[GIT_REPOSITORY]}" "$checkout"
    git -C "$checkout" fetch --quiet --depth=1 origin "$ref"
    git -C "$checkout" checkout --quiet --detach FETCH_HEAD
    git -C "$checkout" submodule update --quiet --init --recursive --depth=1 2>/dev/null || true
  ) || die "Stažení repozitáře selhalo."
  sha=$(git -C "$checkout" rev-parse HEAD)
  if [[ -e $checkout/.env ]]; then die "Repozitář obsahuje .env; secrets nepatří do Gitu. Deployment odmítnut."; fi
  dockerfile=$(resolve_dockerfile "$checkout")
  buildenv=$(mktemp "$(app_state_dir "$APP")/.buildenv.XXXXXX")
  build_env_file "$buildenv"
  while IFS= read -r n; do [[ -n $n ]] && args+=(--build-arg "$n"); done < "$buildenv"
  for n in ${A[TEMPLATE_ARGS]:-}; do args+=(--build-arg "$n"); done
  BUILT_IMAGE="dkit/$APP:${sha:0:12}"
  say "Sestavuji $BUILT_IMAGE (${A[BUILD]:-dockerfile})"
  # Pull fresh base images by default (security updates); BUILD_PULL=0 builds from the local cache.
  [[ ${A[BUILD_PULL]:-$(g BUILD_PULL 1)} == 0 ]] || args+=(--pull)
  if ! DOCKER_BUILDKIT=1 docker build "${args[@]}" \
      --secret "id=buildenv,src=$buildenv" \
      --label "dkit.app=$APP" --label "dkit.revision=$sha" \
      -f "$dockerfile" -t "$BUILT_IMAGE" "$checkout"; then
    rm -f "$buildenv"
    die "Build selhal. Zdrojový kód ponechán v $checkout pro diagnostiku."
  fi
  rm -f "$buildenv"
  rm -rf -- "$checkout"
  BUILT_REVISION=$sha
}

build_from_image(){
  local digest
  say "Stahuji image ${A[IMAGE]}"
  if ! docker pull "${A[IMAGE]}"; then
    docker image inspect "${A[IMAGE]}" >/dev/null 2>&1 || die "Image ${A[IMAGE]} nelze stáhnout."
    warn "Stažení selhalo; použije se lokální image ${A[IMAGE]}."
  fi
  digest=$(docker image inspect -f '{{.Id}}' "${A[IMAGE]}")
  digest=${digest#sha256:}
  BUILT_IMAGE="dkit/$APP:${digest:0:12}"
  docker tag "${A[IMAGE]}" "$BUILT_IMAGE"
  BUILT_REVISION="${A[IMAGE]}@${digest:0:12}"
}

# Fills empty persistent directories with the image's content at that path (like Docker
# named volumes do), so e.g. Laravel's storage/ skeleton exists on first deploy.
persist_seed(){
  local image=$1 p name path dir cid
  for p in ${A[PERSIST]:-}; do
    name=${p%%:*} path=${p#*:} dir=$(persist_host_dir "$name")
    [[ -d $dir && -z "$(ls -A "$dir")" ]] || continue
    cid=$(docker create "$image")
    if docker cp -q "$cid:$path/." "$dir/" 2>/dev/null; then
      [[ -z ${A[PERSIST_OWNER]:-} ]] || chown -R "${A[PERSIST_OWNER]}" "$dir"
      say "Perzistentní adresář $name naplněn z image ($path)"
    fi
    docker rm "$cid" >/dev/null
  done
}

# Waits until the slot answers on HEALTH_PATH from inside the edge network.
wait_web_healthy(){
  local slot=$1 url i state
  url="http://$APP-web-$slot:${A[WEB_PORT]:-8080}${A[HEALTH_PATH]:-/}"
  for i in $(seq 1 "${A[HEALTH_TIMEOUT]:-90}"); do
    state=$(container_state "web-$slot")
    case $state in exited|dead|missing|restarting) [[ $i -gt 5 ]] && break ;; esac
    if probe_http "$url"; then ok "web-$slot odpovídá na $url"; return 0; fi
    sleep 1
  done
  dc_app logs --tail=80 "web-$slot" >&2 || true
  warn "web-$slot neodpověděl na $url (stav kontejneru: $state)."
  return 1
}

services_up(){
  shared_provision
  local svcs; svcs=$(dedicated_services | tr '\n' ' ')
  [[ -n ${svcs// /} ]] || return 0
  say "Spouštím služby: $svcs"
  # shellcheck disable=SC2086
  dc_app up -d --wait --wait-timeout 300 $svcs || die "Služby nejsou healthy: $svcs"
  if is_dedicated s3; then
    local out
    out=$(dc_app exec -T s3 sh -c "echo 's3.bucket.list' | weed shell -master=127.0.0.1:9333 2>/dev/null | grep -qw $(s3_bucket) \
      || echo 's3.bucket.create -name $(s3_bucket)' | weed shell -master=127.0.0.1:9333 2>&1")
    ! grep -qi 'error' <<<"$out" || die "Bucket $(s3_bucket) se nepodařilo vytvořit: $(grep -i error <<<"$out" | head -n1)"
  fi
}

workers_up(){
  local w; w=$(app_workers | tr '\n' ' ')
  [[ -n ${w// /} ]] || return 0
  say "Restartuji workery s novým image: $w"
  # shellcheck disable=SC2086
  dc_app --profile app up -d --no-build --no-deps --force-recreate $w
}

# Waits until Traefik has loaded the route to the slot and the app answers through it.
wait_route(){ # wait_route <slot>
  local slot=$1 d url i
  [[ ${A[PUBLIC]:-1} != 0 ]] || return 0
  d=$(app_domains | head -n1)
  url="http://$APP-web-$slot:${A[WEB_PORT]:-8080}"
  for i in $(seq 1 30); do
    if traefik_serves "$APP-web" "$url" && probe_via_traefik "$d" "${A[HEALTH_PATH]:-/}"; then
      ok "Traefik směruje $d na web-$slot"; return 0
    fi
    sleep 1
  done
  return 1
}

switch_route(){ # switch_route <slot>; restores the previous route when verification fails
  local slot=$1 dyn backup=""
  dyn="$DKIT_STATE/traefik/dynamic/$APP.yml"
  [[ -f $dyn ]] && { backup=$(mktemp "$DKIT_STATE/traefik/.$APP.XXXXXX"); cp -p "$dyn" "$backup"; }
  render_routes "$slot"
  if wait_route "$slot"; then rm -f "$backup"; return 0; fi
  warn "Traefik nepřevzal novou trasu nebo kontrola přes něj selhala; vracím předchozí směrování."
  if [[ -n $backup ]]; then mv -f "$backup" "$dyn"; else rm -f "$dyn"; fi
  return 1
}

prune_images(){
  local keep=${A[KEEP_IMAGES]:-$(g KEEP_IMAGES 3)} img
  docker images "dkit/$APP" --format '{{.Repository}}:{{.Tag}}' | tail -n "+$((keep + 1))" | while read -r img; do
    [[ $img == "${S[IMAGE_ACTIVE]:-}" || $img == "${S[IMAGE_PREVIOUS]:-}" ]] && continue
    docker rmi "$img" >/dev/null 2>&1 && say "Smazán starý image $img" || true
  done
}

ensure_platform(){
  traefik_running || cmd_platform_up
}

cmd_deploy(){
  local ref="" migrate=0 migrate_flag="" n=${1:-}; shift || true
  while [[ $# -gt 0 ]]; do
    case $1 in
      --ref) ref=$2; shift 2 ;;
      --migrate) migrate=1; shift ;;
      --no-migrate) migrate_flag=off; shift ;;
      *) die "Neznámý parametr: $1" ;;
    esac
  done
  app_load "${n:?Zadejte aplikaci}"
  [[ -n $ref ]] || ref=${A[GIT_BRANCH]:-main}
  [[ $ref =~ ^[A-Za-z0-9._/-]+$ ]] || die "Neplatná --ref."
  [[ ${A[AUTO_MIGRATE]:-0} != 1 || $migrate_flag == off ]] || migrate=1
  app_lock "$APP"
  render_app
  ensure_platform

  local active target
  active=${S[ACTIVE_SLOT]:-}
  target=blue; [[ $active == blue ]] && target=green

  if [[ ${A[SOURCE]:-git} == image ]]; then build_from_image; else build_from_git "$ref"; fi

  persist_seed "$BUILT_IMAGE"
  services_up

  S[IMAGE_${target^^}]=$BUILT_IMAGE
  if [[ $migrate == 1 && -n ${A[MIGRATE_CMD]:-} ]]; then
    say "Spouštím migrace: ${A[MIGRATE_CMD]}"
    dc_app --profile app run --rm --no-deps -T ${A[MIGRATE_USER]:+--user "${A[MIGRATE_USER]}"} "web-$target" sh -c "${A[MIGRATE_CMD]}" || die "Migrace selhaly; provoz zůstal na předchozí verzi."
  elif [[ -n ${A[MIGRATE_CMD]:-} ]]; then
    say "Migrace přeskočeny (spusťte s --migrate nebo nastavte AUTO_MIGRATE=1)."
  fi

  say "Spouštím web-$target"
  dc_app --profile app up -d --no-build --no-deps --force-recreate "web-$target"
  wait_web_healthy "$target" || die "Nová verze není zdravá; provoz zůstal na předchozí verzi (web-$target ponechán pro diagnostiku)."

  switch_route "$target" || die "Přepnutí selhalo; provoz zůstal na předchozí verzi."

  S[PREVIOUS_SLOT]=$active
  S[IMAGE_PREVIOUS]=${S[IMAGE_ACTIVE]:-}
  S[REVISION_PREVIOUS]=${S[REVISION_ACTIVE]:-}
  S[ACTIVE_SLOT]=$target
  S[IMAGE_ACTIVE]=$BUILT_IMAGE
  S[REVISION_ACTIVE]=$BUILT_REVISION
  S[DEPLOYED_AT]=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  state_save

  workers_up
  if [[ -n $active && $(setting KEEP_PREVIOUS 1) == 0 ]]; then dc_app stop "web-$active" >/dev/null; fi
  prune_images
  ok "Nasazeno: $APP $BUILT_REVISION (slot $target)"
  [[ -z $active || $(setting KEEP_PREVIOUS 1) == 0 ]] || say "Předchozí slot web-$active běží dál pro okamžitý rollback (dkit rollback $APP)."
}

cmd_rollback(){
  app_load "${1:?Zadejte aplikaci}"
  app_lock "$APP"
  local prev=${S[PREVIOUS_SLOT]:-} img=${S[IMAGE_PREVIOUS]:-} cur=${S[ACTIVE_SLOT]:-}
  [[ $prev == blue || $prev == green ]] || die "Není uložena předchozí verze."
  { [[ -n $img ]] && docker image inspect "$img" >/dev/null 2>&1; } || die "Image předchozí verze ($img) již neexistuje."
  render_app
  ensure_platform
  S[IMAGE_${prev^^}]=$img
  say "Spouštím web-$prev ($img)"
  dc_app --profile app up -d --no-build --no-deps "web-$prev"
  wait_web_healthy "$prev" || die "Předchozí verze není zdravá; směrování nezměněno."
  switch_route "$prev" || die "Přepnutí selhalo."
  local cimg=${S[IMAGE_ACTIVE]:-} crev=${S[REVISION_ACTIVE]:-}
  S[ACTIVE_SLOT]=$prev S[PREVIOUS_SLOT]=$cur
  S[IMAGE_ACTIVE]=$img S[IMAGE_PREVIOUS]=$cimg
  S[REVISION_ACTIVE]=${S[REVISION_PREVIOUS]:-} S[REVISION_PREVIOUS]=$crev
  S[DEPLOYED_AT]=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  state_save
  workers_up
  ok "Vráceno na ${S[REVISION_ACTIVE]} (slot $prev). Databázové migrace NEBYLY vráceny."
}

require_deployed(){ [[ -n ${S[ACTIVE_SLOT]:-} ]] || die "'$APP' ještě nebyla nasazena (dkit deploy $APP)."; }

running_procs(){ printf 'web-%s ' "${S[ACTIVE_SLOT]}"; app_workers | tr '\n' ' '; }

# Re-renders config and recreates running containers with the current env (no rebuild).
cmd_apply(){
  app_load "${1:?Zadejte aplikaci}"
  app_lock "$APP"
  render_app
  require_deployed
  ensure_platform
  services_up
  # shellcheck disable=SC2046
  dc_app --profile app up -d --no-build --no-deps --remove-orphans $(running_procs)
  wait_web_healthy "${S[ACTIVE_SLOT]}" || die "Aplikace po změně konfigurace neodpovídá."
  render_routes "${S[ACTIVE_SLOT]}"
  wait_route "${S[ACTIVE_SLOT]}" || warn "Ověření přes Traefik selhalo (domény/certifikát?)."
  ok "Konfigurace '$APP' aplikována."
}

cmd_start(){
  app_load "${1:?Zadejte aplikaci}"
  app_lock "$APP"
  render_app; require_deployed; ensure_platform; services_up
  # shellcheck disable=SC2046
  dc_app --profile app up -d --no-build --no-deps $(running_procs)
  wait_web_healthy "${S[ACTIVE_SLOT]}" || warn "Web zatím neodpovídá."
  render_routes "${S[ACTIVE_SLOT]}"
  wait_route "${S[ACTIVE_SLOT]}" || warn "Ověření přes Traefik selhalo."
  ok "'$APP' spuštěna."
}

cmd_stop(){
  app_load "${1:?Zadejte aplikaci}"
  app_lock "$APP"
  rm -f "$DKIT_STATE/traefik/dynamic/$APP.yml"
  dc_app --profile app stop
  ok "'$APP' zastavena (data zachována)."
}

cmd_restart(){
  app_load "${1:?Zadejte aplikaci}"
  require_deployed
  local svc=${2:-}
  if [[ -n $svc ]]; then dc_app --profile app restart "$svc"; else
    # shellcheck disable=SC2046
    dc_app --profile app restart $(running_procs)
  fi
  ok "Restartováno."
}
