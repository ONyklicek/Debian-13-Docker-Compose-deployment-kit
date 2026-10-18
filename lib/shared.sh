# shellcheck shell=bash
# Shared platform services (one MariaDB / PostgreSQL / S3 for many apps). Each app gets its own
# database, user and bucket-scoped S3 key; the services live only on the internal dkit_services
# network. Redis is always per app (it has no reliable per-tenant isolation).

platform_secrets_ensure(){
  local f="$DKIT_ETC/platform.env"
  local -A P=()
  [[ -f $f ]] || install -m 0600 /dev/null "$f"
  cfg_load "$f" P
  [[ -n ${P[SHARED_MARIADB_ROOT_PASSWORD]:-} ]] || cfg_set "$f" SHARED_MARIADB_ROOT_PASSWORD "$(rand_hex)"
  [[ -n ${P[SHARED_POSTGRES_PASSWORD]:-} ]] || cfg_set "$f" SHARED_POSTGRES_PASSWORD "$(rand_hex)"
  [[ -n ${P[SHARED_S3_ADMIN_KEY]:-} ]] || cfg_set "$f" SHARED_S3_ADMIN_KEY "$(rand_hex 10)"
  [[ -n ${P[SHARED_S3_ADMIN_SECRET]:-} ]] || cfg_set "$f" SHARED_S3_ADMIN_SECRET "$(rand_hex)"
  chmod 0600 "$f"
}

platform_secret(){ local -A P=(); cfg_load "$DKIT_ETC/platform.env" P; printf '%s' "${P[$1]:-}"; }

# Starts a shared service (idempotent) and waits until it is healthy.
shared_up(){
  local svc=$1
  platform_secrets_ensure
  install -d -m 0700 "$DKIT_STATE/shared"
  if [[ $svc == s3 ]]; then
    install -d -m 0700 "$DKIT_STATE/shared/s3"
    # Bootstrap identity so the S3 API is never open before keys are configured.
    jq -n --arg k "$(platform_secret SHARED_S3_ADMIN_KEY)" --arg s "$(platform_secret SHARED_S3_ADMIN_SECRET)" \
      '{identities:[{name:"admin",credentials:[{accessKey:$k,secretKey:$s}],actions:["Admin","Read","List","Tagging","Write"]}]}' \
      | atomic_write "$DKIT_STATE/shared/s3.json" 0600
  fi
  if [[ "$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "dkit-$svc" 2>/dev/null)" != healthy ]]; then
    say "Spouštím sdílenou službu $svc"
    dc_platform up -d --wait --wait-timeout 300 "shared-$svc" || die "Sdílená služba $svc není healthy (dkit platform logs shared-$svc)."
  fi
}

# weed shell exits 0 even when a command fails, so errors are detected in its output.
weed_shell(){
  local out
  out=$(docker exec -i dkit-s3 weed shell -master=127.0.0.1:9333 2>&1 <<<"$1") || return 1
  if grep -qiE '^> *error|^error' <<<"$out"; then warn "weed shell: $(grep -iE 'error' <<<"$out" | head -n1)"; return 1; fi
}

# Creates/updates the app's database, user and S3 identity on the shared instances.
shared_provision(){
  local svc db user pw
  db=$(db_name) user=$(db_user)
  for svc in $(shared_list); do
    app_has_service "$svc" || continue
    shared_up "$svc"
    case $svc in
      mariadb)
        pw=${SV[MARIADB_PASSWORD]}
        docker exec -i dkit-mariadb sh -c 'exec mariadb -uroot -p"$MARIADB_ROOT_PASSWORD"' <<SQL || die "Zřízení databáze $db ve sdílené MariaDB selhalo."
CREATE DATABASE IF NOT EXISTS \`$db\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS '$user'@'%' IDENTIFIED BY '$pw';
ALTER USER '$user'@'%' IDENTIFIED BY '$pw';
GRANT ALL PRIVILEGES ON \`$db\`.* TO '$user'@'%';
FLUSH PRIVILEGES;
SQL
        ;;
      postgres)
        pw=${SV[POSTGRES_PASSWORD]}
        docker exec -i dkit-postgres psql -q -v ON_ERROR_STOP=1 -U postgres >/dev/null <<SQL || die "Zřízení databáze $db ve sdíleném PostgreSQL selhalo."
DO \$\$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '$user') THEN CREATE ROLE "$user" LOGIN PASSWORD '$pw';
  ELSE ALTER ROLE "$user" WITH LOGIN PASSWORD '$pw'; END IF;
END \$\$;
SELECT 'CREATE DATABASE "$db" OWNER "$user"' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '$db')\gexec
REVOKE ALL ON DATABASE "$db" FROM PUBLIC;
GRANT ALL ON DATABASE "$db" TO "$user";
SQL
        ;;
      s3) shared_s3_provision || die "Zřízení bucketu $(s3_bucket) ve sdíleném S3 selhalo."
        ;;
    esac
  done
}

shared_s3_provision(){
  local b; b=$(s3_bucket)
  weed_shell "s3.configure -user=admin -access_key=$(platform_secret SHARED_S3_ADMIN_KEY) -secret_key=$(platform_secret SHARED_S3_ADMIN_SECRET) -actions=Read,Write,List,Tagging,Admin -apply" || return 1
  if ! docker exec -i dkit-s3 weed shell -master=127.0.0.1:9333 2>/dev/null <<<"s3.bucket.list" | grep -qw "$b"; then
    weed_shell "s3.bucket.create -name $b" || return 1
  fi
  # Key limited to the app's own bucket; applied live, no restart of the shared S3.
  weed_shell "s3.configure -user=app-$APP -access_key=${SV[S3_ACCESS_KEY]} -secret_key=${SV[S3_SECRET_KEY]} -buckets=$b -actions=Read,Write,List,Tagging,Admin -apply"
}

# Drops the app's database, user, bucket and key from the shared instances (purge only).
shared_deprovision(){
  local svc db user
  db=$(db_name) user=$(db_user)
  for svc in $(shared_list); do
    app_has_service "$svc" || continue
    docker inspect "dkit-$svc" >/dev/null 2>&1 || continue
    case $svc in
      mariadb) docker exec -i dkit-mariadb sh -c 'exec mariadb -uroot -p"$MARIADB_ROOT_PASSWORD"' <<<"DROP DATABASE IF EXISTS \`$db\`; DROP USER IF EXISTS '$user'@'%';" || warn "Odstranění DB $db selhalo." ;;
      postgres) docker exec -i dkit-postgres psql -q -U postgres <<<"DROP DATABASE IF EXISTS \"$db\" WITH (FORCE); DROP ROLE IF EXISTS \"$user\";" >/dev/null || warn "Odstranění DB $db selhalo." ;;
      s3) weed_shell "s3.configure -user=app-$APP -delete -apply"; weed_shell "s3.bucket.delete -name $(s3_bucket)" || warn "Odstranění bucketu selhalo." ;;
    esac
  done
}

# Runs a DB client for the app on the right container (dedicated or shared).
db_exec(){ # db_exec <mariadb|postgres> <tty-args…> -- <client-args…>
  local svc=$1; shift
  local tty=()
  while [[ $1 != -- ]]; do tty+=("$1"); shift; done; shift
  if is_shared "$svc"; then
    if [[ $svc == mariadb ]]; then
      docker exec "${tty[@]}" -e MYSQL_PWD="${SV[MARIADB_PASSWORD]}" dkit-mariadb "$@"
    else
      docker exec "${tty[@]}" -e PGPASSWORD="${SV[POSTGRES_PASSWORD]}" dkit-postgres "$@"
    fi
  else
    if [[ $svc == mariadb ]]; then
      dc_app exec "${tty[@]}" -e MYSQL_PWD="${SV[MARIADB_PASSWORD]}" mariadb "$@"
    else
      dc_app exec "${tty[@]}" -e PGPASSWORD="${SV[POSTGRES_PASSWORD]}" postgres "$@"
    fi
  fi
}
