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

// Phase-2a curated node packs — pinned by commit SHA, vetted 2026-06-06.
// Re-vet on any bump: Impact-Pack/Subpack ship tags; the other three are rolling main.
variable "IMPACT_PACK_REF" {
  // renovate: datasource=git-refs depName=ComfyUI-Impact-Pack packageName=https://github.com/ltdrdata/ComfyUI-Impact-Pack
  default = "ba09fbc4c05688667e7bfda35823933c9fe09196"
}

variable "IMPACT_SUBPACK_REF" {
  // renovate: datasource=git-refs depName=ComfyUI-Impact-Subpack packageName=https://github.com/ltdrdata/ComfyUI-Impact-Subpack
  default = "5b4e55058ae48e18e1c6d974000461ad1e240135"
}

variable "USDU_REF" {
  // renovate: datasource=git-refs depName=ComfyUI_UltimateSDUpscale packageName=https://github.com/ssitu/ComfyUI_UltimateSDUpscale
  default = "bebd5696fddd61cb0d08949a222c508898ab5577"
}

variable "CONTROLNET_AUX_REF" {
  // renovate: datasource=git-refs depName=comfyui_controlnet_aux packageName=https://github.com/Fannovel16/comfyui_controlnet_aux
  default = "e8b689a513c3e6b63edc44066560ca5919c0576e"
}

variable "IPADAPTER_REF" {
  // renovate: datasource=git-refs depName=ComfyUI_IPAdapter_plus packageName=https://github.com/cubiq/ComfyUI_IPAdapter_plus
  default = "a0f451a5113cf9becb0847b92884cb10cbdec0ef"
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
    VERSION            = "${VERSION}"
    DISTRIBUTED_REF    = "${DISTRIBUTED_REF}"
    IMPACT_PACK_REF    = "${IMPACT_PACK_REF}"
    IMPACT_SUBPACK_REF = "${IMPACT_SUBPACK_REF}"
    USDU_REF           = "${USDU_REF}"
    CONTROLNET_AUX_REF = "${CONTROLNET_AUX_REF}"
    IPADAPTER_REF      = "${IPADAPTER_REF}"
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
