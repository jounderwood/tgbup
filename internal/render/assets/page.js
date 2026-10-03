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
