# licence-gate proxy — contract notes

Build-time findings that pin the proxy contract. Keep with the code; the
durable design lives in `gavinmcfall/lighthouse` (ADR 008, ADR 015, flow 3).

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

## /userdata scoping injects per-caller bucket AND preserves %2F encoding (v0.1.3, Plan 1b 2026-06-04)

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

**The Phase-1 surface bug:** Go's `httputil.NewSingleHostReverseProxy` (the
default passthrough) decodes `%2F`→`/` when rebuilding URLs. ComfyUI's frontend
packs multi-segment paths into the single `{file}` route param as
`workflows%2Ffoo.json`; once decoded, master sees `/userdata/<user>/workflows/foo.json`
(multi-segment), returns 405 because its POST route is `/userdata/{file}`
(single segment). Fix: dedicated `forwardEncoded` that sets `RawPath` so the
encoding survives. Asserted by `TestUserdataPreservesEncodedSlashThroughForward`.

**Validation rejects unsafe segments AFTER URL-decoding** (so `%2E%2E` →`..` is
caught), and the user identifier itself is sanity-checked against URL-meaningful
chars before being interpolated into the path. Two-callers-disjoint-buckets test
is the canary — if it breaks, isolation is broken.
