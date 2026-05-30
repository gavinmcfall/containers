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
        tini; \
    rm -rf /var/lib/apt/lists/*

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
    for d in custom_nodes/*/; do \
        if [ -f "$d/requirements.txt" ]; then pip install -r "$d/requirements.txt"; fi; \
    done; \
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
