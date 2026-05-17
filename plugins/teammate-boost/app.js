(function (root) {
  function clamp(n, lo, hi) {
    if (typeof n !== 'number' || Number.isNaN(n)) return lo;
    if (n < lo) return lo;
    if (n > hi) return hi;
    return n;
  }

  const GAUGE_STYLES = new Set(['radial', 'bar', 'column']);
  const IMPLEMENTED_GAUGE_STYLES = new Set(['radial']);
  const COLOR_SCHEMES = new Set(['cyan', 'violet', 'teamBlue', 'teamOrange']);

  const DEFAULT_CONFIG = Object.freeze({
    gaugeStyle: 'radial',
    colorScheme: 'cyan',
    lowBoostThreshold: 20,
    showNames: true,
  });

  function coerceConfig(raw) {
    const src = raw && typeof raw === 'object' ? raw : {};

    let gaugeStyle = src.gaugeStyle;
    if (!GAUGE_STYLES.has(gaugeStyle) || !IMPLEMENTED_GAUGE_STYLES.has(gaugeStyle)) {
      gaugeStyle = DEFAULT_CONFIG.gaugeStyle;
    }

    const colorScheme = COLOR_SCHEMES.has(src.colorScheme)
      ? src.colorScheme
      : DEFAULT_CONFIG.colorScheme;

    let lowBoostThreshold;
    if (typeof src.lowBoostThreshold === 'number' && !Number.isNaN(src.lowBoostThreshold)) {
      lowBoostThreshold = Math.round(clamp(src.lowBoostThreshold, 0, 100));
    } else {
      lowBoostThreshold = DEFAULT_CONFIG.lowBoostThreshold;
    }

    const showNames = src.showNames === undefined ? DEFAULT_CONFIG.showNames : !!src.showNames;

    return { gaugeStyle, colorScheme, lowBoostThreshold, showNames };
  }

  function isLowBoost(boost, threshold) {
    if (typeof boost !== 'number' || Number.isNaN(boost)) return false;
    if (!(threshold > 0)) return false;
    return boost < threshold;
  }

  function collectTeammates(match) {
    if (!match?.me) return [];
    const meTeam = match.me.team;
    if (typeof meTeam !== 'number') return [];
    const meId = match.me.id;

    const out = [];
    const players = Array.isArray(match.players) ? match.players : [];
    for (const p of players) {
      if (!p) continue;
      if (p.id === meId) continue;
      if (p.team !== meTeam) continue;
      if (typeof p.boost !== 'number') continue;
      out.push({
        id: p.id,
        name: p.name || p.id || '',
        boost: clamp(p.boost, 0, 100),
      });
    }
    out.sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
    return out;
  }

  // Roster pass without the boost filter: same team, not me. Used by
  // the overlay tick handler together with applyBoostMemory so that a
  // teammate's card doesn't flicker out on transient frames where RL
  // omits the Boost field (respawn, phase edges).
  function collectTeammateRoster(match) {
    if (!match?.me) return [];
    const meTeam = match.me.team;
    if (typeof meTeam !== 'number') return [];
    const meId = match.me.id;

    const out = [];
    const players = Array.isArray(match.players) ? match.players : [];
    for (const p of players) {
      if (!p) continue;
      if (p.id === meId) continue;
      if (p.team !== meTeam) continue;
      out.push({
        id: p.id,
        name: p.name || p.id || '',
        boost: typeof p.boost === 'number' ? clamp(p.boost, 0, 100) : null,
      });
    }
    out.sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
    return out;
  }

  // Fill in boost from memory when the current tick is missing it.
  // Mutates memory in place with the latest numeric values. A teammate
  // is dropped only if we have no numeric value for them in the current
  // roster and have never seen one before.
  function applyBoostMemory(roster, memory) {
    const out = [];
    for (const t of roster) {
      let boost = t.boost;
      if (typeof boost !== 'number') {
        boost = memory.get(t.id);
        if (typeof boost !== 'number') continue;
      } else {
        memory.set(t.id, boost);
      }
      out.push({ id: t.id, name: t.name, boost });
    }
    // Forget memory for ids that have left the roster so we don't leak
    // stale data across matches.
    const present = new Set(roster.map((t) => t.id));
    for (const id of memory.keys()) {
      if (!present.has(id)) memory.delete(id);
    }
    return out;
  }

  function renderCard(t, config) {
    const card = document.createElement('div');
    card.className = 'tb-card';
    if (isLowBoost(t.boost, config.lowBoostThreshold)) card.classList.add('tb-low');

    const dial = document.createElement('div');
    dial.className = 'tb-dial';
    dial.style.setProperty('--p', String(t.boost));
    dial.setAttribute('data-value', String(t.boost));

    const meta = document.createElement('div');
    meta.className = 'tb-meta';
    if (config.showNames) {
      const name = document.createElement('span');
      name.className = 'tb-name';
      name.textContent = t.name;
      meta.appendChild(name);
    }
    const lbl = document.createElement('span');
    lbl.className = 'tb-lbl';
    lbl.textContent = card.classList.contains('tb-low') ? 'boost · low' : 'boost';
    meta.appendChild(lbl);

    card.appendChild(dial);
    card.appendChild(meta);
    return card;
  }

  function renderInto(host, teammates, config) {
    if (!host) return;
    if (!teammates.length) {
      host.replaceChildren();
      return;
    }
    const stack = document.createElement('div');
    stack.className = 'tb-stack';
    stack.setAttribute('data-color', config.colorScheme);
    for (const t of teammates) stack.appendChild(renderCard(t, config));
    host.replaceChildren(stack);
  }

  function bootOverlay() {
    const host = document.getElementById('root');
    let config = DEFAULT_CONFIG;
    const boostMemory = new Map();

    function paint(match) {
      const roster = collectTeammateRoster(match);
      const teammates = applyBoostMemory(roster, boostMemory);
      renderInto(host, teammates, config);
    }

    RLT.plugin.register({
      async ready() {
        config = coerceConfig(await RLT.store.get('config'));
        RLT.store.onChange('config', async () => {
          config = coerceConfig(await RLT.store.get('config'));
          paint(RLT.match.current);
        });
        paint(RLT.match.current);
      },
      onTick(m) {
        paint(m);
      },
    });
  }

  function bootSettings() {
    const host = document.getElementById('root');

    function render(config) {
      const form = document.createElement('form');
      form.className = 'tb-set';
      form.innerHTML = `
        <fieldset>
          <legend>Gauge style</legend>
          <label class="row"><input type="radio" name="gauge" value="radial"> Radial</label>
          <label class="row"><input type="radio" name="gauge" value="bar" disabled> Bar <span class="hint">(coming soon)</span></label>
          <label class="row"><input type="radio" name="gauge" value="column" disabled> Column <span class="hint">(coming soon)</span></label>
        </fieldset>
        <fieldset>
          <legend>Color scheme</legend>
          <label class="row"><input type="radio" name="color" value="cyan"><span class="swatch cyan"></span>Cyan</label>
          <label class="row"><input type="radio" name="color" value="violet"><span class="swatch violet"></span>Violet</label>
          <label class="row"><input type="radio" name="color" value="teamBlue"><span class="swatch teamBlue"></span>Team blue</label>
          <label class="row"><input type="radio" name="color" value="teamOrange"><span class="swatch teamOrange"></span>Team orange</label>
        </fieldset>
        <fieldset>
          <legend>Low-boost emphasis</legend>
          <label class="row">
            Pulse below
            <input type="number" name="low" min="0" max="100" step="5">
            <span class="hint" data-role="low-hint"></span>
          </label>
        </fieldset>
        <fieldset>
          <legend>Display</legend>
          <label class="row"><input type="checkbox" name="names"> Show teammate names</label>
        </fieldset>
      `;

      form.querySelector(`input[name="gauge"][value="${config.gaugeStyle}"]`).checked = true;
      form.querySelector(`input[name="color"][value="${config.colorScheme}"]`).checked = true;
      form.querySelector('input[name="low"]').value = String(config.lowBoostThreshold);
      form.querySelector('input[name="names"]').checked = !!config.showNames;

      function updateLowHint() {
        const v = parseInt(form.querySelector('input[name="low"]').value, 10);
        form.querySelector('[data-role="low-hint"]').textContent = v === 0 ? 'Disabled' : '';
      }
      updateLowHint();

      form.addEventListener('change', async () => {
        updateLowHint();
        const next = {
          gaugeStyle: form.querySelector('input[name="gauge"]:checked').value,
          colorScheme: form.querySelector('input[name="color"]:checked').value,
          lowBoostThreshold: parseInt(form.querySelector('input[name="low"]').value, 10),
          showNames: form.querySelector('input[name="names"]').checked,
        };
        const coerced = coerceConfig(next);
        await RLT.store.set('config', coerced);
      });

      host.replaceChildren(form);
    }

    RLT.plugin.register({
      async ready() {
        render(coerceConfig(await RLT.store.get('config')));
      },
    });
  }

  function bootDashboard() {
    const host = document.getElementById('root');
    const mock = [
      { id: 'a-kestrel', name: 'Kestrel', boost: 76 },
      { id: 'b-halvers', name: 'Halverson', boost: 4 },
    ];

    function rerender(config) {
      renderInto(host, mock, config);
    }

    RLT.plugin.register({
      async ready() {
        let config = coerceConfig(await RLT.store.get('config'));
        rerender(config);
        RLT.store.onChange('config', async () => {
          config = coerceConfig(await RLT.store.get('config'));
          rerender(config);
        });
      },
    });
  }

  const TeammateBoost = {
    clamp,
    coerceConfig,
    collectTeammates,
    collectTeammateRoster,
    applyBoostMemory,
    isLowBoost,
    _internal: { renderCard, renderInto, DEFAULT_CONFIG },
  };

  if (root) root.TeammateBoost = TeammateBoost;

  if (typeof RLT !== 'undefined') {
    if (RLT.isOverlay) bootOverlay();
    else if (RLT.isSettingsView) bootSettings();
    else if (RLT.isDashboard) bootDashboard();
  }
})(typeof window !== 'undefined' ? window : null);
