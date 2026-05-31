# licence-gate — build STATUS & resume guide

> Durable resume doc for the Lighthouse licence-gate proxy build. If you're a
> fresh session picking this up: read this + `CONTRACT-NOTES.md` here, then the
> declaration in `~/my_other_repos/lighthouse` (pull latest; cluster-deploy
> target is `docs/operations/cluster-deploy-contract.md`).

## Who / what

- **I am Chat B**, the implementer. Lighthouse image-gen lands in `gavinmcfall/home-ops`; the **declaration/contract** lives in `gavinmcfall/lighthouse` (read-only for me — Chat A owns it). Bespoke images live in **`gavinmcfall/containers`** (this repo, public monorepo; `lighthouse` ComfyUI master image already published).
- **Milestone (Gavin-approved 2026-05-31):** drive image-gen **end-to-end on the heavy tier** (gpu-vengeance + gpu-vixen, both Gate-3 proven). Plans 3–6 + light tier deferred.

## Done

- ✅ `apps/lighthouse` ComfyUI master image PUBLISHED: `ghcr.io/gavinmcfall/lighthouse:0.22.0@sha256:16a45ddfa747f270ec38cf1c33b8132441e70db8bd327ace34b9617070dd06b4` (ComfyUI v0.22.0 + ComfyUI-Distributed, CPU torch, no Manager, Sentinel dropped per ADR 015).
- ✅ **licence-gate logic core — 7 packages, all `go test ./...` green, race-clean** (this dir, `internal/`):
  | pkg | what |
  |---|---|
  | `workflow` | `Parse` POST /prompt; ONE `Allowlist=map[string]Spec{ModelFields[]{Field,Folder}, OutputField}`; `DefaultAllowlist` (intrinsic node table); `LoadAllowlist(json)`; `Graph.Validate` = **default-deny** unknown class_type; `Graph.ModelReferences` |
  | `registry` | `Load(json)` + `Resolve(filename)→(Entry,bool)`; Entry{Filename,SHA256,Licence,CommercialOK,TierFit,Substitutes} |
  | `gate` | `Evaluate(refs,reg,Job{Commercial})→Decision{Allowed,Action,Violations}` — commercial-by-default; ActionAllow/ActionReject, ReasonNonCommercial/ReasonUnknown |
  | `identity` | `ResolveCommercial(class,personalFlag)→(bool,rationale)`; `ParseAuth(authHeader,brandToken)→Identity{User,Class,Groups}` (decode-only JWT; brand-token special case); `Identity.HasGroup` |
  | `isolation` | `RewriteOutputs(g,user,allow)` write-side; `ScopeView(user,filename,subfolder,typ)` read-side confine; `PromptOwners` LRU `Remember`/`Owner` (evicted→deny) |
  | `audit` | `NewWriter(io.Writer)`+`Write(Record)` — one JSON line/record to stdout (ADR 016); Record = identity-half + gate-half + ts/prompt_id |

  NOT committed/pushed until the Dockerfile existed (CI would fail). **Now committable — see §Done below.**

- ✅ **HTTP reverse-proxy server DONE** (2026-06-01, TDD, `internal/proxy` 12 tests green + race-clean; `main.go` config-loader tested). Listens `:8000` → ComfyUI `:8188`. Routes:
  - **POST `/prompt`**: `ParseAuth` (401 if missing) → personal flag via `X-Lighthouse-Personal` header → `ResolveCommercial` → `Parse` → `Validate` (400 on unknown node) → `ModelReferences` → `gate.Evaluate` → **403 + violations/substitutes JSON + audit** OR **allow**: `RewriteOutputs(<user>/)` (400 on unsafe prefix) → re-encode envelope (preserves `client_id`/`extra_data`) → forward → capture `prompt_id` → `Owners.Remember` → `audit.Write`.
  - **GET `/view`**: `ScopeView` → **canonical 404** on deny; upstream 404 is **normalised to the same canonical bytes** (denied ≡ genuine-404, asserted byte-identical — no existence side-channel). Else stream.
  - **GET `/history`** (object filter) + **GET `/queue`** (running/pending tuple filter, prompt_id at index 1): keep only entries `Owners.Owner()` == caller; unknown→drop.
  - `/userdata`, `/upload/image`, `/ws`: **passthrough — documented Phase-1 known-gap** (heavy-tier runs trusted admin+brand; tighten later).
- ✅ **Dockerfile + docker-bake.hcl DONE** — distroless static nonroot, base images digest-pinned + renovate comments; builds `licence-gate:0.1.0`, boots clean, 401s unauth. The existing `build.yaml` (change-detect + matrix + reads VERSION from bake + `image-all`) picks it up automatically → publishes `ghcr.io/gavinmcfall/licence-gate` on push to main. **No build.yaml edit needed.**
- ✅ **model-serve sidecar DONE** (`apps/model-serve`, TDD, race-clean, smoke-verified): token-auth (constant-time, fail-closed on empty) `GET /models/<path>` streaming from the PVC, `:9090`, traversal-safe, no dir listing, `/healthz` open. Dockerfile/bake mirror licence-gate → publishes `ghcr.io/gavinmcfall/model-serve`.
- ✅ **Chat A review notes (2026-06-01) actioned**: (1) decode-not-verify pinned in CONTRACT-NOTES as load-bearing on Envoy-verify + only-via-Envoy NetPol; (2) brand token now `crypto/subtle` constant-time + never logged; (3) audit composition confirmed.

## Next (in order)

### 1. Commit + push (publishes both images)
Commit `apps/licence-gate/**` + `apps/model-serve/**` (own files only). Push to main → CI builds + publishes `ghcr.io/gavinmcfall/licence-gate:0.1.0` and `…/model-serve:0.1.0`. **Gavin gate:** push triggers a public CI publish — confirm before pushing.

### 2. home-ops HelmRelease (cluster-deploy strand)
Translate `cluster-deploy-contract.md` into BJW-S app-template YAML. **VERIFY the real ComfyUI-Distributed orchestrator-only mechanism against the shipped image first** (contract §3 guesses `COMFYUI_ORCHESTRATOR_ONLY`). **Resolve the namespace conflict** (Gavin said `cortex`; contract says `lighthouse`).

### 3. Seed a minimal SDXL `workshop` workflow JSON
Standard graph (CheckpointLoaderSimple→CLIPTextEncode×2→EmptyLatentImage→KSampler→VAEDecode→SaveImage) to seed the allowlist for the first smoke (Gavin can't export real workflows until ComfyUI's deployed). All those nodes are already in `DefaultAllowlist`.

### Then: smoke test (`docs/operations/heavy-tier-e2e-smoke-test.md`) S1/S2 + A1–A5 → tick Plan 1.

## Gavin's tasks (post-deploy; I cue him)
Models downloaded **in-container after cluster live** (his choice) → so model-serve is on the critical path. Then: worker `:8188` firewall (Vengeance+Vixen, allow-only-from-cluster), `wol.ps1` wake at smoke, eyeball OIDC login + confirm JWT carries `groups` claim. (OIDC client, all 6 1P secrets incl worker token, Phase-0 Gate-3 = already DONE.)

## Coordination — OpenRoom (see CONTRACT-NOTES + memory)
Relay `wss://openroom.nerdz.cloud`, room `lighthouse`. **Receive is flaky** (listener drops, NO history replay) → Gavin bridges. To reconnect: `openroom-room join "lighthouse"` (bg) + a Monitor tailing its output; **send** via `OPENROOM_RELAY=wss://openroom.nerdz.cloud openroom send "lighthouse" "<msg>"`. My messages are signed `[Chat B / home-ops impl …]`.
