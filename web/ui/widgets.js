// Shared pieces of the UI: the base class, navigation, item rows, chips, the
// date field and the toast. Presentation only.
import { LitElement, html, nothing } from 'lit';
import { t, label, fmtDate } from './i18n.js';
import { store } from './store.js';

export { html, nothing };

// View is the base of every screen: light DOM (one global stylesheet), and a
// reload() that runs on entry and whenever data changed.
export class View extends LitElement {
  createRenderRoot() { return this; }
  connectedCallback() {
    super.connectedCallback();
    this._changed = () => this.reload?.();
    window.addEventListener('gtd-changed', this._changed);
    this.reload?.();
  }
  disconnectedCallback() {
    super.disconnectedCallback();
    window.removeEventListener('gtd-changed', this._changed);
  }
  // run performs a user action and shows a failure as a toast.
  async run(action) {
    try {
      return await action();
    } catch (e) {
      toast(t(e.message), true);
      return undefined;
    }
  }
}

export function go(path) {
  history.pushState({}, '', path);
  window.dispatchEvent(new PopStateEvent('popstate'));
}

export function toast(message, isError) {
  window.dispatchEvent(new CustomEvent('gtd-toast', { detail: { message, isError } }));
}

export const stateChip = (state) => html`<span class="chip state s-${state}">${t('state.' + state)}</span>`;

export function contextLabel(key) {
  if (!key) return '';
  if (key.startsWith('agenda:')) return `${t('context.agenda')} · ${key.slice(7)}`;
  const known = ['errands', 'calls', 'computer', 'home', 'office', 'anywhere'];
  return known.includes(key) ? t('context.' + key) : key;
}

// meta renders the chips under an item title.
export function meta(b, opts = {}) {
  return html`
    ${opts.state ? stateChip(b.state) : nothing}
    ${b.project_title && !opts.noProject ? html`<span class="chip project">${b.project_title}</span>` : nothing}
    ${b.area_title && opts.area ? html`<span class="chip">${b.area_title}</span>` : nothing}
    ${b.context ? html`<span class="chip context">${contextLabel(b.context)}</span>` : nothing}
    ${b.owner && b.owner !== store.me.user.id ? html`<span class="chip owner">${b.owner}</span>` : nothing}
    ${b.waiting_on ? html`<span class="chip waiting">${t('item.waiting_on', { name: b.waiting_on })}</span>` : nothing}
    ${b.due ? html`<span class="chip due">${t('item.due', { date: fmtDate(b.due) })}</span>` : nothing}
    ${b.target ? html`<span class="chip ${b.slipping ? 'slipping' : 'target'}">${t('item.target', { date: fmtDate(b.target) })}</span>` : nothing}
    ${b.deferred ? html`<span class="chip deferred">${t('item.deferred', { date: fmtDate(b.defer_until) })}</span>` : nothing}
    ${b.unblocked ? html`<span class="chip unblocked">${t('item.unblocked')}</span>` : nothing}
    ${(b.waits_for || []).map((w) => html`<span class="chip waits">${t('item.waits_for', { title: w.title })}${w.owner ? ` (${w.owner})` : ''}</span>`)}
    ${b.time ? html`<span class="chip">${t('time.' + b.time)}</span>` : nothing}
    ${b.energy ? html`<span class="chip">${t('energy.' + b.energy)}</span>` : nothing}
    ${(b.badges || []).map((x) => html`<span class="chip badge">${x.split(':').slice(1).join(':')}</span>`)}
    ${b.attachments ? html`<span class="chip">📎 ${b.attachments}</span>` : nothing}
    ${b.rollup ? html`
      <span class="chip">${b.rollup.done}/${b.rollup.total}</span>
      ${b.state === 'active' && !b.rollup.alive ? html`<span class="chip stuck">${t('project.stuck')}</span>` : nothing}
      ${b.rollup.slipping ? html`<span class="chip slipping">${t('project.slipping')}</span>` : nothing}` : nothing}`;
}

// row is an item in a list. It links to the item's page.
export function row(b, opts = {}) {
  return html`
    <div class="row kind-${b.kind}" data-id=${b.id} tabindex="0">
      <a class="row-main" href=${opts.href || '/i/' + b.id}>
        <span class="title">${b.title}</span>
        <span class="meta">${meta(b, opts)}</span>
      </a>
      ${opts.actions ? html`<span class="row-actions">${opts.actions}</span>` : nothing}
    </div>`;
}

// chips renders a single-choice (or multi-choice) chip group.
export function chips(options, selected, onPick) {
  const sel = [].concat(selected || []);
  return html`<div class="chips">${options.map((o) => html`
    <button type="button" class="chip pick ${sel.includes(o.value) ? 'on' : ''}" data-value=${o.value} @click=${() => onPick(o.value)}>${label(o)}</button>`)}</div>`;
}

// dateField is a date input with the server's presets (tomorrow, next week…).
export function dateField(value, onChange, presets = store.me.presets) {
  return html`
    <div class="datefield">
      <input type="date" .value=${value || ''} @change=${(e) => onChange(e.target.value)}>
      ${chips(presets, value, (v) => onChange(v === value ? '' : v))}
      ${value ? html`<button type="button" class="link" @click=${() => onChange('')}>${t('action.clear')}</button>` : nothing}
    </div>`;
}

// select renders a <select> over [{id|value, title|text|label}] with an empty entry.
export function select(options, value, onChange, none) {
  return html`
    <select @change=${(e) => onChange(e.target.value)}>
      ${none === undefined ? nothing : html`<option value="" ?selected=${!value}>${none}</option>`}
      ${options.map((o) => {
        const v = o.id ?? o.value;
        return html`<option value=${v} ?selected=${v === value}>${o.title ?? label(o)}</option>`;
      })}
    </select>`;
}
