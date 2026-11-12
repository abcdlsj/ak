// Asks before a destructive form is sent, and follows a device sign-in.
document.addEventListener("submit", (e) => {
  const msg = e.target.dataset.confirm;
  if (msg && !confirm(msg)) e.preventDefault();
});

document.addEventListener("DOMContentLoaded", () => {
  const status = document.getElementById("device-status");
  if (!status) return;
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
});
