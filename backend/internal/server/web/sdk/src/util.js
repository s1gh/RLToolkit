// Tiny pub/sub. Used everywhere internal events fan out (bus, identity,
// encounters, match, state, focus, status-stable, events, …).
export function emitter() {
  const subs = new Map();
  return {
    on(ev, fn) {
      if (!subs.has(ev)) subs.set(ev, new Set());
      subs.get(ev).add(fn);
      return () => this.off(ev, fn);
    },
    off(ev, fn) {
      const set = subs.get(ev);
      if (set) set.delete(fn);
    },
    emit(ev, ...args) {
      const set = subs.get(ev);
      if (set)
        for (const fn of set) {
          try {
            fn(...args);
          } catch (e) {
            console.error('[RLT]', ev, e);
          }
        }
      const all = subs.get('*');
      if (all)
        for (const fn of all) {
          try {
            fn(ev, ...args);
          } catch (e) {
            console.error('[RLT] *', e);
          }
        }
    },
  };
}

// Misc helpers exposed via RLT.util.
export const util = {
  rafBatcher(fn) {
    let scheduled = false;
    return function scheduleRender() {
      if (scheduled) return;
      scheduled = true;
      requestAnimationFrame(() => {
        scheduled = false;
        fn();
      });
    };
  },
};
