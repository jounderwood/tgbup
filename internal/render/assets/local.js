// Files on this Mac: a 💾 next to each shows its full path on hover. The path
// comes from the page's own address, so it is right wherever the archive
// folder is.
window.tgbupLocal = (mark, href) => {
  const url = new URL(href, location.href);
  const path = url.protocol === 'file:' ? decodeURIComponent(url.pathname) : '';
  mark.title = path ? 'On this Mac: ' + path : 'On this Mac';
  return mark;
};

document.addEventListener('DOMContentLoaded', () => {
  for (const m of document.querySelectorAll('.local[data-href]')) window.tgbupLocal(m, m.dataset.href);
});
