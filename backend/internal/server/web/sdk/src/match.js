import { emitter } from './util.js';
import { bus, addEvent } from './bus.js';
import { identity } from './identity.js';
import { encounters } from './encounters.js';
import { isBotId } from './bot.js';
import { state } from './state.js';
import { resolvePlayerIn, emitTyped } from './events.js';

// Enriched match state. Builds the public RLT.match object plus its
// onMatch / onTick / onRoster subscriptions.

export const match = (function () {
  const ev = emitter();
  let cur = null;
  let lastFingerprint = '';
  const RECORDING_PHASES = new Set(['countdown', 'live', 'paused', 'replay']);

  function build(d) {
    const guid = d.MatchGuid || 'local';

    const players = (d.Players || []).map((p) => {
      const id = p.PrimaryId || '';
      const name = p.Name || 'Unknown';
      const enc = id ? encounters.get(id) : null;
      return {
        id,
        name,
        team: p.TeamNum,
        isMe: identity._isMe(id),
        isBot: isBotId(id),
        score: p.Score | 0,
        goals: p.Goals | 0,
        assists: p.Assists | 0,
        saves: p.Saves | 0,
        shots: p.Shots | 0,
        demos: p.Demos | 0,
        touches: p.Touches | 0,
        carTouches: p.CarTouches | 0,
        boost: typeof p.Boost === 'number' ? p.Boost : null,
        speed: typeof p.Speed === 'number' ? p.Speed : null,
        // Booleans from the spectator-only block. Absent fields → false,
        // which is what plugins want as a default.
        boosting: !!p.bBoosting,
        onGround: !!p.bOnGround,
        onWall: !!p.bOnWall,
        powersliding: !!p.bPowersliding,
        demolished: !!p.bDemolished,
        supersonic: !!p.bSupersonic,
        hasCar: !!p.bHasCar,
        // Present only on the frame this player was demolished. Resolved
        // to the enriched player-ref shape in a second pass below, once
        // the full roster is built. Stash the raw stub here for now.
        attackerRaw: p.Attacker || null,
        attacker: null,
        encounterCount: enc ? enc.count : 1,
        aliases: enc ? enc.names.filter((n) => n !== name) : [],
        firstSeen: enc ? enc.first_seen : null,
        lastSeen: enc ? enc.last_seen : null,
        platform: id ? id.split('|')[0] : '?',
        raw: p,
      };
    });

    // Resolve attackers now that the roster exists. resolvePlayerIn
    // looks up the attacker against the just-built `players` array, so
    // attacker.player / attacker.isMe / attacker.encounter are all
    // populated when the attacker is in the same match.
    for (const p of players) {
      if (p.attackerRaw) {
        p.attacker = resolvePlayerIn(players, p.attackerRaw);
      }
      delete p.attackerRaw;
    }

    const blue = players.filter((p) => p.team === 0);
    const orange = players.filter((p) => p.team === 1);
    const game = d.Game || null;
    const me = players.find((p) => p.isMe) || null;

    // Normalize Game.Teams into lowercase-keyed entries plus blue/orange
    // split shortcuts. ColorPrimary/ColorSecondary are raw hex strings
    // (no '#' prefix) — plugins prepend '#' when applying.
    const teams = Array.isArray(game?.Teams)
      ? game.Teams.filter((t) => t && typeof t.TeamNum === 'number')
          .map((t) => ({
            teamNum: t.TeamNum,
            name: t.Name || '',
            score: t.Score | 0,
            colorPrimary: t.ColorPrimary || '',
            colorSecondary: t.ColorSecondary || '',
            raw: t,
          }))
          .sort((a, b) => a.teamNum - b.teamNum)
      : [];
    const blueTeam = teams.find((t) => t.teamNum === 0) || null;
    const orangeTeam = teams.find((t) => t.teamNum === 1) || null;

    // Conditional replay-only fields. The API emits Frame and Elapsed
    // only while a replay is active (goal replay or match-history
    // replay). Group them under a single nullable object so plugins
    // can gate with `if (m.replayInfo) { … }`.
    const hasFrame = game && typeof game.Frame === 'number';
    const hasElapsed = game && typeof game.Elapsed === 'number';
    const replayInfo =
      hasFrame || hasElapsed
        ? {
            frame: hasFrame ? game.Frame : null,
            elapsed: hasElapsed ? game.Elapsed : null,
          }
        : null;

    return {
      guid,
      players,
      blue,
      orange,
      me,
      game,
      arena: game ? (game.Arena || '').replace(/_P$/, '').replace(/_/g, ' ') : '',
      clockSeconds: game ? game.TimeSeconds | 0 : null,
      overtime: !!game?.bOvertime,
      // bReplay covers both goal replays and history replays — the
      // authoritative way to detect either, instead of inferring from
      // GoalReplayStart/End edges.
      replay: !!game?.bReplay,
      hasWinner: !!game?.bHasWinner,
      winner: game?.Winner || '',
      scoreBlue: (game?.Teams?.find((t) => t.TeamNum === 0) || game?.Teams?.[0])?.Score | 0,
      scoreOrange: (game?.Teams?.find((t) => t.TeamNum === 1) || game?.Teams?.[1])?.Score | 0,
      // Normalized team metadata: full array plus blue/orange shortcuts
      // (mirroring the existing match.blue / match.orange split).
      teams,
      blueTeam,
      orangeTeam,
      replayInfo,
      // Ball state. Lowercase keys + .raw mirror the resolved-player
      // pattern used elsewhere in the SDK. TeamNum 255 is the API's
      // "ball has not been touched yet" sentinel; lastTouchTeam
      // normalizes it to null so plugins don't have to remember.
      ball: game?.Ball
        ? {
            speed: typeof game.Ball.Speed === 'number' ? game.Ball.Speed : null,
            teamNum: typeof game.Ball.TeamNum === 'number' ? game.Ball.TeamNum : null,
            lastTouchTeam:
              typeof game.Ball.TeamNum === 'number' && game.Ball.TeamNum !== 255
                ? game.Ball.TeamNum
                : null,
            raw: game.Ball,
          }
        : null,
      // Spectator camera target — only meaningful when bHasTarget.
      target: game?.bHasTarget ? game.Target || null : null,
      raw: d,
    };
  }

  // Record everyone on the current roster against this match guid.
  // Only meaningful during recording phases — caller is responsible
  // for that gate. Returns true if any player triggered a count
  // create/bump, so the caller can rebuild `cur` so encounterCount
  // reflects the new ledger.
  function recordRoster() {
    if (!cur) return false;
    let bumped = false;
    const seen = new Set();
    for (const p of cur.raw.Players || []) {
      const id = p.PrimaryId || '';
      if (!id || seen.has(id)) continue;
      seen.add(id);
      if (encounters._record(id, p.Name || 'Unknown', cur.guid)) bumped = true;
    }
    return bumped;
  }

  function buildFromRoster(guid, list) {
    const players = list.map((p) => {
      const id = p.id || '';
      const name = p.name || 'Unknown';
      const enc = id ? encounters.get(id) : null;
      return {
        id,
        name,
        team: p.team | 0,
        isMe: p.isMe === true,
        isBot: p.isBot === true,
        score: 0,
        goals: 0,
        assists: 0,
        saves: 0,
        shots: 0,
        demos: 0,
        touches: 0,
        carTouches: 0,
        boost: null,
        speed: null,
        boosting: false,
        onGround: false,
        onWall: false,
        powersliding: false,
        demolished: false,
        supersonic: false,
        hasCar: false,
        attacker: null,
        encounterCount: enc ? enc.count : 1,
        aliases: enc ? enc.names.filter((n) => n !== name) : [],
        firstSeen: enc ? enc.first_seen : null,
        lastSeen: enc ? enc.last_seen : null,
        platform: p.platform || '',
        raw: { PrimaryId: id, Name: name, TeamNum: p.team | 0 },
      };
    });
    return {
      guid,
      players,
      blue: players.filter((p) => p.team === 0),
      orange: players.filter((p) => p.team === 1),
      game: null,
      ball: null,
      target: null,
      raw: { Players: players.map((p) => p.raw) },
    };
  }

  function rosterFingerprintOf(view) {
    if (!view) return '';
    return (
      view.guid +
      '|' +
      identity.id +
      '|' +
      view.players
        .map((p) => p.id + ':' + p.team + ':' + (p.encounterCount > 1 ? 'r' : 'n'))
        .join(',')
    );
  }

  bus.on('_RosterChanged', (env) => {
    if (!env) return;
    const guid = env.matchGuid || env.match_guid || env.MatchGUID || 'local';
    const list = Array.isArray(env.players) ? env.players : [];
    cur = buildFromRoster(guid, list);

    if (RECORDING_PHASES.has(state.phase) && cur.players.length > 0) {
      if (recordRoster()) {
        cur = buildFromRoster(guid, list);
        lastFingerprint = '';
      }
    }

    ev.emit('roster', cur);
    const fp = rosterFingerprintOf(cur);
    if (fp !== lastFingerprint) {
      lastFingerprint = fp;
      ev.emit('change', cur);
    }
  });

  bus.on('UpdateState', (d) => {
    if (!d) return;
    cur = build(d);
    ev.emit('tick', cur);

    if (RECORDING_PHASES.has(state.phase) && cur.players.length > 0) {
      if (recordRoster()) {
        cur = build(cur.raw);
        lastFingerprint = '';
      }
    }

    emitTyped('UpdateState', cur);

    const fp = rosterFingerprintOf(cur);
    if (fp !== lastFingerprint) {
      lastFingerprint = fp;
      ev.emit('change', cur);
    }
  });

  function clear() {
    if (!cur) return;
    cur = null;
    lastFingerprint = '';
    ev.emit('change', null);
    ev.emit('tick', null);
  }

  bus.on('MatchDestroyed', clear);
  bus.on('_status', (s) => {
    if (s === 'disconnected') clear();
  });

  // Re-fingerprint when identity changes so isMe is up to date.
  identity.onChange(() => {
    lastFingerprint = '';
    if (cur) cur = build(cur.raw);
    ev.emit('change', cur);
  });

  // Re-stamp encounter fields when the ledger changes so plugins that
  // only subscribe to onRoster (and therefore never rebuild cur from a
  // fresh UpdateState tick) see up-to-date counts.
  encounters.onChange(() => {
    if (!cur) return;
    let changed = false;
    for (const p of cur.players) {
      const enc = p.id ? encounters.get(p.id) : null;
      const count = enc ? enc.count : 1;
      if (p.encounterCount !== count) {
        p.encounterCount = count;
        p.aliases = enc ? enc.names.filter((n) => n !== p.name) : [];
        p.firstSeen = enc ? enc.first_seen : null;
        p.lastSeen = enc ? enc.last_seen : null;
        changed = true;
      }
    }
    if (changed) {
      lastFingerprint = '';
      ev.emit('roster', cur);
      ev.emit('change', cur);
    }
  });

  // Catch-up recording: RL sends the first UpdateState (which triggers
  // _RosterChanged) before CountdownBegin, so the roster often lands
  // while state.phase is still "lobby" — outside RECORDING_PHASES.
  // When the phase later transitions into a recording phase, re-run
  // recordRoster so the encounter ledger reflects the match. The
  // encounters.onChange handler above re-stamps cur.players when the
  // ledger changes, so we don't need to rebuild cur here. Read the
  // phase from the raw _MatchState envelope because the state module
  // processes the same event and may not have updated state.phase yet.
  bus.on('_MatchState', (snap) => {
    if (!snap) return;
    const p = String(snap.phase || 'none');
    if (!RECORDING_PHASES.has(p)) return;
    if (!cur || cur.players.length === 0) return;
    recordRoster();
  });

  return {
    get current() {
      return cur;
    },
    onChange(fn) {
      addEvent('UpdateState');
      return ev.on('change', fn);
    },
    onTick(fn) {
      addEvent('UpdateState');
      return ev.on('tick', fn);
    },
    subscribe() {
      addEvent('UpdateState');
    },
    onRoster(fn) {
      return ev.on('roster', fn);
    },
  };
})();

// Expose state on the existing match object for discoverability.
// (Previously called match.lifecycle; renamed to match the server-side
// _MatchState event name.)
match.state = state;
match.onState = (fn) => state.onChange(fn);
