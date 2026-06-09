// Lighthouse identity — a native ComfyUI sidebar tab (not a floating badge).
//
// Registers a left-sidebar icon (PrimeIcons) whose panel shows "Signed in as
// <name>" + a Sign out button. Identity comes from oauth2-proxy's
// /oauth2/userinfo (same origin; oauth2-proxy fronts this app); sign-out links to
// /oauth2/sign_out. Auth lives entirely in the proxy — this only surfaces it.
import { app } from "../../scripts/app.js";

async function resolveName() {
  try {
    const r = await fetch("/oauth2/userinfo", {
      credentials: "include",
      headers: { Accept: "application/json" },
    });
    if (r.ok) {
      const info = await r.json();
      // preferredUsername is the human name; user/email fall back to the sub
      // (we key the per-user bucket on sub via OIDC_EMAIL_CLAIM).
      return info.preferredUsername || info.user || info.email || "account";
    }
  } catch (_) {
    /* no session / offline — still show the tab so sign-out is reachable */
  }
  return "account";
}

app.registerExtension({
  name: "Lighthouse.Identity",
  async setup() {
    const name = await resolveName();
    try {
      app.extensionManager.registerSidebarTab({
        id: "lighthouse-identity",
        icon: "pi pi-user",
        title: "Account",
        tooltip: `Signed in as ${name}`,
        type: "custom",
        render: (el) => {
          el.innerHTML = "";
          const wrap = document.createElement("div");
          wrap.style.cssText =
            "padding:14px;display:flex;flex-direction:column;gap:12px;font:13px system-ui,-apple-system,sans-serif;";

          const who = document.createElement("div");
          who.innerHTML = `Signed in as<br><b style="font-size:14px">${name}</b>`;

          const out = document.createElement("a");
          out.textContent = "Sign out";
          out.href = "/oauth2/sign_out?rd=/";
          out.className = "p-button p-component";
          out.style.cssText =
            "text-decoration:none;text-align:center;padding:7px 12px;cursor:pointer;";

          wrap.append(who, out);
          el.appendChild(wrap);
        },
      });
    } catch (e) {
      console.warn("[lighthouse-identity] sidebar tab registration failed:", e);
    }
  },
});
