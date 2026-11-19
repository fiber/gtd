// The generic wizard renderer. A wizard is a step machine on the server
// (wizard/ in Go). Each step says what to ask: this file draws it by step
// type and posts the answer. It knows nothing about any particular wizard.
import { store } from './store.js';
import { t, label } from './i18n.js';
import { View, html, nothing, chips, dateField, select, go } from './widgets.js';

class Wizard extends View {
  static properties = {
    kind: {}, item: {}, step: { state: true }, values: { state: true }, error: { state: true },
    left: { state: true }, busy: { state: true },
  };

  connectedCallback() {
    super.connectedCallback();
    // Number keys answer a choice step: 1 for the first option, and so on.
    this._keys = (e) => {
      const typing = ['INPUT', 'TEXTAREA', 'SELECT'].includes(e.target.tagName);
      const o = this.step?.type === 'choice' && !typing && !e.metaKey && !e.ctrlKey && this.step.options[Number(e.key) - 1];
      if (o) {
        e.preventDefault();
        this.answer(o.value);
      }
    };
    document.addEventListener('keydown', this._keys);
    this.start();
  }
  disconnectedCallback() {
    super.disconnectedCallback();
    clearInterval(this._timer);
    document.removeEventListener('keydown', this._keys);
  }

  async start() {
    const res = await this.run(() => store.wizardStart(this.kind, this.item));
    if (!res) return;
    if (res.done) {
      this.step = null;
      this.dispatchEvent(new CustomEvent('finished', { detail: { empty: true } }));
      return;
    }
    this.session = res.session;
    this.show(res.step);
  }

  show(step) {
    this.step = step;
    this.error = null;
    this.values = Object.fromEntries((step.fields || []).map((f) => [f.key, f.value || '']));
    clearInterval(this._timer);
    this.left = step.data?.timer || 0;
    if (this.left) {
      this._timer = setInterval(() => { if (this.left > 0) this.left -= 1; }, 1000);
    }
  }

  updated(changed) {
    if (changed.has('step') && this.step) this.querySelector('input[type=text], textarea')?.focus();
  }

  set(key, value) {
    this.values = { ...this.values, [key]: value };
  }

  async answer(choice) {
    if (this.busy) return;
    this.busy = true;
    try {
      const res = await store.wizardStep(this.kind, this.session, { choice, values: this.values });
      if (res.done) {
        clearInterval(this._timer);
        this.step = null;
        this.dispatchEvent(new CustomEvent('finished', { detail: res }));
      } else {
        this.show(res.step);
      }
    } catch (e) {
      this.error = { field: e.field, text: t(e.message) };
    }
    this.busy = false;
  }

  field(f) {
    const v = this.values[f.key];
    let input;
    switch (f.type) {
      case 'textarea':
        input = html`<textarea rows="5" .value=${v} @input=${(e) => this.set(f.key, e.target.value)}></textarea>`;
        break;
      case 'date':
        input = dateField(v, (x) => this.set(f.key, x), f.options || []);
        break;
      case 'select':
        input = select(f.options, v, (x) => this.set(f.key, x));
        break;
      case 'chips':
        input = chips(f.options, v, (x) => this.set(f.key, x === v ? '' : x));
        break;
      default:
        input = html`<input type="text" .value=${v} list=${f.key === 'waiting_on' ? 'wizard-people' : nothing}
          @input=${(e) => this.set(f.key, e.target.value)}>`;
    }
    return html`
      <div class="field ${this.error?.field === f.key ? 'invalid' : ''}" data-key=${f.key}>
        <label>${t(f.label)}${f.required ? ' *' : ''}</label>
        ${input}
        ${f.hint ? html`<small>${t(f.hint)}</small>` : nothing}
        ${this.error?.field === f.key ? html`<small class="error">${this.error.text}</small>` : nothing}
      </div>`;
  }

  // What the step shows besides the question: the original capture, its
  // attachments, what was already done, the plan so far, the timer.
  data(d) {
    if (!d) return nothing;
    const clock = `${Math.floor(this.left / 60)}:${String(this.left % 60).padStart(2, '0')}`;
    return html`
      ${d.original ? html`<pre class="original">${d.original}</pre>` : nothing}
      ${(d.attachments || []).map((a) => (a.mime || '').startsWith('image/')
        ? html`<img class="att" src=${'/api/v1/att/' + a.sha256} alt=${a.name}>`
        : html`<a class="chip file" href=${'/api/v1/att/' + a.sha256} download=${a.name}>${a.name}</a>`)}
      ${d.timer ? html`<p class="timer ${this.left === 0 ? 'over' : ''}">${clock}</p>` : nothing}
      ${d.done ? html`<ul class="donelist">${d.done.map((x) => html`<li>✓ ${x}</li>`)}</ul>` : nothing}
      ${d.after ? html`<ol class="afterlist">${d.after.map((x) => html`<li>${x}</li>`)}</ol>` : nothing}
      ${d.people ? html`<datalist id="wizard-people">${d.people.map((p) => html`<option value=${p}></option>`)}</datalist>` : nothing}`;
  }

  render() {
    const s = this.step;
    if (!s) return nothing;
    return html`
      <form class="wizard step-${s.type}" data-step=${s.key} @submit=${(e) => { e.preventDefault(); this.answer(s.options[0].value); }}>
        <div class="frame-head"><span>${t('wizard.title.' + this.kind)}</span><span>${s.data?.total ? t('wizard.progress', { index: s.data.index, total: s.data.total }) : ''}</span></div>
        <div class="frame-body">
        <h1>${t(s.question)}</h1>
        ${s.subject ? html`<p class="subject">${s.subject}</p>` : nothing}
        ${this.data(s.data)}
        ${(s.fields || []).map((f) => this.field(f))}
        ${this.error && !s.fields?.some((f) => f.key === this.error.field) ? html`<p class="error">${this.error.text}</p>` : nothing}
        <div class="buttons ${s.type}">
          ${s.options.map((o, i) => html`
            <button type=${s.type === 'form' && i === 0 ? 'submit' : 'button'} ?disabled=${this.busy} data-value=${o.value}
              class=${(s.preselect ? o.value === s.preselect : i === 0) ? 'primary' : ''}
              @click=${s.type === 'form' && i === 0 ? null : () => this.answer(o.value)}>${label(o)}</button>`)}
        </div>
        </div>
      </form>`;
  }
}
customElements.define('gtd-wizard', Wizard);

// Clarify: process the inbox, one capture at a time, oldest first (SPEC §5.2).
class Clarify extends View {
  static properties = { item: {}, empty: { state: true } };
  next(e) {
    if (e.detail.empty) {
      this.empty = true;
      return;
    }
    const w = this.querySelector('gtd-wizard');
    w.item = '';
    w.start(); // the next capture
  }
  render() {
    if (this.empty) {
      return html`<section class="done"><h1>${t('clarify.inbox_zero')}</h1>
        <a class="button primary" href="/engage">${t('nav.engage')}</a></section>`;
    }
    return html`<gtd-wizard kind="clarify" .item=${this.item} @finished=${(e) => this.next(e)}></gtd-wizard>`;
  }
}
customElements.define('gtd-clarify', Clarify);

// Process note: turn a note's open task lines into actions.
class Process extends View {
  static properties = { item: {} };
  render() {
    return html`<gtd-wizard kind="note" .item=${this.item} @finished=${() => go('/i/' + this.item)}></gtd-wizard>`;
  }
}
customElements.define('gtd-process', Process);
