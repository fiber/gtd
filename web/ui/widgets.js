// Shared pieces of the UI: the base class, navigation, item rows, chips, the
// date field and the toast. Presentation only.
import { LitElement, html, svg, nothing } from 'lit';
import { t, label, fmtDate } from './i18n.js';
import { store, BASE } from './store.js';

export { html, nothing };

// Icons: inline SVG, drawn with the current text colour.
const s1 = (strings) => html`<svg class="icon" viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor"
  stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${svg(strings)}</svg>`;
export const icon = {
  capture: s1`<path d="M12 5v14M5 12h14"/>`,
  inbox: s1`<path d="M4 13l2.6-7h10.8L20 13v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1z"/><path d="M4 13h4.5a3.5 3.5 0 0 0 7 0H20"/>`,
  engage: s1`<circle cx="12" cy="12" r="9"/><path d="M8.3 12.4l2.6 2.6 4.8-5.2"/>`,
  projects: s1`<path d="M12 4l8 4-8 4-8-4z"/><path d="M4 12l8 4 8-4"/><path d="M4 16l8 4 8-4"/>`,
  board: s1`<rect x="4" y="5" width="4.4" height="14" rx="1.2"/><rect x="9.8" y="5" width="4.4" height="9" rx="1.2"/><rect x="15.6" y="5" width="4.4" height="11.5" rx="1.2"/>`,
  search: s1`<circle cx="11" cy="11" r="6.2"/><path d="M20 20l-4.4-4.4"/>`,
  areas: s1`<circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="4.6"/><circle cx="12" cy="12" r=".6"/>`,
  settings: s1`<path d="M5 8h8M18.5 8h.5M5 16h.5M11 16h8"/><circle cx="15.8" cy="8" r="2.4"/><circle cx="8.2" cy="16" r="2.4"/>`,
  more: s1`<circle cx="6" cy="12" r="1.1"/><circle cx="12" cy="12" r="1.1"/><circle cx="18" cy="12" r="1.1"/>`,
  notes: s1`<path d="M7 3.5h7.5L19 8v12.5H7z"/><path d="M14 3.5V8h5M10 12.5h6M10 16h6"/>`,
  review: s1`<path d="M19.5 12a7.5 7.5 0 1 1-2.2-5.3"/><path d="M19.8 4.2v3.6h-3.6"/><path d="M9 12.2l2.2 2.2 3.8-4"/>`,
  help: s1`<circle cx="12" cy="12" r="9"/><path d="M9.6 9.4a2.5 2.5 0 1 1 3.6 2.3c-.8.4-1.2 1-1.2 1.9"/><circle cx="12" cy="16.8" r=".6"/>`,
  sun: s1`<circle cx="12" cy="12" r="4"/><path d="M12 3v2M12 19v2M3 12h2M19 12h2M5.6 5.6l1.4 1.4M17 17l1.4 1.4M5.6 18.4L7 17M17 7l1.4-1.4"/>`,
  moon: s1`<path d="M20 14.4A8 8 0 0 1 9.6 4 8 8 0 1 0 20 14.4z"/>`,
  expand: s1`<path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5"/>`,
  camera: s1`<rect x="3" y="7" width="18" height="13" rx="3"/><circle cx="12" cy="13.5" r="3.4"/><path d="M8.6 7l1.5-2.5h3.8L15.4 7"/>`,
  clip: s1`<path d="M20 11.6l-7.7 7.7a4.6 4.6 0 0 1-6.5-6.5l8-8a3.1 3.1 0 0 1 4.4 4.4l-7.9 7.9a1.55 1.55 0 0 1-2.2-2.2l7.2-7.2"/>`,
};

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

// go navigates to an app path such as "/inbox" or "/i/<id>".
export function go(path) {
  history.pushState({}, '', BASE + path);
  window.dispatchEvent(new PopStateEvent('popstate'));
}

// toast shows a short message. action adds one button, e.g. Undo.
// rel turns an app path into a link relative to the page's <base href>.
export const rel = (path) => path.replace(/^\//, '') || './';

// appPath is the current location as an app path, without the base.
export const appPath = () => location.pathname.slice(BASE.length) || '/';

export function toast(message, isError, action) {
  window.dispatchEvent(new CustomEvent('gtd-toast', { detail: { message, isError, action } }));
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
    ${b.resurfaced ? html`<span class="chip unblocked">${t('item.resurfaced')}</span>` : nothing}
    ${(b.waits_for || []).map((w) => html`<span class="chip waits">${t('item.waits_for', { title: w.title })}${w.owner ? ` (${w.owner})` : ''}</span>`)}
    ${b.repeat ? html`<span class="chip repeat">↻ ${t('item.repeats')}</span>` : nothing}
    ${b.time ? html`<span class="chip">${t('time.' + b.time)}</span>` : nothing}
    ${b.energy ? html`<span class="chip">${t('energy.' + b.energy)}</span>` : nothing}
    ${(b.badges || []).map((x) => html`<span class="chip badge">${x.split(':').slice(1).join(':')}</span>`)}
    ${b.attachments ? html`<span class="chip">${t('item.files', { n: b.attachments })}</span>` : nothing}
    ${b.rollup ? html`
      <span class="chip">${b.rollup.done}/${b.rollup.total}</span>
      ${b.state === 'active' && !b.rollup.alive ? html`<span class="chip stuck">${t('project.stuck')}</span>` : nothing}
      ${b.rollup.slipping ? html`<span class="chip slipping">${t('project.slipping')}</span>` : nothing}` : nothing}`;
}

// row is an item in a list. It links to the item's page.
export function row(b, opts = {}) {
  return html`
    <div class="row kind-${b.kind}" data-id=${b.id} tabindex="0">
      <span class="mark" data-kind=${b.kind} data-state=${b.state}></span>
      <a class="row-main" href=${rel(opts.href || '/i/' + b.id)}>
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
