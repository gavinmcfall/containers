package tier

import (
	"encoding/json"
	"fmt"
)

// LoadFromGPUConfig parses the ComfyUI-Distributed gpu_config.json and returns a
// worker_id -> tier map from each worker's optional "tier" field. The master
// ignores the extra field, so one config serves both. A worker with no tier maps
// to "" (it won't match any constrained tier — it must be tagged to participate
// in tier-constrained jobs).
func LoadFromGPUConfig(data []byte) (Map, error) {
	var cfg struct {
		Workers []struct {
			ID   string `json:"id"`
			Tier string `json:"tier"`
		} `json:"workers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse gpu_config: %w", err)
	}
	m := make(Map, len(cfg.Workers))
	for _, w := range cfg.Workers {
		m[w.ID] = w.Tier
	}
	return m, nil
}
