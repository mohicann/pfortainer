package pf

import (
	"strconv"
	"strings"
	"time"

	"github.com/mohicann/pfortainer/internal/httpx"
)

const emptyDatasetThreshold = 1 << 20

type ZfsPool struct {
	Name   string `json:"name"`
	Size   string `json:"size"`
	Alloc  string `json:"alloc"`
	Free   string `json:"free"`
	Health string `json:"health"`
	UsePct int32  `json:"use_pct"`
}

type ZfsDataset struct {
	Name        string `json:"name"`
	ShortName   string `json:"short_name"`
	Parent      string `json:"parent"`
	Kind        string `json:"kind"`
	Used        string `json:"used"`
	Avail       string `json:"avail"`
	Refer       string `json:"refer"`
	Mountpoint  string `json:"mountpoint"`
	Depth       int32  `json:"depth"`
	IndentPx    int32  `json:"indent_px"`
	IsPool      bool   `json:"is_pool"`
	HasChildren bool   `json:"has_children"`
	HasFiles    bool   `json:"has_files"`
	CanDelete   bool   `json:"can_delete"`
}

type Snapshot struct {
	Name, Dataset, SnapName string
	Creation                time.Time
	Used, Refer             string
}

// rustLines: str::lines(\n 기준, 끝의 \r 제거, 마지막 빈 줄 없음).
func rustLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	for i, p := range parts {
		parts[i] = strings.TrimSuffix(p, "\r")
	}
	return parts
}

func parseI64(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func listZfsPools(hc *HostClient) ([]ZfsPool, error) {
	out, err := hc.Get("/zpool-list", []string{"zpool", "list", "-H", "-p", "-o", "name,size,alloc,free,health"})
	if err != nil {
		return nil, err
	}
	pools := []ZfsPool{}
	for _, line := range rustLines(strings.TrimSpace(lossy(out))) {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 5 {
			continue
		}
		size, alloc, free := parseI64(f[1]), parseI64(f[2]), parseI64(f[3])
		var use int32
		if size > 0 {
			use = int32(rround(float64(alloc) / float64(size) * 100))
		}
		pools = append(pools, ZfsPool{f[0], fmtBytes(size), fmtBytes(alloc), fmtBytes(free), f[4], use})
	}
	return pools, nil
}

func lastSnapAt(name string) (int, bool) {
	at := strings.LastIndex(name, "@")
	if at < 0 {
		return 0, false
	}
	sl := strings.LastIndex(name, "/")
	return at, sl < 0 || at > sl
}

func zShortName(name string) string {
	if at, ok := lastSnapAt(name); ok {
		return "@" + name[at+1:]
	}
	if s := strings.LastIndex(name, "/"); s >= 0 {
		return name[s+1:]
	}
	return name
}

func zParent(name string) string {
	if at, ok := lastSnapAt(name); ok {
		return name[:at]
	}
	if s := strings.LastIndex(name, "/"); s >= 0 {
		return name[:s]
	}
	return ""
}

func zDepth(name string) int32 {
	if at, ok := lastSnapAt(name); ok {
		return int32(strings.Count(name[:at], "/") + 1)
	}
	return int32(strings.Count(name, "/"))
}

func listZfsDatasets(hc *HostClient) ([]ZfsDataset, error) {
	out, err := hc.Get("/zfs-list", []string{"zfs", "list", "-H", "-p", "-o", "name,type,used,avail,refer,mountpoint"})
	if err != nil {
		return nil, err
	}
	lines := rustLines(strings.TrimSpace(lossy(out)))
	var names []string
	for _, l := range lines {
		if n := strings.Split(l, "\t")[0]; n != "" {
			names = append(names, n)
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
	ds := []ZfsDataset{}
	for _, line := range lines {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 6 {
			continue
		}
		name, typ := f[0], f[1]
		used, avail, refer := parseI64(f[2]), parseI64(f[3]), parseI64(f[4])
		d := zDepth(name)
		isPool := !strings.Contains(name, "/") && !strings.Contains(name, "@")
		hc := hasChildren(name)
		hasFiles := refer >= emptyDatasetThreshold
		ds = append(ds, ZfsDataset{Name: name, ShortName: zShortName(name), Parent: zParent(name), Kind: typ, Used: fmtBytes(used), Avail: fmtBytes(avail),
			Refer: fmtBytes(refer), Mountpoint: f[5], Depth: d, IndentPx: d * 16, IsPool: isPool, HasChildren: hc, HasFiles: hasFiles,
			CanDelete: !isPool && !hc && !hasFiles && typ == "filesystem"})
	}
	return ds, nil
}

func hostPost(hc *HostClient, path string, body any) ([]byte, error) {
	return hc.Post(path, httpx.MarshalJSON(body))
}

func createZfsDataset(hc *HostClient, path string) error {
	_, err := hostPost(hc, "/zfs/dataset", httpx.Ordered{{"path", path}})
	return err
}

func destroyZfsDataset(hc *HostClient, name string) error {
	_, err := hostPost(hc, "/zfs/dataset/delete", httpx.Ordered{{"name", name}})
	return err
}

func listSnapshots(hc *HostClient, dataset string) ([]Snapshot, error) {
	args := []string{"zfs", "list", "-H", "-p", "-t", "snapshot", "-o", "name,creation,used,refer", "-s", "creation"}
	path := "/zfs-list-snapshot"
	if dataset != "" {
		args = append(args, "-r", dataset)
		path += "?dataset=" + urlenc(dataset)
	}
	out, err := hc.Get(path, args)
	if err != nil {
		return nil, err
	}
	var snaps []Snapshot
	for _, line := range rustLines(strings.TrimSpace(lossy(out))) {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 4 {
			continue
		}
		at := strings.LastIndex(f[0], "@")
		if at < 0 {
			continue
		}
		snaps = append(snaps, Snapshot{Name: f[0], Dataset: f[0][:at], SnapName: f[0][at+1:], Creation: time.Unix(parseI64(f[1]), 0).UTC(),
			Used: fmtBytes(parseI64(f[2])), Refer: fmtBytes(parseI64(f[3]))})
	}
	return snaps, nil
}

func createSnapshot(hc *HostClient, dataset, snap string) error {
	_, err := hostPost(hc, "/zfs/snapshot", httpx.Ordered{{"dataset", dataset}, {"snapname", snap}})
	return err
}

func destroySnapshot(hc *HostClient, name string) error {
	_, err := hostPost(hc, "/zfs/snapshot/delete", httpx.Ordered{{"name", name}})
	return err
}

func rollbackSnapshot(hc *HostClient, name string) error {
	_, err := hostPost(hc, "/zfs/rollback", httpx.Ordered{{"name", name}})
	return err
}

func cloneSnapshot(hc *HostClient, name, target string) error {
	_, err := hostPost(hc, "/zfs/clone", httpx.Ordered{{"name", name}, {"target", target}})
	return err
}

func listSnapshotsByDataset(hc *HostClient, dataset string) (map[string][]Snapshot, []string, error) {
	snaps, err := listSnapshots(hc, dataset)
	if err != nil {
		return nil, nil, err
	}
	groups := map[string][]Snapshot{}
	var order []string
	for _, s := range snaps {
		if _, ok := groups[s.Dataset]; !ok {
			order = append(order, s.Dataset)
		}
		groups[s.Dataset] = append(groups[s.Dataset], s)
	}
	return groups, order, nil
}

func pruneSnapshots(hc *HostClient, dataset, prefix string, keep int32) error {
	if keep <= 0 {
		return nil
	}
	_, err := hostPost(hc, "/zfs/snapshot/prune", httpx.Ordered{{"dataset", dataset}, {"prefix", prefix}, {"keep", keep}})
	return err
}
