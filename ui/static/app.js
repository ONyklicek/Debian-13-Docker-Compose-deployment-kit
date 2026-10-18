// Progressive enhancements; forms work without JavaScript except live job output and adding rows.
document.addEventListener('DOMContentLoaded', () => {
  // Page tabs (links work without JS too)
  document.querySelectorAll('.tabs a[data-tab]').forEach(a => a.addEventListener('click', e => {
    e.preventDefault();
    document.querySelectorAll('.tabs a[data-tab]').forEach(x => x.classList.toggle('on', x === a));
    document.querySelectorAll('.tab').forEach(t => t.classList.toggle('on', t.id === a.dataset.tab));
    history.replaceState(null, '', a.getAttribute('href'));
  }));
  document.querySelectorAll('[data-subtab]').forEach(btn => btn.addEventListener('click', () => {
    document.querySelectorAll('[data-subtab]').forEach(b => b.classList.toggle('on', b === btn));
    document.querySelectorAll('.subtab').forEach(t => t.classList.toggle('on', t.id === btn.dataset.subtab));
  }));

  // Editable tables: add a row from the hidden template row, remove rows
  document.querySelectorAll('[data-add-row]').forEach(btn => btn.addEventListener('click', () => {
    const table = document.getElementById(btn.dataset.addRow);
    const row = table.querySelector('tr.template').cloneNode(true);
    row.hidden = false; row.classList.remove('template');
    row.querySelectorAll('input').forEach(i => { i.disabled = false; });
    table.tBodies[0].appendChild(row);
    row.querySelector('input').focus();
  }));
  document.addEventListener('click', e => {
    if (e.target.matches('[data-remove-row]')) e.target.closest('tr').remove();
    if (e.target.matches('[data-reveal]')) {
      const i = e.target.parentElement.querySelector('input');
      i.type = i.type === 'password' ? 'text' : 'password';
      e.target.textContent = i.type === 'password' ? 'zobrazit' : 'skrýt';
    }
  });

  // Confirmation for disruptive actions
  const questions = {
    rollback: 'Přepnout zpět na předchozí verzi?',
    stop: 'Zastavit aplikaci? Web přestane odpovídat.',
    restart: 'Restartovat běžící kontejnery?',
    remove: 'Odregistrovat aplikaci? Kontejnery se zastaví, data zůstanou.',
    'bulk-deploy': 'Nasadit znovu VŠECHNY aplikace (postupně)?',
    'bulk-apply': 'Použít konfiguraci u všech aplikací (restart kontejnerů)?',
    'bulk-backup': 'Spustit zálohu všech aplikací?',
    'remove-preset': 'Odstranit preset?',
    'reset-template': 'Zahodit vlastní úpravy šablony?',
  };
  document.querySelectorAll('form[data-confirm]').forEach(f => f.addEventListener('submit', e => {
    const q = questions[f.dataset.confirm];
    if (q && !confirm(q)) e.preventDefault();
  }));

  // Create preset/template: the name is part of the URL
  const cp = document.getElementById('create-preset');
  if (cp) cp.addEventListener('submit', () => { cp.action = '/presets/' + encodeURIComponent(cp.elements.name.value); });
  const ct = document.getElementById('create-template');
  if (ct) ct.addEventListener('submit', e => { e.preventDefault(); location.href = '/templates/' + encodeURIComponent(ct.elements.name.value); });

  // Dashboard filter
  const filter = document.getElementById('filter');
  const stateFilter = document.getElementById('state-filter');
  if (filter) {
    const rows = [...document.querySelectorAll('#apps tbody tr')];
    const apply = () => {
      const q = filter.value.trim().toLowerCase(), st = stateFilter.value;
      let shown = 0;
      rows.forEach(r => {
        const ok = (!q || r.dataset.search.toLowerCase().includes(q)) && (!st || r.dataset.state === st);
        r.hidden = !ok; if (ok) shown++;
      });
      document.getElementById('no-match').hidden = shown > 0;
    };
    filter.addEventListener('input', apply);
    stateFilter.addEventListener('change', apply);
  }

  // New app form: show fields relevant to the preset
  const preset = document.getElementById('preset');
  if (preset) {
    const sync = () => {
      const img = preset.value === 'image';
      document.querySelectorAll('[data-for="git"]').forEach(el => el.hidden = img);
      document.querySelectorAll('[data-for="image"]').forEach(el => el.hidden = !img);
      document.getElementById('preset-hint').textContent = preset.selectedOptions[0]?.dataset.hint || '';
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
        const exit = r.headers.get('X-Exit');
        if (r.headers.get('X-Running') === 'true') { setTimeout(poll, 1000); return; }
        state.className = 'badge ' + (exit === '0' ? 'ok' : 'err');
        state.textContent = exit === '0' ? 'OK' : `chyba ${exit}`;
      } catch (e) { setTimeout(poll, 3000); }
    };
    poll();
  }
});
