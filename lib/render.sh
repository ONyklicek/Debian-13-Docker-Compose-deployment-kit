# shellcheck shell=bash
# Generates per-app files from app.env: compose.yaml, runtime.env, s3.json and Traefik routes.

# Creates missing service credentials once; existing values are never rotated automatically.
services_ensure_secrets(){
  local f changed=0
  f="$(app_dir "$APP")/services.env"
  [[ -f $f ]] || { install -m 0600 /dev/null "$f"; }
  _need(){ [[ -n ${SV[$1]:-} ]] || { SV[$1]=$2; cfg_set "$f" "$1" "$2"; changed=1; }; }
  if app_has_service mariadb; then _need MARIADB_ROOT_PASSWORD "$(rand_hex)"; _need MARIADB_PASSWORD "$(rand_hex)"; fi
  if app_has_service postgres; then _need POSTGRES_PASSWORD "$(rand_hex)"; fi
  if app_has_service redis; then _need REDIS_PASSWORD "$(rand_hex)"; fi
  if app_has_service s3; then _need S3_ACCESS_KEY "$(rand_hex 10)"; _need S3_SECRET_KEY "$(rand_hex)"; fi
  chmod 0600 "$f"
  [[ $changed -eq 0 ]] || say "Vygenerována hesla služeb do $f"
  unset -f _need
}

db_name(){ printf '%s' "${A[DB_NAME]:-${APP//-/_}}"; }
db_user(){ printf '%s' "${A[DB_USER]:-${APP//-/_}}"; }
s3_bucket(){ printf '%s' "${A[S3_BUCKET]:-$APP}"; }

# Connection settings handed to the app. Later env_file entries (the user's env) override these.
render_runtime_env(){
  local dbsvc first_domain
  first_domain=$(app_domains | head -n1)
  {
    echo "# Vygenerováno dkit; hodnoty lze přepsat v $(app_dir "$APP")/env"
    echo "TZ=$(g TZ Europe/Prague)"
    [[ -z $first_domain ]] || echo "APP_URL=https://$first_domain"
    if app_has_service postgres; then
      local pu pp pd; pu=$(db_user) pp=${SV[POSTGRES_PASSWORD]} pd=$(db_name)
      echo "POSTGRES_HOST=postgres"; echo "POSTGRES_PORT=5432"; echo "POSTGRES_DB=$pd"
      echo "POSTGRES_USER=$pu"; echo "POSTGRES_PASSWORD=$pp"
      echo "POSTGRES_URL=postgresql://$pu:$pp@postgres:5432/$pd"
    fi
    if app_has_service mariadb; then
      local mu mp md; mu=$(db_user) mp=${SV[MARIADB_PASSWORD]} md=$(db_name)
      echo "MARIADB_HOST=mariadb"; echo "MARIADB_PORT=3306"; echo "MARIADB_DATABASE=$md"
      echo "MARIADB_USER=$mu"; echo "MARIADB_PASSWORD=$mp"
      echo "MARIADB_URL=mysql://$mu:$mp@mariadb:3306/$md"
    fi
    # Generic DB_* / DATABASE_URL point at the database listed first in SERVICES.
    dbsvc=$(app_services | grep -m1 -xE 'mariadb|postgres' || true)
    case $dbsvc in
      mariadb)
        echo "DB_HOST=mariadb"; echo "DB_PORT=3306"; echo "DB_DATABASE=$(db_name)"
        echo "DB_USERNAME=$(db_user)"; echo "DB_PASSWORD=${SV[MARIADB_PASSWORD]}"
        echo "DATABASE_URL=mysql://$(db_user):${SV[MARIADB_PASSWORD]}@mariadb:3306/$(db_name)" ;;
      postgres)
        echo "DB_HOST=postgres"; echo "DB_PORT=5432"; echo "DB_DATABASE=$(db_name)"
        echo "DB_USERNAME=$(db_user)"; echo "DB_PASSWORD=${SV[POSTGRES_PASSWORD]}"
        echo "DATABASE_URL=postgresql://$(db_user):${SV[POSTGRES_PASSWORD]}@postgres:5432/$(db_name)" ;;
    esac
    if app_has_service redis; then
      echo "REDIS_HOST=redis"; echo "REDIS_PORT=6379"; echo "REDIS_PASSWORD=${SV[REDIS_PASSWORD]}"
      echo "REDIS_URL=redis://:${SV[REDIS_PASSWORD]}@redis:6379/0"
    fi
    if app_has_service s3; then
      echo "S3_ENDPOINT=http://s3:8333"; echo "S3_BUCKET=$(s3_bucket)"; echo "S3_REGION=us-east-1"
      echo "S3_ACCESS_KEY=${SV[S3_ACCESS_KEY]}"; echo "S3_SECRET_KEY=${SV[S3_SECRET_KEY]}"
      echo "AWS_ENDPOINT=http://s3:8333"; echo "AWS_ENDPOINT_URL=http://s3:8333"
      echo "AWS_BUCKET=$(s3_bucket)"; echo "AWS_DEFAULT_REGION=us-east-1"; echo "AWS_USE_PATH_STYLE_ENDPOINT=true"
      echo "AWS_ACCESS_KEY_ID=${SV[S3_ACCESS_KEY]}"; echo "AWS_SECRET_ACCESS_KEY=${SV[S3_SECRET_KEY]}"
      [[ -z ${A[S3_DOMAIN]:-} ]] || echo "AWS_URL=https://${A[S3_DOMAIN]}/$(s3_bucket)"
    fi
  } | atomic_write "$(app_state_dir "$APP")/runtime.env" 0600
}

render_s3_config(){
  app_has_service s3 || return 0
  jq -n --arg k "${SV[S3_ACCESS_KEY]}" --arg s "${SV[S3_SECRET_KEY]}" \
    '{identities:[{name:"app",credentials:[{accessKey:$k,secretKey:$s}],actions:["Admin","Read","List","Tagging","Write"]}]}' \
    | atomic_write "$(app_state_dir "$APP")/s3.json" 0600
}

persist_host_dir(){ printf '%s/data/%s' "$(app_state_dir "$APP")" "$1"; }

# Creates persistent directories; ownership is set only when the directory is new.
# Content is seeded from the image on first deploy (see persist_seed).
persist_prepare(){
  local p name dir owner=${A[PERSIST_OWNER]:-}
  for p in ${A[PERSIST]:-}; do
    name=${p%%:*} dir=$(persist_host_dir "$name")
    if [[ ! -d $dir ]]; then
      install -d -m 0770 "$dir"
      [[ -z $owner ]] || chown "$owner" "$dir"
    fi
  done
  if app_has_service s3; then install -d -m 0700 "$(app_state_dir "$APP")/s3"; fi
}

render_compose(){
  local sd ad w p name dir svc cmd port slot
  sd=$(app_state_dir "$APP") ad=$(app_dir "$APP")
  {
    echo "# Vygenerováno příkazem 'dkit' z $ad/app.env — NEUPRAVUJTE ručně."
    echo "# Vlastní doplňky patří do $ad/compose.override.yaml."
    echo "name: dkit-$APP"
    echo
    echo "x-proc: &proc"
    echo "  restart: unless-stopped"
    echo "  profiles: [\"app\"]"
    echo "  env_file:"
    echo "    - $(json_str "$sd/runtime.env")"
    echo "    - $(json_str "$ad/env")"
    echo "  security_opt: [\"no-new-privileges:true\"]"
    echo "  logging: {driver: json-file, options: {max-size: \"10m\", max-file: \"5\"}}"
    [[ -z ${A[MEMORY_LIMIT]:-} ]] || echo "  mem_limit: ${A[MEMORY_LIMIT]}"
    if [[ -n ${A[PERSIST]:-} ]]; then
      echo "  volumes:"
      for p in ${A[PERSIST]}; do
        name=${p%%:*} dir=${p#*:}
        echo "    - $(compose_str "$(persist_host_dir "$name"):$dir")"
      done
    fi
    if [[ -n ${A[SERVICES]:-} ]]; then
      echo "  depends_on:"
      for svc in $(app_services); do echo "    $svc: {condition: service_healthy}"; done
    fi
    echo
    echo "services:"
    for slot in blue green; do
      echo "  web-$slot:"
      echo "    <<: *proc"
      echo "    image: \${IMAGE_${slot^^}:-dkit/$APP:none}"
      [[ -z ${A[WEB_CMD]:-} ]] || echo "    command: [\"sh\", \"-c\", $(compose_str "${A[WEB_CMD]}")]"
      echo "    networks:"
      echo "      backend: {}"
      echo "      edge: {aliases: [\"$APP-web-$slot\"]}"
    done
    for w in $(app_workers); do
      cmd=${A[WORKER_${w}_CMD]} port=${A[WORKER_${w}_PORT]:-}
      echo "  $w:"
      echo "    <<: *proc"
      echo "    image: \${IMAGE_ACTIVE:-dkit/$APP:none}"
      echo "    command: [\"sh\", \"-c\", $(compose_str "$cmd")]"
      [[ -z ${A[WORKER_${w}_USER]:-} ]] || echo "    user: $(json_str "${A[WORKER_${w}_USER]}")"
      echo "    networks:"
      echo "      backend: {}"
      if [[ -n $port ]]; then echo "      edge: {aliases: [\"$APP-$w\"]}"; else echo "      edge: {}"; fi
    done
    if app_has_service mariadb; then
      cat <<EOF
  mariadb:
    image: $(json_str "$(service_image MARIADB_IMAGE mariadb:11.4)")
    restart: unless-stopped
    environment:
      MARIADB_DATABASE: $(json_str "$(db_name)")
      MARIADB_USER: $(json_str "$(db_user)")
      MARIADB_PASSWORD: \${MARIADB_PASSWORD:?}
      MARIADB_ROOT_PASSWORD: \${MARIADB_ROOT_PASSWORD:?}
      MARIADB_AUTO_UPGRADE: "1"
      TZ: $(json_str "$(g TZ Europe/Prague)")
    volumes: ["mariadb_data:/var/lib/mysql"]
    networks: [backend]
    healthcheck:
      test: ["CMD", "healthcheck.sh", "--connect", "--innodb_initialized"]
      interval: 10s
      timeout: 5s
      retries: 30
      start_period: 30s
    security_opt: ["no-new-privileges:true"]
    stop_grace_period: 1m
EOF
    fi
    if app_has_service postgres; then
      cat <<EOF
  postgres:
    image: $(json_str "$(service_image POSTGRES_IMAGE postgres:17-alpine)")
    restart: unless-stopped
    environment:
      POSTGRES_DB: $(json_str "$(db_name)")
      POSTGRES_USER: $(json_str "$(db_user)")
      POSTGRES_PASSWORD: \${POSTGRES_PASSWORD:?}
      TZ: $(json_str "$(g TZ Europe/Prague)")
    volumes: ["postgres_data:/var/lib/postgresql/data"]
    networks: [backend]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U \"\$\$POSTGRES_USER\" -d \"\$\$POSTGRES_DB\""]
      interval: 10s
      timeout: 5s
      retries: 30
      start_period: 20s
    security_opt: ["no-new-privileges:true"]
    stop_grace_period: 1m
EOF
    fi
    if app_has_service redis; then
      cat <<EOF
  redis:
    image: $(json_str "$(service_image REDIS_IMAGE redis:7.4-alpine)")
    restart: unless-stopped
    command: ["sh", "-ec", "exec redis-server --appendonly yes --requirepass \"\$\$REDIS_PASSWORD\""]
    environment:
      REDIS_PASSWORD: \${REDIS_PASSWORD:?}
      REDISCLI_AUTH: \${REDIS_PASSWORD:?}
    volumes: ["redis_data:/data"]
    networks: [backend]
    healthcheck:
      test: ["CMD-SHELL", "redis-cli ping | grep -q PONG"]
      interval: 10s
      timeout: 5s
      retries: 10
    security_opt: ["no-new-privileges:true"]
    stop_grace_period: 30s
EOF
    fi
    if app_has_service s3; then
      cat <<EOF
  s3:
    image: $(json_str "$(service_image S3_IMAGE chrislusf/seaweedfs:3.97)")
    restart: unless-stopped
    command: ["server", "-ip.bind=0.0.0.0", "-dir=/data", "-s3", "-s3.port=8333", "-s3.config=/etc/seaweedfs/s3.json", "-volume.max=0"]
    volumes:
      - $(json_str "$sd/s3:/data")
      - $(json_str "$sd/s3.json:/etc/seaweedfs/s3.json:ro")
    networks:
      backend: {}
EOF
      [[ -z ${A[S3_DOMAIN]:-} ]] || echo "      edge: {aliases: [\"$APP-s3\"]}"
      cat <<EOF
    healthcheck:
      test: ["CMD-SHELL", "wget -q -O /dev/null http://127.0.0.1:8333/healthz"]
      interval: 10s
      timeout: 5s
      retries: 30
      start_period: 20s
    security_opt: ["no-new-privileges:true"]
EOF
    fi
    echo
    echo "networks:"
    echo "  backend:"
    echo "    internal: true"
    echo "  edge:"
    echo "    external: true"
    echo "    name: $EDGE_NETWORK"
    if app_has_service mariadb || app_has_service postgres || app_has_service redis; then
      echo
      echo "volumes:"
      app_has_service mariadb && echo "  mariadb_data: {}"
      app_has_service postgres && echo "  postgres_data: {}"
      app_has_service redis && echo "  redis_data: {}"
    fi
  } | atomic_write "$sd/compose.yaml.new" 0600

  # Merge an optional user override into a single file so every compose call sees the same model.
  if [[ -f $ad/compose.override.yaml ]]; then
    IMAGE_BLUE=x IMAGE_GREEN=x IMAGE_ACTIVE=x docker compose -p "dkit-$APP" -f "$sd/compose.yaml.new" -f "$ad/compose.override.yaml" \
      --env-file "$ad/services.env" config --no-interpolate > "$sd/compose.merged" \
      || { rm -f "$sd/compose.merged"; die "compose.override.yaml je neplatný."; }
    chmod 0600 "$sd/compose.merged"
    mv -f "$sd/compose.merged" "$sd/compose.yaml.new"
  fi
  mv -f "$sd/compose.yaml.new" "$sd/compose.yaml"
  dc_app config -q || die "Vygenerovaná konfigurace Compose je neplatná."
}

# Renders everything except routing (routing changes only on deploy/rollback/apply).
render_app(){
  install -d -m 0750 "$(app_state_dir "$APP")"
  [[ -f "$(app_dir "$APP")/env" ]] || install -m 0600 /dev/null "$(app_dir "$APP")/env"
  services_ensure_secrets
  render_runtime_env
  render_s3_config
  persist_prepare
  render_compose
}

host_rule(){
  local d rule=""
  for d in $(app_domains); do rule+="${rule:+ || }Host(\`$d\`)"; done
  printf '%s' "$rule"
}

# Writes the Traefik route for the app pointing at the given web slot.
render_routes(){
  local slot=$1 dyn w port path hr
  dyn="$DKIT_STATE/traefik/dynamic/$APP.yml"
  if [[ ${A[PUBLIC]:-1} == 0 ]]; then rm -f "$dyn"; return 0; fi
  hr=$(host_rule)
  {
    echo "# Vygenerováno dkit pro aplikaci $APP (aktivní slot: $slot)"
    echo "http:"
    echo "  routers:"
    echo "    $APP-web:"
    echo "      rule: $(json_str "$hr")"
    echo "      entryPoints: [websecure]"
    echo "      service: $APP-web"
    echo "      middlewares: [dkit-headers@file]"
    echo "      tls: {certResolver: le}"
    for w in $(app_workers); do
      port=${A[WORKER_${w}_PORT]:-} path=${A[WORKER_${w}_PATH]:-}
      [[ -n $port && -n $path ]] || continue
      echo "    $APP-$w:"
      echo "      rule: $(json_str "($hr) && PathPrefix(\`$path\`)")"
      echo "      entryPoints: [websecure]"
      echo "      service: $APP-$w"
      echo "      priority: 10000"
      echo "      tls: {certResolver: le}"
    done
    if [[ -n ${A[S3_DOMAIN]:-} ]]; then
      echo "    $APP-s3:"
      echo "      rule: $(json_str "Host(\`${A[S3_DOMAIN]}\`)")"
      echo "      entryPoints: [websecure]"
      echo "      service: $APP-s3"
      echo "      tls: {certResolver: le}"
    fi
    echo "  services:"
    echo "    $APP-web:"
    echo "      loadBalancer:"
    echo "        servers: [{url: $(json_str "http://$APP-web-$slot:${A[WEB_PORT]:-8080}")}]"
    for w in $(app_workers); do
      port=${A[WORKER_${w}_PORT]:-} path=${A[WORKER_${w}_PATH]:-}
      [[ -n $port && -n $path ]] || continue
      echo "    $APP-$w:"
      echo "      loadBalancer:"
      echo "        servers: [{url: $(json_str "http://$APP-$w:$port")}]"
    done
    if [[ -n ${A[S3_DOMAIN]:-} ]]; then
      echo "    $APP-s3:"
      echo "      loadBalancer:"
      echo "        servers: [{url: \"http://$APP-s3:8333\"}]"
    fi
  } | atomic_write "$dyn" 0644
}
