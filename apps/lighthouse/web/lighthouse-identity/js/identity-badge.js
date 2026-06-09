// Lighthouse identity badge.
//
// Frontend-only, zero ComfyUI-API dependency (just DOM + fetch) so it survives
// frontend version churn. Loaded by ComfyUI as a WEB_DIRECTORY extension module.
// Shows "Signed in as <name> · Sign out", sourcing identity from oauth2-proxy's
// /oauth2/userinfo (same origin; oauth2-proxy fronts this app) and linking
// sign-out to /oauth2/sign_out. Auth lives entirely in the proxy — this only
// surfaces what the proxy already established.
(async () => {
  const BADGE_ID = "lh-identity-badge";

  let name = "signed in";
  try {
    const r = await fetch("/oauth2/userinfo", {
      credentials: "include",
      headers: { Accept: "application/json" },
    });
    if (r.ok) {
      const info = await r.json();
      // oauth2-proxy userinfo: preferredUsername is the human name; user/email
      // fall back to the sub (we key the bucket on sub via OIDC_EMAIL_CLAIM).
      name = info.preferredUsername || info.user || info.email || name;
    }
  } catch (_) {
    /* offline / no session — show a generic badge with a sign-out affordance */
  }

  const mount = () => {
    if (document.getElementById(BADGE_ID)) return;
    const badge = document.createElement("div");
    badge.id = BADGE_ID;
    badge.style.cssText = [
      "position:fixed", "bottom:8px", "left:8px", "z-index:1000",
      "display:flex", "gap:8px", "align-items:center",
      "background:rgba(20,20,20,.72)", "color:#e8e8e8",
      "font:12px system-ui,-apple-system,sans-serif",
      "padding:4px 9px", "border-radius:7px",
      "box-shadow:0 1px 4px rgba(0,0,0,.4)", "user-select:none",
    ].join(";");

    const who = document.createElement("span");
    who.textContent = "\u{1F464} " + name; // 👤
    who.title = "Signed in to Lighthouse";

    const sep = document.createElement("span");
    sep.textContent = "·";
    sep.style.opacity = ".5";

    const out = document.createElement("a");
    out.textContent = "Sign out";
    out.href = "/oauth2/sign_out?rd=/";
    out.style.cssText = "color:#7ab8ff;text-decoration:none;cursor:pointer";
    out.onmouseenter = () => (out.style.textDecoration = "underline");
    out.onmouseleave = () => (out.style.textDecoration = "none");

    badge.append(who, sep, out);
    document.body.appendChild(badge);
  };

  if (document.body) mount();
  else window.addEventListener("DOMContentLoaded", mount);
})();
