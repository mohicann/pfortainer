package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// ── View Models ───────────────────────────────────────────────────────────────

type Breadcrumb struct {
	Name string
	Path string
}

type FileEntry struct {
	Name      string
	Path      string
	IsDir     bool
	IsLink    bool
	LinkTarget string
	Size      string
	SizeRaw   int64
	Mode      string
	ModeOctal string
	Owner     string
	Group     string
	ModTime   string
	Icon      string
}

type ClipBoard struct {
	Op    string   // "copy" | "cut"
	Paths []string // 절대 경로 목록
}

type FileListVM struct {
	ActivePage  string
	CurrentUser SessionUser
	CWD         string
	Breadcrumbs []Breadcrumb
	Entries     []FileEntry
	Parent      string
	Clip        ClipBoard
	ShowHidden  bool
}

type FileEditVM struct {
	ActivePage string
	FilePath   string
	Content    string
	ReadOnly   bool
	TooLarge   bool
	Binary     bool
}

// ── Security ──────────────────────────────────────────────────────────────────

func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	c := filepath.Clean("/" + p)
	if c == "" {
		return "/"
	}
	return c
}

func safePath(p string) (string, bool) {
	c := cleanPath(p)
	// 반드시 절대경로여야 함
	if !filepath.IsAbs(c) {
		return "", false
	}
	return c, true
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func fileIcon(name string, isDir, isLink bool) string {
	if isLink {
		return "bi-arrow-right-square"
	}
	if isDir {
		return "bi-folder-fill"
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".go", ".py", ".js", ".ts", ".rb", ".rs", ".c", ".cpp", ".h", ".java", ".sh", ".bash", ".zsh", ".fish", ".pl", ".php":
		return "bi-file-code"
	case ".html", ".htm", ".xml", ".css", ".json", ".yaml", ".yml", ".toml", ".ini", ".conf", ".cfg":
		return "bi-file-earmark-code"
	case ".txt", ".md", ".rst", ".log":
		return "bi-file-text"
	case ".pdf":
		return "bi-file-pdf"
	case ".jpg", ".jpeg", ".png", ".gif", ".svg", ".webp", ".ico", ".bmp", ".tiff":
		return "bi-file-image"
	case ".mp4", ".mkv", ".avi", ".mov", ".webm":
		return "bi-file-play"
	case ".mp3", ".wav", ".ogg", ".flac", ".aac":
		return "bi-file-music"
	case ".zip", ".tar", ".gz", ".bz2", ".xz", ".zst", ".7z", ".rar", ".tgz", ".txz":
		return "bi-file-zip"
	case ".deb", ".rpm", ".pkg":
		return "bi-box-seam"
	case ".db", ".sqlite", ".sqlite3":
		return "bi-database"
	case ".pem", ".crt", ".key", ".p12", ".pfx":
		return "bi-shield-lock"
	}
	return "bi-file-earmark"
}

func ownerGroup(info os.FileInfo) (string, string) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "?", "?"
	}
	uid := strconv.FormatUint(uint64(stat.Uid), 10)
	gid := strconv.FormatUint(uint64(stat.Gid), 10)
	if u, err := user.LookupId(uid); err == nil {
		uid = u.Username
	}
	if g, err := user.LookupGroupId(gid); err == nil {
		gid = g.Name
	}
	return uid, gid
}

func modeOctal(m os.FileMode) string {
	return strconv.FormatUint(uint64(m.Perm()), 8)
}

func toFileEntry(dir, name string, info os.FileInfo) FileEntry {
	fullPath := filepath.Join(dir, name)
	isLink := info.Mode()&os.ModeSymlink != 0
	isDir := info.IsDir()

	linkTarget := ""
	if isLink {
		if t, err := os.Readlink(fullPath); err == nil {
			linkTarget = t
		}
		// lstat 후 실제 타입 확인
		if ri, err := os.Stat(fullPath); err == nil {
			isDir = ri.IsDir()
		}
	}

	owner, group := ownerGroup(info)

	return FileEntry{
		Name:       name,
		Path:       fullPath,
		IsDir:      isDir,
		IsLink:     isLink,
		LinkTarget: linkTarget,
		Size:       fmtBytes(info.Size()),
		SizeRaw:    info.Size(),
		Mode:       info.Mode().String(),
		ModeOctal:  modeOctal(info.Mode()),
		Owner:      owner,
		Group:      group,
		ModTime:    info.ModTime().UTC().Format("2006-01-02 15:04"),
		Icon:       fileIcon(name, isDir, isLink),
	}
}

func buildBreadcrumbs(path string) []Breadcrumb {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	crumbs := []Breadcrumb{{Name: "/", Path: "/"}}
	cur := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		cur += "/" + p
		crumbs = append(crumbs, Breadcrumb{Name: p, Path: cur})
	}
	return crumbs
}

func parentDir(path string) string {
	p := filepath.Dir(path)
	if p == path {
		return ""
	}
	return p
}

func isBinary(b []byte) bool {
	return bytes.ContainsRune(b, 0)
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func (h *handlers) fileList(w http.ResponseWriter, r *http.Request) {
	rawPath := r.URL.Query().Get("path")
	if rawPath == "" {
		rawPath = "/"
	}
	cwd, ok := safePath(rawPath)
	if !ok {
		http.Error(w, "잘못된 경로입니다.", http.StatusBadRequest)
		return
	}

	showHidden := r.URL.Query().Get("hidden") == "1"

	f, err := os.Open(cwd)
	if err != nil {
		http.Error(w, "디렉토리를 열 수 없습니다: "+err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()

	infos, err := f.Readdir(-1)
	if err != nil {
		http.Error(w, "디렉토리를 읽을 수 없습니다: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var dirs, files []FileEntry
	for _, info := range infos {
		if !showHidden && strings.HasPrefix(info.Name(), ".") {
			continue
		}
		entry := toFileEntry(cwd, info.Name(), info)
		if entry.IsDir {
			dirs = append(dirs, entry)
		} else {
			files = append(files, entry)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	entries := append(dirs, files...)

	clip := getClipboard(r)

	render(w, "filemanager", FileListVM{
		ActivePage:  "filemanager",
		CurrentUser: userFrom(r),
		CWD:         cwd,
		Breadcrumbs: buildBreadcrumbs(cwd),
		Entries:     entries,
		Parent:      parentDir(cwd),
		Clip:        clip,
		ShowHidden:  showHidden,
	})
}

// fileListJSON returns directory contents as JSON for the file-browser modal.
func (h *handlers) fileListJSON(w http.ResponseWriter, r *http.Request) {
	rawPath := r.URL.Query().Get("path")
	if rawPath == "" {
		rawPath = "/"
	}
	cwd, ok := safePath(rawPath)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 경로입니다."})
		return
	}

	f, err := os.Open(cwd)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	defer f.Close()

	infos, err := f.Readdir(-1)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	type entry struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		IsDir bool   `json:"isDir"`
	}
	var dirs, files []entry
	for _, info := range infos {
		if strings.HasPrefix(info.Name(), ".") {
			continue
		}
		e := entry{
			Name:  info.Name(),
			Path:  filepath.Join(cwd, info.Name()),
			IsDir: info.IsDir(),
		}
		if e.IsDir {
			dirs = append(dirs, e)
		} else {
			files = append(files, e)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	parent := parentDir(cwd)
	writeJSON(w, http.StatusOK, map[string]any{
		"cwd":     cwd,
		"parent":  parent,
		"entries": append(dirs, files...),
	})
}

func (h *handlers) fileEdit(w http.ResponseWriter, r *http.Request) {
	rawPath := r.URL.Query().Get("path")
	fpath, ok := safePath(rawPath)
	if !ok {
		http.Error(w, "잘못된 경로입니다.", http.StatusBadRequest)
		return
	}

	const maxSize = 1 << 20 // 1 MB

	info, err := os.Stat(fpath)
	if err != nil {
		http.Error(w, "파일을 찾을 수 없습니다.", http.StatusNotFound)
		return
	}

	vm := FileEditVM{
		ActivePage: "filemanager",
		FilePath:   fpath,
		ReadOnly:   false,
	}

	if info.Size() > maxSize {
		vm.TooLarge = true
		render(w, "fileedit", vm)
		return
	}

	data, err := os.ReadFile(fpath)
	if err != nil {
		http.Error(w, "파일을 읽을 수 없습니다: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if isBinary(data) {
		vm.Binary = true
		render(w, "fileedit", vm)
		return
	}
	vm.Content = string(data)
	render(w, "fileedit", vm)
}

func (h *handlers) fileSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 요청입니다."})
		return
	}
	fpath, ok := safePath(req.Path)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 경로입니다."})
		return
	}
	if err := os.WriteFile(fpath, []byte(req.Content), 0644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (h *handlers) fileDownload(w http.ResponseWriter, r *http.Request) {
	rawPath := r.URL.Query().Get("path")
	fpath, ok := safePath(rawPath)
	if !ok {
		http.Error(w, "잘못된 경로입니다.", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(fpath)
	if err != nil || info.IsDir() {
		http.Error(w, "파일을 찾을 수 없습니다.", http.StatusNotFound)
		return
	}
	f, err := os.Open(fpath)
	if err != nil {
		http.Error(w, "파일을 열 수 없습니다.", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	ct := mime.TypeByExtension(filepath.Ext(fpath))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filepath.Base(fpath)+"\"")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	io.Copy(w, f)
}

func (h *handlers) fileUpload(w http.ResponseWriter, r *http.Request) {
	const maxMem = 32 << 20 // 32 MB
	if err := r.ParseMultipartForm(maxMem); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "업로드 파싱 실패: " + err.Error()})
		return
	}
	rawDir := r.FormValue("dir")
	dir, ok := safePath(rawDir)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 경로입니다."})
		return
	}

	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "파일이 없습니다."})
		return
	}
	for _, fh := range files {
		src, err := fh.Open()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "파일 열기 실패: " + err.Error()})
			return
		}
		defer src.Close()
		// 파일명 내 경로 구분자 제거
		name := filepath.Base(fh.Filename)
		dst, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "파일 생성 실패: " + err.Error()})
			return
		}
		defer dst.Close()
		if _, err := io.Copy(dst, src); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "쓰기 실패: " + err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (h *handlers) fileMkdir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir  string `json:"dir"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 요청입니다."})
		return
	}
	dir, ok := safePath(req.Dir)
	if !ok || strings.ContainsAny(req.Name, "/\x00") || req.Name == "" || req.Name == "." || req.Name == ".." {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 이름입니다."})
		return
	}
	target := filepath.Join(dir, req.Name)
	if err := os.Mkdir(target, 0755); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (h *handlers) fileCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir  string `json:"dir"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 요청입니다."})
		return
	}
	dir, ok := safePath(req.Dir)
	if !ok || strings.ContainsAny(req.Name, "/\x00") || req.Name == "" || req.Name == "." || req.Name == ".." {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 이름입니다."})
		return
	}
	target := filepath.Join(dir, req.Name)
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	f.Close()
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (h *handlers) fileDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 요청입니다."})
		return
	}
	for _, p := range req.Paths {
		fpath, ok := safePath(p)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 경로: " + p})
			return
		}
		if fpath == "/" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "루트 디렉토리는 삭제할 수 없습니다."})
			return
		}
		if err := os.RemoveAll(fpath); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (h *handlers) fileRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		NewName string `json:"newName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 요청입니다."})
		return
	}
	fpath, ok := safePath(req.Path)
	if !ok || strings.ContainsAny(req.NewName, "/\x00") || req.NewName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 이름입니다."})
		return
	}
	dir := filepath.Dir(fpath)
	target := filepath.Join(dir, req.NewName)
	if _, err := os.Lstat(target); err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "같은 이름의 파일이 이미 존재합니다."})
		return
	}
	if err := os.Rename(fpath, target); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (h *handlers) fileChmod(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths []string `json:"paths"`
		Mode  string   `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 요청입니다."})
		return
	}
	mode64, err := strconv.ParseUint(req.Mode, 8, 32)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 권한 형식입니다 (예: 644)."})
		return
	}
	perm := os.FileMode(mode64)
	for _, p := range req.Paths {
		fpath, ok := safePath(p)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 경로: " + p})
			return
		}
		if err := os.Chmod(fpath, perm); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// ── Clipboard (copy / move) ───────────────────────────────────────────────────

const clipCookieName = "fm_clip"

func getClipboard(r *http.Request) ClipBoard {
	c, err := r.Cookie(clipCookieName)
	if err != nil {
		return ClipBoard{}
	}
	var clip ClipBoard
	if err := json.Unmarshal([]byte(c.Value), &clip); err != nil {
		return ClipBoard{}
	}
	return clip
}

func setClipboardCookie(w http.ResponseWriter, clip ClipBoard) {
	b, _ := json.Marshal(clip)
	http.SetCookie(w, &http.Cookie{
		Name:     clipCookieName,
		Value:    string(b),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearClipboardCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     clipCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
}

func (h *handlers) fileClip(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Op    string   `json:"op"`    // "copy" | "cut"
		Paths []string `json:"paths"` // 절대 경로
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 요청입니다."})
		return
	}
	if req.Op != "copy" && req.Op != "cut" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "op는 copy 또는 cut이어야 합니다."})
		return
	}
	clean := make([]string, 0, len(req.Paths))
	for _, p := range req.Paths {
		cp, ok := safePath(p)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 경로: " + p})
			return
		}
		clean = append(clean, cp)
	}
	setClipboardCookie(w, ClipBoard{Op: req.Op, Paths: clean})
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (h *handlers) filePaste(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DestDir string `json:"destDir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 요청입니다."})
		return
	}
	dest, ok := safePath(req.DestDir)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 대상 경로입니다."})
		return
	}
	clip := getClipboard(r)
	if len(clip.Paths) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "클립보드가 비어 있습니다."})
		return
	}
	for _, src := range clip.Paths {
		name := filepath.Base(src)
		target := filepath.Join(dest, name)
		if clip.Op == "cut" {
			if err := os.Rename(src, target); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "이동 실패: " + err.Error()})
				return
			}
		} else {
			if err := copyPath(src, target); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "복사 실패: " + err.Error()})
				return
			}
		}
	}
	if clip.Op == "cut" {
		clearClipboardCookie(w)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func copyPath(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyDir(src, dst, info.Mode())
	}
	return copyFile(src, dst, info.Mode())
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func copyDir(src, dst string, mode os.FileMode) error {
	if err := os.Mkdir(dst, mode); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			info, _ := e.Info()
			if err := copyDir(s, d, info.Mode()); err != nil {
				return err
			}
		} else {
			info, _ := e.Info()
			if err := copyFile(s, d, info.Mode()); err != nil {
				return err
			}
		}
	}
	return nil
}
