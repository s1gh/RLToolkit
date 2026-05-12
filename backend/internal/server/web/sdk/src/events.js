import { emitter } from './util.js';
import { bus, addEvent } from './bus.js';
import { identity } from './identity.js';
import { encounters } from './encounters.js';
import { isBotId } from './bot.js';

// Typed events layer.
//
// resolvePlayerIn is exported so match.js can stitch attacker refs
// against the freshly-built roster.

export function resolvePlayerIn(roster, ref) {
  if (!ref) return null;
  let player = null;
  if (roster?.length) {
    if (ref.PrimaryId) {
      player = roster.find((p) => p.id === ref.PrimaryId) || null;
    }
    if (!player && typeof ref.Shortcut === 'number') {
      player = roster.find((p) => p.raw?.Shortcut === ref.Shortcut) || null;
    }
    if (!player && ref.Name) {
      player = roster.find((p) => p.name === ref.Name) || null;
    }
  }
  const id = player?.id || ref.PrimaryId || '';
  const enc = id ? encounters.get(id) : null;
  return {
    name: ref.Name || player?.name || 'Unknown',
    shortcut: typeof ref.Shortcut === 'number' ? ref.Shortcut : null,
    team: typeof ref.TeamNum === 'number' ? ref.TeamNum : player ? player.team : null,
    id,
    isMe: identity._isMe(id),
    isBot: isBotId(id),
    player,
    encounter: enc,
    raw: ref,
  };
}

// Recent-events ring buffer so plugins booting mid-match can show
// context. Useful for dedup ("did I already see this GoalScored?") and
// for "show the last N events" debug panels. See RLT.events.recent.
const recentByType = new Map();
const RECENT_LIMIT = 50;
function recordRecent(ev, data) {
  let arr = recentByType.get(ev);
  if (!arr) {
    arr = [];
    recentByType.set(ev, arr);
  }
  arr.push({ at: Date.now(), data });
  if (arr.length > RECENT_LIMIT) arr.splice(0, arr.length - RECENT_LIMIT);
}

export const eventsBus = emitter();

export function emitTyped(name, payload) {
  recordRecent(name, payload);
  eventsBus.emit(name, payload);
}

bus.on('ClockUpdatedSeconds', (d) => {
  if (!d) return;
  emitTyped('ClockUpdatedSeconds', {
    matchGuid: d.MatchGuid || null,
    seconds: d.TimeSeconds != null ? d.TimeSeconds : null,
    overtime: !!d.bOvertime,
    raw: d,
  });
});

// Pass-through events: wrap raw payload for consistent typed API.
[
  'MatchCreated',
  'MatchInitialized',
  'MatchDestroyed',
  'MatchPaused',
  'MatchUnpaused',
  'CountdownBegin',
  'RoundStarted',
  'GoalReplayStart',
  'GoalReplayWillEnd',
  'GoalReplayEnd',
  'PodiumStart',
  'ReplayCreated',
  'GoalScored',
  'BallHit',
  'CrossbarHit',
  'StatfeedEvent',
  'MatchEnded',
].forEach((name) => {
  bus.on(name, (d) => {
    emitTyped(name, {
      matchGuid: d?.MatchGuid || null,
      raw: d || null,
    });
  });
});

// Bridge synthetics from raw bus → eventsBus for register()-style handlers.
[
  '_StatfeedEvent',
  '_PlayerDemolished',
  '_UnknownStatfeed',
  '_BallHit',
  '_CrossbarHit',
  '_MatchEnded',
  '_GoalScored',
  '_OwnGoal',
  '_FlipReset',
  '_HatTrick',
  '_Save',
  '_EpicSave',
  '_Shot',
  '_Assist',
  '_Center',
  '_Clear',
  '_BicycleHit',
  '_FirstTouch',
  '_FirstBlood',
  '_OvertimeStarted',
  '_GoalReplayStarted',
  '_MatchEnded',
  '_MatchState',
  '_IdentityChanged',
  // UpdateState-diff synthetics — same bridge contract as the rest:
  // raw bus delivers them, register({ events }) handlers listen on
  // eventsBus, so they need a forward here or they silently no-op.
  '_PlayerJoined',
  '_PlayerLeft',
  '_PlayerScoreChanged',
  '_BoostPickup',
  '_BoostConsumed',
  '_BallPossessionChanged',
  '_TeamScoreChanged',
  '_DemoChain',
  '_FastestShotOfMatch',
].forEach((name) => {
  bus.on(name, (payload) => emitTyped(name, payload));
});

function makeOn(name) {
  return (fn) => {
    addEvent(name);
    return eventsBus.on(name, fn);
  };
}

export const events = {
  on: (name, fn) => {
    addEvent(name);
    return eventsBus.on(name, fn);
  },
  off: (name, fn) => eventsBus.off(name, fn),
  recent(name, n) {
    const arr = recentByType.get(name) || [];
    return n ? arr.slice(-n) : arr.slice();
  },

  onUpdateState: makeOn('UpdateState'),
  onClockUpdatedSeconds: makeOn('ClockUpdatedSeconds'),
  onMatchCreated: makeOn('MatchCreated'),
  onMatchInitialized: makeOn('MatchInitialized'),
  onMatchDestroyed: makeOn('MatchDestroyed'),
  onMatchPaused: makeOn('MatchPaused'),
  onMatchUnpaused: makeOn('MatchUnpaused'),

  onCountdownBegin: makeOn('CountdownBegin'),
  onRoundStarted: makeOn('RoundStarted'),

  onGoalReplayStart: makeOn('GoalReplayStart'),
  onGoalReplayWillEnd: makeOn('GoalReplayWillEnd'),
  onGoalReplayEnd: makeOn('GoalReplayEnd'),

  onPodiumStart: makeOn('PodiumStart'),
  onReplayCreated: makeOn('ReplayCreated'),

  onEnrichedStatfeedEvent: makeOn('_StatfeedEvent'),
  onEnrichedBallHit: makeOn('_BallHit'),
  onEnrichedCrossbarHit: makeOn('_CrossbarHit'),
  onEnrichedMatchEnded: makeOn('_MatchEnded'),
  onEnrichedGoalScored: makeOn('_GoalScored'),
  onOwnGoal: makeOn('_OwnGoal'),
  onPlayerDemolished: makeOn('_PlayerDemolished'),
  onFlipReset: makeOn('_FlipReset'),
  onHatTrick: makeOn('_HatTrick'),
  onSave: makeOn('_Save'),
  onEpicSave: makeOn('_EpicSave'),
  onShot: makeOn('_Shot'),
  onAssist: makeOn('_Assist'),
  onCenter: makeOn('_Center'),
  onClear: makeOn('_Clear'),
  onBicycleHit: makeOn('_BicycleHit'),
  onPlayerJoined: makeOn('_PlayerJoined'),
  onPlayerLeft: makeOn('_PlayerLeft'),
  onPlayerScoreChanged: makeOn('_PlayerScoreChanged'),
  onBoostPickup: makeOn('_BoostPickup'),
  onBallPossessionChanged: makeOn('_BallPossessionChanged'),
  onTeamScoreChanged: makeOn('_TeamScoreChanged'),
  onFirstTouch: makeOn('_FirstTouch'),
  onFirstBlood: makeOn('_FirstBlood'),
  onOvertimeStarted: makeOn('_OvertimeStarted'),
  onDemoChain: makeOn('_DemoChain'),
  onFastestShotOfMatch: makeOn('_FastestShotOfMatch'),
  onGoalReplayStarted: makeOn('_GoalReplayStarted'),
  onMatchEnded: makeOn('_MatchEnded'),
  onUnknownStatfeed: makeOn('_UnknownStatfeed'),
};
