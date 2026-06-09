# Lighthouse identity badge — a WEB-ONLY custom node (no graph nodes, so no §7
# allowlist surface). It only ships a frontend JS extension that shows "Signed in
# as <user> · Sign out", reading identity from oauth2-proxy's /oauth2/userinfo and
# linking sign-out to /oauth2/sign_out. ComfyUI auto-serves + loads WEB_DIRECTORY.
WEB_DIRECTORY = "./js"
NODE_CLASS_MAPPINGS = {}
NODE_DISPLAY_NAME_MAPPINGS = {}
__all__ = ["NODE_CLASS_MAPPINGS", "NODE_DISPLAY_NAME_MAPPINGS", "WEB_DIRECTORY"]
