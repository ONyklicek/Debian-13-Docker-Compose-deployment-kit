# Produkční bezpečnostní checklist

- [ ] Před instalací ověřená záloha a plán obnovy.
- [ ] DNS A/AAAA všech domén ukazuje na správný server.
- [ ] Firewall/security group povoluje jen potřebné porty (22, 80, 443); instalátor firewall nemění.
- [ ] SSH přihlášení jen klíčem, omezený root login, automatické bezpečnostní aktualizace.
- [ ] Git deploy key je read-only a omezený na jediný repozitář; `known_hosts` ověřen nezávisle.
- [ ] `/etc/dkit` má práva 0700, `env`, `services.env`, `backup.env` a `ui.token` 0600, vlastník root.
- [ ] Žádný repozitář neobsahuje `.env` ani jiné secrets (dkit deploy s `.env` odmítne).
- [ ] Laravel: `APP_DEBUG=false`, stabilní `APP_KEY` (zálohovaný mimo server).
- [ ] Databáze, Redis a S3 nemají publikované porty; jsou jen v interní síti aplikace.
- [ ] Adminer spuštěn jen na 127.0.0.1 přes SSH tunel a po použití zastaven (`dkit adminer <app> stop`).
- [ ] Web UI: jen loopback + SSH tunel; pokud `UI_DOMAIN`, pak silný token, omezení IP/VPN a vědomé přijetí rizika (UI = root na serveru).
- [ ] Token web UI po odchodu správce rotován (`dkit ui token --rotate`).
- [ ] Traefik dashboard není vystaven; Docker socket není připojen do Traefiku.
- [ ] Zálohy (`dkit backup --all`) běží pravidelně do repozitáře mimo server, jsou šifrované Resticem a obnova byla otestována.
- [ ] Migrace jsou zpětně kompatibilní; rollback image se nezaměňuje s rollbackem schématu.
- [ ] Verze image služeb (`*_IMAGE` v `dkit.env`) jsou připnuté a pravidelně aktualizované.
- [ ] CI nasazuje přes omezené sudo (`dkit deploy <app> *`), ne přes root SSH nebo Docker socket.
- [ ] Monitoring disku, RAM, certifikátů, záloh a dostupnosti domén.
