// /extensions/core/clipspace.js — compatibility shim (baked into the master image).
//
// WHY: the bundled ComfyUI SPA frontend (>= 1.43) no longer ships this legacy
// module — `extensions/core/clipspace.ts` has no top-level export, so the frontend
// build emits no back-compat shim and there is no `/extensions/core/clipspace.js`
// in the package. ComfyUI-Impact-Pack's `js/impact-sam-editor.js` still does
// `import { ClipspaceDialog } from "../../extensions/core/clipspace.js"`, so the
// request 404s and the browser blocks the module (strict MIME) → the SAM mask
// editor never loads.
//
// FIX: this file restores `/extensions/core/clipspace.js` with a faithful
// re-implementation of the only two members impact-sam-editor.js uses
// (`ClipspaceDialog.registerButton` + `.invalidatePreview`), built on the public
// `ComfyApp` global. It is a frontend asset (served by ComfyUI's own static
// handler with text/javascript), NOT security — deliberately NOT in the
// licence-gate proxy. Survives Impact-Pack bumps untouched. No upstream version
// fixes the import (the broken line exists in every Impact-Pack release).
//
// If a future pinned node imports richer ClipspaceDialog members
// (`.instance`, `.show()`, the copy-to-clipspace UI), widen this shim.

const ComfyApp = window.comfyAPI.app.ComfyApp;

export class ClipspaceDialog {
  static items = [];

  static registerButton(name, contextPredicate, callback) {
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = name;
    button.contextPredicate = contextPredicate;
    button.onclick = callback;
    ClipspaceDialog.items.push(button);
  }

  static invalidatePreview() {
    const clipspace = ComfyApp.clipspace;
    if (!clipspace?.imgs?.length) return;
    const preview = document.getElementById("clipspace_preview");
    if (!preview) return;
    const img = clipspace.imgs[clipspace.selectedIndex];
    if (!img) return;
    preview.src = img.src;
    preview.style.maxHeight = "100%";
    preview.style.maxWidth = "100%";
  }
}
