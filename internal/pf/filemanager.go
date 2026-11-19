package pf

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/user"
	"path"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mohicann/pfortainer/internal/httpx"
)

type Breadcrumb struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type FileEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	IsDir      bool   `json:"is_dir"`
	IsLink     bool   `json:"is_link"`
	LinkTarget string `json:"link_target"`
	Size       string `json:"size"`
	SizeRaw    int64  `json:"size_raw"`
	Mode       string `json:"mode"`
	ModeOctal  string `json:"mode_octal"`
	Owner      string `json:"owner"`
	Group      string `json:"group"`
	ModTime    string `json:"mod_time"`
	MtimeRaw   int64  `json:"mtime_raw"`
	Icon       string `json:"icon"`
}

type ClipBoard struct {
	Op    string   `json:"op" serde:"default"`
	Paths []string `json:"paths" serde:"default"`
}

// cleanPath: filemanager::clean_path.
func cleanPath(p string) string {
	var stack []string
	for _, seg := range strings.Split("/"+p, "/") {
		switch seg {
		case "", ".":
		case "..":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		default:
			stack = append(stack, seg)
		}
	}
	return "/" + strings.Join(stack, "/")
}

func safePath(p string) (string, bool) {
	c := cleanPath(p)
	return c, strings.HasPrefix(c, "/")
}

func fileIcon(name string, isDir, isLink bool) string {
	if isLink {
		return "bi-arrow-right-square"
	}
	if isDir {
		return "bi-folder-fill"
	}
	ext := ""
	// Path::extension: 마지막 '.' 뒤(이름이 '.'으로 시작하고 '.'이 하나뿐이면 없음)
	if i := strings.LastIndex(name, "."); i > 0 {
		ext = strings.ToLower(name[i+1:])
	}
	switch ext {
	case "go", "py", "js", "ts", "rb", "rs", "c", "cpp", "h", "java", "sh", "bash", "zsh", "fish", "pl", "php":
		return "bi-file-code"
	case "html", "htm", "xml", "css", "json", "yaml", "yml", "toml", "ini", "conf", "cfg":
		return "bi-file-earmark-code"
	case "txt", "md", "rst", "log":
		return "bi-file-text"
	case "pdf":
		return "bi-file-pdf"
	case "jpg", "jpeg", "png", "gif", "svg", "webp", "ico", "bmp", "tiff":
		return "bi-file-image"
	case "mp4", "mkv", "avi", "mov", "webm":
		return "bi-file-play"
	case "mp3", "wav", "ogg", "flac", "aac":
		return "bi-file-music"
	case "zip", "tar", "gz", "bz2", "xz", "zst", "7z", "rar", "tgz", "txz":
		return "bi-file-zip"
	case "deb", "rpm", "pkg":
		return "bi-box-seam"
	case "db", "sqlite", "sqlite3":
		return "bi-database"
	case "pem", "crt", "key", "p12", "pfx":
		return "bi-shield-lock"
	}
	return "bi-file-earmark"
}

func ownerGroup(uid, gid uint32) (string, string) {
	o, g := strconv.FormatUint(uint64(uid), 10), strconv.FormatUint(uint64(gid), 10)
	if u, err := user.LookupId(o); err == nil {
		o = u.Username
	}
	if gr, err := user.LookupGroupId(g); err == nil {
		g = gr.Name
	}
	return o, g
}

func formatMode(mode uint32, isDir, isLink bool) string {
	t := byte('-')
	if isLink {
		t = 'L'
	} else if isDir {
		t = 'd'
	}
	perm := mode & 0o777
	bit := func(b uint32, c byte) byte {
		if perm&b != 0 {
			return c
		}
		return '-'
	}
	x := func(b uint32, special bool, on, off byte) byte {
		switch {
		case perm&b != 0 && special:
			return on
		case special:
			return off
		case perm&b != 0:
			return 'x'
		}
		return '-'
	}
	return string([]byte{t, bit(0o400, 'r'), bit(0o200, 'w'), x(0o100, mode&0o4000 != 0, 's', 'S'),
		bit(0o040, 'r'), bit(0o020, 'w'), x(0o010, mode&0o2000 != 0, 's', 'S'),
		bit(0o004, 'r'), bit(0o002, 'w'), x(0o001, mode&0o1000 != 0, 't', 'T')})
}

func toFileEntry(dir, name string) (FileEntry, error) {
	full := strings.TrimRight(dir, "/") + "/" + name
	var st syscall.Stat_t
	if err := syscall.Lstat(full, &st); err != nil {
		return FileEntry{}, err
	}
	mode := uint32(st.Mode)
	isLink := mode&syscall.S_IFMT == syscall.S_IFLNK
	isDir := mode&syscall.S_IFMT == syscall.S_IFDIR
	target := ""
	if isLink {
		if t, err := os.Readlink(full); err == nil {
			target = t
		}
		if fi, err := os.Stat(full); err == nil {
			isDir = fi.IsDir()
		}
	}
	owner, group := ownerGroup(st.Uid, st.Gid)
	mt := statMtime(&st)
	t := time.Unix(mt, 0).UTC()
	return FileEntry{Name: name, Path: full, IsDir: isDir, IsLink: isLink, LinkTarget: target, Size: fmtBytes(st.Size), SizeRaw: st.Size,
		Mode: formatMode(mode, isDir, isLink), ModeOctal: strconv.FormatUint(uint64(mode&0o777), 8), Owner: owner, Group: group,
		ModTime: fmt.Sprintf("%04d-%02d-%02d %02d:%02d", t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute()), MtimeRaw: mt, Icon: fileIcon(name, isDir, isLink)}, nil
}

// listDir: (디렉터리, 파일), 각각 sort(name|mtime) 기준 정렬.
func listDir(cwd string, hidden bool, sortBy string, desc bool) ([]FileEntry, []FileEntry, error) {
	f, err := os.Open(cwd)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, nil, err
	}
	dirs, files := []FileEntry{}, []FileEntry{}
	for _, n := range names {
		if !hidden && strings.HasPrefix(n, ".") {
			continue
		}
		fe, err := toFileEntry(cwd, n)
		if err != nil {
			continue
		}
		if fe.IsDir {
			dirs = append(dirs, fe)
		} else {
			files = append(files, fe)
		}
	}
	less := func(a, b FileEntry) int {
		if sortBy == "mtime" {
			switch {
			case a.MtimeRaw < b.MtimeRaw:
				return -1
			case a.MtimeRaw > b.MtimeRaw:
				return 1
			}
			return 0
		}
		return strings.Compare(a.Name, b.Name)
	}
	srt := func(es []FileEntry) {
		sort.SliceStable(es, func(i, j int) bool {
			c := less(es[i], es[j])
			if desc {
				return c > 0
			}
			return c < 0
		})
	}
	srt(dirs)
	srt(files)
	return dirs, files, nil
}

func buildBreadcrumbs(p string) []Breadcrumb {
	crumbs := []Breadcrumb{{"/", "/"}}
	cur := ""
	for _, s := range strings.Split(strings.Trim(p, "/"), "/") {
		if s == "" {
			continue
		}
		cur += "/" + s
		crumbs = append(crumbs, Breadcrumb{s, cur})
	}
	return crumbs
}

func parentDir(p string) string {
	t := strings.TrimRight(p, "/")
	if t == "" {
		return ""
	}
	i := strings.LastIndex(t, "/")
	switch {
	case i == 0:
		return "/"
	case i > 0:
		return t[:i]
	}
	return ""
}

func isBinary(b []byte) bool { return strings.IndexByte(string(b), 0) >= 0 }

// copyPath: 디렉터리면 재귀, 아니면 파일 복사(권한 유지 — std::fs::copy).
func copyPath(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return copyDir(src, dst)
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return &os.PathError{Op: "copy", Path: src, Err: syscall.EISDIR}
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, fi.Mode().Perm()|fi.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky))
}

func copyDir(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.Mkdir(dst, 0o777); err != nil {
		return err
	}
	if err := os.Chmod(dst, fi.Mode().Perm()|fi.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)); err != nil {
		return err
	}
	d, err := os.Open(src)
	if err != nil {
		return err
	}
	ents, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return err
	}
	for _, e := range ents {
		s, t := src+"/"+e.Name(), dst+"/"+e.Name()
		if e.IsDir() {
			if err := copyDir(s, t); err != nil {
				return err
			}
		} else if err := copyFile(s, t); err != nil {
			return err
		}
	}
	return nil
}

// ── hostd 경유 ───────────────────────────────────────────────────────────────

type listResp struct {
	Dirs  []FileEntry `json:"dirs"`
	Files []FileEntry `json:"files"`
}

func listRemote(hc *HostClient, p string, hidden bool, sortBy string, desc bool) ([]FileEntry, []FileEntry, error) {
	h, o := "0", "asc"
	if hidden {
		h = "1"
	}
	if desc {
		o = "desc"
	}
	body, err := hc.GetAgent("/fs/list?path=" + urlenc(p) + "&hidden=" + h + "&sort=" + urlenc(sortBy) + "&order=" + o)
	if err != nil {
		return nil, nil, err
	}
	var r listResp
	if err := httpx.Unmarshal(body, &r); err != nil {
		return nil, nil, hostOther(err.Error())
	}
	return r.Dirs, r.Files, nil
}

type statResp struct {
	Size  uint64 `json:"size"`
	IsDir bool   `json:"is_dir"`
}

func statRemote(hc *HostClient, p string) (uint64, bool, error) {
	body, err := hc.GetAgent("/fs/stat?path=" + urlenc(p))
	if err != nil {
		return 0, false, err
	}
	var r statResp
	if err := httpx.Unmarshal(body, &r); err != nil {
		return 0, false, hostOther(err.Error())
	}
	return r.Size, r.IsDir, nil
}

func readRemote(hc *HostClient, p string) ([]byte, error) {
	return hc.GetAgent("/fs/read?path=" + urlenc(p))
}

func writeRemote(hc *HostClient, p string, content []byte) error {
	_, err := hostPost(hc, "/fs/write", map[string]any{"path": p, "content_base64": base64.StdEncoding.EncodeToString(content)})
	return err
}

func fsPost(hc *HostClient, path string, body map[string]any) error {
	_, err := hostPost(hc, path, body)
	return err
}

var _ = path.Clean
