# dkit — Debian 13 / Docker Compose deployment kit

Univerzální nástroj pro provoz webových aplikací na jednom serveru s Debianem 13: **CLI `dkit`**, **webové rozhraní**, blue/green nasazení bez výpadku, automatické HTTPS přes Traefik a Let's Encrypt, **kontejnery pro databáze, Redis a S3 úložiště** a šifrované zálohy přes Restic.

Nasadit jde cokoli, co běží v Dockeru: Laravel, Symfony nebo čisté PHP, Node.js (Express, Next.js, Nuxt), statický web nebo SPA, aplikaci s vlastním `Dockerfile` v libovolném jazyce, nebo hotový image z registru (Gitea, n8n, Uptime Kuma, vlastní image z GHCR).

```
            Internet :80/:443
                   │
           ┌───────▼────────┐      /var/lib/dkit/traefik/dynamic/<app>.yml
           │ Traefik (TLS)  │◄──── (směrování generuje dkit)
           └───┬───────┬────┘
      síť dkit_edge    │
   ┌───────────▼─┐   ┌─▼───────────┐
   │ shop        │   │ blog        │   každá aplikace = vlastní Compose projekt
   │ web-blue ◄──┼─┐ │ web-green   │   web-blue / web-green (blue/green)
   │ web-green   │ │ │ worker…     │   workery (queue, scheduler, websockety…)
   │ queue, cron │ │ │ postgres    │   služby jen v interní síti aplikace:
   │ mariadb     │ │ └─────────────┘   mariadb | postgres | redis | s3
   │ redis, s3   │ └ aktivní slot
   └─────────────┘
```

## Co obsahuje

| Část | Popis |
|---|---|
| `bin/dkit` | CLI (bash): správa aplikací, deploy, rollback, logy, DB, zálohy, platforma |
| `ui/` | Webové rozhraní (Go, bez externích závislostí), běží jako kontejner `dkit-ui` |
| `platform/compose.yaml` | Sdílený Traefik (80/443, Let's Encrypt) a web UI |
| `presets/` | Výchozí konfigurace: `laravel`, `php`, `node`, `static`, `dockerfile`, `image` |
| `templates/` | Dockerfile šablony pro repozitáře bez vlastního Dockerfile: `php`, `node`, `static` |
| `install.sh` | Instalátor pro Debian 13 (Docker z oficiálního repozitáře, nástroje, adresáře) |

Rozložení na serveru:

| Cesta | Obsah | Při upgradu |
|---|---|---|
| `/opt/dkit` | kód kitu | nahrazen |
| `/etc/dkit/dkit.env` | globální nastavení (ACME e-mail, image služeb, UI) | nikdy se nepřepisuje |
| `/etc/dkit/apps/<app>/` | `app.env` (nastavení kitu), `env` (proměnné aplikace, 0600), `services.env` (hesla služeb, 0600), `ssh/` (deploy key), volitelně `compose.override.yaml` | nikdy se nepřepisuje |
| `/etc/dkit/templates/<šablona>/Dockerfile` | lokální úprava šablony (má přednost) | nikdy se nepřepisuje |
| `/var/lib/dkit/apps/<app>/` | vygenerovaný `compose.yaml`, `state.env`, perzistentní data `data/<jméno>/`, S3 data `s3/` | zachováno |
| `/var/lib/dkit/traefik/dynamic/` | vygenerované směrování | generováno |

## Důležitá omezení

1. **Jeden server není vysoká dostupnost.** Host, disk, Docker a síť zůstávají single point of failure.
2. Blue/green se týká webu. Databáze, Redis, S3 a workery jsou sdílené mezi verzemi; workery se po přepnutí restartují s novým image.
3. Migrace databáze nelze obecně vrátit. Používejte zpětně kompatibilní (expand/contract) migrace; rollback vrací jen image.
4. Aplikace musí na `HEALTH_PATH` vracet 2xx/3xx, jinak deploy nepřepne provoz.
5. Aplikace sdílí síť `dkit_edge` (kvůli Traefiku). Databáze a další služby jsou v interní síti každé aplikace a z ostatních aplikací dostupné nejsou.
6. Web UI ovládá Docker, tj. má **práva roota na serveru**. Výchozí je jen `127.0.0.1` a přístup přes SSH tunel.
7. Zálohy jsou skutečně externí až po nastavení Restic repozitáře mimo server a úspěšném testu obnovy.

## 1. Instalace

Předpoklady: Debian 13 (amd64/arm64), root/sudo, DNS záznamy domén míří na server, porty 80/443 povolené ve firewallu (instalátor firewall nemění).

```bash
git clone https://github.com/onyklicek/debian-13-docker-compose-deployment-kit.git
cd debian-13-docker-compose-deployment-kit
sudo ./install.sh --email admin@firma.cz --start
```

Instalátor doinstaluje Docker Engine, Compose a Buildx z oficiálního repozitáře Dockeru (pokud chybí), nástroje (`git`, `jq`, `restic`…), zkopíruje kit do `/opt/dkit`, vytvoří `/usr/local/bin/dkit` a výchozí `/etc/dkit/dkit.env`. Opakované spuštění = upgrade kitu; konfigurace ani data se nemění.

Bez `--start` spusťte Traefik ručně po úpravě `/etc/dkit/dkit.env`:

```bash
sudo dkit platform up
sudo dkit doctor
```

## 2. První aplikace

```bash
# Laravel z GitHubu, MariaDB + Redis, scheduler a queue worker
sudo dkit app create shop --preset laravel --domain shop.firma.cz,www.shop.firma.cz \
  --repo git@github.com:firma/shop.git --branch main

# Node.js aplikace s PostgreSQL, Redisem a S3 úložištěm
sudo dkit app create api --preset node --domain api.firma.cz \
  --repo git@github.com:firma/api.git --services "postgres redis s3"

# Statický web / SPA
sudo dkit app create web --preset static --domain www.firma.cz --repo https://github.com/firma/web.git

# Libovolný jazyk s Dockerfile v repozitáři (Go, Python, Rust, .NET, Java…)
sudo dkit app create svc --preset dockerfile --domain svc.firma.cz --repo git@github.com:firma/svc.git --port 8000

# Hotový image bez buildu
sudo dkit app create kuma --preset image --domain status.firma.cz --image louislam/uptime-kuma:1 --port 3001
```

Bez parametrů se `dkit app create` interaktivně doptá. Pro soukromý repozitář vložte deploy key (jen pro čtení, jen pro daný repozitář) a ověřený host key:

```bash
sudo install -m 0600 deploy_key /etc/dkit/apps/shop/ssh/deploy_key
ssh-keyscan github.com | sudo tee /etc/dkit/apps/shop/ssh/known_hosts   # otisk ověřte nezávisle
```

Pak zkontrolujte konfiguraci a nasaďte:

```bash
sudo dkit app edit shop            # app.env – nastavení kitu
sudo dkit app edit shop env        # proměnné aplikace a secrets
sudo dkit deploy shop --migrate    # první nasazení včetně migrací
```

## 3. Konfigurace aplikace (`app.env`)

| Klíč | Význam |
|---|---|
| `DOMAINS` | domény čárkou oddělené; první je hlavní (`APP_URL`) |
| `SOURCE` | `git` (build ze zdrojáků) nebo `image` (hotový image) |
| `GIT_REPOSITORY`, `GIT_BRANCH` | repozitář a výchozí větev |
| `BUILD` | `dockerfile` (vlastní v repozitáři), `php`, `node`, `static` (šablony kitu) |
| `DOCKERFILE` | cesta k Dockerfile v repozitáři (pro `BUILD=dockerfile`) |
| `TEMPLATE_ARGS` | parametry šablony, např. `PHP_VERSION=8.3 PHP_EXTENSIONS=imagick APT_PACKAGES=ffmpeg`, `STATIC_DIR=build SPA=1`, `NODE_VERSION=20` |
| `BUILD_ARGS` | jména proměnných z `env`, které dostane build (např. `VITE_*`, `NEXT_PUBLIC_*`) |
| `IMAGE` | image pro `SOURCE=image` |
| `WEB_PORT`, `HEALTH_PATH`, `HEALTH_TIMEOUT` | port aplikace v kontejneru, kontrola zdraví (s), výchozí 8080, `/`, 90 |
| `WEB_CMD` | přepíše příkaz webového kontejneru |
| `WORKERS` | seznam workerů; každý má `WORKER_<jméno>_CMD`, volitelně `_USER`, `_PORT` a `_PATH` (vlastní trasa, např. websockety `/app`) |
| `SERVICES` | kontejnery služeb: `mariadb`, `postgres`, `redis`, `s3` |
| `PERSIST` | perzistentní adresáře `jméno:/cesta/v/kontejneru`; prázdný adresář se při prvním deployi naplní obsahem z image |
| `PERSIST_OWNER` | `uid:gid` nových perzistentních adresářů (např. `33:33` pro www-data) |
| `MIGRATE_CMD`, `MIGRATE_USER`, `AUTO_MIGRATE` | příkaz migrací, spouští se v novém image před přepnutím provozu (jen s `--migrate` nebo `AUTO_MIGRATE=1`) |
| `S3_DOMAIN` | volitelná veřejná doména pro S3 (presigned URL, veřejné soubory) |
| `MEMORY_LIMIT` | limit paměti procesů aplikace, např. `1g` |
| `KEEP_PREVIOUS` | `1` (výchozí) nechá předchozí slot běžet pro okamžitý rollback; `0` šetří RAM |
| `PUBLIC` | `0` = bez veřejné trasy (interní služba) |
| `MARIADB_IMAGE`, `POSTGRES_IMAGE`, `REDIS_IMAGE`, `S3_IMAGE` | verze služeb pro tuto aplikaci |

### Služby a proměnné, které aplikace dostane

Soubor `env` čte Docker Compose: hodnoty se znakem `$` uzavřete do jednoduchých uvozovek (`HESLO='a$b'`) nebo zdvojte `$$`.

Každá aplikace má **vlastní** kontejnery služeb a vlastní náhodná hesla (`/etc/dkit/apps/<app>/services.env`). Připojení se předá automaticky do prostředí aplikace; hodnoty v `env` mají přednost.

| Služba | Kontejner | Proměnné |
|---|---|---|
| `mariadb` | MariaDB 11.4 | `DB_HOST`, `DB_PORT`, `DB_DATABASE`, `DB_USERNAME`, `DB_PASSWORD`, `DATABASE_URL`, `MARIADB_*` |
| `postgres` | PostgreSQL 17 | totéž s `POSTGRES_*`; `DB_*`/`DATABASE_URL` míří na první databázi v `SERVICES` |
| `redis` | Redis 7.4 (AOF) | `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD`, `REDIS_URL` |
| `s3` | SeaweedFS (S3 API) | `S3_ENDPOINT`, `S3_BUCKET`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, `AWS_*` (kompatibilní s Laravel `s3` diskem, AWS SDK, boto3) |

Bucket se jménem aplikace (`S3_BUCKET`) se vytvoří automaticky. Databáze a Redis používají Docker volumes, S3 a `PERSIST` adresáře jsou v `/var/lib/dkit/apps/<app>/`.

Pro cokoli dalšího (Meilisearch, Mailpit, ClickHouse…) přidejte `/etc/dkit/apps/<app>/compose.override.yaml`; sloučí se s vygenerovanou konfigurací:

```yaml
services:
  meilisearch:
    image: getmeili/meilisearch:v1.15
    restart: unless-stopped
    networks: [backend]
    volumes: ["meili:/meili_data"]
volumes:
  meili: {}
```

## 4. Nasazení, rollback a provoz

```bash
dkit apps                         # přehled
dkit deploy shop                  # build z GIT_BRANCH, blue/green přepnutí
dkit deploy shop --ref v2.3.1     # konkrétní tag/commit
dkit deploy shop --migrate        # se spuštěním MIGRATE_CMD
dkit rollback shop                # okamžitý návrat na předchozí verzi
dkit apply shop                   # změny env/app.env bez nového buildu
dkit logs shop -f                 # logy webu a workerů
dkit logs shop queue --tail 500
dkit exec shop -- php artisan tinker
dkit db shell shop | dkit db dump shop > dump.sql | dkit db import shop < dump.sql
dkit adminer shop                 # Adminer na 127.0.0.1:8081 (SSH tunel), pak: dkit adminer shop stop
dkit stop shop / dkit start shop
dkit app remove shop              # odregistrovat (data zůstanou), --purge smaže vše
```

Průběh `dkit deploy`:

1. Klon repozitáře v daném commitu (bez `.env` v repozitáři, jinak odmítne), build image `dkit/<app>:<sha>` na serveru.
2. Spuštění služeb a čekání na jejich healthcheck.
3. Volitelně migrace v novém image.
4. Start neaktivního slotu (`web-blue`/`web-green`) a kontrola `HEALTH_PATH` uvnitř sítě.
5. Přepnutí trasy v Traefiku a ověření přes Traefik; při selhání se vrátí předchozí trasa.
6. Restart workerů s novým image, úklid starých image (`KEEP_IMAGES`).

Selhání v kterémkoli kroku nechá provoz na předchozí verzi.

## 5. Webové rozhraní

```bash
sudo dkit ui start                              # build a spuštění kontejneru dkit-ui
sudo dkit ui token                              # přístupový token
ssh -L 9000:127.0.0.1:9000 uživatel@server      # z pracovní stanice
```

Otevřete `http://127.0.0.1:9000`. Rozhraní umí: přehled aplikací a kontejnerů, vytvoření aplikace z presetu, deploy (větev/tag, migrace), rollback, restart, start/stop, zálohu, úpravu `app.env`, `env` a `compose.override.yaml` s validací (neplatná změna se vrátí), logy a živý výstup úloh.

- Token změníte `dkit ui token --rotate` (odhlásí všechny relace).
- Veřejný přístup přes HTTPS: nastavte `UI_DOMAIN` v `dkit.env` a `dkit ui start`. Rozhraní má práva roota — zvažte omezení IP adres nebo VPN.
- UI volá stejné CLI (`/opt/dkit/bin/dkit`), takže CLI a UI se nikdy nerozcházejí; souběžné operace nad jednou aplikací blokuje zámek.

## 6. Zálohy

```bash
sudo cp /etc/dkit/backup.env.example /etc/dkit/backup.env && sudo chmod 0600 /etc/dkit/backup.env
sudo nano /etc/dkit/backup.env      # RESTIC_REPOSITORY mimo server + RESTIC_PASSWORD
sudo dkit backup --all              # nebo dkit backup shop
sudo dkit backup list shop
```

Záloha aplikace obsahuje SQL dump každé databáze, konfiguraci `/etc/dkit/apps/<app>`, `PERSIST` adresáře a data S3. Retence 7 denních / 5 týdenních / 12 měsíčních. Pravidelné spouštění nastavte systemd timerem nebo cronem až po úspěšném ručním běhu, např. `/etc/cron.d/dkit-backup`:

```
30 2 * * * root /usr/local/bin/dkit backup --all >> /var/log/dkit-backup.log 2>&1
```

Obnova: `restic restore <snapshot> --target /tmp/obnova`, pak `dkit db import <app> < …/mariadb.sql` (po rozbalení gzip) a zkopírování souborů. Obnovu pravidelně testujte v izolovaném prostředí.

## 7. Nasazení z GitHub Actions

Vzor je v `examples/github-actions-deploy.yml`. Doporučení: samostatný uživatel `deploy` s omezeným sudo jen na konkrétní příkaz:

```
# /etc/sudoers.d/dkit-deploy
deploy ALL=(root) NOPASSWD: /usr/local/bin/dkit deploy shop *
```

Nedávejte CI neomezený root SSH ani přístup k Docker socketu. Akce připněte na plné commit SHA.

## 8. Šablony buildů

- **php** – nginx + PHP-FPM (8.4), Composer, frontend build (npm/pnpm/yarn), rozšíření `pdo_mysql pdo_pgsql intl bcmath gd zip redis opcache…`; další přes `TEMPLATE_ARGS="PHP_EXTENSIONS=imagick soap"`. Docroot `DOCROOT=public`.
- **node** – `npm ci` / pnpm / yarn, `build` pokud existuje, `npm start`, port `$PORT` (3000), běží jako uživatel `node`.
- **static** – volitelný npm build, výstup `STATIC_DIR` servírován neprivilegovaným nginx na 8080, `SPA=1` pro fallback na `index.html`.

Proměnné z `BUILD_ARGS` se při buildu šablon zpřístupní jako `.env.production.local` (načítá Vite, Next.js, Nuxt) a vždy i jako `--build-arg`. Vlastní úpravu šablony uložte do `/etc/dkit/templates/<jméno>/Dockerfile`, upgrade ji nepřepíše.

## 9. Aplikace za proxy

Traefik předává `X-Forwarded-For/Proto/Host`. Aplikace musí proxy důvěřovat, jinak generuje `http://` odkazy:

- Laravel 11+: `->withMiddleware(fn ($m) => $m->trustProxies(at: '*'))` v `bootstrap/app.php`.
- Symfony: `framework.trusted_proxies: '172.16.0.0/12,10.0.0.0/8'`.
- Express: `app.set('trust proxy', true)`.

## 10. Přechod z verze 1 (Laravel-only)

Verze 1 používala `/opt/laravel-erp` a `/etc/laravel-erp`. Postup: `dkit app create erp --preset laravel …`, do `/etc/dkit/apps/erp/env` přeneste `APP_KEY` a další proměnné, `dkit deploy erp`, pak `dkit db import erp < dump.sql` z původní databáze a zkopírujte `storage/` do `/var/lib/dkit/apps/erp/data/storage/`. Starý stack zastavte před `dkit platform up` (porty 80/443).
