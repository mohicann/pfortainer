package pf

import "github.com/mohicann/pfortainer/internal/httpx"

type ReplResult struct {
	CurrentSnapshot string `json:"current_snapshot" serde:"default"`
	Output          string `json:"output" serde:"default"`
}

func runReplication(hc *HostClient, src, tgt, last string, rec bool) (*ReplResult, error) {
	body := map[string]any{"source_dataset": src, "target_path": tgt, "last_snapshot": last, "recursive": rec}
	resp, err := hc.Post("/replication/run", httpx.MarshalJSON(body))
	if err != nil {
		return nil, err
	}
	var r ReplResult
	if err := httpx.Unmarshal(resp, &r); err != nil {
		return nil, hostOther(err.Error())
	}
	return &r, nil
}
