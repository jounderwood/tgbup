// Search over the per-chat indexes (search/<chat>.js). They are loaded as
// scripts, since pages opened from disk may not fetch, and only once the
// search box is used, since big chats make them large.
// Results are built with DOM text nodes only, never from HTML strings.
window.tgbupSearch = {
  chats: [],
  add(c) {
    for (const e of c.entries) e.push(e[3].toLowerCase());
    // Which file it came from: a search can be limited to one chat's file.
    c.file = document.currentScript ? document.currentScript.getAttribute('src') : '';
    this.chats.push(c);
  },
  loaded: {},
  load(files) {
    return Promise.all(files.map((src) => this.loaded[src] || (this.loaded[src] = new Promise((resolve) => {
      const s = document.createElement('script');
      s.src = src;
      s.onload = s.onerror = resolve;
      document.head.append(s);
    }))));
  },
  // init runs the box on the index page. #q=…&chat=<id> (from the search box
  // on any page) fills it and limits it to that chat until "all chats".
  init(files) {
    const q = document.getElementById('q');
    const out = document.getElementById('results');
    const scope = document.getElementById('scope');
    const params = new URLSearchParams(location.hash.slice(1));
    let only = params.get('chat') ? 'search/' + params.get('chat') + '.js' : '';
    if (only && !files.includes(only)) only = '';
    const chosen = () => (only ? [only] : files);
    const run = async () => {
      if (!chosen().every((f) => this.loaded[f])) out.textContent = 'Loading the search index…';
      await this.load(chosen());
      this.show(q.value.trim().toLowerCase(), out, chosen());
      scope.replaceChildren();
      if (only) {
        const all = Object.assign(document.createElement('a'), { href: '#', textContent: 'search all chats' });
        all.addEventListener('click', (e) => { e.preventDefault(); only = ''; run(); });
        scope.append('In one chat only · ', all);
      }
    };
    q.addEventListener('focus', () => this.load(chosen()));
    let timer;
    q.addEventListener('input', () => {
      clearTimeout(timer);
      timer = setTimeout(run, 150);
    });
    if (params.get('q')) {
      q.value = params.get('q');
      run();
    }
  },
  show(needle, out, files) {
    out.replaceChildren();
    if (needle.length < 2) return;
    const max = 200;
    let found = 0;
    for (const c of this.chats) {
      if (files && !files.includes(c.file)) continue;
      for (const [, date, page, text, lower] of c.entries) {
        const at = lower.indexOf(needle);
        if (at < 0) continue;
        out.append(this.hit(c.chat, date, page, text, at, needle.length));
        if (++found >= max) {
          out.append(Object.assign(document.createElement('p'), { textContent: 'First ' + max + ' results shown.' }));
          return;
        }
      }
    }
    if (!found) out.append(Object.assign(document.createElement('p'), { textContent: 'Nothing found.' }));
  },
  hit(chat, date, page, text, at, len) {
    const div = document.createElement('div');
    div.className = 'hit';
    const meta = document.createElement('div');
    meta.className = 'meta';
    meta.textContent = chat + ' · ' + date;
    const a = document.createElement('a');
    a.href = page;
    const from = Math.max(0, at - 60);
    a.append((from > 0 ? '…' : '') + text.slice(from, at));
    a.append(Object.assign(document.createElement('mark'), { textContent: text.slice(at, at + len) }));
    a.append(text.slice(at + len, at + len + 120) + (at + len + 120 < text.length ? '…' : ''));
    div.append(meta, a);
    return div;
  },
};
