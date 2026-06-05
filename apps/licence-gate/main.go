// Command licence-gate is the Lighthouse licence-gate reverse proxy: the single
// HTTP entrypoint in front of the ComfyUI master. It authenticates (decode-only;
// Envoy verifies upstream), licence-gates (commercial-by-default), scopes every
// output to the caller's bucket, and audits each render to stdout (ADR 015/016).
//
// Configuration (all via env; files are mounted ConfigMaps/Secrets):
//
//	LIGHTHOUSE_LISTEN          listen address           (default :8000)
//	LIGHTHOUSE_UPSTREAM        ComfyUI master base URL  (default http://127.0.0.1:8188)
//	LIGHTHOUSE_REGISTRY_PATH   licence registry JSON    (default /etc/lighthouse/registry.json) — required
//	LIGHTHOUSE_ALLOWLIST_PATH  node allowlist JSON      (default /etc/lighthouse/allowlist.json) — optional, falls back to built-in
//	LIGHTHOUSE_DR_TOKEN        brand service-account bearer (from Secret; optional)
//	LIGHTHOUSE_OWNERS_CAP      prompt-owner LRU capacity (default 4096)
//	LIGHTHOUSE_GPU_CONFIG_PATH gpu_config.json for worker tiers (default /config/gpu_config.json) — optional, enables tier-aware dispatch
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/gavinmcfall/containers/apps/licence-gate/internal/audit"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/isolation"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/proxy"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/registry"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/tier"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/workflow"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("licence-gate: %v", err)
	}
}

func run() error {
	listen := envOr("LIGHTHOUSE_LISTEN", ":8000")
	upstreamRaw := envOr("LIGHTHOUSE_UPSTREAM", "http://127.0.0.1:8188")
	regPath := envOr("LIGHTHOUSE_REGISTRY_PATH", "/etc/lighthouse/registry.json")
	allowPath := envOr("LIGHTHOUSE_ALLOWLIST_PATH", "/etc/lighthouse/allowlist.json")

	upstream, err := url.Parse(upstreamRaw)
	if err != nil {
		return fmt.Errorf("parse LIGHTHOUSE_UPSTREAM %q: %w", upstreamRaw, err)
	}
	reg, allow, err := loadResources(regPath, allowPath)
	if err != nil {
		return err
	}

	cap := 4096
	if v := os.Getenv("LIGHTHOUSE_OWNERS_CAP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cap = n
		}
	}

	// Worker→tier map for tier-aware dispatch (optional). Read from the same
	// gpu_config.json the master uses (workers[].tier; master ignores the field).
	// Absent/unparseable → nil → tier filtering disabled (fan-out unchanged).
	var workerTiers tier.Map
	gpuConfigPath := envOr("LIGHTHOUSE_GPU_CONFIG_PATH", "/config/gpu_config.json")
	if data, err := os.ReadFile(gpuConfigPath); err == nil {
		if wt, err := tier.LoadFromGPUConfig(data); err == nil {
			workerTiers = wt
		} else {
			log.Printf("gpu_config tier parse failed (tier filtering disabled): %v", err)
		}
	}

	p := proxy.New(proxy.Config{
		Upstream:    upstream,
		Allowlist:   allow,
		Registry:    reg,
		BrandToken:  os.Getenv("LIGHTHOUSE_DR_TOKEN"),
		Audit:       audit.NewWriter(os.Stdout),
		Owners:      isolation.NewPromptOwners(cap),
		Now:         func() string { return time.Now().UTC().Format(time.RFC3339) },
		WorkerTiers: workerTiers,
	})

	log.Printf("licence-gate listening on %s → upstream %s (registry %s, %d allowlisted nodes, %d worker tiers)",
		listen, upstream, regPath, len(allow), len(workerTiers))
	return http.ListenAndServe(listen, p)
}

// loadResources loads the licence registry (required) and node allowlist. A
// missing allowlist file falls back to the built-in DefaultAllowlist; a missing
// registry is fatal (the gate cannot make commercial decisions without it).
func loadResources(regPath, allowPath string) (*registry.Registry, workflow.Allowlist, error) {
	regData, err := os.ReadFile(regPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read registry %q: %w", regPath, err)
	}
	reg, err := registry.Load(regData)
	if err != nil {
		return nil, nil, fmt.Errorf("load registry: %w", err)
	}

	allow := workflow.DefaultAllowlist
	allowData, err := os.ReadFile(allowPath)
	switch {
	case err == nil:
		allow, err = workflow.LoadAllowlist(allowData)
		if err != nil {
			return nil, nil, fmt.Errorf("load allowlist: %w", err)
		}
	case errors.Is(err, fs.ErrNotExist):
		// optional — keep the built-in default
	default:
		return nil, nil, fmt.Errorf("read allowlist %q: %w", allowPath, err)
	}
	return reg, allow, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
