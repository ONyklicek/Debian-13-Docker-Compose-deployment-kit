# Produkční bezpečnostní checklist

- [ ] Před instalací ověřený backup a plán obnovy.
- [ ] DNS A/AAAA pro APP_DOMAIN ukazuje na správný host; Reverb používá stejnou doménu přes `/app/{key}`.
- [ ] Firewall/security group má administrátorem povolené pouze potřebné porty; instalátor firewall nemění.
- [ ] SSH login klíčový, root login omezený, pravidelné bezpečnostní aktualizace.
- [ ] GitHub deploy key je read-only a omezený na jediný repozitář.
- [ ] `/etc/laravel-erp/secrets.env` a `backup.env` mají mode 0600 a vlastníka root.
- [ ] `APP_DEBUG=false`, stabilní APP_KEY, hesla unikátní a náhodná.
- [ ] DB/Redis nemají publikované host porty.
- [ ] phpMyAdmin pouze přes SSH tunel na loopbacku a po použití vypnutý.
- [ ] Traefik dashboard není vystaven; Docker socket není připojen do Traefiku.
- [ ] Zálohy se ukládají mimo tento server, jsou šifrované Resticem a obnova byla otestována.
- [ ] DB migrace jsou zpětně kompatibilní; rollback obrazu se nezaměňuje s rollbackem schématu.
- [ ] Aplikace má vlastní testy pro auth, tenancy, uploady, e-mail, queue, scheduler, Reverb a tisk/PDF.
- [ ] Připnuté Docker image verze/digests a GitHub Actions na plné SHA podle firemní politiky.
- [ ] Monitoring disku, RAM, certifikátů, záloh a HTTP health checků.
