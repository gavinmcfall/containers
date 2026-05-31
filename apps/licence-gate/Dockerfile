# syntax=docker/dockerfile:1

# licence-gate — Lighthouse licence-gate reverse proxy.
#
# The single HTTP entrypoint in front of the ComfyUI master: authenticates
# (decode-only; Envoy verifies the JWT upstream), licence-gates renders
# (commercial-by-default), scopes every output to the caller's bucket, and audits
# each render to stdout (ADR 015/016). GPL separation from ComfyUI is by design —
# this is a standalone Go service, not a ComfyUI plugin.
#
# Pure-stdlib Go (no external modules, no go.sum); static CGO-free binary on
# distroless. See gavinmcfall/home-ops cortex (or lighthouse ns) HelmRelease.

# renovate: datasource=docker depName=golang
ARG GO_IMAGE=golang:1.22-bookworm@sha256:3d699e4d15d0f8f13c9195c0632a16702b8cbdece2955af1c23b37ae5d55a253

FROM ${GO_IMAGE} AS build
WORKDIR /src
# Copy module metadata first for layer caching (stdlib-only ⇒ download is a no-op).
COPY go.mod ./
COPY . .
RUN set -eux; \
    CGO_ENABLED=0 GOOS=linux go build \
        -trimpath -ldflags="-s -w" \
        -o /out/licence-gate .

# renovate: datasource=docker depName=gcr.io/distroless/static-debian12
FROM gcr.io/distroless/static-debian12:nonroot@sha256:d093aa3e30dbadd3efe1310db061a14da60299baff8450a17fe0ccc514a16639

COPY --from=build /out/licence-gate /usr/local/bin/licence-gate

# distroless :nonroot runs as 65532; the pod securityContext pins runAsUser=568
# (shared fsGroup with the ComfyUI master) at deploy time.
EXPOSE 8000
ENTRYPOINT ["/usr/local/bin/licence-gate"]
