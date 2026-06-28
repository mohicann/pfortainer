package main

import "encoding/json"

type ReplResult struct {
	CurrentSnapshot string `json:"current_snapshot"`
	Output          string `json:"output"`
}

func runReplication(source, target, lastSnap string, recursive bool) (ReplResult, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"source_dataset": source,
		"target_path":    target,
		"last_snapshot":  lastSnap,
		"recursive":      recursive,
	})
	b, err := hostPost("/replication/run", body)
	if err != nil {
		return ReplResult{}, err
	}
	var r ReplResult
	err = json.Unmarshal(b, &r)
	return r, err
}
