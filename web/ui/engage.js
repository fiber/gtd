// Engage: what can I do now? (SPEC §5.3) The questions are remembered per
// device. The server filters and orders; this file shows at most 7.
import { store } from './store.js';
import { t } from './i18n.js';
import { View, html, nothing, row, chips, toast } from './widgets.js';

const KEY = 'gtd.engage';
const TIMES = ['5m', '15m', '30m', '1h', '2h+'].map((v) => ({ value: v, label: 'time.' + v }));
const ENERGIES = ['low', 'med', 'high'].map((v) => ({ value: v, label: 'energy.' + v }));

class Engage extends View {
  static properties = {
    tab: { state: true }, answers: { state: true }, data: { state: true }, all: { state: true },
    hidden: { state: true }, menu: { state: true }, focus: { state: true },
  };

  constructor() {
    super();
    this.tab = '';
    this.hidden = [];
    try {
      this.answers = JSON.parse(localStorage.getItem(KEY)) || {};
    } catch {
      this.answers = {};
    }
    this.answers = { context: [], time: '', energy: '', ...this.answers };
  }

  async reload() {
    const a = this.answers;
    this.data = await this.run(() => store.engage(this.tab ? { tab: this.tab } : { context: a.context, time: a.time, energy: a.energy }));
  }

  answer(key, value) {
    const a = { ...this.answers };
    if (key === 'context') {
      a.context = a.context.includes(value) ? a.context.filter((c) => c !== value) : [...a.context, value];
    } else {
      a[key] = a[key] === value ? '' : value; // tap again to skip the question
    }
    this.answers = a;
    localStorage.setItem(KEY, JSON.stringify(a));
    this.reload();
  }

  setTab(tab) {
    this.tab = tab;
    this.all = false;
    this.reload();
  }

  async done(b) {
    await this.run(async () => {
      await store.transition(b, 'done');
      toast(t('engage.done_toast'));
    });
  }

  async defer(b, date) {
    this.menu = null;
    await this.run(() => store.patch(b, { defer_until: date }));
  }

  actions(b) {
    return html`
      <button class="primary" data-done @click=${() => this.done(b)}>${t('action.done')}</button>
      <button @click=${() => { this.menu = this.menu === b.id ? null : b.id; }}>${t('action.defer')}</button>
      <button @click=${() => { this.hidden = [...this.hidden, b.id]; }}>${t('action.not_now')}</button>
      <button @click=${() => { this.focus = b; }}>${t('action.focus')}</button>
      ${this.menu === b.id ? html`<div class="menu">${chips(store.me.presets, '', (d) => this.defer(b, d))}</div>` : nothing}`;
  }

  render() {
    if (this.focus) {
      return html`<gtd-focus .brief=${this.focus} @exit=${() => { this.focus = null; this.reload(); }}></gtd-focus>`;
    }
    const d = this.data;
    const tabs = [['', 'engage.tab.next'], ['waiting', 'engage.tab.waiting'], ['dates', 'engage.tab.dates']];
    const items = (d?.items || []).filter((b) => !this.hidden.includes(b.id));
    const shown = this.all || this.tab ? items : items.slice(0, 7);
    return html`
      <header class="bar"><h1>${t('nav.engage')}</h1></header>
      <div class="tabs">${tabs.map(([k, label]) => html`
        <button class=${this.tab === k ? 'on' : ''} @click=${() => this.setTab(k)}>${t(label)}</button>`)}</div>
      ${this.tab ? nothing : html`
        <div class="questions">
          <div><label>${t('engage.where')}</label>${chips(d?.contexts || [], this.answers.context, (v) => this.answer('context', v))}</div>
          <div><label>${t('engage.time')}</label>${chips(TIMES, this.answers.time, (v) => this.answer('time', v))}</div>
          <div><label>${t('engage.energy')}</label>${chips(ENERGIES, this.answers.energy, (v) => this.answer('energy', v))}</div>
        </div>`}
      ${!d ? nothing : shown.length ? shown.map((b) => row(b, { state: !!this.tab, actions: this.tab ? null : this.actions(b) }))
        : html`<p class="empty">${t(this.tab ? 'engage.empty_tab' : 'engage.empty')}</p>`}
      ${shown.length < items.length ? html`
        <button class="show-more" @click=${() => { this.all = true; }}>${t('engage.more', { n: items.length - shown.length })}</button>` : nothing}`;
  }
}
customElements.define('gtd-engage', Engage);

// Focus mode: one action on screen, its notes and attachments, a timer, and a
// single capture field whose captures go to the inbox silently (D-02).
class Focus extends View {
  static properties = { brief: {}, item: { state: true }, seconds: { state: true }, captured: { state: true } };

  connectedCallback() {
    super.connectedCallback();
    this.seconds = 0;
    this._timer = setInterval(() => { this.seconds += 1; }, 1000);
    this._esc = (e) => { if (e.key === 'Escape') this.exit(); };
    document.addEventListener('keydown', this._esc);
  }
  disconnectedCallback() {
    super.disconnectedCallback();
    clearInterval(this._timer);
    document.removeEventListener('keydown', this._esc);
  }

  async reload() {
    this.item = await this.run(() => store.item(this.brief.id));
  }

  exit() { this.dispatchEvent(new CustomEvent('exit')); }

  async done() {
    await this.run(() => store.transition(this.item, 'done'));
    this.exit();
  }

  async capture(e) {
    if (e.key !== 'Enter' || !e.target.value.trim()) return;
    const text = e.target.value;
    e.target.value = '';
    await this.run(() => store.capture(text));
    this.captured = (this.captured || 0) + 1;
  }

  render() {
    const it = this.item;
    if (!it) return nothing;
    const clock = `${Math.floor(this.seconds / 60)}:${String(this.seconds % 60).padStart(2, '0')}`;
    return html`
      <section class="focus">
        <p class="timer">${clock}</p>
        <h1>${it.title}</h1>
        ${it.project_title ? html`<p class="subject">${it.project_title}</p>` : nothing}
        <div class="body" .innerHTML=${it.body_html}></div>
        <div class="atts">${(it.attachments || []).map((a) => (a.mime || '').startsWith('image/')
          ? html`<img class="att" src=${'api/v1/att/' + a.sha256} alt=${a.name}>`
          : html`<a class="chip file" href=${'api/v1/att/' + a.sha256} download=${a.name}>${a.name}</a>`)}</div>
        <div class="buttons">
          <button class="primary" @click=${() => this.done()}>${t('action.done')}</button>
          <button @click=${() => this.exit()}>${t('action.exit')}</button>
        </div>
        <input class="focus-capture" type="text" placeholder=${t('focus.capture')} @keydown=${(e) => this.capture(e)}>
        ${this.captured ? html`<small>${t('focus.captured', { n: this.captured })}</small>` : nothing}
      </section>`;
  }
}
customElements.define('gtd-focus', Focus);
