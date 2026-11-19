// store.js is the single data layer. Components never call fetch themselves.
// Online it reads from the API. The offline source (IndexedDB) arrives with M3.
// It holds no GTD rules: it asks the server and passes on what the user did.

// BASE is the path the app is served under ("/gtd"), taken from the page's
// <base href>. Everything the app addresses is below it, so the same build
// runs under any server that forwards that path.
export const BASE = new URL(document.baseURI).pathname.replace(/\/$/, '');

export class ApiError extends Error {
  constructor(status, body) {
    super(body.error || `HTTP ${status}`);
    this.status = status;
    this.field = body.field;
  }
}

async function request(method, path, body, raw) {
  const init = { method, headers: {}, credentials: 'same-origin' };
  if (raw) {
    init.body = body;
  } else if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  const res = await fetch(BASE + '/api/v1' + path, init);
  let data = {};
  try { data = await res.json(); } catch { /* no JSON body */ }
  if (res.status === 401 && path !== '/login') {
    store.me = null;
    window.dispatchEvent(new CustomEvent('gtd-auth'));
  }
  if (!res.ok) throw new ApiError(res.status, data);
  return data;
}

const get = (path) => request('GET', path);
const post = (path, body) => request('POST', path, body ?? {});
const put = (path, body) => request('PUT', path, body ?? {});
const query = (params) => {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params || {})) {
    for (const x of [].concat(v)) if (x !== undefined && x !== null && x !== '') q.append(k, x);
  }
  const s = q.toString();
  return s ? '?' + s : '';
};

// changed tells every view that data changed, so lists and counts reload.
const changed = (detail) => window.dispatchEvent(new CustomEvent('gtd-changed', { detail }));

export const store = {
  me: null, // {user, lang, spaces, today, presets, server}

  async loadMe() {
    this.me = await get('/me');
    return this.me;
  },
  async login(user, password) {
    await post('/login', { user, password });
    return this.loadMe();
  },
  async logout() {
    await post('/logout');
    this.me = null;
    window.dispatchEvent(new CustomEvent('gtd-auth'));
  },
  async setMe(patch) {
    this.me = await put('/me', patch);
    return this.me;
  },
  space(id) {
    return this.me.spaces.find((s) => s.id === id);
  },

  // Reads.
  inbox: () => get('/inbox'),
  item: (id) => get('/items/' + encodeURIComponent(id)),
  comments: (id) => get(`/items/${encodeURIComponent(id)}/comments`),
  engage: (params) => get('/engage' + query(params)),
  projects: () => get('/projects'),
  plan: (id) => get('/plan/' + encodeURIComponent(id)),
  board: (params) => get('/board' + query(params)),
  areas: () => get('/areas'),
  notes: (params) => get('/notes' + query(params)),
  ideas: () => get('/ideas'),
  help: () => get('/help'),
  search: (q) => get('/search' + query({ q })),
  pick: (q) => get('/pick' + query({ q })),
  options: (space) => get(`/spaces/${encodeURIComponent(space)}/options`),
  templates: (space) => get(`/spaces/${encodeURIComponent(space)}/templates`),
  render: (item, text) => post('/render', { item, text }),
  aiStatus: () => get('/ai/status'),

  // Writes. Every change to an item is an op; the server validates it.
  async ops(space, ops) {
    const res = await post('/ops', { space, ops });
    changed(res);
    return res;
  },
  patch(item, changes) {
    return this.ops(item.space, [{
      type: 'patch', item: item.id, base_rev: item.rev,
      // The old value goes along: if the item moved on meanwhile (another op
      // raised its rev), the server still accepts a change to a field that
      // nobody else touched.
      changes: Object.entries(changes).map(([field, value]) => ({ field, old: item[field], new: value })),
    }]);
  },
  transition(item, state) {
    return this.ops(item.space, [{
      type: 'transition', item: item.id, base_rev: item.rev, changes: [{ field: 'state', new: state }],
    }]);
  },
  create(space, fields) {
    return this.ops(space, [{ type: 'create', payload: fields }]);
  },
  comment(item, body) {
    return this.ops(item.space, [{ type: 'comment', item: item.id, payload: { body } }]);
  },
  async capture(text, files) {
    let res;
    if (files && files.length) {
      const form = new FormData();
      form.append('text', text);
      for (const f of files) form.append('file', f, f.name);
      res = await request('POST', '/capture', form, true);
    } else {
      res = await post('/capture', { text });
    }
    changed();
    return res;
  },
  async attach(item, file) {
    const form = new FormData();
    form.append('file', file, file.name);
    const att = await request('POST', '/attachments', form, true);
    await this.ops(item.space, [{ type: 'attach', item: item.id, payload: att }]);
    return att;
  },
  async dismiss(seq) {
    await post(`/notifications/${seq}/dismiss`);
    changed();
  },
  async saveTemplates(space, yaml) {
    const res = await put(`/spaces/${encodeURIComponent(space)}/templates`, { yaml });
    await this.loadMe();
    return res;
  },

  // Wizards: the server holds the step machine, the client renders its steps.
  changePassword: (current, password) => put('/me/password', { current, password }),
  wizardStart: (kind, item, skip) => post(`/wizard/${kind}/start`, { item: item || '', skip: skip || 0 }),
  async wizardStep(kind, session, input) {
    const res = await post(`/wizard/${kind}/${session}/step`, input);
    if (res.done) changed(res);
    return res;
  },
};
