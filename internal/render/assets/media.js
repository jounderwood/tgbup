// The media page: filters every chat's media index (media/<chat>.js) by kind,
// chat, period and name. Filters live in the URL hash, so a view can be
// bookmarked. Everything is built from DOM text nodes, never HTML strings.
window.tgbupMedia = {
  chats: [],
  add(c) { this.chats.push(c); },
  kinds: ['photo', 'video', 'round', 'gif', 'sticker', 'voice', 'audio', 'file', 'link', 'place', 'contact', 'poll', 'todo'],
  visual: new Set(['photo', 'video', 'round', 'gif', 'sticker']),
  pageSize: 200,

  init(files) {
    Promise.all(files.map((src) => new Promise((resolve) => {
      const s = document.createElement('script');
      s.src = src;
      s.onload = s.onerror = resolve;
      document.head.append(s);
    }))).then(() => this.start());
  },

  start() {
    this.items = [];
    for (const c of this.chats) for (const it of c.items) { it.c = String(c.id); it.lower = (it.n || '').toLowerCase(); this.items.push(it); }
    this.items.sort((a, b) => (a.d < b.d ? 1 : a.d > b.d ? -1 : 0)); // newest first
    const counts = {};
    for (const it of this.items) counts[it.k] = (counts[it.k] || 0) + 1;
    const types = document.getElementById('types');
    for (const k of this.kinds) {
      if (!counts[k]) continue;
      const label = document.createElement('label');
      const box = Object.assign(document.createElement('input'), { type: 'checkbox', value: k });
      label.append(box, ' ' + k + ' (' + counts[k] + ')');
      types.append(label);
    }
    this.read();
    document.getElementById('filters').addEventListener('input', (e) => {
      if (e.target.id === 'chat') { document.getElementById('topic').value = ''; this.topics(); }
      this.write();
      this.show();
    });
    window.addEventListener('hashchange', () => { this.read(); this.show(); });
    document.getElementById('more').addEventListener('click', () => { this.shown += this.pageSize; this.render(); });
    this.show();
  },

  // read and write keep the filters in the hash: #types=photo,video&chat=-100&from=2026-01-01&to=&q=
  read() {
    const p = new URLSearchParams(location.hash.slice(1));
    const types = (p.get('types') || '').split(',').filter(Boolean);
    for (const box of document.querySelectorAll('#types input')) box.checked = types.includes(box.value);
    document.getElementById('chat').value = p.get('chat') || '';
    this.topics();
    document.getElementById('topic').value = p.get('topic') || '';
    document.getElementById('from').value = p.get('from') || '';
    document.getElementById('to').value = p.get('to') || '';
    document.getElementById('q').value = p.get('q') || '';
  },
  write() {
    const p = new URLSearchParams();
    const types = [...document.querySelectorAll('#types input:checked')].map((b) => b.value);
    if (types.length) p.set('types', types.join(','));
    for (const id of ['chat', 'topic', 'from', 'to', 'q']) {
      const v = document.getElementById(id).value;
      if (v) p.set(id, v);
    }
    history.replaceState(null, '', '#' + p.toString());
  },

  show() {
    const types = new Set([...document.querySelectorAll('#types input:checked')].map((b) => b.value));
    const chat = document.getElementById('chat').value;
    const topic = document.getElementById('topic').value;
    const from = document.getElementById('from').value;
    const to = document.getElementById('to').value;
    const q = document.getElementById('q').value.trim().toLowerCase();
    this.matched = this.items.filter((it) => (!types.size || types.has(it.k)) && (!chat || it.c === chat) && (!topic || it.t === topic)
      && (!from || it.d.slice(0, 10) >= from) && (!to || it.d.slice(0, 10) <= to) && (!q || it.lower.includes(q)));
    this.shown = this.pageSize;
    this.render();
  },

  // topics fills the topic filter with the selected chat's topics; it is off without a chat.
  topics() {
    const chat = document.getElementById('chat').value;
    const sel = document.getElementById('topic');
    const names = [...new Set(this.items.filter((it) => it.c === chat && it.t).map((it) => it.t))].sort();
    sel.replaceChildren(Object.assign(document.createElement('option'), { value: '', textContent: 'All topics' }));
    for (const n of names) sel.append(Object.assign(document.createElement('option'), { value: n, textContent: n }));
    sel.disabled = !names.length;
  },

  render() {
    const grid = document.getElementById('grid');
    const list = document.getElementById('list');
    grid.replaceChildren();
    list.replaceChildren();
    const page = this.matched.slice(0, this.shown);
    for (const it of page) (this.visual.has(it.k) && it.f ? grid : list).append(this.visual.has(it.k) && it.f ? this.tile(it) : this.row(it));
    document.getElementById('count').textContent = this.matched.length + ' items' + (this.matched.length > page.length ? ', showing ' + page.length : '');
    document.getElementById('more').hidden = this.matched.length <= page.length;
  },

  link(href, text, cls) {
    const a = Object.assign(document.createElement('a'), { href, textContent: text });
    if (cls) a.className = cls;
    if (/^https?:/.test(href)) a.rel = 'noopener';
    return a;
  },

  tile(it) {
    const div = document.createElement('div');
    div.className = 'tile';
    const a = this.link(it.f, '');
    if (it.k === 'photo' || it.k === 'sticker') {
      a.append(Object.assign(document.createElement('img'), { src: it.f, loading: 'lazy', alt: it.n || '' }));
    } else {
      a.append(Object.assign(document.createElement('span'), { className: 'play', textContent: '▶ ' + it.k }));
    }
    div.append(a, this.link(it.m, it.d.slice(0, 10), 'when'));
    if (this.isLocal(it.f)) div.append(this.local(it.f));
    return div;
  },

  // isLocal: f is a file in the archive, not an outside link or a map.
  isLocal(f) { return !!f && !/^[a-z][a-z0-9+.-]*:/i.test(f); },

  // local marks a file on this Mac with its path (see local.js).
  local(href) {
    const b = Object.assign(document.createElement('button'), { type: 'button', className: 'local', textContent: '💾' });
    return window.tgbupLocal ? window.tgbupLocal(b, href) : b;
  },

  row(it) {
    const div = document.createElement('div');
    div.className = 'row-item';
    div.append(Object.assign(document.createElement('span'), { className: 'kind', textContent: it.k }));
    div.append(it.f ? this.link(it.f, it.n || it.f, 'name') : Object.assign(document.createElement('span'), { className: 'name', textContent: (it.x ? '⏳ ' : '') + (it.n || it.k) }));
    if (this.isLocal(it.f)) div.append(this.local(it.f));
    const meta = [it.s, it.x, it.t, it.d].filter(Boolean).join(' · ');
    div.append(Object.assign(document.createElement('span'), { className: 'meta', textContent: meta }));
    div.append(this.link(it.m, 'message', 'when'));
    return div;
  },
};
