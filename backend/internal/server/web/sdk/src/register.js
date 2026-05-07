import { pluginName } from './env.js';
import { getManifest, isManifestLoaded, manifestPromise } from './manifest.js';
import { makeNamespacedStore } from './store.js';
import { bus, addEvent } from './bus.js';
import { eventsBus } from './events.js';
import { match } from './match.js';
import { identity } from './identity.js';
import { encounters } from './encounters.js';
import { state } from './state.js';
import { focus } from './focus.js';

// Plugin registration API. Wraps a {events, onMatch, onTick, ready,
// init, dispose, …} spec into a stable handle the host app can
// dispose / list / introspect.

export const plugin = (function () {
  const registry = [];

  function shouldFire(spec) {
    if (!spec.whilePhase) return true;
    if (spec.whilePhase === '*') return true;
    const allow = Array.isArray(spec.whilePhase) ? spec.whilePhase : [spec.whilePhase];
    const cur = state.phase;
    if (allow.indexOf(cur) !== -1) return true;
    // 'idle' is a back-compat alias for 'none'.
    if (cur === 'none' && allow.indexOf('idle') !== -1) return true;
    return false;
  }

  function wrap(spec, fn, opts) {
    const phaseGated = !opts || opts.phaseGated !== false;
    return (...args) => {
      if (phaseGated && !shouldFire(spec)) return;
      try {
        return fn(...args);
      } catch (e) {
        console.error('[RLT] plugin "' + spec.name + '" handler threw:', e);
      }
    };
  }
  const gate = (spec, fn) => wrap(spec, fn);
  const isolate = (spec, fn) => wrap(spec, fn, { phaseGated: false });

  function register(spec) {
    spec = spec || {};
    const manifest = getManifest();
    const name = spec.name || manifest?.name || pluginName;
    const unsubs = [];
    let disposed = false;
    const pluginStore = makeNamespacedStore(name, { allowWrites: !!spec.allowWrites });

    if (spec.events) {
      for (const evName of Object.keys(spec.events)) {
        const handler = spec.events[evName];
        if (typeof handler !== 'function') continue;
        if (evName !== '*') addEvent(evName);
        const sub =
          evName === '*'
            ? bus.on('*', gate(spec, handler))
            : eventsBus.on(evName, gate(spec, handler));
        unsubs.push(sub);
      }
    }

    // onState/onMatchActive/onFocusChange bypass whilePhase gating.
    // onLifecycle is an alias for onState — kept so plugins from the
    // pre-rename era keep working; new plugins should use onState.
    if (typeof spec.onMatch === 'function') unsubs.push(match.onChange(gate(spec, spec.onMatch)));
    if (typeof spec.onTick === 'function') unsubs.push(match.onTick(gate(spec, spec.onTick)));
    if (typeof spec.onRoster === 'function')
      unsubs.push(match.onRoster(gate(spec, spec.onRoster)));
    if (typeof spec.onIdentity === 'function')
      unsubs.push(identity.onChange(gate(spec, spec.onIdentity)));
    if (typeof spec.onEncounters === 'function')
      unsubs.push(encounters.onChange(gate(spec, spec.onEncounters)));
    const onStateFn = spec.onState || spec.onLifecycle;
    if (typeof onStateFn === 'function')
      unsubs.push(state.onChange(isolate(spec, onStateFn)));
    if (typeof spec.onMatchActive === 'function')
      unsubs.push(state.onMatchActive(isolate(spec, spec.onMatchActive)));
    if (typeof spec.onFocusChange === 'function')
      unsubs.push(focus.onChange(isolate(spec, spec.onFocusChange)));

    const handle = {
      name,
      version: spec.version || manifest?.version || null,
      author: spec.author || manifest?.author || null,
      title: spec.title || manifest?.title || null,
      manifest,
      get disposed() {
        return disposed;
      },
      store: pluginStore,
      events: Object.keys(spec.events || {}),
      spec,
      dispose() {
        if (disposed) return;
        disposed = true;
        for (const u of unsubs) {
          try {
            u();
          } catch {}
        }
        unsubs.length = 0;
        if (typeof spec.dispose === 'function') {
          try {
            spec.dispose();
          } catch (e) {
            console.error('[RLT] plugin "' + name + '" dispose threw:', e);
          }
        }
        const i = registry.indexOf(handle);
        if (i >= 0) registry.splice(i, 1);
      },
    };
    registry.push(handle);

    if (!isManifestLoaded()) {
      manifestPromise.then((m) => {
        if (!m || disposed) return;
        handle.manifest = m;
        if (!spec.name) handle.name = m.name || handle.name;
        if (!spec.version) handle.version = m.version || handle.version;
        if (!spec.author) handle.author = m.author || handle.author;
        if (!spec.title) handle.title = m.title || handle.title;
      });
    }

    if (typeof spec.init === 'function') {
      try {
        spec.init(handle);
      } catch (e) {
        console.error('[RLT] plugin "' + name + '" init threw:', e);
      }
    }
    let readyFired = false;
    const fireReady = () => {
      if (readyFired || disposed) return;
      if (!(identity.isReady() && encounters.isReady())) return;
      readyFired = true;
      if (typeof spec.ready === 'function') {
        try {
          spec.ready(handle);
        } catch (e) {
          console.error('[RLT] plugin "' + name + '" ready threw:', e);
        }
      }
    };
    if (identity.isReady() && encounters.isReady()) {
      // Defer to microtask to avoid TDZ on `const handle = register(…)`.
      Promise.resolve().then(fireReady);
    } else {
      unsubs.push(identity.onChange(fireReady));
      unsubs.push(encounters.onChange(fireReady));
    }

    const logRegistered = () =>
      console.debug('[RLT] plugin registered:', handle.name, handle.version || '(no version)');
    if (isManifestLoaded() || handle.version) {
      logRegistered();
    } else {
      manifestPromise.then(logRegistered);
    }
    return handle;
  }

  return {
    register,
    list() {
      return registry.map((h) => ({
        name: h.name,
        version: h.version,
        author: h.author,
        events: h.events.slice(),
        disposed: h.disposed,
      }));
    },
    get(name) {
      return registry.find((h) => h.name === name) || null;
    },
  };
})();
