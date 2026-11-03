(() => {
  const typed = document.querySelector("[data-typewriter]");
  if (!typed) return;

  let words;
  try {
    words = JSON.parse(typed.dataset.typewriter || "[]");
  } catch {
    return;
  }
  if (!Array.isArray(words) || words.length === 0) return;

  const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  if (reduced) {
    typed.textContent = words[0];
    return;
  }

  let word = 0;
  let char = 0;
  let deleting = false;

  const tick = () => {
    const current = words[word];
    if (!deleting) {
      char += 1;
      typed.textContent = current.slice(0, char);
      if (char === current.length) {
        deleting = true;
        window.setTimeout(tick, 1700);
        return;
      }
      window.setTimeout(tick, 72 + Math.random() * 46);
      return;
    }

    char -= 1;
    typed.textContent = current.slice(0, char);
    if (char === 0) {
      deleting = false;
      word = (word + 1) % words.length;
      window.setTimeout(tick, 260);
      return;
    }
    window.setTimeout(tick, 34);
  };

  tick();
})();
