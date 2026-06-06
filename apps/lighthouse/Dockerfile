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
    # Build-time gate: USDU submodule must be physically present, else it egresses on import.
    test -f custom_nodes/ComfyUI_UltimateSDUpscale/repositories/ultimate_sd_upscale/scripts/ultimate-upscale.py; \
    find custom_nodes -type d -name .git -prune -exec rm -rf {} +

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
