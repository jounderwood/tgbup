// Files on this Mac: a 💾 next to each shows its full path and copies it.
// The path comes from the page's own address, so it is right wherever the
// archive folder is.
window.tgbupLocal = (button, href) => {
  const url = new URL(href, location.href);
  const path = url.protocol === 'file:' ? decodeURIComponent(url.pathname) : '';
  button.title = path ? 'On this Mac: ' + path + ' — click to copy the path' : 'On this Mac';
  button.addEventListener('click', () => {
    if (!path) return;
    const done = () => { button.textContent = '✓'; setTimeout(() => { button.textContent = '💾'; }, 1200); };
    (navigator.clipboard ? navigator.clipboard.writeText(path) : Promise.reject()).then(done, () => window.prompt('Path on this Mac', path));
  });
  return button;
};

document.addEventListener('DOMContentLoaded', () => {
  for (const b of document.querySelectorAll('button.local[data-href]')) window.tgbupLocal(b, b.dataset.href);
});
