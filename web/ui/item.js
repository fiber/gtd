// gtd-note is the page of one item of any kind (/i/<id>): title, state,
// fields, the Markdown body with its editor, attachments, relations and the
// thread. Every change is sent as an op. The body is shown as the HTML the
// server rendered; the editor shows tokens as readable labels ([[Title]],
// ![[name.png]]) and maps them back to tokens on save.
import { store } from './store.js';
import { t, fmtTime } from './i18n.js';
import { View, html, nothing, stateChip, chips, dateField, select, go, toast, icon } from './widgets.js';

const ENERGIES = ['low', 'med', 'high'].map((v) => ({ value: v, label: 'energy.' + v }));
const TIMES = ['5m', '15m', '30m', '1h', '2h+'].map((v) => ({ value: v, label: 'time.' + v }));

class Note extends View {
  static properties = {
    itemId: {}, item: { state: true }, comments: { state: true }, editing: { state: true },
    preview: { state: true }, picks: { state: true }, showOriginal: { state: true }, more: { state: true },
  };

  willUpdate(changed) {
    if (changed.has('itemId') && changed.get('itemId') !== undefined) {
      this.editing = false;
      this.item = null;
      this.reload();
    }
  }

  async reload() {
    const id = this.itemId;
    const item = await this.run(() => store.item(id));
    if (!item || id !== this.itemId) return;
    this.item = item;
    this.comments = (await this.run(() => store.comments(id))) || [];
    if (!this.editing && new URLSearchParams(location.search).get('edit')) {
      history.replaceState({}, '', location.pathname);
      this.startEdit();
    }
  }

  set(field, value) {
    return this.run(() => store.patch(this.item, { [field]: value }));
  }

  // ---- body editor ----

  get area() { return this.querySelector('.editor'); }

  startEdit() {
    this.refs = new Map(this.item.refs.map((r) => [r.label, r.token]));
    this.editText = this.item.body_edit;
    this.editing = true;
    this.preview = null;
  }

  // toTokens replaces the labels the editor shows by the stored tokens.
  toTokens(text) {
    const labels = [...this.refs.keys()].sort((a, b) => b.length - a.length);
    for (const l of labels) text = text.replaceAll(l, this.refs.get(l));
    return text;
  }

  // addRef registers a label for a token and returns the label to insert.
  addRef(lbl, token) {
    let l = lbl;
    for (let n = 2; this.refs.has(l) && this.refs.get(l) !== token; n += 1) {
      l = lbl.endsWith(']]') ? `${lbl.slice(0, -2)} (${n})]]` : `${lbl} (${n})`;
    }
    this.refs.set(l, token);
    return l;
  }

  insert(text, replaceFrom) {
    const a = this.area;
    const start = replaceFrom ?? a.selectionStart;
    a.setRangeText(text, start, a.selectionEnd, 'end');
    a.focus();
  }

  async save() {
    const ok = await this.set('body', this.toTokens(this.area.value));
    if (ok) this.editing = false;
  }

  async togglePreview() {
    if (this.preview !== null) {
      this.preview = null;
      return;
    }
    const res = await this.run(() => store.render(this.item.id, this.toTokens(this.area.value)));
    if (res) this.preview = res.html;
  }

  async attach(e) {
    const files = [...e.target.files];
    e.target.value = '';
    for (const f of files) {
      const att = await this.run(() => store.attach(this.item, f));
      if (!att) return;
      if (this.editing) {
        const image = (att.mime || '').startsWith('image/');
        const lbl = this.addRef(`${image ? '!' : ''}[[${att.name}]]`, `${image ? '!' : ''}[[att:${att.sha256}]]`);
        this.insert(lbl + '\n');
      }
    }
  }

  // Typing "[[" opens the link picker: a title search (SPEC §3.3).
  async onInput() {
    const a = this.area;
    const before = a.value.slice(0, a.selectionStart);
    const m = before.match(/\[\[([^[\]\n]{0,60})$/);
    if (!m) {
      this.picks = null;
      return;
    }
    const from = before.length - m[0].length;
    const found = await this.run(() => store.pick(m[1]));
    if (found && this.area.selectionStart === a.selectionStart) {
      this.picks = { from, list: found.filter((x) => x.id !== this.item.id) };
    }
  }

  pick(x) {
    this.insert(this.addRef(`[[${x.title}]]`, `[[${x.id}]]`), this.picks.from);
    this.picks = null;
  }

  // Pasting an item's URL inserts a link to it.
  async onPaste(e) {
    const text = (e.clipboardData?.getData('text') || '').trim();
    const m = text.match(/^https?:\/\/[^/\s]+\/i\/([0-9A-Za-z_-]+)$/);
    if (!m || !text.startsWith(location.origin + '/')) return;
    e.preventDefault();
    const x = await this.run(() => store.item(m[1]));
    this.insert(x ? this.addRef(`[[${x.title}]]`, `[[${x.id}]]`) : text);
  }

  // ---- actions ----

  // From the trash the way back is "Restore", from done or dropped "Reopen":
  // the plain state name would not say that.
  moveLabel(state) {
    const from = this.item.state;
    if (from === 'trashed') return t('transition.restore');
    if (from === 'done' || from === 'dropped') return t('transition.reopen');
    return t('transition.' + state);
  }

  // Trashing leaves the page: there is nothing more to do with the item. The
  // toast offers the way back.
  async move(state) {
    const it = this.item;
    const res = await this.run(() => store.transition(it, state));
    if (!res || state !== 'trashed') return;
    const undo = async () => {
      const now = await store.item(it.id);
      await store.transition(now, now.transitions[0]);
    };
    toast(t('item.trashed_toast'), false, { label: t('action.undo'), run: undo });
    if (history.length > 1) history.back();
    else go('/projects');
  }

  async fork() {
    const it = this.item;
    const res = await this.run(() => store.create(it.space, {
      kind: 'note', state: it.project ? 'filed' : 'unsorted', project: it.project || '',
      title: t('note.fork_title', { title: it.title }), derived_from: [it.id], body: `[[${it.id}]]\n\n`,
    }));
    if (res) go('/i/' + res.ops[0].item + '?edit=1');
  }

  async addComment(e) {
    e.preventDefault();
    const input = e.target.querySelector('textarea');
    if (!input.value.trim()) return;
    await this.run(async () => {
      await store.comment(this.item, input.value.trim());
      input.value = '';
    });
  }

  async makeAction(c) {
    const it = this.item;
    const project = it.kind === 'project' ? it.id : it.project || '';
    await this.run(async () => {
      await store.create(it.space, { kind: 'action', title: c.body.split('\n')[0].slice(0, 200), project, derived_from: [it.id] });
      toast(t('thread.action_created'));
    });
  }

  // ---- rendering ----

  fields() {
    const it = this.item;
    const o = it.options;
    const space = store.space(it.space);
    const f = (key, input) => html`<div class="field"><label>${t('field.' + key)}</label>${input}</div>`;
    const members = (space?.members || []).map((m) => ({ id: m, title: m }));
    const out = [];
    if (it.kind === 'action' || it.kind === 'note') {
      out.push(f('project', select(o.projects, it.project, (v) => this.set('project', v), t('option.no_project'))));
    }
    if (it.kind === 'project') {
      out.push(f('parent', select(o.projects.filter((p) => p.id !== it.id), it.parent, (v) => this.set('parent', v), t('option.none'))));
    }
    if (['action', 'project', 'note'].includes(it.kind)) {
      out.push(f('area', select(o.areas, it.area, (v) => this.set('area', v), t('option.none'))));
    }
    if (it.kind === 'action') {
      out.push(f('context', chips(o.contexts, it.context, (v) => this.set('context', v === it.context ? '' : v))));
      if (space?.kind === 'shared') {
        out.push(f('owner', select(members, it.owner, (v) => this.set('owner', v), t('option.anyone'))));
      }
      out.push(f('waiting_on', html`<input type="text" .value=${it.waiting_on || ''} list="people"
        @change=${(e) => this.set('waiting_on', e.target.value.trim())}>
        <datalist id="people">${(o.people || []).map((p) => html`<option value=${p}></option>`)}</datalist>`));
    }
    if (['action', 'project', 'note'].includes(it.kind)) {
      out.push(f('defer_until', dateField(it.defer_until, (v) => this.set('defer_until', v))));
    }
    if (it.kind === 'action' || it.kind === 'project') {
      out.push(f('target', dateField(it.target, (v) => this.set('target', v))));
      out.push(html`<div class="field"><label>${t('field.due')}</label>
        ${dateField(it.due, (v) => this.set('due', v), [])}<small>${t('hint.due')}</small></div>`);
    }
    if (it.kind === 'action') {
      out.push(f('energy', chips(ENERGIES, it.energy, (v) => this.set('energy', v === it.energy ? '' : v))));
      out.push(f('time', chips(TIMES, it.time, (v) => this.set('time', v === it.time ? '' : v))));
      const candidates = o.actions.filter((a) => a.id !== it.id && !(it.blocked_by || []).includes(a.id));
      out.push(html`<div class="field"><label>${t('field.blocked_by')}</label>
        <div class="chips">${it.blockers.map((b) => html`
          <span class="chip ${['done', 'dropped', 'trashed'].includes(b.state) ? 'finished' : 'waits'}">
            <a href=${'/i/' + b.id}>${b.title}</a>${b.owner ? ` (${b.owner})` : ''}
            <button class="x" aria-label=${t('action.remove')}
              @click=${() => this.set('blocked_by', it.blocked_by.filter((x) => x !== b.id))}>×</button></span>`)}</div>
        ${select(candidates, '', (v) => v && this.set('blocked_by', [...(it.blocked_by || []), v]), t('option.add_blocker'))}</div>`);
    }
    if (it.templates.length) {
      out.push(f('template', select(it.templates.map((x) => ({ id: x.key, title: x.label?.[store.me.lang] || x.key })), it.template,
        (v) => this.set('template', v), t('option.none'))));
    }
    for (const tf of it.template_def?.fields || []) {
      const v = it.fields?.[tf.key] || '';
      const setField = (x) => this.set('fields', { ...(it.fields || {}), [tf.key]: x });
      let input;
      if (tf.type === 'enum') input = chips(tf.options.map((x) => ({ value: x, text: x })), v, (x) => setField(x === v ? '' : x));
      else if (tf.type === 'date') input = dateField(v, setField);
      else input = html`<input type=${tf.type === 'number' ? 'number' : 'text'} step="any" .value=${v} @change=${(e) => setField(e.target.value.trim())}>`;
      out.push(html`<div class="field"><label>${tf.label?.[store.me.lang] || tf.key}</label>${input}</div>`);
    }
    return out;
  }

  body() {
    const it = this.item;
    if (!this.editing) {
      return html`
        <div class="body" .innerHTML=${it.body_html}></div>
        <div class="buttons">
          <button @click=${() => this.startEdit()}>${t(it.body ? 'action.edit' : 'note.write')}</button>
          ${it.tasks ? html`<a class="button primary" href=${'/process/' + it.id}>${t('note.process', { n: it.tasks })}</a>` : nothing}
          <label class="button">${icon.clip} ${t('note.attach')}<input type="file" multiple hidden @change=${(e) => this.attach(e)}></label>
        </div>`;
    }
    return html`
      <div class="editor-wrap">
        <textarea class="editor" rows="14" .value=${this.editText} ?hidden=${this.preview !== null}
          @input=${() => this.onInput()} @paste=${(e) => this.onPaste(e)}
          @keydown=${(e) => { if (e.key === 'Escape' && this.picks) { this.picks = null; e.stopPropagation(); } }}></textarea>
        ${this.picks ? html`<div class="picker">
          ${this.picks.list.length ? this.picks.list.map((x) => html`
            <button type="button" @click=${() => this.pick(x)}>${x.title} <small>${t('kind.' + x.kind)}</small></button>`)
            : html`<span class="empty">${t('picker.empty')}</span>`}</div>` : nothing}
        ${this.preview !== null ? html`<div class="body preview" .innerHTML=${this.preview}></div>` : nothing}
      </div>
      <small>${t('note.editor_hint')}</small>
      <div class="buttons">
        <button class="primary" @click=${() => this.save()}>${t('action.save')}</button>
        <button @click=${() => this.togglePreview()}>${t(this.preview !== null ? 'action.edit' : 'note.preview')}</button>
        <label class="button" title=${t('capture.camera')}>${icon.camera}<input type="file" accept="image/*" capture hidden @change=${(e) => this.attach(e)}></label>
        <label class="button" title=${t('capture.file')}>${icon.clip}<input type="file" multiple hidden @change=${(e) => this.attach(e)}></label>
        <button class="link" @click=${() => { this.editing = false; }}>${t('action.cancel')}</button>
      </div>`;
  }

  related(key, list) {
    if (!list.length) return nothing;
    return html`<div class="related"><label>${t(key)}</label>
      ${list.map((x) => html`<a class="chip link" data-state=${x.state} href=${'/i/' + x.id}>${x.title}</a>`)}</div>`;
  }

  render() {
    const it = this.item;
    if (!it) return nothing;
    const editable = it.kind !== 'capture';
    return html`
      <article class="item kind-${it.kind}">
        <header>
          <span class="meta">
            <span class="chip">${t('kind.' + it.kind)}</span>${stateChip(it.state)}
            <span class="chip">${it.space_name}</span>
            ${it.deferred ? html`<span class="chip deferred">${t('item.deferred', { date: it.defer_until })}</span>` : nothing}
            ${it.rollup && it.state === 'active' && !it.rollup.alive ? html`<span class="chip stuck">${t('project.stuck')}</span>` : nothing}
          </span>
          <input class="title" type="text" .value=${it.title} aria-label=${t('field.title')}
            @change=${(e) => e.target.value.trim() && this.set('title', e.target.value.trim())}>
          <div class="buttons">
            ${it.kind === 'capture' && it.state === 'inbox' ? html`<a class="button primary" href=${'/clarify?item=' + it.id}>${t('inbox.clarify')}</a>` : nothing}
            ${it.kind === 'project' ? html`<a class="button primary" href=${'/p/' + it.id}>${t('plan.open')}</a>` : nothing}
            ${it.transitions.map((s) => html`
              <button @click=${() => this.move(s)}>${this.moveLabel(s)}</button>`)}
            ${editable && it.state !== 'trashed' ? html`<button @click=${() => this.fork()}>${t('note.fork')}</button>` : nothing}
          </div>
        </header>
        ${it.state === 'trashed' ? html`<p class="notice">${t('item.in_trash')}</p>` : nothing}
        ${it.warnings.length ? html`<ul class="warnings">${it.warnings.map((w) => html`<li>${t('warning.short.' + w.Type)}</li>`)}</ul>` : nothing}

        <div class="item-cols">
        <div class="item-main">
        ${this.body()}

        ${(it.attachments || []).length ? html`<div class="related"><label>${t('item.attachments')}</label>
          ${it.attachments.map((a) => html`<a class="chip file" href=${'/api/v1/att/' + a.sha256} target="_blank" rel="noopener">${a.name}</a>`)}</div>` : nothing}
        ${this.related('item.sources', it.sources)}
        ${this.related('item.derived', it.derived)}
        ${this.related('item.dependents', it.dependents)}
        ${this.related('item.backlinks', it.backlinks)}

        <section class="thread">
          <h2>${t('thread.title')}</h2>
          ${(this.comments || []).map((c) => html`
            <div class="comment">
              <div class="comment-head"><b>${c.author_name}</b> <span>${fmtTime(c.ts)}</span>
                ${c.deleted ? nothing : html`<button class="link" @click=${() => this.makeAction(c)}>${t('thread.make_action')}</button>`}</div>
              ${c.deleted ? html`<p class="empty">${t('thread.deleted')}</p>` : html`<div class="body" .innerHTML=${c.body_html}></div>`}
            </div>`)}
          <form class="inline" @submit=${(e) => this.addComment(e)}>
            <textarea rows="1" placeholder=${t('thread.add')}
              @keydown=${(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); e.target.form.requestSubmit(); } }}></textarea>
            <button>${t('action.add')}</button>
          </form>
        </section>
        </div>
        <aside class="item-side">
        ${editable ? html`
          <details class="fields" ?open=${this.more ?? (it.kind !== 'note' || window.innerWidth >= 1000)} @toggle=${(e) => { this.more = e.target.open; }}>
            <summary>${t('item.details')}</summary>
            ${this.fields()}
          </details>` : nothing}
        ${it.original ? html`
          <details class="fields"><summary>${t('item.original')}</summary><pre class="original">${it.original}</pre></details>` : nothing}
        </aside>
        </div>
        <footer><small>${t('item.updated', { time: fmtTime(it.updated), user: it.updated_by })} · rev ${it.rev}</small></footer>
      </article>`;
  }
}
customElements.define('gtd-note', Note);
