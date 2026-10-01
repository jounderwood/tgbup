// Search over the per-chat indexes loaded as scripts (search/<chat>.js).
// Results are built with DOM text nodes only, never from HTML strings.
window.tgbupSearch = {
  chats: [],
  add(c) { this.chats.push(c); },
  init() {
    const q = document.getElementById('q');
    const out = document.getElementById('results');
    let timer;
    q.addEventListener('input', () => {
      clearTimeout(timer);
      timer = setTimeout(() => this.show(q.value.trim().toLowerCase(), out), 150);
    });
  },
  show(needle, out) {
    out.replaceChildren();
    if (needle.length < 2) return;
    const max = 200;
    let found = 0;
    for (const c of this.chats) {
      for (const [, date, page, text] of c.entries) {
        const at = text.toLowerCase().indexOf(needle);
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
