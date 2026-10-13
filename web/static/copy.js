"use strict";
// Buttons with data-copy="<element id>" copy that element's text.
document.addEventListener("click", async (ev) => {
  const btn = ev.target.closest("[data-copy]");
  if (!btn) return;
  const el = document.getElementById(btn.dataset.copy);
  if (!el) return;
  const text = el.value !== undefined ? el.value : el.textContent;
  try {
    await navigator.clipboard.writeText(text);
    const label = btn.textContent;
    btn.textContent = "Copied";
    setTimeout(() => { btn.textContent = label; }, 1500);
  } catch (_) {
    const range = document.createRange();
    range.selectNodeContents(el);
    getSelection().removeAllRanges();
    getSelection().addRange(range);
  }
});
