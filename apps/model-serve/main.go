// Command model-serve is the Lighthouse model-store sidecar (ADR 018): a
// read-only, token-authenticated HTTP file server in front of the
// cluster-canonical models PVC. GPU workers fetch model files from it, cache
// them locally, and verify the sha256 worker-side.
//
// Configuration (env):
//
//	MODELSERVE_LISTEN  listen address          (default :9090)
//	MODELSERVE_ROOT    models PVC mount path    (default /models)
//	MODELSERVE_TOKEN   shared worker bearer token (required; fail-closed if empty)
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/gavinmcfall/containers/apps/model-serve/internal/fileserve"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("model-serve: %v", err)
	}
}

func run() error {
	listen := envOr("MODELSERVE_LISTEN", ":9090")
	root := envOr("MODELSERVE_ROOT", "/models")
	token := os.Getenv("MODELSERVE_TOKEN")

	h, err := fileserve.Handler(root, token)
	if err != nil {
		return err
	}
	log.Printf("model-serve listening on %s → root %s", listen, root)
	return http.ListenAndServe(listen, h)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
