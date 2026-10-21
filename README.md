# dkit — Docker Compose deployment kit

Univerzální nástroj pro provoz **libovolného počtu webů** na jednom serveru s Debianem 13: **CLI `dkit`**, **webové rozhraní**, blue/green nasazení bez výpadku, automatické HTTPS přes Traefik a Let's Encrypt, **kontejnery pro databáze, Redis a S3 úložiště** (vlastní pro každý web, nebo sdílené) a šifrované zálohy přes Restic.

Vše je nastavitelné bez ruční editace souborů — formuláři ve webovém rozhraní nebo krátkými příkazy (`dkit app set`, `dkit env set`, `dkit domain add`, `dkit worker add`, `dkit config set`). Každá změna se zkontroluje a neplatná se automaticky vrátí. Vlastní presety a šablony buildů upgrade kitu nepřepíše.

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

## Stav podpory platforem

Nové Go CLI (`cmd/dkit`) se sestavuje pro Windows, macOS a Linux. Na všech třech
platformách nabízí bezpečné cesty konfigurace a stavu podle uživatele, výpis a
výběr Docker contextu a předání příkazů přímo Docker CLI. Docker Desktop může
na Windows a macOS provozovat Linuxové kontejnery; vzdálený Docker context lze
použít také tam, kde je dostupný Docker CLI a síťové připojení k daemonu.

**Debian instalátor a legacy `dkit deploy` zůstávají Linux/Bash funkcemi.**
Instalátor zpřístupní také přenosné Go CLI jako `dkit-go` v terminálu serveru;
web UI podporuje plný výchozí legacy engine i volitelný portable Go CLI engine.
Portable Go CLI umí sestavit Git app image,
spravovat omezený Compose projekt a nově také omezené web-only blue/green
nasazení přes `dkit app compose deploy` a návrat přes `rollback`. Blue/green
vyžaduje podporovaný image/Git build, nepodporuje workery ani automatické DB
migrace a sdílí databáze i persistentní adresáře obou verzí. Nejde o plnou
náhradu legacy deploy enginu. Portable UI podporuje výběr Git refu při buildu,
validovaný editor `compose.override.yaml` a sekvenční hromadné deploy/apply/
backup akce; při použití override je blue/green deploy/rollback odmítnut.
Správa platformy a automatické migrace při deployi stále vyžadují legacy engine.
Aktuální `install.sh` je určen pouze pro Debian 13 a
stávající blue/green engine používá Bash, unixové nástroje, Linuxové cesty,
bind mounty a předpoklady o síti/kontejnerech. Výběr Docker contextu sám o sobě
nečiní tento engine kompatibilním s Windows/macOS ani negarantuje vzdálené
nasazení. Na Linuxu zůstává současná instalace a její `/etc/dkit` a
`/var/lib/dkit` výchozí cesty zachována; Go CLI předává deployment příkazy
stávajícímu Bash enginu. Debian 13 je podporovaný deployment host, nikoliv požadavek pro
portable CLI ani pro Docker kontejnery.

### Matice přenosné podpory

| Oblast veřejného CLI | Portable Go stav | Hranice |
| --- | --- | --- |
| Cesty, Docker context, info/status/doctor | Podporováno | Context vybírá Docker CLI; přenosná cesta sama neimplementuje síť ani daemon. |
| App list/check/create/clone/get/set/remove, env list/set/unset/replace, domain a worker config | Podporováno; Go Compose app web route používá Traefik file provider, worker path routes Docker provider labels | `--purge` je explicitně destruktivní a smaže Compose volumes i lokální state; renderer vyžaduje předem dostupnou síť `dkit_edge` a kompatibilní platformní Traefik. |
| Globální config, vlastní presety a build šablony | Portable CLI implementuje get/set a preset/template list/show/create/remove/reset | Změna globálního configu sama nerestartuje platformu; vlastní položky jsou lokální pro config directory. |
| Git build, image app, omezený Compose lifecycle a web-only blue/green deploy/rollback | Částečně podporováno | `app compose deploy [--ref]` před nasazením sestaví zadaný Git ref; workery a DB migrace nejsou koordinované a sloty sdílejí persistentní data. `compose.override.yaml` funguje v single-service lifecycle, ale ne v blue/green. |
| DB shell/dump/import/migrate; app logs/exec | Podporováno pro vlastní služby | Sdílené DB, S3 a plná legacy migrace zůstávají nepodporované. |
| Backup, snapshot list a restore staging | Částečně podporováno | Není zaručena konzistence živých named volumes; chybí platformní/global parity. |
| Legacy `dkit deploy`, `dkit rollback`, `dkit apply`, UI blue/green | Linux/Bash only | Go má samostatné omezené `dkit app compose deploy/rollback`; legacy deploy workflow zůstává Bash. Portable UI nabízí jen omezené app Compose akce, odmítne workery a migrace samo neprovádí. |
| Web UI | Portable režim pro app/config + výchozí legacy režim | Bulk akce jsou sekvenční per-app jobs; portable backup podporuje jen některé app modely. Správa platformy ani automatické migrace při deployi nejsou přenosné; override je zakázán při blue/green deploy/rollback. |
| Adminer, platform management, install/upgrade | Linux/Bash only | Jsou svázané s Debian hostem a legacy platform Compose projektem. |

### Portable Go CLI

Vyžaduje Go 1.22+ pro sestavení a Docker CLI s Compose pluginem pro Docker
příkazy. Při pushi tagu `v<verze>` shodného se souborem `VERSION` CI otestuje
CLI na Linuxu, macOS a Windows a vytvoří GitHub Release s binárními balíčky
pro Linux amd64/arm64, macOS amd64/arm64 a Windows amd64/arm64. Balíčky
obsahují portable CLI (`dkit`), nikoli Debian instalátor ani web UI; publikují
se také kontrolní součty v `SHA256SUMS`.

```sh
go install ./cmd/dkit
dkit paths
dkit context list
dkit context use desktop-linux
dkit docker info
dkit compose -f compose.yaml ps
dkit compose -f compose.yaml config
dkit status
dkit status --json
dkit app create shop --preset laravel --domain shop.example.com --repo https://github.com/org/shop.git
dkit app create kuma --preset image --domain status.example.com --image louislam/uptime-kuma:1
dkit app clone shop staging --domain staging.example.com
dkit app remove staging --yes
dkit app build shop --ref main
dkit app list
dkit app list --json
dkit apps --json
dkit app show shop --json
dkit app check shop
dkit app compose render kuma
dkit app compose render kuma --write
dkit app compose config kuma
dkit app compose apply kuma
dkit app compose up kuma
dkit app compose deploy shop --ref v2.3.1
dkit app compose deploy kuma
dkit app compose ps kuma
dkit app compose ps kuma --json
dkit app compose health kuma
dkit app compose health kuma --json
dkit app compose rollback kuma
dkit app compose logs kuma
dkit app compose stop kuma
dkit app compose exec kuma -- sh
dkit domain list shop
dkit domain add shop www.shop.example.com
dkit worker list shop
dkit worker add shop queue --cmd "php artisan queue:work"
dkit env replace kuma < environment.env
dkit db shell app-with-postgres
dkit db dump app-with-postgres > backup.sql
dkit db import app-with-postgres --yes < backup.sql
dkit db migrate shop --yes
dkit logs kuma --tail 100
dkit exec kuma -- sh
dkit backup kuma
dkit backup list kuma
dkit backup restore kuma latest --target ./kuma-restore
dkit app check shop --json
dkit preset list
dkit preset list --json
dkit preset create my-php --from php
dkit template list --json
dkit template create my-node --from node
dkit config get --json
dkit config set KEEP_PREVIOUS=0
dkit doctor
```

Na Debian serveru instalátor sestaví portable CLI z přibalených zdrojů přes
Docker Buildx a nainstaluje příkaz `dkit-go`. Pro přístup k systémové
konfiguraci a stavu jej spouštějte přes `sudo`; standardně používá
`/etc/dkit` a `/var/lib/dkit`. Cesty lze změnit proměnnými
`DKIT_CONFIG_DIR` a `DKIT_STATE_DIR`. Původní příkaz `dkit` nadále poskytuje
plné serverové rozhraní včetně správy platformy a legacy deploy workflow.
Portable UI lze zapnout volbou `DKIT_UI_ENGINE=portable`; výchozí zůstává
plné legacy UI, dokud portable Go workflow nepokrývá všechny operace serveru.

`dkit context use` ukládá výběr do `config.json` v uživatelském konfiguračním
adresáři (Windows: `%AppData%`, macOS: `~/Library/Application Support`,
Linux: `$XDG_CONFIG_HOME` nebo `~/.config`). Stav je v uživatelském datovém
adresáři (Windows: `%LocalAppData%`, macOS: `~/Library/Application Support`,
Linux: `$XDG_DATA_HOME` nebo `~/.local/share`). `DKIT_CONFIG_DIR` a
`DKIT_STATE_DIR` tyto cesty přepíší; pro
kompatibilitu jsou podporovány i `DKIT_ETC` a `DKIT_STATE`. Context se řeší v
pořadí `DOCKER_CONTEXT`, uložená volba `dkit context use`, aktivní context
Docker CLI. `dkit docker ...`,
`dkit compose ...` i `dkit doctor` předají vybraný context explicitně Docker
CLI. `dkit status` bezpečně (bez změny stavu) načte Docker daemon info a seznam
všech kontejnerů; `--json` vypíše strojově čitelný výsledek. Nedostupný daemon,
neplatný výstup nebo chyba Docker CLI skončí chybou s popisem místo prázdného
nebo úspěšného výsledku. Příkaz Compose je průchozí obal nad `docker compose`,
takže přijímá standardní Compose argumenty a pracuje s lokálním souborem Compose;
je vyžadován Docker Compose plugin. U vzdáleného contextu se image, bind-mounty
a relativní cesty vyhodnocují podle schopností Docker Compose a prostředí daemonu
— lokální CLI samo o sobě nepřenáší zdrojové soubory ani data na vzdálený host.

Veřejné Go Compose aplikace se připojují k předem vytvořené Docker síti
`dkit_edge`. Webová trasa se spravuje přes Traefik file provider v
`<state-dir>/traefik/dynamic/<name>.yml`; po úspěšném `up/apply/start` se
soubor atomicky nahradí (včetně starší stejnojmenné legacy trasy) a po
`down/stop/remove` odstraní před vypnutím kontejnerů. Pokud Compose stop/down
selže, předchozí route se atomicky obnoví. `PUBLIC=0` web route ani edge síť nevytváří.
Worker path routes zůstávají opt-in Docker-provider labels
(HTTPS resolver `le`, HSTS a host/path pravidla).
Každý web route soubor obsahuje vlastní namespaced security middleware, takže
nezávisí na vygenerované globální middleware konfiguraci.
Měnící lifecycle operace nad jednou aplikací a její odstranění jsou
serializované OS-level per-app zámkem; různé aplikace se navzájem neblokují.
Stejný zámek chrání i `app build` a přímé `compose render --write`, aby nemohly
souběžně změnit build metadata nebo aktivní model během lifecycle operace.
Platformní `platform/compose.yaml` zpřístupňuje Traefiku pouze read-only
Docker API přes `docker-socket-proxy` na oddělené interní síti; proxy nemá
publikovaný port a `POST=0` zakazuje mutační HTTP metody. Přesto může číst
metadata kontejnerů včetně jejich environment konfigurace, proto je její síťová
izolace důležitá. Pro legacy instalaci aktualizujte platformní Compose a spusťte
`dkit platform up`, aby vznikly sítě `dkit_edge`/`dkit_control` a proxy služba.
File-provider web backend používá aktivní health check podle `HEALTH_PATH`.
Omezený blue/green deploy Go CLI je dostupný přes
`dkit app compose deploy`; jeho hranice a postup jsou popsány níže.
Single-service `up/apply/start` ale předchozí slot neuchovává, takže při
neúspěšném backendu může trasa vracet 503.
U vzdáleného Docker contextu musí na cílovém daemonu už běžet kompatibilní
Traefik/proxy a existovat `dkit_edge`; platformní bind-mounty a ACME úložiště
musí být spravované na hostiteli daemona. CLI samo nepřenáší route soubor z
lokálního `<state-dir>` na vzdálený host; pro veřejné routování musí cesta
namapovaná Traefikem obsahovat stejný spravovaný soubor.

**Provoz aplikací bez Dockeru zatím Go CLI nepodporuje.** Go binary může dodat
control plane a HTTP reverse proxy, ale neumí obecně spustit aplikaci z OCI
image ani zajistit její jazykový runtime. Nativní režim by proto musel být
samostatný backend pro výslovně podporované runtimes (například Go, Node.js a
PHP-FPM), s jejich instalací, process supervision, logy, restartem, izolací a
správou persistentních dat. Pro různorodé aplikace a současnou paritu zůstává
nejjednodušší Docker/Compose; vestavěnou proxy lze mít bez Dockeru jen tehdy,
když aplikace běží jako nativní procesy spravované jiným mechanismem.

`dkit app list` a `dkit app check <name>` pracují pouze se soubory
`<config-dir>/apps/<name>/app.env`. Zachovávají podporované konvence
legacy parseru (`KEY=VALUE`, komentáře, jednoduché/dvojité uvozovky a `export`)
a ověřují statická pole známá z Bash `app_validate`. Výpis zahrnuje i neplatné
konfigurace místo jejich zamlčení; check skončí nenulovým kódem pro neplatný
nebo chybějící soubor. Tato kontrola **není plnou náhradou `dkit app check` v
Bash enginu**: nekontroluje render Compose, dostupnost Dockeru, repozitář,
`env`/credentials, `state.env`, skutečné existence sítí ani readiness aplikace.
Nečte globální `dkit.env` ani nezpracovává surové preset šablony s placeholdery;
pracuje s výsledným `app.env` jedné aplikace a nikdy nevyhodnocuje jeho hodnoty
jako shell kód. Neprovádí žádné Docker volání ani změny souborů.
`dkit apps`/`dkit ls` jsou legacy aliasy pro výpis aplikací; `dkit app
show|info <name> [--json]` zobrazí konfiguraci a případnou Git build revision.
Show je záměrně config-only a neoznačuje Docker runtime, slot ani veřejnou
routovací vrstvu za ověřenou.

`dkit app get <name> [key|--json]` čte hodnoty `app.env`;
`dkit app set <name> KEY=VALUE... [--unset KEY]` provede statickou validaci a
atomicky zapíše změny při zachování komentářů a režimu souboru. `dkit env list
<name>` ve výpisu maskuje klíče končící na `KEY`, `SECRET`, `PASSWORD`, `TOKEN`
nebo `PASS`; `--show` je zobrazí a `--json` vrací úplné hodnoty kompatibilně se
starým CLI, proto s ním zacházejte jako s citlivým výstupem. `dkit env set` a
`dkit env unset` atomicky upraví Compose env soubor a zachovávají Compose
uvozovací pravidla pro `$`, zpětná lomítka a uvozovky. Tyto operace pracují
výhradně v `<config-dir>/apps/<name>` a nevolají Docker. Go verze nekontroluje
výsledný Compose render ani runtime dopad; pro config změny je třeba následně
zvlášť použít podporovaný deployment engine.

`dkit app create <name>` vytvoří lokální konfiguraci z vestavěného nebo
uživatelského presetu v per-user config adresáři. Vyžaduje `--preset` a
alespoň jeden `--domain`, dále `--repo` pro Git preset nebo `--image` pro image
preset; volitelný `--source` musí souhlasit se zdrojem daného presetu.
Přijímá také `--branch`, `--services` a `--port`. Bezpečně
rozbalí známé tokeny presetu a generuje náhodné klíče/secrets kryptograficky;
adresáře vytváří s požadovaným režimem 0700, `app.env` 0640 a tajný `env`
0600. POSIX systémy tato oprávnění vynucují; Windows používá ACL zděděná
z rodičovského adresáře a Go režim souboru plně nepřekládá na ACL.
Vytvoří také adresář `ssh/`, ale neklonuje repozitář, nepřipojuje se k Dockeru
ani nenasazuje. Výsledný `app.env` lze ověřit přes `dkit app check <name>`.
Konfiguraci připraví ve skrytém dočasném adresáři a zveřejní ji atomickým
přejmenováním; neúspěšné zápisy uklidí. Existující aplikaci nikdy nepřepíše.
Odstranění aplikace a další rozšířené operace i nadále vyžadují Bash/Linux
engine. Go CLI vyžaduje
explicitní přepínače a ukládá do vybraného config adresáře (standardně
uživatelského); nepřenáší automaticky vytvořenou aplikaci do `/etc/dkit`.
Chcete-li ji na Debianu použít s Bash deployment enginem, nastavte při vytváření
i následných příkazech `DKIT_CONFIG_DIR=/etc/dkit`, například:
`sudo env DKIT_CONFIG_DIR=/etc/dkit dkit app create shop --preset laravel
--domain shop.example.com --repo https://github.com/org/shop.git`.
Interaktivní tvorba bez přepínačů zůstává dostupná v původním Bash CLI.

`dkit app clone <source> <name> --domain <domain> [--no-env]` kopíruje
validovaný `app.env` a standardně i aplikační `env`, ale nepřenáší `services.env`
(při následném `render --write` se vytvoří nové service credentials) ani
`build.json`/stav zdrojové aplikace. Odstraní zdrojové `DB_NAME`, `DB_USER`,
S3 jména/doménu a Go-only `PUBLISH_PORT`; doménu nastaví na zadanou hodnotu.
Klonování zdrojové aplikace s `compose.override.yaml` nebo privátním SSH Git
buildem odmítne, místo aby ztratilo konfiguraci nebo klíče. `--no-env` vytvoří
prázdný env soubor. Zkopírované
proměnné mohou obsahovat tajemství; CLI na to upozorní a doporučuje rotovat
app-specific klíče. Vytvoření probíhá atomickým přejmenováním a nic nenasazuje.

`dkit app build <name> [--ref <git-ref>]` klonuje HTTPS repozitář do dočasného
checkoutu, sestaví image přes Docker CLI a uloží jeho revision-tagged image
metadata do portable state. Docker context se přenese i do `docker build`, takže
lokální checkout může Docker CLI poslat vzdálenému daemonu jako build context.
Build metadata ukládá název tohoto Docker contextu; Compose lifecycle odmítne
jeden Git image používat z jiného contextu a vyžádá nový build.
Podporuje `BUILD=dockerfile` (včetně omezené relativní cesty `DOCKERFILE`) a
vestavěné šablony `php`, `node`, `static`, s uživatelským override v
`<config-dir>/templates/<name>/Dockerfile`. Root `.env` v checkoutu vede k
odmítnutí buildu. Při úspěchu se checkout smaže; při selhání zůstává pro
diagnostiku. `BUILD_ARGS` jsou předány jako Docker build args i v BuildKit
secret `.env.production.local`, proto smějí obsahovat pouze veřejné build-time
hodnoty (například `VITE_*`), nikdy tajemství. SSH/private Git credentials,
custom templates mimo per-user config, speciální legacy template options,
cache/retence checkoutů a automatické deploy nejsou v této portable cestě
podporovány.

Pro Git app lze po úspěšném buildu vygenerovat a spustit omezený Compose model.
`build.json` váže image na konfigurační/build-env fingerprint; změna build
nastavení nebo build env způsobí odmítnutí stale modelu a je nutné build
opakovat:

```sh
dkit app build shop --ref main
dkit app compose render shop --write
dkit app compose up shop
```

`dkit app compose render <name>` vytvoří přehled Compose konfigurace pro
aplikaci `SOURCE=image` nebo Git app s aktuálním portable buildem; podporuje
vlastní MariaDB, PostgreSQL a Redis deklarované v `SERVICES`. Výstup vypíše na
stdout a nevolá Docker.
Volba `--write` uloží vygenerovaný soubor do
`<state-dir>/apps/<name>/compose.yaml` s režimem 0600 na POSIX systémech a
umožní jej zkontrolovat přes `dkit compose --env-file
<config-dir>/apps/<name>/services.env -f <cesta> config` (env-file je třeba
jen pro databázová/Redis hesla). Při `--write` se pro deklarované služby
bezpečně vytvoří chybějící náhodná hesla v `services.env` (POSIX 0600,
Windows zděděné ACL) a app runtime proměnné v `<state-dir>/apps/<name>/runtime.env`
(POSIX 0600). Stávající hesla se nerotují. Bez `PUBLISH_PORT` je port
aplikace dostupný pouze uvnitř Compose sítě; při nastavení `PUBLISH_PORT` se
váže jen na `127.0.0.1:<PUBLISH_PORT>` hostitele Docker daemonu (například pro
lokální reverse proxy). U vzdáleného Docker contextu je to loopback vzdáleného
hostitele, nikoli počítače s CLI. Port databáze/Redis se nikdy nepublikuje a
jejich data jsou v pojmenovaných volumes. Vedle app kontejneru generuje interní
`health-probe` service s připnutým `curlimages/curl` tagem, který kontroluje
`http://app:<WEB_PORT><HEALTH_PATH>`; `HEALTH_TIMEOUT` nastavuje počet sekundových
pokusů (1–240, výchozí 90). `PUBLISH_PORT` je volitelné Go-only pole,
ověřované jako port 1–65535; při kolizi portů chybu vrátí Compose. Veřejné
aplikace mají web route přes Traefik file provider; workery s
`WORKER_<name>_PATH` dostávají Traefik Docker-provider labels a připojení k
externí síti `dkit_edge`; je nutné mít spuštěnou kompatibilní platformu
popsanou výše. File-provider backend aktivně ověřuje stejnou cestu jako
`HEALTH_PATH` a nepředává jí provoz, dokud kontrola neuspěje; router ale může
v této době vracet 503. Samostatný `health-probe` neurčuje Docker health status
app kontejneru a tento mechanismus není blue/green přepnutí ani automatický
rollback. Záměrně odmítne Git build bez předchozího `dkit app build`, S3,
sdílené služby, `WEB_CMD` a veřejnou S3 routu, místo aby je tiše vynechal.
Migrace se nespouštějí automaticky;
`dkit db migrate <name> --yes` je spustí výslovně přes jednorázový app kontejner.
`WORKERS` generuje další kontejnery se stejným image, runtime/app env, zadaným
shell příkazem, volitelným uživatelem a interním `expose` portem; přiřazená
`WORKER_<name>_PATH` veřejná routa se vytvoří jen pro veřejnou aplikaci.
`PERSIST=name:/container/path` používá per-app pojmenované Docker volumes
sdílené webem a workery; Docker při prvním připojení naplní prázdný volume
obsahem image v cílové cestě. `PERSIST_OWNER` je zatím odmítnuto (změna
vlastnictví napříč platformami není implementována); named volumes zůstávají
na Docker daemonu a portable Restic záloha je nezahrnuje.
`dkit app compose config|apply|up|deploy|down|start|stop|restart|ps|health|rollback|logs|exec <name>` používá dříve
vygenerovaný model, pevný Compose project `dkit-<name>` a právě zvolený Docker
context; načte také lokální `services.env` pro Compose interpolaci. `up`
používá `--detach --no-build --remove-orphans --wait --wait-timeout 300`;
`down` záměrně nepředává `--volumes`,
takže pojmenované volumes zůstávají zachovány. Tyto lifecycle příkazy skutečně
mění/spouštějí kontejnery; před jejich použitím ověřte model pomocí
`dkit compose --env-file <config-dir>/apps/<name>/services.env -f
<state-dir>/apps/<name>/compose.yaml config`. Nepodporují
libovolné Compose přepínače ani automaticky znovu negenerují konfiguraci.
Web se tak spouští jako jeden Compose service plus HTTP probe a případní
workeři, ne jako blue/green deployment. `up` čeká až 300 sekund na úspěšný HTTP probe;
`start` a `restart` po spuštění pollují app a probe až do `HEALTH_TIMEOUT` a při
neúspěchu veřejnou trasu nezmění.
(`--wait` vyžaduje Docker Compose v2 s podporou `--wait`); při neúspěchu skončí
chybou a neprohlásí app za připravenou. U veřejné app Traefik navíc ověřuje
backend podle `HEALTH_PATH`; dokud neuspěje, backend nedostává uživatelský
provoz, ale route může vracet 503. Nejde o přepnutí na předchozí slot ani
automatický deploy či rollback. Workery se spouští a aktualizují v rámci
Compose operací; `logs`/`exec` přijímají jejich deklarovaná jména. Sdílené služby, jejich credentials,
rotace hesel a plný legacy renderer/deployment zůstávají nepodporované v
portable Go CLI; vlastní MariaDB/PostgreSQL/Redis jsou jediné podporované
automaticky spravované služby. `exec` spouští příkaz ve zvolené service
(výchozí `app`) bez alokace TTY; bez příkazu použije `sh`.
`dkit app compose health <name>` je read-only diagnostika nad zvoleným Docker
contextem: vypíše Compose stavy služeb a uspěje pouze tehdy, když app běží,
všechny nalezené kontejnery jsou spuštěné, jejich případné health checks nejsou
ve stavu `starting`/`unhealthy` a HTTP `health-probe` hlásí `healthy`. Neověřuje
veřejné routování, TLS ani blue/green stav. Health status sidecaru Traefik
nepoužívá jako health status app kontejneru.
`dkit app compose config <name>` předá připravený projekt Docker Compose s
`config --quiet`, aby ověřil syntaxi, interpolaci a model bez změny Docker
stavu; předem vyžaduje `dkit app compose render <name> --write`.
`dkit app compose apply <name>` nejprve znovu vygeneruje model z aktuálního
`app.env`/`env`, pak spustí Compose `up --detach --no-build --remove-orphans
--wait`; může proto znovu vytvořit app/služby a stáhnout nakonfigurovaný image.
Nespouští Git build ani DB migraci a nepřepíná veřejný provoz. Pokud `up`
selže, příkaz oznámí chybu, ale automaticky nevrací konfiguraci ani data.
`dkit app compose deploy <name>` je oddělený web-only blue/green příkaz:
pro Git aplikaci vyžaduje nejprve `dkit app build <name>`; pro `SOURCE=image`
stáhne nakonfigurovaný image. Image před spuštěním připne na Docker image ID,
spustí neaktivní slot a jeho HTTP health probe, a teprve po úspěšném ověření
atomicky přepíše Traefik route a uloží nový aktivní/předchozí slot. Předchozí
slot zůstává spuštěný a `dkit app compose rollback <name>` jej znovu ověří,
přesměruje route a prohodí stav. Neověřuje, že Traefik už změnu souboru načetl;
file provider ji zpracovává asynchronně. Příkaz odmítá aplikace s workery a
`PUBLISH_PORT`, neprovádí DB migrace a nevrací DB změny. Obě web verze sdílejí
DB, Redis a perzistentní adresáře, takže musí být kompatibilní se souběžným
provozem. Deployment state ukládá SHA-256 Compose modelu, runtime hodnot
konfigurace aplikace a vstupních souborů `env`, `runtime.env` a `services.env`.
Změny výběru image a build nastavení jsou z runtime otisku vynechány, aby běžný
deploy nové image zachoval možnost návratu. Rollback a příkazy nad aktivním
slotem odmítnou pokračovat, pokud se model, runtime konfigurace nebo některý
env soubor změnil mimo deploy CLI; deploy i rollback tyto otisky kontrolují
znovu po health checku těsně před přepnutím route. Rollback znovu použije
ověřený model posledního deploye, nikoli nový render z aktuální konfigurace.
Pokud se runtime
konfigurace nebo env soubory změnily mezi dvěma deployi, starý slot si ponechá
otisk původních vstupů a rollback na něj se záměrně odmítne; nejdříve je nutné
znovu nasadit kompatibilní konfiguraci.
Při aktivním slotovém stavu jsou single-service lifecycle (`up`,
`apply`, `down`, `start`, `stop`, `restart`), render se zápisem a odstranění
aplikace odmítnuty, aby nesmazaly sloty; `ps`, `health`, `logs` a `exec`
pracují s aktivním slotem. U veřejných aplikací musí být Traefikův file-provider
adresář dostupný také na hostiteli Docker daemonu; vzdálený context sám route
soubor nepřenáší. Před nasazením ověřte sdílené volume a kompatibilitu změn.
`dkit app remove <name> --yes` nejprve zastaví projekt přes zvolený Docker
context a potom přesune konfiguraci do `apps-removed`, přičemž lokální state a
Compose volumes ponechá a odstraní spravovanou web route. `--purge` navíc před odstraněním konfigurace smaže
Compose-managed volumes a lokální app state; nelze jej vrátit a nemaže Docker
image ani sdílené služby. Obě varianty vyžadují existující Go-managed Compose
model; legacy app bez něj odmítne odebrat, protože runtime nelze bezpečně
identifikovat. Chyba při `compose down` ponechá konfiguraci i state beze změny.
Při změně modelu přes `render --write` CLI uchová jeden předchozí model
spravovaný Go CLI a SHA-256 manifest v app state. Při prvním přechodu ze starší
Compose konfigurace bez manifestu se starý soubor za důvěryhodnou historii
nepovažuje; rollback je dostupný až po další změně modelu spravované Go CLI.
`dkit app compose rollback <name>` ověří
tento záznam, spustí předchozí Compose model ve zvoleném Docker contextu,
vyčká na Compose health a ověří HTTP probe; při úspěchu prohodí aktivní a
předchozí lokální model, při neúspěchu nepovýší historii
modelu a pokusí se znovu spustit předchozí aktivní model ze zabezpečeného
dočasného snapshotu; pokud ani obnova neprojde, příkaz vrátí explicitní chybu.
Je to explicitní operace měnící stav kontejnerů. Vrací pouze Compose
model/image konfiguraci, nikoli DB data ani migrace, a nepřepíná veřejný
provoz, domény ani TLS a neaktualizuje dynamický web route soubor; nejde o
náhradu legacy blue/green rollbacku. Další
úspěšný render nastaví novou jednokrokovou předchozí verzi.

`dkit domain list|add|remove <app> [domain...]` a `dkit worker
list|add|remove <app> [worker] ...` spravují příslušné hodnoty v `app.env`
přes stejnou validaci a atomický zápis jako `dkit app set`. Domény se při
přidání normalizují na malá písmena a duplicity se ignorují. Worker `add`
vyžaduje `--cmd` a volitelně přijímá `--port`, `--path` a `--user`; všechna
pole projdou validací konfigurace před zápisem. Tyto příkazy nemění Docker
stav ani nevytvářejí veřejné routy: Go Compose renderer stále odmítá worker
`--path` a neumí připojit domény k veřejnému proxy/TLS. Po změně workerů je
nutné znovu vygenerovat Compose model a explicitně spustit `dkit app compose up`;
`up` odstraní osiřelé service kontejnery daného Compose projektu, ale ponechá
named volumes. Odstranění workeru z konfigurace samo jeho již běžící kontejner
nemění.
`dkit db shell <name>` otevře interaktivní klient pro první vlastní databázi
MariaDB/PostgreSQL v pořadí `SERVICES`; `dkit db dump <name>` vypíše SQL dump
na stdout (lze přesměrovat do souboru). Obě operace vyžadují předchozí
`render --write` a pracují přes zvolený Docker context bez vystavení DB portu.
`dkit db import <name> --yes` vyžaduje explicitní potvrzení a SQL na stdin;
import může změnit nebo částečně přepsat data a jeho účinek nemusí být
transakční. `dkit db migrate <name> --yes` vyžaduje `MIGRATE_CMD` v app.env,
spouští jej přes `sh -c` v jednorázovém kontejneru nad aktuálním Compose modelem
a předává případné `MIGRATE_USER`; migrace mohou nevratně změnit data a nikdy
se nespouštějí automaticky. Na Linuxu zůstává legacy syntaxe
`dkit db import <name>` s interaktivním potvrzením dostupná přes Bash engine.
Přenosné zálohování podporuje omezený rozsah popsaný níže; Adminer a sdílené
databáze zatím nejsou implementované v portable CLI. `dkit logs <name> [service...] [-f] [--tail N]` a
`dkit exec <name> [service] [-- command...]` pracují nad právě vykresleným
Compose modelem; `exec` bez příkazu otevře `sh`. Na Linuxu se aplikace
postavené z Gitu nadále předávají původnímu Bash enginu. Přenosná verze
odmítne neznámou Compose službu nebo zastaralý/ručně upravený model.

### Přenosné zálohování (omezený rozsah)

`dkit backup <name>` a `dkit backup list [name]` používají `restic` a
kompatibilní formát `<config-dir>/backup.env` s položkami
`RESTIC_REPOSITORY=...` a `RESTIC_PASSWORD=...`; další proměnné Restic
(například `RESTIC_PASSWORD_FILE` či backendové proměnné) jsou předány
procesu `restic`. Hodnota `BACKUP_HOST` (poté `DKIT_HOSTNAME`) se použije jako
název hostitele snímku, stejně jako v Bash enginu. Portable příkaz existující repozitář sám neinicializuje
ani nemaže a neprovádí `forget`/`prune`. Na POSIX
systémech odmítá `backup.env` dostupný skupině nebo ostatním; nastavte
oprávnění `0600`. Na Windows Go neumí nastavit ACL podle `FileMode`, proto
se uplatní ACL zděděná z nadřazeného adresáře a přístupová práva je třeba
zkontrolovat samostatně.

Portable záloha je dostupná pro aplikace `SOURCE=image`, jejichž compose model
byl vygenerován přes `dkit app compose render <name> --write`. Zahrnuje
konfiguraci aplikace, její lokální stavový adresář a SQL dump vlastních
MariaDB/PostgreSQL služeb přes vybraný Docker context. Existující Compose
named volumes pro databáze, Redis a `PERSIST` se exportují pomocným
`busybox:1.36.1` kontejnerem jen pro čtení; chybějící volumes se nevytvářejí
ani nepředstírají jako zálohované. Docker CLI předá archiv i ze vzdáleného
contextu do lokálního Restic procesu. Snapshoty Docker volumes mohou zachytit
soubory během změn a **nejsou aplikačně konzistentní**; SQL dump databází je
samostatný logický export. Dumpy se dočasně vytvářejí pod
`<state-dir>/apps/<name>/backup-dumps` s požadovanými režimy `0700`/`0600`
(POSIX je vynucuje; Windows používá zděděná ACL) a po Restic příkazu se
odstraní. Volume archivy jsou pod `<state-dir>/apps/<name>/volume-snapshots`
a po záloze se odstraní. Při selhání exportu se záloha Resticu nespustí;
zbylá složka po přerušení způsobí odmítnutí místo přepsání. Dump i volume
archivy jsou ve snímku uložené pod těmito relativními cestami. Konfigurace aplikace
obsahuje také `env` a `services.env` a je zašifrována až cílovým Restic
repozitářem, proto používejte důvěryhodný repozitář a silné heslo.

Toto není parity s Linuxovým `backup`: nezahrnuje writable layer kontejnerů,
S3 data, sdílené služby,
globální/platformní konfiguraci, všechny aplikace ani retenci/pruning. Git
build aplikace se na Linuxu předá Bash enginu, zatímco Windows/macOS jej
odmítnou. `backup list` čte snímky z už existujícího Restic repozitáře.
`dkit backup restore <name> [snapshot] --target <new-directory>` obnoví snímek
označený `dkit:<name>` (a stejným hostem); bez ID použije `latest`. Cílová
složka musí ještě neexistovat a její rodič musí existovat. Obnova probíhá jen
do této nové staging složky; nic se nekopíruje do aktivní konfigurace/stavu a
neprovádí se žádná Docker operace. Při chybě Resticu zůstane případný částečný
obsah pro ruční inspekci. Následné ověření SQL dumpu a řízené obnovení do
databáze zůstává na uživateli. Bulk/platform zálohování zůstává Linux/Bash
funkcí.

`dkit preset list` vypisuje katalog vestavěných presetů zabalených v binárce a
vlastních presetů v `<config-dir>/presets/<name>/app.env`; vlastní preset
přepíše vestavěný preset stejného jména. `preset show/create/remove` umožňují
číst a spravovat vlastní presety. `template list/show/create/reset` poskytují
základní správu build šablon v `<config-dir>/templates`; plná interaktivní
editace šablon a legacy workflow zůstávají v Bash enginu. Globální `config
get/set` pracuje s `dkit.env` v config directory a při jeho změně samo
nerestartuje platformu.

Vytvoření binárky pro aktuální OS: `go build -o dkit ./cmd/dkit` (na Windows
vznikne `dkit.exe`). Cross-build lze provést například `GOOS=darwin GOARCH=arm64
go build -o dkit-darwin-arm64 ./cmd/dkit`.

## Co obsahuje

| Část | Popis |
|---|---|
| `cmd/dkit` | Portable Go CLI: user-scoped paths, Docker contexts, status, app/global config, presets/templates, Git builds, limited Compose lifecycle/DB/logs/exec, Restic app backup/staged restore |
| `bin/dkit` a `lib/` | Aktuální deployment engine (Bash; zatím Linux/Debian) |
| `ui/` | Webové rozhraní (Go, bez externích závislostí), běží jako kontejner `dkit-ui` |
| `platform/compose.yaml` | Traefik (80/443, Let's Encrypt), web UI a volitelné sdílené MariaDB/PostgreSQL/S3 |
| `presets/` | Výchozí konfigurace: `laravel`, `php`, `node`, `static`, `dockerfile`, `image` |
| `templates/` | Dockerfile šablony pro repozitáře bez vlastního Dockerfile: `php`, `node`, `static` |
| `install.sh` | Instalátor pro Debian 13 (Docker z oficiálního repozitáře, nástroje, adresáře) |

Rozložení na serveru:

| Cesta | Obsah | Při upgradu |
|---|---|---|
| `/opt/dkit` | kód kitu | nahrazen |
| `/etc/dkit/dkit.env` | globální nastavení (ACME e-mail, image služeb, sdílení, UI) | nikdy se nepřepisuje |
| `/etc/dkit/platform.env` | hesla sdílených služeb (0600) | nikdy se nepřepisuje |
| `/etc/dkit/presets/<preset>/` | vlastní presety (mají přednost před výchozími) | nikdy se nepřepisuje |
| `/etc/dkit/apps/<app>/` | `app.env` (nastavení kitu), `env` (proměnné aplikace, 0600), `services.env` (hesla služeb, 0600), `ssh/` (deploy key), volitelně `compose.override.yaml` | nikdy se nepřepisuje |
| `/etc/dkit/templates/<šablona>/Dockerfile` | lokální úprava šablony (má přednost) | nikdy se nepřepisuje |
| `/var/lib/dkit/apps/<app>/` | vygenerovaný `compose.yaml`, `state.env`, `network.env` (subnet), perzistentní data `data/<jméno>/`, S3 data `s3/` | zachováno |
| `/var/lib/dkit/shared/` | data sdíleného S3 | zachováno |
| `/var/lib/dkit/traefik/dynamic/` | vygenerované směrování | generováno |

## Důležitá omezení

1. **Jeden server není vysoká dostupnost.** Host, disk, Docker a síť zůstávají single point of failure.
2. Blue/green se týká webu. Databáze, Redis, S3 a workery jsou sdílené mezi verzemi; workery se po přepnutí restartují s novým image.
3. Migrace databáze nelze obecně vrátit. Používejte zpětně kompatibilní (expand/contract) migrace; rollback vrací jen image.
4. Aplikace musí na `HEALTH_PATH` vracet 2xx/3xx, jinak deploy nepřepne provoz.
5. Aplikace sdílí síť `dkit_edge` (kvůli Traefiku). Vlastní databáze a služby jsou v interní síti každé aplikace; sdílené služby v interní síti `dkit_services`, kde každá aplikace má vlastní účet bez přístupu k datům ostatních.
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

Pak zkontrolujte konfiguraci (ve web UI na záložce *Nastavení*, nebo z CLI) a nasaďte:

```bash
sudo dkit app get shop                    # aktuální nastavení
sudo dkit env set shop MAIL_MAILER=smtp MAIL_HOST=smtp.firma.cz
sudo dkit deploy shop --migrate           # první nasazení včetně migrací
```

Další web stejného typu založíte jedním příkazem — dostane vlastní databázi, hesla a data:

```bash
sudo dkit app clone shop shop2 --domain shop2.firma.cz && sudo dkit deploy shop2
```

## 3. Snadné úpravy

Všechno jde měnit ve **webovém rozhraní** (formuláře bez znalosti formátu souborů) nebo krátkými příkazy. Každá změna se zkontroluje; neplatná se vrátí a nic se nerozbije.

| Co | Web UI | CLI |
|---|---|---|
| Domény | Aplikace → Nastavení | `dkit domain add shop www.shop.cz` · `dkit domain remove …` |
| Port, healthcheck, build, zdroj, služby, limity | Aplikace → Nastavení | `dkit app set shop WEB_PORT=3000 PUBLISH_PORT=13000 MEMORY_LIMIT=1g` |
| Proměnné prostředí a secrets | Aplikace → Proměnné | `dkit env set shop KLÍČ=hodnota` · `dkit env unset shop KLÍČ` · `dkit env list shop` |
| Workery (fronty, plánovač, websockety) | Aplikace → Nastavení → Workery | `dkit worker add shop queue --cmd "php artisan queue:work" --user www-data` |
| Další kontejnery | Aplikace → Soubory → compose.override.yaml | `dkit app edit shop compose.override.yaml` |
| Globální nastavení | Nastavení | `dkit config set KEEP_PREVIOUS=0` · `dkit config edit` |
| Vlastní preset (výchozí konfigurace nových webů) | Nastavení → Presety | `dkit preset create wordpress --from php` |
| Vlastní Dockerfile šablona | Nastavení → Šablony buildu | `dkit template edit php` · `dkit template reset php` |
| Nový web podle existujícího | Aplikace → Klon | `dkit app clone shop shop2 --domain shop2.cz` |

Změny domén se projeví okamžitě. Ostatní změny převezme běžící web po **„Použít konfiguraci“** (`dkit apply shop`, bez buildu), změny zdroje/buildu po dalším deployi. Hromadně: `dkit apply --all`, `dkit deploy --all` nebo tlačítka na přehledu aplikací.

## 4. Neomezený počet webů

Počet webů omezuje jen výkon serveru. Kit řeší úzká místa, která by jinak brzdila desítky až stovky webů:

- **Síťové adresy.** Docker má ve výchozím stavu adresní prostor jen pro ~30 sítí. dkit dává každé aplikaci malý subnet z `APP_SUBNET_POOL` (výchozí `10.210.0.0/16` po `/26` = 1024 aplikací; rozsah lze zvětšit, nesmí kolidovat s LAN).
- **Sdílené služby.** `SHARED_SERVICES="mariadb postgres s3"` v `dkit.env` (nebo v UI *Nastavení*) způsobí, že nové weby nedostanou vlastní kontejner databáze/S3, ale vlastní **databázi, uživatele a bucket** v jedné sdílené instanci. Ušetří stovky MB RAM na web. Oprávnění jsou oddělená (uživatel webu nevidí cizí databáze; S3 klíč funguje jen pro vlastní bucket). Redis je vždy vlastní (~5 MB). Nastavení se při vytvoření webu zapíše do jeho `app.env`, takže pozdější změna globální hodnoty existující weby nepřesune; u webu jde změnit ručně (data se nepřenášejí — dump a import).
- **RAM při deployi.** `KEEP_PREVIOUS=0` zastaví předchozí verzi po úspěšném deployi (rollback pak trvá pár sekund místo okamžitě).
- **Disk.** Ponechávají se jen poslední image (`KEEP_IMAGES`), checkouty se po buildu mažou.
- **Přehled.** Web UI má tabulku s vyhledáváním a filtrem stavu; `dkit apps --json` načte stav všech webů jedním dotazem na Docker.

Doporučení pro desítky malých webů (např. WordPress, prezentace): `SHARED_SERVICES="mariadb s3"`, `KEEP_PREVIOUS=0`, vlastní preset přes `dkit preset create` a nové weby přes `dkit app create … --preset <váš>` nebo `dkit app clone`.

## 5. Referenční přehled nastavení (`app.env`)

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
| `PUBLISH_PORT` | Portable Go Compose only: volitelný host port 1–65535, svázaný pouze s loopbackem Docker hostitele |
| `WEB_CMD` | přepíše příkaz webového kontejneru |
| `WORKERS` | seznam workerů; každý má `WORKER_<jméno>_CMD`, volitelně `_USER` a `_PORT`; Go Compose mapuje veřejné `_PATH` přes Traefik, pokud je `PUBLIC` zapnuté |
| `SERVICES` | kontejnery služeb: `mariadb`, `postgres`, `redis`, `s3` |
| `SHARED_SERVICES` | které z nich běží ve sdílené instanci (`mariadb postgres s3`); nastaví se při vytvoření z `dkit.env` |
| `DB_NAME`, `DB_USER`, `S3_BUCKET` | jména databáze/uživatele/bucketu (výchozí podle jména aplikace) |
| `PERSIST` | perzistentní adresáře `jméno:/cesta/v/kontejneru`; portable Compose používá named volumes sdílené webem/workery |
| `PERSIST_OWNER` | `uid:gid` nových perzistentních adresářů (např. `33:33` pro www-data); portable Compose jej zatím odmítá |
| `MIGRATE_CMD`, `MIGRATE_USER`, `AUTO_MIGRATE` | příkaz migrací; portable CLI jej spouští pouze výslovným `dkit db migrate <name> --yes`, `AUTO_MIGRATE` se ignoruje |
| `S3_DOMAIN` | volitelná veřejná doména pro S3 (presigned URL, veřejné soubory) |
| `MEMORY_LIMIT` | limit paměti procesů aplikace, např. `1g` |
| `KEEP_PREVIOUS` | `1` (výchozí) nechá předchozí slot běžet pro okamžitý rollback; `0` šetří RAM |
| `PUBLIC` | `0` = bez veřejné trasy (interní služba) |
| `MARIADB_IMAGE`, `POSTGRES_IMAGE`, `REDIS_IMAGE`, `S3_IMAGE`, `S3_ARGS` | verze a parametry vlastních služeb této aplikace |

### Služby a proměnné, které aplikace dostane

Soubor `env` čte Docker Compose: hodnoty se znakem `$` uzavřete do jednoduchých uvozovek (`HESLO='a$b'`) nebo zdvojte `$$`.

Každá aplikace má vlastní náhodná hesla (`/etc/dkit/apps/<app>/services.env`) a vlastní kontejnery služeb, případně vlastní databázi ve sdílené instanci (hostitel `dkit-mariadb`, `dkit-postgres`, `dkit-s3`). Připojení se předá automaticky do prostředí aplikace; hodnoty v `env` mají přednost.

| Služba | Kontejner | Proměnné |
|---|---|---|
| `mariadb` | MariaDB 11.4 | `DB_HOST`, `DB_PORT`, `DB_DATABASE`, `DB_USERNAME`, `DB_PASSWORD`, `DATABASE_URL`, `MARIADB_*` |
| `postgres` | PostgreSQL 17 | totéž s `POSTGRES_*`; `DB_*`/`DATABASE_URL` míří na první databázi v `SERVICES` |
| `redis` | Redis 7.4 (AOF) | `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD`, `REDIS_URL` |
| `s3` | SeaweedFS (S3 API) | `S3_ENDPOINT`, `S3_BUCKET`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, `AWS_*` (kompatibilní s Laravel `s3` diskem, AWS SDK, boto3) |

Bucket se jménem aplikace (`S3_BUCKET`, u jmen kratších než 3 znaky s příponou `-files`) se vytvoří automaticky. Databáze a Redis používají Docker volumes, S3 a `PERSIST` adresáře jsou v `/var/lib/dkit/apps/<app>/`.

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

## 6. Nasazení, rollback a provoz

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
dkit app remove shop --yes        # legacy: potvrzené odstranění; portable Go: viz omezení výše, nesmaže shared services
dkit deploy --all                 # postupně všechny weby (pokračuje i po chybě jednoho)
```

Průběh `dkit deploy`:

1. Klon repozitáře v daném commitu (bez `.env` v repozitáři, jinak odmítne), build image `dkit/<app>:<sha>` na serveru.
2. Spuštění služeb a čekání na jejich healthcheck.
3. Volitelně migrace v novém image.
4. Start neaktivního slotu (`web-blue`/`web-green`) a kontrola `HEALTH_PATH` uvnitř sítě.
5. Přepnutí trasy v Traefiku a ověření přes Traefik; při selhání se vrátí předchozí trasa.
6. Restart workerů s novým image, úklid starých image (`KEEP_IMAGES`).

Selhání v kterémkoli kroku nechá provoz na předchozí verzi.

## 7. Webové rozhraní

```bash
sudo dkit ui start                              # build a spuštění kontejneru dkit-ui
sudo dkit ui token                              # přístupový token
ssh -L 9000:127.0.0.1:9000 uživatel@server      # z pracovní stanice
```

Otevřete `http://127.0.0.1:9000`. Rozhraní umí:

- **Přehled** webů s vyhledáváním a filtrem stavu; hromadné deploy/apply/backup akce se v portable režimu spouštějí sekvenčně po aplikacích a výsledek každé je v jedné úloze (backup může některé app typy odmítnout).
- **Vytvoření** webu z presetu (včetně vlastních) a **klonování** existujícího.
- U webu záložky **Přehled** (stav, kontejnery, logy, podporované deploy/rollback a lifecycle akce), **Nastavení**, **Proměnné**, **Soubory** a **Klon a odstranění**. Rozsah akcí závisí na enginu; portable režim spouští hromadné akce jako sekvenční per-app úlohy, ale neprovádí správu platformy ani automatické migrace při deployi. Override editor ověřuje sloučený Compose model; při aktivním blue/green nasazení override není podporován.
- **Nastavení** serveru: globální hodnoty, presety a šablony buildu.
- Živý výstup všech úloh.

- Token změníte `dkit ui token --rotate` (odhlásí všechny relace).
- Veřejný přístup přes HTTPS: nastavte `UI_DOMAIN` v `dkit.env` a `dkit ui start`. Rozhraní má práva roota — zvažte omezení IP adres nebo VPN.
- Výchozí `legacy` režim volá `/opt/dkit/bin/dkit` a zachovává plnou Debian/Bash funkcionalitu. Volitelný `portable` režim používá Go CLI z UI image (`/usr/local/bin/dkit-go`) pro konfiguraci aplikací, env, globální nastavení, presety/šablony a podporované Compose akce.
- Portable režim podporuje výběr Git refu, validovaný editor `compose.override.yaml` v single-service režimu a sekvenční hromadné deploy/apply/backup úlohy. Nemá správu platformy ani automatické migrace při deployi; override je odmítnut při blue/green deploy/rollback. Stav kontejnerů ověřuje přes Docker Compose a sloty zobrazuje jako Go deployment metadata. Výchozím režimem zůstává `legacy`.
- Režim lze změnit v Nastavení serveru, nebo nastavit `DKIT_UI_ENGINE=portable` a `DKIT_BIN=/usr/local/bin/dkit-go` v `/etc/dkit/dkit.env`; následné `sudo dkit ui start` image sestaví a UI restartuje. UI ovládá Docker, tj. má práva roota na serveru.

## 8. Zálohy

```bash
sudo cp /etc/dkit/backup.env.example /etc/dkit/backup.env && sudo chmod 0600 /etc/dkit/backup.env
sudo nano /etc/dkit/backup.env      # RESTIC_REPOSITORY mimo server + RESTIC_PASSWORD
sudo dkit backup --all              # nebo dkit backup shop
sudo dkit backup list shop
```

Záloha aplikace obsahuje SQL dump každé databáze (i ze sdílené instance), konfiguraci `/etc/dkit/apps/<app>`, `PERSIST` adresáře a data vlastního S3. `--all` navíc zálohuje globální konfiguraci, presety, šablony a data sdíleného S3 (tag `dkit:_platform`). Retence 7 denních / 5 týdenních / 12 měsíčních. Pravidelné spouštění nastavte systemd timerem nebo cronem až po úspěšném ručním běhu, např. `/etc/cron.d/dkit-backup`:

```
30 2 * * * root /usr/local/bin/dkit backup --all >> /var/log/dkit-backup.log 2>&1
```

Obnova: `restic restore <snapshot> --target /tmp/obnova`, pak `dkit db import <app> < …/mariadb.sql` (po rozbalení gzip) a zkopírování souborů. Obnovu pravidelně testujte v izolovaném prostředí.

## 9. Nasazení z GitHub Actions

Vzor je v `examples/github-actions-deploy.yml`. Doporučení: samostatný uživatel `deploy` s omezeným sudo jen na konkrétní příkaz:

```
# /etc/sudoers.d/dkit-deploy
deploy ALL=(root) NOPASSWD: /usr/local/bin/dkit deploy shop *
```

Nedávejte CI neomezený root SSH ani přístup k Docker socketu. Akce připněte na plné commit SHA.

## 10. Šablony buildů

- **php** – nginx + PHP-FPM (8.4), Composer, frontend build (npm/pnpm/yarn), rozšíření `pdo_mysql pdo_pgsql intl bcmath gd zip redis opcache…`; další přes `TEMPLATE_ARGS="PHP_EXTENSIONS=imagick soap"`. Docroot `DOCROOT=public`.
- **node** – `npm ci` / pnpm / yarn, `build` pokud existuje, `npm start`, port `$PORT` (3000), běží jako uživatel `node`.
- **static** – volitelný npm build, výstup `STATIC_DIR` servírován neprivilegovaným nginx na 8080, `SPA=1` pro fallback na `index.html`.

Proměnné z `BUILD_ARGS` se při buildu šablon zpřístupní jako `.env.production.local` (načítá Vite, Next.js, Nuxt) a vždy i jako `--build-arg`. Šablonu upravíte ve web UI (Nastavení → Šablony buildu) nebo `dkit template edit <jméno>`; vznikne vlastní kopie v `/etc/dkit/templates/<jméno>/`, kterou upgrade nepřepíše. Stejně lze vytvořit úplně novou šablonu a použít ji přes `BUILD=<jméno>`.

## 11. Aplikace za proxy

Traefik předává `X-Forwarded-For/Proto/Host`. Aplikace musí proxy důvěřovat, jinak generuje `http://` odkazy:

- Laravel 11+: `->withMiddleware(fn ($m) => $m->trustProxies(at: '*'))` v `bootstrap/app.php`.
- Symfony: `framework.trusted_proxies: '172.16.0.0/12,10.0.0.0/8'`.
- Express: `app.set('trust proxy', true)`.

## 12. Přechod z verze 1 (Laravel-only)

Verze 1 používala `/opt/laravel-erp` a `/etc/laravel-erp`. Postup: `dkit app create erp --preset laravel …`, do `/etc/dkit/apps/erp/env` přeneste `APP_KEY` a další proměnné, `dkit deploy erp`, pak `dkit db import erp < dump.sql` z původní databáze a zkopírujte `storage/` do `/var/lib/dkit/apps/erp/data/storage/`. Starý stack zastavte před `dkit platform up` (porty 80/443).
