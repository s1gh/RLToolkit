// Identity strip — collapsed by default ("you: <name> ✎"); expands inline
// to an editor on pencil click. When unclaimed, defaults to expanded so
// the user notices.
window.DV = window.DV || {};

DV.identity = (function () {
  const $ = DV.dom.$;

  function setMode(mode) {
    const strip = $('id-strip');
    const panel = $('id-panel');
    if (!strip) return;
    strip.dataset.mode = mode;
    if (panel) panel.hidden = mode !== 'expanded';
  }

  function render() {
    const val = $('id-val');
    const hint = $('id-hint');
    if (!val) return;

    if (!RLT.me.id) {
      val.textContent = 'unclaimed';
      val.classList.add('empty');
      if (hint) hint.hidden = false;
      // Auto-expand when unclaimed so the user notices the editor.
      setMode('expanded');
      return;
    }
    const enc = RLT.encounters.get(RLT.me.id);
    const name = enc ? enc.names[enc.names.length - 1] : 'Player';
    val.textContent = name;
    val.classList.remove('empty');
    if (hint) hint.hidden = true;
  }

  function bind() {
    const idIn = $('id-input');
    const open = () => {
      if (idIn) idIn.value = RLT.me.id;
      setMode('expanded');
      idIn?.focus();
    };
    const close = () => setMode('collapsed');

    $('id-edit')?.addEventListener('click', open);
    $('id-cancel')?.addEventListener('click', close);

    $('id-set')?.addEventListener('click', async () => {
      await RLT.me.set(idIn.value);
      RLT.ui.toast(RLT.me.id ? 'Identity claimed' : 'Identity cleared');
      close();
    });
    $('id-clear')?.addEventListener('click', async () => {
      await RLT.me.clear();
      if (idIn) idIn.value = '';
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
