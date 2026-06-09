# syntax=docker/dockerfile:1

# lighthouse — ComfyUI orchestrator-only master for the Lighthouse image-gen path.
#
# Bundles stock ComfyUI + a *vetted node allowlist* (ComfyUI-Distributed, ComfyUI-Sentinel)
# baked at build time. ComfyUI-Manager is deliberately NOT installed, so runtime node
# install is impossible by construction — new nodes require a reviewed image rebuild.
#
# The master runs CPU-only torch and never owns a GPU: ComfyUI-Distributed dispatches
# renders to the Windows/WSL2 GPU workers. See gavinmcfall/home-ops cortex/comfyui-master.

FROM docker.io/library/python:3.12-slim-bookworm

ARG VENDOR
ARG VERSION
ARG DISTRIBUTED_REF
# Phase-2a curated node packs — pinned by commit SHA (vetted 2026-06-06).
ARG IMPACT_PACK_REF
ARG IMPACT_SUBPACK_REF
ARG USDU_REF
ARG CONTROLNET_AUX_REF
ARG IPADAPTER_REF

ENV \
    PIP_BREAK_SYSTEM_PACKAGES=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_ROOT_USER_ACTION=ignore \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    HOME=/app \
    COMFYUI_PORT=8188

WORKDIR /app

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
        patch \
        tini; \
    rm -rf /var/lib/apt/lists/*

# Vendor patches applied to pinned upstream during build. Patch directory pinned
# in this repo (containers/apps/lighthouse/patches/) so changes are explicit,
# reviewable, and CI fails loudly if a patch stops applying after an upstream
# version bump. See specific patch headers for context + upstream PR status.
COPY patches /patches
# Frontend compat assets (e.g. the clipspace shim) baked into the image, NOT the
# security proxy — they belong in the frontend serving path.
COPY web /web

# ComfyUI core (pinned tag). CPU-only torch installed FIRST so ComfyUI's unpinned
# `torch` requirement resolves to the already-present CPU build (no CUDA pull).
RUN set -eux; \
    git clone --depth 1 --branch "v${VERSION}" \
        https://github.com/comfyanonymous/ComfyUI.git /app; \
    pip install --index-url https://download.pytorch.org/whl/cpu \
        torch torchvision torchaudio; \
    pip install -r requirements.txt

# Vetted node allowlist — pinned by commit SHA. No ComfyUI-Manager => no runtime install.
# Per-user output isolation is NOT done here: it lives in the licence-gate proxy in front
# of this master (ADR 015 — proxy owns per-user output bucketing, reads + writes), so the
# deprecated ComfyUI-Sentinel auth plugin is deliberately absent.
RUN set -eux; \
    git clone https://github.com/robertvoy/ComfyUI-Distributed.git \
        custom_nodes/ComfyUI-Distributed; \
    git -C custom_nodes/ComfyUI-Distributed checkout "${DISTRIBUTED_REF}"; \
    # Vendor patches applied to ComfyUI-Distributed. If a patch stops applying
    # cleanly after a DISTRIBUTED_REF bump, this RUN step fails loudly — that's
    # the signal to rebase the patch against the new upstream version.
    for p in /patches/*.patch; do \
        if [ -f "$p" ]; then \
            echo "applying $p"; \
            patch -p1 --directory=custom_nodes/ComfyUI-Distributed --no-backup-if-mismatch < "$p"; \
        fi; \
    done; \
    for d in custom_nodes/*/; do \
        if [ -f "$d/requirements.txt" ]; then pip install -r "$d/requirements.txt"; fi; \
    done; \
    find custom_nodes -type d -name .git -prune -exec rm -rf {} +

# Phase-2a curated node packs — pinned by commit SHA, vetted 2026-06-06 (security
# reports in lighthouse IMPL-STATUS). The licence-gate §7 allowlist (default-deny)
# admits only the specific vetted class_types; everything else these packs register
# stays denied. Model auto-download is suppressed (skip_download_model sentinel +
# HF_HUB_OFFLINE at runtime); all weights are pre-staged on the worker model dirs.
# This master is orchestrator-only (--cpu) and needs the nodes present so it can
# VALIDATE graphs; the actual render runs on the GPU workers (symmetric install).
RUN set -eux; \
    # Impact-Pack + Subpack — FaceDetailer / SAMLoader / UltralyticsDetectorProvider.
    git clone https://github.com/ltdrdata/ComfyUI-Impact-Pack custom_nodes/ComfyUI-Impact-Pack; \
    git -C custom_nodes/ComfyUI-Impact-Pack checkout "${IMPACT_PACK_REF}"; \
    git clone https://github.com/ltdrdata/ComfyUI-Impact-Subpack custom_nodes/ComfyUI-Impact-Subpack; \
    git -C custom_nodes/ComfyUI-Impact-Subpack checkout "${IMPACT_SUBPACK_REF}"; \
    # Drop the unpinned `git+…/sam2` dep (SAM2 unneeded for vit_b; supply-chain risk).
    sed -i '/sam2/d' custom_nodes/ComfyUI-Impact-Pack/requirements.txt; \
    # Suppress install-time + first-run model auto-download (weights pre-staged).
    touch custom_nodes/skip_download_model; \
    # Ultimate SD Upscale — the vendored A1111 USDU script is a git submodule; init it
    # so the `__init__.py` runtime master.zip fallback never fires (egress-safe).
    git clone https://github.com/ssitu/ComfyUI_UltimateSDUpscale custom_nodes/ComfyUI_UltimateSDUpscale; \
    git -C custom_nodes/ComfyUI_UltimateSDUpscale checkout "${USDU_REF}"; \
    git -C custom_nodes/ComfyUI_UltimateSDUpscale submodule update --init --recursive; \
    # ControlNet aux preprocessors — only Canny + DepthAnythingV2 get allowlisted.
    git clone https://github.com/Fannovel16/comfyui_controlnet_aux custom_nodes/comfyui_controlnet_aux; \
    git -C custom_nodes/comfyui_controlnet_aux checkout "${CONTROLNET_AUX_REF}"; \
    # IPAdapter plus — no insightface; FaceID nodes left un-allowlisted.
    git clone https://github.com/cubiq/ComfyUI_IPAdapter_plus custom_nodes/ComfyUI_IPAdapter_plus; \
    git -C custom_nodes/ComfyUI_IPAdapter_plus checkout "${IPADAPTER_REF}"; \
    # Install each pack's runtime deps (sam2 line stripped; USDU + IPAdapter declare none).
    for d in custom_nodes/ComfyUI-Impact-Pack custom_nodes/ComfyUI-Impact-Subpack custom_nodes/comfyui_controlnet_aux; do \
        if [ -f "$d/requirements.txt" ]; then pip install -r "$d/requirements.txt"; fi; \
    done; \
    # ultralytics + comfyui_controlnet_aux pull opencv-python (the GUI build), whose cv2
    # needs libGL/libxcb system libs absent from this slim base — so Impact-Pack and
    # controlnet_aux fail `import cv2` and don't register. This master is headless and
    # only needs the nodes importable to VALIDATE graphs; normalise to the headless cv2
    # build (identical API, no GUI/GL libs). Done last so no transitive dep reasserts the
    # GUI build. The import gate below fails the build loudly if cv2 ever can't load again.
    pip uninstall -y opencv-python opencv-contrib-python 2>/dev/null || true; \
    pip install --force-reinstall --no-cache-dir opencv-python-headless; \
    python -c "import cv2; print('cv2 headless OK', cv2.__version__)"; \
    # Build-time gate: USDU submodule must be physically present, else it egresses on import.
    test -f custom_nodes/ComfyUI_UltimateSDUpscale/repositories/ultimate_sd_upscale/scripts/ultimate-upscale.py; \
    find custom_nodes -type d -name .git -prune -exec rm -rf {} +

# Restore /extensions/core/clipspace.js for ComfyUI-Impact-Pack's SAM mask editor.
# The bundled SPA frontend (>=1.43) no longer ships this legacy module; Impact-Pack
# still imports ClipspaceDialog from it, so it 404s and the editor won't load. We bake
# a faithful compat shim into the frontend package's static extensions/core dir — a
# frontend asset served by ComfyUI's own static handler (correct text/javascript),
# deliberately NOT served from the licence-gate (security stays out of frontend assets).
# The test -d/-f gates fail the build loudly if the frontend layout ever moves.
RUN set -eux; \
    CORE_DIR="$(python -c 'import comfyui_frontend_package, os; print(os.path.join(os.path.dirname(comfyui_frontend_package.__file__), "static", "extensions", "core"))')"; \
    test -d "$CORE_DIR"; \
    cp /web/clipspace-compat.js "$CORE_DIR/clipspace.js"; \
    test -f "$CORE_DIR/clipspace.js"

# Lighthouse identity badge — a web-only custom node (no graph nodes) that shows
# "Signed in as <user> · Sign out" sourced from oauth2-proxy's /oauth2/userinfo.
# ComfyUI auto-serves + loads WEB_DIRECTORY js, so no frontend-package surgery.
RUN set -eux; \
    cp -r /web/lighthouse-identity custom_nodes/lighthouse-identity; \
    test -f custom_nodes/lighthouse-identity/js/identity-badge.js

# A passwd/group entry for the non-root UID — torch's getpass.getuser() (inductor
# cache setup) calls getpwuid(568) on import and crashes without it. HOME=/app above.
RUN set -eux; \
    echo 'comfyui:x:568:568::/app:/usr/sbin/nologin' >> /etc/passwd; \
    echo 'comfyui:x:568:' >> /etc/group

# Storage mount points (Rook-Ceph RWX PVCs are mounted here at runtime).
RUN set -eux; \
    mkdir -p /app/models /app/output /app/input /app/user /app/temp; \
    chown -R 568:568 /app

USER 568:568
EXPOSE 8188
VOLUME ["/app/models", "/app/output", "/app/user"]

ENTRYPOINT ["tini", "--"]
# --cpu: never claim a local GPU (orchestrator-only; Distributed routes renders to workers).
# Orchestrator-only "never render locally" is finalised in the Distributed plugin config
# (master excluded from the worker pool) — see the home-ops HelmRelease.
CMD ["python", "main.py", "--listen", "0.0.0.0", "--port", "8188", "--cpu", "--disable-auto-launch"]
