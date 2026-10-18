// Small progressive enhancements; every page works without JavaScript except live job output.
document.addEventListener('DOMContentLoaded', () => {
  // Configuration tabs
  document.querySelectorAll('[data-tab]').forEach(btn => btn.addEventListener('click', () => {
    document.querySelectorAll('[data-tab]').forEach(b => b.classList.toggle('on', b === btn));
    document.querySelectorAll('.tab').forEach(t => t.classList.toggle('on', t.id === btn.dataset.tab));
  }));

  // Confirmation for disruptive actions
  const questions = {
    rollback: 'Přepnout zpět na předchozí verzi?',
    stop: 'Zastavit aplikaci? Web přestane odpovídat.',
    restart: 'Restartovat běžící kontejnery?',
    remove: 'Odregistrovat aplikaci? Kontejnery se zastaví, data zůstanou.',
  };
  document.querySelectorAll('form[data-confirm]').forEach(f => f.addEventListener('submit', e => {
    const q = questions[f.dataset.confirm];
    if (q && !confirm(q)) e.preventDefault();
  }));

  // New app form: show fields relevant to the preset
  const preset = document.getElementById('preset');
  if (preset) {
    const hints = {
      laravel: 'PHP-FPM + nginx, MariaDB, Redis, scheduler a queue worker, migrace přes artisan.',
      php: 'Obecné PHP (Symfony, Nette, WordPress…) s nginx + PHP-FPM a MariaDB.',
      node: 'Node.js aplikace (npm start na portu 3000) s PostgreSQL.',
      static: 'Statický web nebo SPA, volitelný npm build, servírováno nginx.',
      dockerfile: 'Libovolný jazyk — použije Dockerfile z repozitáře.',
      image: 'Hotový image z registru (Gitea, n8n, Uptime Kuma, vlastní image…).',
    };
    const sync = () => {
      const img = preset.value === 'image';
      document.querySelectorAll('[data-for="git"]').forEach(el => el.hidden = img);
      document.querySelectorAll('[data-for="image"]').forEach(el => el.hidden = !img);
      document.getElementById('preset-hint').textContent = hints[preset.value] || '';
    };
    preset.addEventListener('change', sync);
    sync();
    const custom = document.getElementById('custom-services');
    const box = document.getElementById('services');
    const syncSvc = () => box.querySelectorAll('input').forEach(i => i.disabled = !custom.checked);
    custom.addEventListener('change', syncSvc);
    syncSvc();
  }

  // Live job output
  const log = document.getElementById('job-log');
  if (log) {
    const state = document.getElementById('job-state');
    let offset = 0;
    const poll = async () => {
      try {
        const r = await fetch(`/jobs/${log.dataset.job}/raw?offset=${offset}`, { credentials: 'same-origin' });
        if (!r.ok) throw new Error(r.status);
        const text = await r.text();
        offset = parseInt(r.headers.get('X-Offset') || offset, 10);
        if (text) {
          const atBottom = log.scrollTop + log.clientHeight >= log.scrollHeight - 30;
          log.textContent += text;
          if (atBottom) log.scrollTop = log.scrollHeight;
        }
        const running = r.headers.get('X-Running') === 'true';
        const exit = r.headers.get('X-Exit');
        if (running) { setTimeout(poll, 1000); return; }
        state.className = 'badge ' + (exit === '0' ? 'ok' : 'err');
        state.textContent = exit === '0' ? 'OK' : `chyba ${exit}`;
      } catch (e) { setTimeout(poll, 3000); }
    };
    poll();
  }
});
