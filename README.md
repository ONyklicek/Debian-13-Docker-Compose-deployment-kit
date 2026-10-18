# Laravel ERP — Debian 13 / Docker Compose deployment kit

Tento projekt je **bezpečný výchozí základ**, nikoli univerzální garance kompatibility s každou Laravel aplikací. Před produkčním použitím otestujte sestavení, migrace, obnovu záloh a rollback s konkrétním ERP repozitářem.

## Co obsahuje

- Debian 13 Trixie instalátor s kontrolou OS, oprávnění, volného místa, Dockeru/Compose a portů.
- Docker Compose: Traefik, MariaDB, Redis, dvě webové instance (blue/green), Horizon, Reverb, trvalý `schedule:work` a volitelný phpMyAdmin.
- Kód se klonuje z GitHubu na server; image se sestavuje lokálně z konkrétního Git commitu.
- Přepnutí webové trasy přes Traefik file provider, kontrola health endpointu a základní rollback.
- Secrets mimo Git a image, lokální loopback-only phpMyAdmin, Restic zálohy do externího úložiště.
- Instalátor je opakovatelný: existující konfiguraci nepřepisuje, neprovádí `docker compose down -v`, nemaže volumes a nemění firewall.

## Důležitá omezení

1. **Jedna mašina není vysoká dostupnost.** Host, disk, Docker a síť zůstávají single point of failure.
2. Blue/green se týká webové vrstvy. MariaDB, Redis, Reverb, Horizon a Scheduler jsou sdílené služby. Nasazení aplikace může vyžadovat kompatibilní migrace a restart dlouho běžících procesů.
3. Databázové migrace nelze obecně automaticky vrátit. Používejte expand/contract migrace; rollback obrazu nevrací databázové změny.
4. Reverb klienti mohou při restartu Reverb ztratit spojení a znovu se připojit.
5. Dockerfile očekává běžnou Laravel aplikaci s `composer.lock`; pro frontend očekává `package-lock.json` pokud existuje a příkaz `npm run build`. Specifické rozšíření PHP, Chrome/Browsershot, Imagick, ffmpeg, vlastní build kroky nebo odlišný frontend mohou vyžadovat úpravy Dockerfile.
6. Výchozí Nginx konfigurace obsluhuje Laravel přes PHP-FPM v jednom aplikačním kontejneru. Laravel aplikace musí poskytovat `/up` (Laravel 11+ standardně). Pokud ne, změňte healthcheck i Traefik health URL.
7. `APP_KEY` musí být stabilní. Instalátor jej vygeneruje pouze při první inicializaci secrets. Nikdy ho negenerujte znovu při běžném deploymentu.
8. Zálohy jsou skutečně externí teprve po nastavení Restic repozitáře mimo tento server a úspěšném testu obnovy.

## 0. Správa webů (TUI + web UI)

Projekt obsahuje i jednoduchý lokální správce webů pro snadné přidání a úpravu domén a upstreamů.

- TUI: `sudo python3 /opt/laravel-erp/site_manager.py tui`
- Web UI: `sudo python3 /opt/laravel-erp/site_manager.py web --host 127.0.0.1 --port 8000`
- Generování Traefik pravidel: `sudo python3 /opt/laravel-erp/site_manager.py generate`

Výchozí režim je lokální a bezpečný: web UI se nesmí publikuje na veřejný internet. Pro LAN přístup spusťte na daném rozhraní, například `--host 0.0.0.0`, a nastavte přihlášení / SSH tunnel dle vašeho provozu.

## 1. Předpoklady

- Čistý nebo spravovaný Debian 13 Trixie x86_64/arm64, systemd, root/sudo.
- Veřejné DNS jméno ERP směřuje na server; Reverb používá stejnou doménu přes websocket cestu `/app/{key}`.
- GitHub repo je dostupné přes deploy key nebo read-only GitHub token. Doporučeno SSH deploy key s přístupem jen ke konkrétnímu repozitáři.
- TCP 80/443 jsou dostupné z internetu; firewall musí být nastaven správcem. Instalátor pravidla nemění.
- Dostatek RAM/disku pro současný běh dvou web image, DB, Redis a build procesu.
- Repo má `composer.json`, `composer.lock`, `artisan`, `public/`, `bootstrap/` a kompatibilní Laravel verzi.

## 2. Instalace

Rozbalte ZIP a spusťte:

```bash
cd laravel-erp-deploy-debian13
sudo ./install.sh
```

Pokud Docker Engine nebo Compose plugin nejsou dostupné, instalátor se je pokusí nainstalovat z oficiálního repozitáře Dockeru. Pokud zdroj APT ještě není nastaven, přidá vlastní `/etc/apt/sources.list.d/docker.sources` a podpisový klíč; existující zdroje ani klíče nepřepisuje. Je vyžadován přístup k internetu.

Instalátor **nepřidá pravidla firewallu**. Pokud porty 80/443 blokuje firewall nebo cloud security group, nastavte je samostatně a vědomě.

Při první instalaci se vytvoří `/opt/laravel-erp` a `/etc/laravel-erp`. Secrets jsou uloženy v `/etc/laravel-erp/secrets.env` s právy `0600`. Existující soubory se nepřepisují. Před prvním spuštěním upravte `/etc/laravel-erp/stack.env` a `secrets.env`.

## 3. Konfigurace

V `/etc/laravel-erp/stack.env` nastavte:
- `APP_DOMAIN` — veřejná doména ERP
- `ACME_EMAIL` — email pro Let's Encrypt
- `GITHUB_REPOSITORY` — SSH URL nebo HTTPS URL soukromého repozitáře
- `GIT_BRANCH` — větev pro deployment
- `PMA_BIND_PORT` — lokální port phpMyAdmin (výchozí 8081)

V `secrets.env` nastavte `APP_KEY`, DB hesla, `REVERB_APP_KEY`, `REVERB_APP_SECRET`, `PMA_BLOWFISH_SECRET` a další Laravel secrets. Vygenerované náhodné hodnoty ponechte, pokud aplikace nevyžaduje konkrétní hodnotu.

Git SSH klíč umístěte do `/etc/laravel-erp/ssh/` s právy `0600`, přidejte GitHub host key ověřený nezávislým kanálem do `known_hosts` a upravte `GIT_SSH_COMMAND` podle potřeby. Nezapínejte `StrictHostKeyChecking=no`.

## 4. První deployment

Po úpravě konfigurace:

```bash
sudo /opt/laravel-erp/bin/deploy.sh
```

Při prvním nasazení se webový slot vytvoří, zkontroluje a přepne. Skript vyžaduje nastavení domén a repozitáře. Migrations se spouštějí pouze pokud je výslovně povolíte:

```bash
sudo ALLOW_MIGRATIONS=1 /opt/laravel-erp/bin/deploy.sh
```

**Pozor:** migrace se spustí před přepnutím webu. Udržujte je zpětně kompatibilní s dosavadní verzí. Automatický rollback image nevrátí migrace.

## 5. GitHub deployment

Základní GitHub Actions workflow je v `.github/workflows/deploy.yml.example`. Zkopírujte jej do aplikace a nastavte repository secrets:
- `DEPLOY_HOST`, `DEPLOY_USER`, `DEPLOY_SSH_KEY`
- `DEPLOY_KNOWN_HOSTS` — předem ověřený host key
- `DEPLOY_PATH` — obvykle `/opt/laravel-erp`

Používejte samostatného deploy uživatele s omezeným sudo oprávněním pouze pro konkrétní deployment příkaz. Nedávejte GitHub Actions neomezený root SSH ani přístup k Docker socketu. Workflow je šablona; před produkcí připněte GitHub Actions na ověřené plné commit SHA a doplňte testy aplikace.

## 6. phpMyAdmin

phpMyAdmin není veřejně routovaný. Spusťte jej pouze tehdy, když jej potřebujete:

```bash
cd /opt/laravel-erp
sudo docker compose --env-file /etc/laravel-erp/stack.env --env-file /etc/laravel-erp/secrets.env --profile admin up -d phpmyadmin
```

Z pracovní stanice:

```bash
ssh -L 8081:127.0.0.1:8081 user@server
```

Otevřete `http://127.0.0.1:8081`. Přístup je omezen na loopback serveru a SSH tunel. Po použití:

```bash
sudo docker compose --env-file /etc/laravel-erp/stack.env --env-file /etc/laravel-erp/secrets.env --profile admin stop phpmyadmin
```

## 7. Zálohy mimo server

Nainstalujte `restic` a nastavte `/etc/laravel-erp/backup.env` (viz `backup.env.example`) na **externí** Restic repository (SFTP/S3/compatible). Tento soubor musí mít práva `0600`.

```bash
sudo install -m 0600 backup.env.example /etc/laravel-erp/backup.env
sudo nano /etc/laravel-erp/backup.env
sudo /opt/laravel-erp/bin/backup.sh
```

Nastavte plán přes vlastní systemd timer/cron až po úspěšném ručním běhu. Záloha zahrnuje SQL dump a vybrané aplikační persistentní soubory. Ověřte obnovu v izolovaném prostředí; záloha bez testu obnovy není ověřená.

## 8. Deployment a rollback

```bash
sudo /opt/laravel-erp/bin/deploy.sh
sudo /opt/laravel-erp/bin/rollback.sh
```

Rollback přepne web na poslední uložený zdravý slot, pokud jeho image stále existuje. Reverb websockety běží na stejné doméně jako web, pod cestou `/app/{key}`. Nevrací databázové migrace ani externí změny. Nedělejte `docker image prune -a`, `docker volume prune` ani `docker compose down -v` jako součást běžného nasazení.

## 9. Kontrola a provoz

```bash
cd /opt/laravel-erp
sudo docker compose --env-file /etc/laravel-erp/stack.env --env-file /etc/laravel-erp/secrets.env ps
sudo docker compose --env-file /etc/laravel-erp/stack.env --env-file /etc/laravel-erp/secrets.env logs --tail=200 traefik db redis app-blue app-green horizon reverb scheduler
```

- Traefik ACME potřebuje funkční DNS a přístup na port 80.
- U aplikace ověřte logy, e-mail, queue, Reverb, scheduled tasks, uploads a PDF/print workflow.
- V produkci zapněte monitoring, alerty na volné místo, zálohy, certifikáty a health status.
- Po každé změně Dockerfile otestujte image build na kopii repozitáře.
