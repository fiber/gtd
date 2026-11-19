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
    showPreview: { state: true }, saveState: { state: true }, moving: { state: true }, proposals: { state: true }, asking: { state: true },
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

  // AI, on request: ask for task lines in this text. What comes back is a
  // list to tick; nothing changes until "Add to the note".
  aiAllowed() {
    const ai = store.me.ai;
    const sp = store.me.spaces.find((x) => x.id === this.item.space);
    const policy = sp?.ai || 'local';
    return ai?.on && (policy === 'any' || (policy === 'local' && ai.local));
  }
  async suggest() {
    this.asking = true;
    this.proposals = null;
    try {
      const res = await store.suggestTaskLines(this.item.id);
      this.proposals = res.lines.map((l) => ({ ...l, on: true }));
      if (!res.lines.length) toast(t('tasklines.none'));
    } catch (e) {
      toast(t(e.message), true);
    }
    this.asking = false;
  }
  async addProposals() {
    const lines = this.proposals.filter((l) => l.on).map(({ who, text }) => ({ who, text }));
    if (!lines.length) return;
    const res = await this.run(() => store.addTaskLines(this.item.id, lines));
    if (!res) return;
    this.proposals = null;
    toast(t('tasklines.added', { n: res.added }));
    await this.reload();
  }

  // Move to another space: first ask what would move and who would see it,
  // then, confirmed, do it. The item keeps its address.
  async moveTo(space, confirm) {
    if (!space) return;
    const res = await this.run(() => store.moveItem(this.item.id, space, confirm));
    if (!res) return;
    if (!confirm) {
      this.moving = { ...res, id: space };
      return;
    }
    this.moving = null;
    toast(t('move.done', { n: res.moved }));
    await this.reload();
  }

  // Hand a delegated action back, with the reason in its thread.
  async handBack(e) {
    e.preventDefault();
    const it = this.item;
    const why = e.target.why.value.trim();
    if (!why) return;
    await this.run(() => store.ops(it.space, [
      { type: 'comment', item: it.id, payload: { body: why } },
      { type: 'patch', item: it.id, base_rev: it.rev, changes: [{ field: 'owner', old: it.owner, new: it.delegated_by }] },
    ]));
    toast(t('item.handed_back'));
  }

  // ---- body editor ----
  //
  // A textarea with a live preview beside it on wide screens (a toggle on
  // narrow ones). It saves by itself shortly after typing stops, and when the
  // page is left. Images can be pasted or dropped in.

  get area() { return this.querySelector('.editor'); }
  get wide() { return window.matchMedia('(min-width: 1000px)').matches; }

  startEdit() {
    this.refs = new Map(this.item.refs.map((r) => [r.label, r.token]));
    this.editText = this.item.body_edit;
    this.editing = true;
    this.showPreview = false;
    this.preview = this.item.body_html;
    this.saveState = 'saved';
  }

  disconnectedCallback() {
    if (this.editing) this.saveNow(); // leaving the page: do not lose the text
    super.disconnectedCallback();
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
    this.changed();
  }

  // changed runs on every edit: preview soon, save a little later.
  changed() {
    this.saveState = 'unsaved';
    clearTimeout(this._previewTimer);
    clearTimeout(this._saveTimer);
    this._previewTimer = setTimeout(() => this.refreshPreview(), 400);
    this._saveTimer = setTimeout(() => this.saveNow(), 1500);
  }

  async refreshPreview() {
    if (!this.editing || !this.area) return;
    try {
      const res = await store.render(this.item.id, this.toTokens(this.area.value));
      this.preview = res.html;
    } catch { /* the preview is a convenience */ }
  }

  // saveNow stores the text if it changed. One save runs at a time; a change
  // made meanwhile is saved right after.
  async saveNow() {
    clearTimeout(this._saveTimer);
    const area = this.area;
    if (!area || !this.item) return true;
    if (this._saving) {
      this._again = true;
      return true;
    }
    const body = this.toTokens(area.value);
    if (body === (this.item.body || '')) {
      this.saveState = 'saved';
      return true;
    }
    this._saving = true;
    this.saveState = 'saving';
    let ok = true;
    try {
      const item = this.item;
      await store.patch(item, { body });
      // Keep typing without waiting for the reload: this is the new state.
      if (this.item === item) this.item = { ...item, body, rev: item.rev + 1 };
      this.saveState = 'saved';
    } catch (e) {
      ok = false;
      this.saveState = 'unsaved';
      toast(t(e.message), true);
    }
    this._saving = false;
    if (this._again) {
      this._again = false;
      return this.saveNow();
    }
    return ok;
  }

  async done() {
    if (await this.saveNow()) {
      this.editing = false;
      this.reload();
    }
  }

  async attachFiles(files) {
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

  attach(e) {
    const files = [...e.target.files];
    e.target.value = '';
    return this.attachFiles(files);
  }

  // Typing "[[" opens the link picker: a title search (SPEC §3.3).
  async onInput() {
    this.changed();
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

  // Pasting an image attaches it. Pasting an item's URL inserts a link to it.
  async onPaste(e) {
    const files = [...(e.clipboardData?.files || [])];
    if (files.length) {
      e.preventDefault();
      // A screenshot from the clipboard has no useful name.
      const stamp = new Date().toISOString().slice(0, 19).replace(/[T:]/g, '-');
      await this.attachFiles(files.map((f) => (/^image\.\w+$/.test(f.name) ? new File([f], `pasted-${stamp}.${f.name.split('.').pop()}`, { type: f.type }) : f)));
      return;
    }
    const text = (e.clipboardData?.getData('text') || '').trim();
    const m = text.match(/^https?:\/\/[^/\s]+\/i\/([0-9A-Za-z_-]+)$/);
    if (!m || !text.startsWith(location.origin + '/')) return;
    e.preventDefault();
    const x = await this.run(() => store.item(m[1]));
    this.insert(x ? this.addRef(`[[${x.title}]]`, `[[${x.id}]]`) : text);
  }

  onDrop(e) {
    const files = [...(e.dataTransfer?.files || [])];
    if (!files.length) return;
    e.preventDefault();
    this.attachFiles(files);
  }

  onEditorKey(e) {
    if (e.key === 'Escape' && this.picks) {
      this.picks = null;
      e.stopPropagation();
    } else if (e.key === 's' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      this.saveNow();
    }
  }

  // Choosing a template on an empty item fills in the template's skeleton.
  setTemplate(key) {
    const tpl = this.item.templates.find((x) => x.key === key);
    const changes = { template: key };
    if (tpl?.body && !(this.item.body || '').trim()) changes.body = tpl.body;
    return this.run(() => store.patch(this.item, changes));
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
    const elsewhere = store.me.spaces.filter((sp) => sp.id !== it.space);
    if (it.kind !== 'area' && elsewhere.length) {
      out.push(f('space', select(elsewhere.map((sp) => ({ id: sp.id, title: sp.id === store.me.user.id ? t('clarify.opt.personal') : sp.name })), '',
        (v) => this.moveTo(v, false), it.space === store.me.user.id ? t('clarify.opt.personal') : it.space_name)));
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
      const weekdays = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];
      const onDays = weekdays.filter((d) => (it.repeat || '').split(',').includes(d));
      const byDays = onDays.length > 0 || this.repeatDays;
      const dayName = (i) => new Intl.DateTimeFormat(store.me.lang, { weekday: 'short' }).format(new Date(2024, 0, i + 1));
      out.push(html`<div class="field"><label>${t('field.repeat')}</label>
        ${select(['1d', '2d', '3d', '1w', '2w', '4w'].map((v) => ({ id: v, title: t('repeat.' + v) })).concat([{ id: 'days', title: t('repeat.days') }]),
          byDays ? 'days' : it.repeat || '',
          (v) => { this.repeatDays = v === 'days'; if (v !== 'days') this.set('repeat', v); else this.requestUpdate(); }, t('repeat.none'))}
        ${byDays ? chips(weekdays.map((d, i) => ({ value: d, text: dayName(i) })), onDays,
          (d) => this.set('repeat', weekdays.filter((x) => (x === d) !== onDays.includes(x)).join(','))) : nothing}
      </div>`);
      const candidates = o.actions.filter((a) => a.id !== it.id && !(it.blocked_by || []).includes(a.id));
      out.push(html`<div class="field"><label>${t('field.blocked_by')}</label>
        <div class="chips">${it.blockers.map((b) => html`
          <span class="chip ${['done', 'dropped', 'trashed'].includes(b.state) ? 'finished' : 'waits'}">
            <a href=${'i/' + b.id}>${b.title}</a>${b.owner ? ` (${b.owner})` : ''}
            <button class="x" aria-label=${t('action.remove')}
              @click=${() => this.set('blocked_by', it.blocked_by.filter((x) => x !== b.id))}>×</button></span>`)}</div>
        ${select(candidates, '', (v) => v && this.set('blocked_by', [...(it.blocked_by || []), v]), t('option.add_blocker'))}</div>`);
    }
    if (it.templates.length) {
      out.push(f('template', select(it.templates.map((x) => ({ id: x.key, title: x.label?.[store.me.lang] || x.key })), it.template,
        (v) => this.setTemplate(v), t('option.none'))));
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
          ${it.body && this.aiAllowed() ? html`<button ?disabled=${this.asking} @click=${() => this.suggest()}
            title=${t('tasklines.hint', { model: store.me.ai.name })}>${this.asking ? t('tasklines.asking') : '✦ ' + t('tasklines.suggest')}</button>` : nothing}
          ${it.tasks ? html`<a class="button primary" href=${'process/' + it.id}>${t('note.process', { n: it.tasks })}</a>` : nothing}
          <label class="button">${icon.clip} ${t('note.attach')}<input type="file" multiple hidden @change=${(e) => this.attach(e)}></label>
        </div>`;
    }
    const split = this.wide;
    const showArea = split || !this.showPreview;
    const showPreview = split || this.showPreview;
    return html`
      <div class="editor-wrap ${split ? 'split' : ''}">
        <textarea class="editor" rows="18" .value=${this.editText} ?hidden=${!showArea} spellcheck="true"
          @input=${() => this.onInput()} @paste=${(e) => this.onPaste(e)} @drop=${(e) => this.onDrop(e)}
          @dragover=${(e) => e.preventDefault()} @keydown=${(e) => this.onEditorKey(e)}></textarea>
        ${this.picks ? html`<div class="picker">
          ${this.picks.list.length ? this.picks.list.map((x) => html`
            <button type="button" @click=${() => this.pick(x)}>${x.title} <small>${t('kind.' + x.kind)}</small></button>`)
            : html`<span class="empty">${t('picker.empty')}</span>`}</div>` : nothing}
        ${showPreview ? html`<div class="body preview" .innerHTML=${this.preview || ''}></div>` : nothing}
      </div>
      <small>${t('note.editor_hint')}</small>
      <div class="buttons">
        <button class="primary" @click=${() => this.done()}>${t('note.done')}</button>
        ${split ? nothing : html`<button @click=${() => { this.showPreview = !this.showPreview; if (this.showPreview) this.refreshPreview(); }}>
          ${t(this.showPreview ? 'action.edit' : 'note.preview')}</button>`}
        <label class="button" title=${t('capture.camera')}>${icon.camera}<input type="file" accept="image/*" capture hidden @change=${(e) => this.attach(e)}></label>
        <label class="button" title=${t('capture.file')}>${icon.clip}<input type="file" multiple hidden @change=${(e) => this.attach(e)}></label>
        <span class="save-state s-${this.saveState}">${t('note.' + this.saveState)}</span>
      </div>`;
  }

  related(key, list) {
    if (!list.length) return nothing;
    return html`<div class="related"><label>${t(key)}</label>
      ${list.map((x) => html`<a class="chip link" data-state=${x.state} href=${'i/' + x.id}>${x.title}</a>`)}</div>`;
  }

  render() {
    const it = this.item;
    if (!it) return nothing;
    const editable = it.kind !== 'capture';
    return html`
      <article class="item kind-${it.kind} ${this.editing ? 'editing' : ''}">
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
            ${it.kind === 'capture' && it.state === 'inbox' ? html`<a class="button primary" href=${'clarify?item=' + it.id}>${t('inbox.clarify')}</a>` : nothing}
            ${it.kind === 'project' ? html`<a class="button primary" href=${'p/' + it.id}>${t('plan.open')}</a>` : nothing}
            ${it.transitions.map((s) => html`
              <button @click=${() => this.move(s)}>${this.moveLabel(s)}</button>`)}
            ${editable && it.state !== 'trashed' ? html`<button @click=${() => this.fork()}>${t('note.fork')}</button>` : nothing}
          </div>
        </header>
        ${it.state === 'trashed' ? html`<p class="notice">${t('item.in_trash')}</p>` : nothing}
        ${this.proposals?.length ? html`<div class="notice proposals">
          <label>${t('tasklines.title')}</label>
          ${this.proposals.map((l) => html`
            <label class="check"><input type="checkbox" .checked=${l.on} @change=${(e) => { l.on = e.target.checked; this.requestUpdate(); }}>
              <span><b>${l.who === 'me' ? t('tasklines.me') : l.who || t('tasklines.open')}:</b> ${l.text}
                <small>“${l.quote}”</small></span></label>`)}
          <div class="buttons"><button class="primary" @click=${() => this.addProposals()}>${t('tasklines.add')}</button>
            <button @click=${() => { this.proposals = null; }}>${t('action.cancel')}</button></div></div>` : nothing}
        ${this.moving ? html`<p class="notice moving">
          <span>${t(this.moving.count > 1 ? 'move.confirm_many' : 'move.confirm_one', { n: this.moving.count, space: this.moving.space, members: this.moving.members.join(', ') })}
            ${this.moving.attachments ? ' ' + t('move.attachments') : ''}</span>
          <button class="primary" @click=${() => this.moveTo(this.moving.id, true)}>${t('move.do')}</button>
          <button @click=${() => { this.moving = null; }}>${t('action.cancel')}</button></p>` : nothing}
        ${it.delegated_by && it.owner === store.me.user.id && !['done', 'dropped', 'trashed'].includes(it.state) ? html`
          <form class="notice handback" @submit=${(e) => this.handBack(e)}>
            <span>${t('item.delegated_to_you', { name: it.delegated_by })}</span>
            <input type="text" name="why" required placeholder=${t('item.handback_why')}>
            <button type="submit">${t('item.handback')}</button>
          </form>` : nothing}
        ${it.delegated_by === store.me.user.id && it.owner !== store.me.user.id ? html`
          <p class="notice">${t('item.delegated_by_you', { name: it.owner })}</p>` : nothing}
        ${it.warnings.length ? html`<ul class="warnings">${it.warnings.map((w) => html`<li>${t('warning.short.' + w.Type)}</li>`)}</ul>` : nothing}

        <div class="item-cols">
        <div class="item-main">
        ${this.body()}

        ${(it.attachments || []).length ? html`<div class="related"><label>${t('item.attachments')}</label>
          ${it.attachments.map((a) => html`<a class="chip file" href=${'api/v1/att/' + a.sha256} target="_blank" rel="noopener">${a.name}</a>`)}</div>` : nothing}
        ${this.related('item.sources', it.sources)}
        ${this.related('item.derived', it.derived)}
        ${this.related('item.dependents', it.dependents)}
        ${this.related('item.backlinks', it.backlinks)}
        ${it.related.length ? html`<div class="related"><label>${t('item.related')}</label>
          ${it.related.map((x) => html`<a class="chip link" data-state=${x.state} href=${'i/' + x.id}
            title=${`${x.space_name} · ${Math.round(x.score * 100)}%`}>${x.title}</a>`)}</div>` : nothing}
        ${it.related_state === 'building' || it.related_state === 'offline' ? html`<small>${t('ai.note.' + it.related_state)}</small>` : nothing}

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
        <footer><small>${t('item.updated', { time: fmtTime(it.updated), user: it.updated_by })} · ${t('item.created_on', { time: fmtTime(it.created) })} · rev ${it.rev}</small></footer>
      </article>`;
  }
}
customElements.define('gtd-note', Note);
