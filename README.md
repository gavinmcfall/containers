# containers

Custom container images, built and published to `ghcr.io/gavinmcfall/<app>` with
semantic-version tags. Layout and versioning follow
[home-operations/containers](https://github.com/home-operations/containers).

## Layout

```
apps/<app>/
├── Dockerfile          # the build
└── docker-bake.hcl     # APP + VERSION (+ pins), Renovate-tracked
```

## Build / versioning

`.github/workflows/build.yaml` detects changed `apps/**`, reads `VERSION` from each
app's `docker-bake.hcl`, and uses `docker/metadata-action` to publish:

| Trigger | What happens |
|---|---|
| Pull request | Build only (validates the Dockerfile); no push |
| Push to `main` | Build + push `:{{version}}`, `:{{major}}.{{minor}}`, `:{{major}}`, `:rolling`, `:sha-<sha>` |

Upstream versions/pins are bumped by Renovate via the `// renovate:` comments in each
`docker-bake.hcl`.

## Apps

### `lighthouse`

ComfyUI **orchestrator-only master** for the Lighthouse image-gen path
(deployed in `gavinmcfall/home-ops` under `cortex/comfyui-master`). Bundles:

- **ComfyUI** (`v0.22.0`, GPL-3.0)
- **ComfyUI-Distributed** (Apache-2.0) — farms renders out to the Windows/WSL2 GPU workers

The vetted nodes are baked at build time; **ComfyUI-Manager is deliberately absent**, so
runtime node install is impossible — new nodes require a reviewed image rebuild. The master
runs CPU-only torch and never owns a GPU.

Per-user output isolation is **not** an in-image auth plugin (the formerly-planned
ComfyUI-Sentinel is deprecated, and identity is owned by Pocket-ID/Envoy upstream). It lives
in the **licence-gate proxy** in front of this master, which owns per-user output bucketing
for both reads and writes — see ADR 015 in `gavinmcfall/lighthouse`.

> Licences: the images bundle GPL-3.0 software (ComfyUI). The build files in this repo are
> MIT; the published images are governed by their bundled components' licences.
