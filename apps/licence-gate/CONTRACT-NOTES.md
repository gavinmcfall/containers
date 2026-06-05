# licence-gate proxy — contract notes

Build-time findings that pin the proxy contract. Keep with the code; the
durable design lives in `gavinmcfall/lighthouse` (ADR 008, ADR 015, flow 3).

## Every gated route has an `/api`-prefixed twin — match BOTH (v0.1.5, Plan-1b HAR 2026-06-04)

ComfyUI's web frontend prepends `/api` to **every** native API call:
`api.apiURL(p) => p.startsWith("/api") ? base+p : base+"/api"+p`. And master
mirrors every route under both `/` and `/api` (`server.py`: `api_routes.route(m,
"/api"+route.path)` for all routes). So a browser hits `/api/prompt`, `/api/view`,
`/api/history`, `/api/queue`, `/api/userdata`; the ComfyUI-Distributed plugin
(its own apiClient, no prefix) hits `/distributed/queue`.

**This bit hard:** through v0.1.4 the proxy matched only the BARE paths. Every
browser request therefore fell through to passthrough — **bypassing the licence
gate (`/api/prompt`) AND per-user isolation (`/api/view`, `/api/history`,
`/api/queue`, `/api/userdata`).** The only reason renders were still gated is that
Lighthouse dispatches via the plugin's `/distributed/queue` (no `/api`). The
visible symptom was the workflow-save 405 (`/api/userdata`); the silent ones were
cross-user image/history/queue reads and an ungated native `/api/prompt`.

`ServeHTTP` now normalises a leading `/api` segment for MATCHING only
(`routePath := strings.TrimPrefix(path, "/api")` guarded to the `/api/` segment),
then forwards the ORIGINAL `r.URL.Path` — master serves both spellings, so no
wire rewrite is needed for prompt/view/history/queue. `RewriteUserdataURL` is
prefix-aware (`/api/userdata` or `/userdata`) and PRESERVES the matched prefix.
Guards: `TestPromptApiPrefixGated`, `TestViewApiPrefixScoped`,
`TestUserdataApiPrefixScopes` + isolation `…_ApiPrefix*`.

> Lesson: when fronting an app whose client uses a path prefix convention, every
> security check must match all spellings the client/server accept. Test through
> the real frontend path, not an in-pod curl that happens to use the bare path.

## Worker→master output data-flow (ADR 015 scope item 3) — RESOLVED 2026-05-30

**Finding:** ComfyUI-Distributed collects results **on the master**. The
*Distributed Collector* node (placed after VAE Decode) "Collects results
(image/video frames and optionally audio) from workers **back to the master**"
([Distributed README](https://github.com/robertvoy/ComfyUI-Distributed),
pin `071d1c1d`). Workers render and stream frames back; the final `SaveImage`
and `/view` serving happen **master-side**. Workers do **not** write outputs
directly to shared storage.

**Consequence for the proxy contract (the boundary does NOT move):**

- **Writes** — the proxy rewrites `filename_prefix` on allowlisted output-saver
  nodes at `POST /prompt`, *before* dispatch. The master's `SaveImage` then
  writes to `/output/<user>/…` on the master's Rook-Ceph RWX PVC. Per-user
  bucketing is achieved at submission time regardless of which GPU rendered the
  frames, because the prefix travels in the workflow graph.
- **Reads** — `/view`, `/history`, `/queue`, `/userdata`, `/upload/image` are
  served by the master; the proxy (in front of the master) scopes them to the
  caller's bucket. No need to proxy reads out to individual workers.
- **Write-side test shape is unchanged** by this finding: assert the proxy
  rewrites `filename_prefix` → `<user>/<original>` and rejects non-allowlisted
  savers / path-bearing filenames before exec.

> If a future workflow pattern bypasses the collector and saves on a worker
> directly, that worker would need the same Ceph `/output` mount + the proxy
> contract would extend to it. Not the case for the orchestrator-only master
> topology we deploy.

## Contract Qs — answered by Chat A (review 2026-05-30)

**Registry hash = integrity check, not a resolve key.** Resolve is by filename
(the workflow JSON only carries the filename). The `sha256` is verified at **PVC
mount time** (or on detected mount change), not per-request — per-request hashing
adds latency for no real gain.
- **home-ops action:** mount the models PVC **ReadOnlyMany** for master + worker
  pods. The substitution threat only exists if a runtime pod can write the model
  store; RO closes it. The hash check is then belt-and-braces against admin-path
  tampering between CI and cluster, not the primary defense.

**`commercial` intent resolved in the identity layer, not the gate.** `gate.Evaluate`
stays policy-pure (`Job{Commercial bool}`). The identity layer translates raw JWT
claim + per-request personal-flag → final `Commercial bool` (fail-safe: forgetting
the flag → commercial → safe model). Identity-policy unit fixtures:
- brand/Dopamine-Racing claim + personal flag → flag IGNORED → `commercial=true`
- family claim + personal flag → flag HONOURED → `commercial=false`
- service-account → always `commercial=true`
- missing flag → `commercial=true`

## Node allowlist — ONE unified map, default-deny (Chat A confirmed 2026-05-30)

There is **one** node-class allowlist with per-class metadata, NOT two parallel
ones (loader + saver). A class_type absent from the map is **rejected pre-exec**
(default-deny — closes loader-smuggle, saver-escape, and any unreasoned class).

    class_type → {
      model_fields: [{field, folder}, ...]  // if it loads a model (licence gate parser)
      output_field: "filename_prefix"        // if it writes a file (proxy rewrite)
      // both = rare; neither = most nodes (KSampler, EmptyLatentImage…) — still
      // allowlisted, just no special handling
    }

**REFACTOR DONE (2026-06-01):** merged into the single `workflow.Allowlist`
(`class_type → Spec{ModelFields, OutputField}`); `DefaultAllowlist` is the
intrinsic table. `Graph.Validate` rejects any class_type not in the map
(default-deny). Phase-1 permitted-set = the nodes a standard SDXL workshop graph
needs (loaders + the three image savers + KSampler/CLIPTextEncode/
EmptyLatentImage/VAEDecode), no more.

### Saver enumeration (ComfyUI v0.22.0) — escape vectors NOT on the Phase-1 set

Output-emitting nodes with a filename field, verified against the shipped image:
`SaveImage`, `SaveAnimatedWEBP`, `SaveAnimatedPNG` (images — **the only Phase-1
savers**); `SaveAudio`/`SaveAudioMP3`/`SaveAudioOpus`, `SaveVideo`/`SaveWEBM`,
`SaveLatent`, `SaveSVGNode`; and **model-writers `CheckpointSave`/`LoraSave`/
`ModelSave`/`VAESave`/`CLIPSave`/`ImageOnlyCheckpointSave`** — these write model
files (an escape vector). All non-image savers are blocked by the allowlist
(pre-exec) AND, for model-writers, by the ReadOnlyMany models PVC (defense-in-depth).
`SaveImageWebsocket` has no file field (WS-only; WS progress scoping is a deferred
Phase-1 known-gap).

## The GPU render path is `/distributed/queue`, NOT just `/prompt` (verified 2026-06-01, v0.1.1)

ComfyUI-Distributed only dispatches a render to remote GPU workers when the
workflow contains a `DistributedCollector` node AND the request hits the plugin's
**custom endpoint `POST /distributed/queue`** (`api/job_routes.py:206` →
`orchestrate_distributed_execution`). A plain `POST /prompt` runs the whole graph
locally on the master (which is `--cpu`). The `/distributed/queue` body carries
the same graph under `"prompt"` (plus `client_id`, `enabled_worker_ids`,
`delegate_master`) and returns a `prompt_id` — so the proxy routes BOTH `/prompt`
and `/distributed/queue` through the identical gate→rewrite→remember→audit
pipeline. **Gating only `/prompt` would let any authenticated caller bypass the
licence gate + output isolation by using the distributed endpoint** — that was a
real hole, closed in v0.1.1 (`TestDistributedQueue*`). The node allowlist must
also permit `DistributedCollector` (+ `DistributedSeed`) or Validate rejects the
distributed workflow — delivered via the mounted allowlist ConfigMap, not the
built-in default.

## Auth is DECODE-ONLY — load-bearing on two upstream guarantees (Chat A review note, 2026-06-01)

`identity.ParseAuth` **decodes** the Pocket-ID JWT for claims (sub/email/groups);
it does **not** cryptographically verify the signature. The bogus-signature test
(`TestParseAuthIgnoresSignature`) asserts this on purpose. This is safe **only**
because BOTH of these hold — weaken either and decode-not-verify becomes a
forgeable-identity vulnerability:

1. **Envoy SecurityPolicy verifies the JWT upstream** (OIDC via Pocket-ID,
   `forwardAccessToken`) — a request reaching the proxy already has a
   cryptographically valid token.
2. **Cilium NetworkPolicy makes the proxy reachable *only* via Envoy** (no
   pod-to-pod bypass path to `:8000`) — nothing can hand the proxy an unverified
   token.

> If a future change exposes the proxy port directly, adds an ingress that skips
> Envoy, or removes the SecurityPolicy JWT validation, `ParseAuth` MUST be
> upgraded to verify signatures (fetch Pocket-ID JWKS) first. Do not relax either
> layer without that change.

**Brand token (`LIGHTHOUSE_DR_TOKEN`) is compared constant-time** (`crypto/subtle`,
2026-06-01) so a timing side-channel can't recover it byte-by-byte. The raw bearer
is never logged (the audit record carries `user`/`model`, never the token).

**Audit record (§6) is composed by the audit writer from two halves:**
- identity layer emits {user, identity-class (brand/family/service), raw personal
  flag, resolved `Commercial` bool + rationale}
- gate emits {model, licence, gate-decision}
- audit writer joins them + timestamp → `{user, model, licence, commercial?,
  gate-decision, timestamp}`. Keeps gate identity-agnostic and identity-policy
  licence-agnostic — both independently unit-testable.

## /userdata scoping injects per-caller bucket; NORMALIZE %2F, never assume it survives (v0.1.3 + v0.1.4, Plan 1b 2026-06-04)

ComfyUI persists workflow files via `/userdata/{file}` — `POST` to write, `GET`
to read/list, `DELETE` to delete, `POST .../move/{dest}` to rename. Master has
**no per-user concept** — every caller writes into one shared namespace. Plan 1b
discovered this when Gavin's "save workflow" landed in a namespace any other
authenticated family member could overwrite, and (silently worse) reading
another user's workflow JSON enumerates their prompts.

`isolation.RewriteUserdataURL` injects the caller's identifier as the leading
directory of the userdata path: `workflows/foo.json` → `<user>/workflows/foo.json`.
The move endpoint scopes BOTH file and dest (one-side scoping leaks the dest as
an escape vector).

**The route-shape constraint:** ComfyUI's frontend packs multi-segment paths
into the single `{file}` route param as `workflows%2Ffoo.json`. master's route is
`/userdata/{file}` — a SINGLE segment — so the whole file path must reach master
as ONE `%2F`-encoded segment. A multi-segment path (literal `/`) returns 405/404
(route mismatch).

**Two bugs, two versions — the lesson is the same: don't assume %2F survives.**

- **v0.1.3** added the scoping but used the default `httputil.NewSingleHostReverseProxy`
  for forward, which decodes `%2F`→`/` when rebuilding the URL. Fix: dedicated
  `forwardEncoded` that sets `RawPath`.
- **v0.1.4** fixed the *inbound* side. Envoy Gateway runs
  `path_with_escaped_slashes_action=UNESCAPE_AND_REDIRECT` (its default), so a
  browser's `POST /userdata/workflows%2FTest%20Flow.json` reaches the proxy
  ALREADY decoded to a literal slash. v0.1.3's rewrite kept the inbound encoding
  verbatim, so the literal slash flowed through and master 405'd on every save.
  In-pod curl (encoded, bypasses Envoy) returned 200, masking it — only the
  browser path hit the bug.

**The robust fix (v0.1.4): operate on the FULLY DECODED logical path, re-encode
as one segment.** `scopeSegment` `url.PathUnescape`s each part (so `%2F`-encoded
and Envoy-decoded inputs converge to the same string), validates, prepends the
bucket, and `url.PathEscape`s the whole `<user>/<path>` as one segment (PathEscape
maps `/`→`%2F`). Result is byte-identical whether the inbound path arrived
encoded or decoded — asserted by `TestRewriteUserdataURL_EnvoyDecodedMatchesEncoded`.
A path-scoping proxy must NOT depend on `%2F` surviving HTTP intermediaries; RFC
3986 permits them to normalize it, and Envoy does by default. We chose this over
flipping Envoy's `escapedSlashesAction` to KeepUnchanged because that is a
gateway-wide policy on a shared external gateway (blast radius across every app)
and weakens a sane `%2F`-smuggling defense for one feature.

**Validation rejects unsafe segments AFTER URL-decoding** (so `%2E%2E` →`..` is
caught), and the user identifier itself is sanity-checked against URL-meaningful
chars before being interpolated into the path. Two-callers-disjoint-buckets test
is the canary — if it breaks, isolation is broken.

## Role-gating + tier-routing ride the model-ref loop (v0.1.6, Plan-1 2026-06-05)

**Role-gating is by MODEL TAG, not workflow.** Registry entries carry
`requires_group` (e.g. NSFW assets → `"mature-content"`). `gate.Evaluate` rejects
(violation reason `requires-group`) unless the caller holds the group for EVERY
referenced asset. The caller's `Groups` are passed into `gate.Job` from
`identity` — the gate stays decode-free (it never parses a JWT; it takes resolved
claims). Enforcing on the workflow's declared `role_allowlist` (the original
ADR-015 phrasing) was rejected as craftable-around: a caller can hand-build a graph
that loads the sensitive model. Gating the immutable asset is structural, like the
licence gate (A1/A3). Coverage = every asset `ModelReferences` extracts:
checkpoints, both LoRA loaders, IPAdapter, Hypernetwork, StyleModel, GLIGEN,
ControlNet, **and inline `embedding:<name>` refs scanned from CLIPTextEncode text**
(those have no loader node — a regexp scan in `ModelReferences` catches them).
Honest boundary: this gates ACCESS to NSFW models/LoRAs, NOT prompt content (a
general model + suggestive prompt is ungated by any model/workflow approach).

**Tier-routing is proxy-side `enabled_worker_ids` filtering, no plugin patch.**
On `POST /distributed/queue`, `applyTierFilter` intersects the envelope's
`enabled_worker_ids` with tier-capable workers (`internal/tier`): acceptable tiers
= intersection of each referenced model's existing `tier_fit`; a worker is kept
only if its tier (from `gpu_config.json` `workers[].tier`, the same file the
master reads — it ignores the extra field) is acceptable. **400 if none qualify** —
never silently fan a job to a worker that would OOM (e.g. SDXL → 3050). Unknown/
untagged models don't constrain (licence gate owns unknown-model policy).

**Ship-inert/activate-via-config:** v0.1.6 is backward-compatible — untagged model
= no role restriction; nil `WorkerTiers` (no `tier` field, or no gpu_config mount)
= no tier filtering. The image is a no-op until the registry/gpu_config are tagged,
so deploy risk is decoupled from policy activation.
