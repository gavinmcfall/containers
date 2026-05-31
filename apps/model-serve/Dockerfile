# syntax=docker/dockerfile:1

# model-serve — Lighthouse model-store sidecar (ADR 018).
#
# Read-only, token-authenticated HTTP file server in front of the
# cluster-canonical models PVC. GPU workers fetch model files, cache locally,
# and verify the sha256 worker-side. Pure-stdlib Go; static CGO-free binary on
# distroless.

# renovate: datasource=docker depName=golang
ARG GO_IMAGE=golang:1.22-bookworm@sha256:3d699e4d15d0f8f13c9195c0632a16702b8cbdece2955af1c23b37ae5d55a253

FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN set -eux; \
    CGO_ENABLED=0 GOOS=linux go build \
        -trimpath -ldflags="-s -w" \
        -o /out/model-serve .

# renovate: datasource=docker depName=gcr.io/distroless/static-debian12
FROM gcr.io/distroless/static-debian12:nonroot@sha256:d093aa3e30dbadd3efe1310db061a14da60299baff8450a17fe0ccc514a16639

COPY --from=build /out/model-serve /usr/local/bin/model-serve

EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/model-serve"]
