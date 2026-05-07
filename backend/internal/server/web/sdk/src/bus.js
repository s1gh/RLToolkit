import { emitter } from './util.js';
import { hostedBus } from './env.js';
import { stampClientSideFields } from './stamps.js';

// SSE bridge + auto-subscription filter.
//
// Mutable state (status, es, subscribedEvents) is module-private.
// Consumers mutate/read via the exported helpers. The pagehide handler
// is wired in index.js so it can also tear down widget watchers in the
// same callback.

export const bus = emitter();

// Hosted-bus mode: parent iframe fans SSE events via postMessage.
export function postToHost(msg) {
  if (!hostedBus) return;
  try {
    window.parent.postMessage(msg, '*');
  } catch (_) {
    /* noop: cross-origin / detached parent */
  }
}

const requiredEvents = new Set([
  'MatchCreated',
  'MatchInitialized',
  'CountdownBegin',
  'RoundStarted',
  'MatchPaused',
  'MatchUnpaused',
  'GoalReplayStart',
  'GoalReplayWillEnd',
  'GoalReplayEnd',
  'MatchEnded',
  'PodiumStart',
  'MatchDestroyed',
  'ReplayCreated',
]);
export const subscribedEvents = new Set(requiredEvents);

let status = 'disconnected';
let es = null;

export function getStatus() {
  return status;
}
export function getEventSource() {
  return es;
}

export function addEvent(name) {
  if (!name || subscribedEvents.has(name)) return;
  subscribedEvents.add(name);
  if (hostedBus) {
    postToHost({ __rlt_bus_addEvent__: true, name });
    return;
  }
  if (es) {
    try {
      es.close();
    } catch (_) {
      /* noop */
    }
    es = null;
    connect();
  }
}

function buildEventsURL() {
  const events = Array.from(subscribedEvents).join(',');
  return '/events?events=' + encodeURIComponent(events);
}

export function dispatchEnvelope(msg) {
  if (!msg) return;
  const event = msg.Event ?? msg.event;
  if (event === '_ConnectionStatus') {
    // _ConnectionStatus is a special framing signal — Status sits at
    // the top level of the envelope (no Data nesting).
    status = msg.Status ?? msg.status;
    bus.emit('_status', status);
    return;
  }
  // Synthetic _-prefixed events ship in the typed envelope shape:
  //   {Event: "_Foo", Data: {...}}
  // Stamp client-side fields onto the inner payload (isMe fallback,
  // encounter ledger), then emit the inner data as the event arg.
  if (typeof event === 'string' && event.length > 0 && event[0] === '_') {
    const data = msg.Data ?? msg.data ?? {};
    stampClientSideFields(data);
    bus.emit(event, data);
    return;
  }
  // Raw RL events: Data is a JSON-encoded string we parse here.
  if (hostedBus && event && !subscribedEvents.has(event)) return;
  let data = msg.Data ?? msg.data;
  if (typeof data === 'string') {
    try {
      data = JSON.parse(data);
    } catch {
      data = null;
    }
  }
  bus.emit(event, data, msg);
}

export function connect() {
  if (hostedBus) return;
  if (es !== null && es.readyState !== 2) return;
  es = new EventSource(buildEventsURL());
  es.onmessage = (e) => {
    clearReconnectWatchdog();
    let msg;
    try {
      msg = JSON.parse(e.data);
    } catch {
      return;
    }
    dispatchEnvelope(msg);
  };
  es.onerror = () => {
    if (es !== null && es.readyState === 2) {
      status = 'disconnected';
      bus.emit('_status', status);
      return;
    }
    armReconnectWatchdog();
  };
}

// Force-close the open EventSource. Called from RLT._reconnect in
// index.js; encapsulates the mutable `es` so callers don't need to.
export function closeEventSource() {
  if (es) {
    try {
      es.close();
    } catch {
      /* noop */
    }
    es = null;
  }
}

// 2× sseHeartbeat — only declare dead after sustained silence.
const RECONNECT_WATCHDOG_MS = 30000;
let reconnectWatchdog = null;
function armReconnectWatchdog() {
  if (reconnectWatchdog) return;
  reconnectWatchdog = setTimeout(() => {
    reconnectWatchdog = null;
    if (es !== null && es.readyState !== 1 && status !== 'disconnected') {
      status = 'disconnected';
      bus.emit('_status', status);
    }
  }, RECONNECT_WATCHDOG_MS);
}
export function clearReconnectWatchdog() {
  if (!reconnectWatchdog) return;
  clearTimeout(reconnectWatchdog);
  reconnectWatchdog = null;
}
