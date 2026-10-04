// Spoilers stay hidden until clicked.
document.addEventListener('click', (e) => {
  const s = e.target.closest('.spoiler');
  if (s) s.classList.toggle('open');
});

// A chat opens on its newest messages, at the bottom, as in a messenger;
// a link to a message (#m123) or a day keeps its place.
window.addEventListener('load', () => {
  if (!location.hash) window.scrollTo(0, document.documentElement.scrollHeight);
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
