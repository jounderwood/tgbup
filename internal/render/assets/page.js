// Spoilers stay hidden until clicked.
document.addEventListener('click', (e) => {
  const s = e.target.closest('.spoiler');
  if (s) s.classList.toggle('open');
});
