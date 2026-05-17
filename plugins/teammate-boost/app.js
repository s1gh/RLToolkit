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

  const TeammateBoost = {
    clamp,
    coerceConfig,
    collectTeammates,
    isLowBoost,
  };

  if (root) root.TeammateBoost = TeammateBoost;
})(typeof window !== 'undefined' ? window : null);
