// Identity — a one-line affordance ("you: <name> ✎") that expands into an
// edit panel. The match view's "this is me" buttons are the primary way to
// claim; this is the manual fallback.
window.DV = window.DV || {};

DV.identity = (function () {
  const $ = DV.dom.$;

  function render() {
    const val = $('id-val'); const hint = $('id-hint');
    if (!val) return;

    if (!RLT.me.id) {
      val.textContent = 'unclaimed';
      val.classList.add('empty');
      if (hint) hint.hidden = false;
      return;
    }
    const enc = RLT.encounters.get(RLT.me.id);
    const name = enc ? enc.names[enc.names.length - 1] : 'Player';
    val.textContent = name;
    val.classList.remove('empty');
    if (hint) hint.hidden = true;
  }

  function bind() {
    const panel = $('id-panel');
    const idIn = $('id-input');
    const open = () => { if (idIn) idIn.value = RLT.me.id; panel.hidden = false; idIn?.focus(); };
    const close = () => { panel.hidden = true; };

    $('id-edit')?.addEventListener('click', open);
    $('id-cancel')?.addEventListener('click', close);

    $('id-set')?.addEventListener('click', async () => {
      await RLT.me.set(idIn.value);
      RLT.ui.toast(RLT.me.id ? 'Identity claimed' : 'Identity cleared');
      close();
    });
    $('id-clear')?.addEventListener('click', async () => {
      await RLT.me.clear();
      RLT.ui.toast('Identity cleared');
      close();
    });

    // Submit-on-Enter inside the edit panel.
    idIn?.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') $('id-set').click();
      if (e.key === 'Escape') close();
    });

    // "this is me" buttons on match rows — close the edit panel if open.
    document.addEventListener('click', async (e) => {
      const btn = e.target.closest('[data-claim-id]');
      if (!btn) return;
      e.preventDefault();
      await RLT.me.set(btn.getAttribute('data-claim-id'));
      RLT.ui.toast('Identity claimed');
      close();
    });
  }

  return { render, bind };
})();
