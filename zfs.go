package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// emptyDatasetThreshold: refer 값이 이 값 미만이면 파일 없는 빈 데이터셋으로 간주 (1 MiB)
const emptyDatasetThreshold = 1 << 20

type ZFSPool struct {
	Name   string
	Size   string
	Alloc  string
	Free   string
	Health string
	UsePct int
}

type ZFSDataset struct {
	Name        string
	ShortName   string // 마지막 경로 컴포넌트 (display용)
	Parent      string // 직계 부모 이름 (풀은 "")
	Type        string // filesystem | volume | snapshot
	Used        string
	Avail       string
	Refer       string
	Mountpoint  string
	Depth       int
	IndentPx    int  // Depth * 16, 템플릿 padding-left용
	IsPool      bool
	HasChildren bool
	HasFiles    bool // refer >= emptyDatasetThreshold
	CanDelete   bool // !IsPool && !HasChildren && !HasFiles && Type==filesystem
}

func zfsRun(bin string, args ...string) ([]byte, error) {
	out, err := exec.Command(bin, args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			msg := strings.TrimSpace(string(ee.Stderr))
			if msg == "" {
				msg = err.Error()
			}
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, err
	}
	return out, nil
}

func listZFSPools() ([]ZFSPool, error) {
	out, err := zfsRun("zpool", "list", "-H", "-p", "-o", "name,size,alloc,free,health")
	if err != nil {
		return nil, err
	}
	var pools []ZFSPool
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 5 {
			continue
		}
		size, _ := strconv.ParseInt(f[1], 10, 64)
		alloc, _ := strconv.ParseInt(f[2], 10, 64)
		free, _ := strconv.ParseInt(f[3], 10, 64)
		usePct := 0
		if size > 0 {
			usePct = int(math.Round(float64(alloc) / float64(size) * 100))
		}
		pools = append(pools, ZFSPool{
			Name:   f[0],
			Size:   fmtBytes(size),
			Alloc:  fmtBytes(alloc),
			Free:   fmtBytes(free),
			Health: f[4],
			UsePct: usePct,
		})
	}
	return pools, nil
}

func listZFSDatasets() ([]ZFSDataset, error) {
	out, err := zfsRun("zfs", "list", "-H", "-p", "-o", "name,type,used,avail,refer,mountpoint")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")

	// 1차: 이름 목록 수집 (자식 여부 판별용)
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		f := strings.SplitN(line, "\t", 2)
		if len(f) >= 1 && f[0] != "" {
			names = append(names, f[0])
		}
	}

	hasChildren := func(name string) bool {
		for _, n := range names {
			if n != name && (strings.HasPrefix(n, name+"/") || strings.HasPrefix(n, name+"@")) {
				return true
			}
		}
		return false
	}

	shortName := func(name string) string {
		// 스냅샷: zdata/foo@snap → @snap
		if atIdx := strings.LastIndex(name, "@"); atIdx > strings.LastIndex(name, "/") {
			return "@" + name[atIdx+1:]
		}
		if slashIdx := strings.LastIndex(name, "/"); slashIdx >= 0 {
			return name[slashIdx+1:]
		}
		return name
	}

	parentOf := func(name string) string {
		if atIdx := strings.LastIndex(name, "@"); atIdx > strings.LastIndex(name, "/") {
			return name[:atIdx]
		}
		if slashIdx := strings.LastIndex(name, "/"); slashIdx >= 0 {
			return name[:slashIdx]
		}
		return ""
	}

	depth := func(name string) int {
		// 스냅샷은 @ 앞의 경로 깊이 + 1
		if atIdx := strings.LastIndex(name, "@"); atIdx > strings.LastIndex(name, "/") {
			return strings.Count(name[:atIdx], "/") + 1
		}
		return strings.Count(name, "/")
	}

	var datasets []ZFSDataset
	for _, line := range lines {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 6 {
			continue
		}
		name := f[0]
		dsType := f[1]
		used, _ := strconv.ParseInt(f[2], 10, 64)
		avail, _ := strconv.ParseInt(f[3], 10, 64)
		refer, _ := strconv.ParseInt(f[4], 10, 64)
		mountpoint := f[5]

		d := depth(name)
		isPool := !strings.Contains(name, "/") && !strings.Contains(name, "@")
		hasC := hasChildren(name)
		hasFiles := refer >= emptyDatasetThreshold
		canDelete := !isPool && !hasC && !hasFiles && dsType == "filesystem"

		datasets = append(datasets, ZFSDataset{
			Name:        name,
			ShortName:   shortName(name),
			Parent:      parentOf(name),
			Type:        dsType,
			Used:        fmtBytes(used),
			Avail:       fmtBytes(avail),
			Refer:       fmtBytes(refer),
			Mountpoint:  mountpoint,
			Depth:       d,
			IndentPx:    d * 16,
			IsPool:      isPool,
			HasChildren: hasC,
			HasFiles:    hasFiles,
			CanDelete:   canDelete,
		})
	}
	return datasets, nil
}

func createZFSDataset(path string) error {
	out, err := exec.Command("zfs", "create", path).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func destroyZFSDataset(name string) error {
	out, err := exec.Command("zfs", "destroy", name).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// ── ZFS 스냅샷 ────────────────────────────────────────────────────────────────

type Snapshot struct {
	Name     string    // 전체 이름: dataset@snapname
	Dataset  string    // @ 앞 부분
	SnapName string    // @ 뒷 부분
	Creation time.Time
	Used     string
	Refer    string
}

// listSnapshots lists all snapshots under dataset (empty = all pools).
// Results are sorted by creation time ascending.
func listSnapshots(dataset string) ([]Snapshot, error) {
	args := []string{"list", "-H", "-p", "-t", "snapshot",
		"-o", "name,creation,used,refer", "-s", "creation"}
	if dataset != "" {
		args = append(args, "-r", dataset)
	}
	out, err := zfsRun("zfs", args...)
	if err != nil {
		return nil, err
	}
	var snaps []Snapshot
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 4 {
			continue
		}
		name := f[0]
		atIdx := strings.LastIndex(name, "@")
		if atIdx < 0 {
			continue
		}
		sec, _ := strconv.ParseInt(f[1], 10, 64)
		used, _ := strconv.ParseInt(f[2], 10, 64)
		refer, _ := strconv.ParseInt(f[3], 10, 64)
		snaps = append(snaps, Snapshot{
			Name:     name,
			Dataset:  name[:atIdx],
			SnapName: name[atIdx+1:],
			Creation: time.Unix(sec, 0),
			Used:     fmtBytes(used),
			Refer:    fmtBytes(refer),
		})
	}
	return snaps, nil
}

// Snapshot write operations are routed through the host agent (pfortainer_hostd)
// because the Jail does not have ZFS delegation for write operations.

func createSnapshot(dataset, snapname string) error {
	body, _ := json.Marshal(map[string]string{"dataset": dataset, "snapname": snapname})
	_, err := hostPost("/zfs/snapshot", body)
	return err
}

func destroySnapshot(fullName string) error {
	body, _ := json.Marshal(map[string]string{"name": fullName})
	_, err := hostPost("/zfs/snapshot/delete", body)
	return err
}

// rollbackSnapshot rolls back to a snapshot. The agent uses -r to destroy newer snapshots.
func rollbackSnapshot(fullName string) error {
	body, _ := json.Marshal(map[string]string{"name": fullName})
	_, err := hostPost("/zfs/rollback", body)
	return err
}

func cloneSnapshot(fullName, target string) error {
	body, _ := json.Marshal(map[string]string{"name": fullName, "target": target})
	_, err := hostPost("/zfs/clone", body)
	return err
}

// listSnapshotsByDataset returns a map of dataset → snapshots, ordered by creation time.
func listSnapshotsByDataset(dataset string) (map[string][]Snapshot, []string, error) {
	snaps, err := listSnapshots(dataset)
	if err != nil {
		return nil, nil, err
	}
	groups := make(map[string][]Snapshot)
	var order []string
	for _, s := range snaps {
		if _, ok := groups[s.Dataset]; !ok {
			order = append(order, s.Dataset)
		}
		groups[s.Dataset] = append(groups[s.Dataset], s)
	}
	return groups, order, nil
}

// pruneSnapshots deletes the oldest snapshots matching prefix under dataset,
// keeping at most keep. keep=0 means keep all. Routed through host agent.
func pruneSnapshots(dataset, prefix string, keep int) error {
	if keep <= 0 {
		return nil
	}
	body, _ := json.Marshal(map[string]any{"dataset": dataset, "prefix": prefix, "keep": keep})
	_, err := hostPost("/zfs/snapshot/prune", body)
	return err
}
