// Asks before a destructive form is sent, copies issued secrets, toggles
// route fields by kind, and follows pending quota/device updates.
document.addEventListener("submit", (e) => {
  const msg = e.target.dataset.confirm;
  if (msg && !confirm(msg)) e.preventDefault();
});

document.addEventListener("click", async (e) => {
  const btn = e.target.closest("[data-copy]");
  if (!btn) return;
  const text = btn.dataset.copy || "";
  if (!text || !navigator.clipboard) return;
  try {
    await navigator.clipboard.writeText(text);
    const prev = btn.textContent;
    btn.textContent = "Copied";
    setTimeout(() => { btn.textContent = prev; }, 1500);
  } catch (_) {}
});

function syncRouteForm() {
  const kindSel = document.querySelector('select[name="type"]');
  if (!kindSel) return;
  const provSel = document.querySelector('select[name="provider"]');
  const isPool = kindSel.value === "pool";
  const hasProvider = provSel && provSel.value !== "";
  document.querySelectorAll('[data-route-field="alias"]').forEach((el) => { el.hidden = isPool; });
  document.querySelectorAll('[data-route-field="pool"]').forEach((el) => { el.hidden = !isPool; });
  document.querySelectorAll('[data-route-field="pool-members"]').forEach((el) => { el.hidden = !isPool || hasProvider; });
}

document.addEventListener("change", (e) => {
  if (e.target.matches('select[name="type"], select[name="provider"]')) syncRouteForm();
});

document.addEventListener("DOMContentLoaded", () => {
  syncRouteForm();

  const status = document.getElementById("device-status");
  if (status) {
    const poll = async () => {
      try {
        const r = await fetch("/ui/accounts/device/status?id=" + encodeURIComponent(status.dataset.id), { credentials: "same-origin" });
        const s = await r.json();
        if (s.done) {
          status.textContent = s.error ? "Not signed in: " + s.error : "Signed in.";
          if (!s.error) setTimeout(() => location.assign("/ui/accounts"), 800);
          return;
        }
      } catch (_) { /* a hiccup; ask again */ }
      setTimeout(poll, 2000);
    };
    poll();
    return;
  }

  if (document.querySelector("[data-pending]")) {
    try {
      const n = Number(sessionStorage.getItem("ak_pending_polls") || "0");
      if (n < 3) {
        sessionStorage.setItem("ak_pending_polls", String(n + 1));
        setTimeout(() => location.reload(), 1500);
      }
    } catch (_) {}
  } else {
    try { sessionStorage.removeItem("ak_pending_polls"); } catch (_) {}
  }
});
