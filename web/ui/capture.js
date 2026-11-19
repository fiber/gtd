// Capture: one text field, camera, file. Enter submits and the field is empty
// again, with a brief confirmation and nothing else (D-02).
import { store } from './store.js';
import { t } from './i18n.js';
import { View, html, nothing, icon, stateChip, toast, go } from './widgets.js';

class Capture extends View {
  static properties = { files: { state: true }, split: { state: true }, done: { state: true }, busy: { state: true }, similar: { state: true }, private: { state: true } };

  constructor() {
    super();
    this.files = [];
    // Privacy mode: nothing from the past appears on this screen, for when it
    // is being shared. Kept per device, so it stays on until switched off.
    try {
      this.private = localStorage.getItem('gtd.private') === '1';
    } catch {
      this.private = false;
    }
  }

  setPrivate(on) {
    this.private = on;
    try { localStorage.setItem('gtd.private', on ? '1' : '0'); } catch { /* private browsing */ }
    clearTimeout(this._lookup);
    this.similar = null;
    if (!on) this.lookup();
    this.field.focus();
  }

  firstUpdated() { this.field.focus(); }
  get field() { return this.querySelector('textarea'); }

  onKey(e) {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { // capture and decide right away
      e.preventDefault();
      this.submit(false, true);
    } else if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      this.submit();
    }
  }

  // A paste of several lines asks once: one item or one per line?
  onPaste(e) {
    const text = e.clipboardData?.getData('text') || '';
    const lines = text.split('\n').map((l) => l.trim()).filter(Boolean);
    if (lines.length > 1 && !this.field.value.trim()) {
      e.preventDefault();
      this.field.value = text.trim();
      this.split = lines;
      this.grow();
    }
  }

  // While typing, look for what is already there: the same thought from last
  // week is better continued than captured twice. It never gets in the way:
  // Enter captures, whatever is shown.
  lookup() {
    clearTimeout(this._lookup);
    const text = this.field.value.trim();
    if (this.private || text.length < 8 || text.includes('\n')) {
      this.similar = null;
      return;
    }
    this._lookup = setTimeout(async () => {
      try {
        const res = await store.search(text);
        if (this.private || this.field.value.trim() !== text) return; // switched off or typed on meanwhile
        this.similar = res.hits.filter((h) => h.state !== 'trashed').slice(0, 4);
      } catch { /* suggestions are optional */ }
    }, 350);
  }

  // Add what was typed to an existing item's thread instead of capturing it.
  async addTo(hit) {
    const text = this.field.value.trim();
    if (!text) return;
    await this.run(async () => {
      await store.comment(hit, text);
      this.field.value = '';
      this.similar = null;
      this.grow();
      toast(t('capture.added', { title: hit.title }));
    });
    this.field.focus();
  }

  grow() {
    const f = this.field;
    f.style.height = 'auto';
    f.style.height = f.scrollHeight + 'px';
  }

  addFiles(e) {
    this.files = [...this.files, ...e.target.files];
    e.target.value = '';
  }

  // submit captures what is in the box. With clarify it is captured as one
  // item and Clarify opens on it at once, ahead of the rest of the inbox.
  async submit(asLines, clarify) {
    const text = this.field.value.trim();
    if ((!text && !this.files.length) || this.busy) return;
    if (this.split && asLines === undefined) return; // answer the question first
    this.busy = true;
    let made = null;
    await this.run(async () => {
      if (asLines) {
        for (const line of this.split) await store.capture(line);
      } else {
        made = await store.capture(text, this.files);
      }
      this.field.value = '';
      this.files = [];
      this.split = null;
      this.similar = null;
      clearTimeout(this._lookup);
      this.grow();
      this.done = true;
      clearTimeout(this._done);
      this._done = setTimeout(() => { this.done = false; }, 1500);
    });
    this.busy = false;
    if (clarify && made?.id) {
      go('/clarify?item=' + encodeURIComponent(made.id));
      return;
    }
    this.field.focus();
  }

  render() {
    return html`
      <section class="capture">
        <textarea rows="1" placeholder=${t('capture.placeholder')} aria-label=${t('nav.capture')}
          @keydown=${(e) => this.onKey(e)} @paste=${(e) => this.onPaste(e)} @input=${() => { this.grow(); this.lookup(); if (this.split) this.split = null; }}></textarea>
        ${this.split ? html`
          <div class="buttons">
            <span>${t('capture.split_question')}</span>
            <button class="primary" @click=${() => this.submit(false)}>${t('capture.one_item')}</button>
            <button @click=${() => this.submit(true)}>${t('capture.n_items', { n: this.split.length })}</button>
          </div>` : nothing}
        <div class="capture-bar">
          <label class="iconbtn" title=${t('capture.camera')}>${icon.camera}
            <input type="file" accept="image/*" capture hidden @change=${(e) => this.addFiles(e)}></label>
          <label class="iconbtn" title=${t('capture.file')}>${icon.clip}
            <input type="file" multiple hidden @change=${(e) => this.addFiles(e)}></label>
          ${this.files.map((f, i) => html`
            <span class="chip">${f.name}
              <button class="x" aria-label=${t('action.remove')} @click=${() => { this.files = this.files.filter((_, j) => j !== i); }}>×</button></span>`)}
          <label class="check private ${this.private ? 'on' : ''}" title=${t('capture.private_hint')}>
            <input type="checkbox" .checked=${this.private} @change=${(e) => this.setPrivate(e.target.checked)}> ${t('capture.private')}</label>
          <span class="hint">${t('capture.hint')}</span>
          <span class="spacer"></span>
          <span class="captured ${this.done ? 'show' : ''}" role="status">${this.done ? t('capture.done') : ''}</span>
          <button ?disabled=${this.busy} title=${t('capture.clarify_hint')} @click=${() => this.submit(false, true)}>${t('capture.clarify')}</button>
          <button class="primary" ?disabled=${this.busy} @click=${() => this.submit()}>${t('capture.submit')}</button>
        </div>
      </section>
      ${this.similar?.length ? html`
        <section class="capture-similar">
          <label>${t('capture.similar')}</label>
          ${this.similar.map((h) => html`
            <div class="row">
              <span class="mark" data-kind=${h.kind} data-state=${h.state}></span>
              <a class="row-main" href=${'i/' + h.id}><span class="title">${h.title}</span>
                <span class="meta">${stateChip(h.state)}</span></a>
              <span class="row-actions"><button @click=${() => this.addTo(h)}>${t('capture.add_to')}</button></span>
            </div>`)}
        </section>` : nothing}`;
  }
}
customElements.define('gtd-capture', Capture);
