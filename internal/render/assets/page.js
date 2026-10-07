// Spoilers stay hidden until clicked.
document.addEventListener('click', (e) => {
  const s = e.target.closest('.spoiler');
  if (s) s.classList.toggle('open');
});

// Videos and voice messages load when they come near the screen: a page can
// have hundreds. A video then shows its first frame; a looping one (a GIF, a
// video sticker) plays, silently, only while it is in view.
window.tgbupLazy = (() => {
  const load = (el) => {
    if (el.getAttribute('src')) return;
    el.preload = 'metadata';
    // #t=0.1 makes Safari draw the first frame instead of a black box.
    el.src = el.dataset.src + (el.tagName === 'VIDEO' && !el.loop ? '#t=0.1' : '');
  };
  if (!('IntersectionObserver' in window)) return load;
  const seen = new IntersectionObserver((entries) => {
    for (const e of entries) {
      if (!e.target.isConnected) { // the media page redrew its tiles
        seen.unobserve(e.target);
        continue;
      }
      if (e.isIntersecting) load(e.target);
      if (!e.target.loop) {
        if (e.isIntersecting) seen.unobserve(e.target);
      } else {
        if (e.isIntersecting) e.target.play().catch(() => {});
        else e.target.pause();
      }
    }
  }, { rootMargin: '300px' });
  return (el) => seen.observe(el);
})();

document.addEventListener('DOMContentLoaded', () => {
  for (const el of document.querySelectorAll('.lazy[data-src]')) window.tgbupLazy(el);
});

// A round video plays and pauses on a click (or Enter, Space), with sound,
// as in Telegram.
const toggleRound = (e) => {
  const v = e.target.closest && e.target.closest('video.round');
  if (!v || v.closest('a')) return;
  if (e.type === 'keydown') {
    if (e.key !== 'Enter' && e.key !== ' ') return;
    e.preventDefault();
  }
  if (!v.getAttribute('src')) v.src = v.dataset.src;
  if (v.paused) v.play().catch(() => {});
  else v.pause();
};
document.addEventListener('click', toggleRound);
document.addEventListener('keydown', toggleRound);

// A chat opens on its newest messages, at the bottom, as in a messenger;
// a link to a message (#m123) or a day keeps its place.
// Videos near the bottom grow as they load, so it keeps to the bottom until
// the reader scrolls themselves.
window.addEventListener('load', () => {
  if (location.hash) return;
  const bottom = () => window.scrollTo(0, document.documentElement.scrollHeight);
  let stick = true;
  for (const ev of ['wheel', 'touchstart', 'keydown', 'mousedown']) {
    window.addEventListener(ev, () => { stick = false; }, { once: true, passive: true });
  }
  document.addEventListener('loadedmetadata', () => { if (stick) bottom(); }, true);
  bottom();
});

// The bar on every page: search from here (this chat, or all chats), and
// pickers for the page and the day, from nav.js next to the page.
document.addEventListener('DOMContentLoaded', () => {
  for (const f of document.querySelectorAll('form.search')) {
    f.addEventListener('submit', (e) => {
      e.preventDefault();
      const q = f.elements.q.value.trim();
      if (!q) return;
      const p = new URLSearchParams({ q });
      if (f.dataset.chat && !(f.elements.all && f.elements.all.checked)) p.set('chat', f.dataset.chat);
      location.href = f.dataset.root + 'index.html#' + p;
    });
  }
  const jump = document.querySelector('.jump');
  const nav = window.tgbupNav;
  if (!jump || !nav || !nav.pages.length) return;
  const current = Number(jump.dataset.page);
  const pages = document.createElement('select');
  pages.setAttribute('aria-label', 'Page');
  for (const p of nav.pages) pages.append(new Option('page ' + p.n + ' · ' + p.range, p.href, false, p.n === current));
  pages.addEventListener('change', () => { location.href = pages.value; });
  const days = Object.keys(nav.days).sort();
  const date = Object.assign(document.createElement('input'), { type: 'date', min: days[0], max: days[days.length - 1], title: 'Go to a day' });
  date.setAttribute('aria-label', 'Go to a day');
  // A day without messages goes to the next one that has some.
  date.addEventListener('change', () => {
    const d = days.find((x) => x >= date.value) || days[days.length - 1];
    if (d) location.href = nav.days[d];
  });
  jump.append(pages, ' ', date);
});
