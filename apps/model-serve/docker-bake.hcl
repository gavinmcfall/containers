target "docker-metadata-action" {}

variable "APP" {
  default = "model-serve"
}

variable "VERSION" {
  // First-party app — semver bumped by hand on release (no upstream datasource).
  default = "0.1.0"
}

variable "SOURCE" {
  default = "https://github.com/gavinmcfall/containers"
}

group "default" {
  targets = ["image-local"]
}

target "image" {
  inherits   = ["docker-metadata-action"]
  context    = "apps/model-serve"
  dockerfile = "Dockerfile"
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
