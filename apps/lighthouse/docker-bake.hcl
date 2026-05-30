target "docker-metadata-action" {}

variable "APP" {
  default = "lighthouse"
}

variable "VERSION" {
  // renovate: datasource=github-releases depName=comfyanonymous/ComfyUI extractVersion=^v(?<version>.+)$
  default = "0.22.0"
}

variable "DISTRIBUTED_REF" {
  // renovate: datasource=git-refs depName=ComfyUI-Distributed packageName=https://github.com/robertvoy/ComfyUI-Distributed
  default = "071d1c1d93f39448eebe93e23f209a69d232e1d8"
}

variable "SOURCE" {
  default = "https://github.com/comfyanonymous/ComfyUI"
}

group "default" {
  targets = ["image-local"]
}

target "image" {
  inherits   = ["docker-metadata-action"]
  context    = "apps/lighthouse"
  dockerfile = "Dockerfile"
  args = {
    VERSION         = "${VERSION}"
    DISTRIBUTED_REF = "${DISTRIBUTED_REF}"
  }
  labels = {
    "org.opencontainers.image.source" = "${SOURCE}"
  }
}

target "image-local" {
  inherits = ["image"]
  output   = ["type=docker"]
  tags     = ["${APP}:${VERSION}"]
}

target "image-all" {
  inherits  = ["image"]
  platforms = ["linux/amd64"]
}
