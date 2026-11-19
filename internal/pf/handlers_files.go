package pf

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/mohicann/pfortainer/internal/httpx"
)

const clipCookie = "fm_clip"

func hostErrStatus(err error) int {
	if isUnavailable(err) {
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

// urlDecode: urlencoding::decode(잘못된 %는 그대로, + 는 그대로).
func urlDecode(s string) (string, bool) {
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHexB(s[i+1]) && isHexB(s[i+2]) {
			n, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			b = append(b, byte(n))
			i += 2
			continue
		}
		b = append(b, s[i])
	}
	return string(b), true
}

func isHexB(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

func getClipboard(r *http.Request) ClipBoard {
	raw, ok := cookieHeader(r)
	if !ok {
		return ClipBoard{Paths: []string{}}
	}
	var val string
	found := false
	for _, part := range strings.Split(raw, ";") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(part), clipCookie+"="); ok {
			val, found = v, true
			break
		}
	}
	if !found {
		return ClipBoard{Paths: []string{}}
	}
	dec, _ := urlDecode(val)
	var c ClipBoard
	if httpx.Unmarshal([]byte(dec), &c) != nil {
		return ClipBoard{Paths: []string{}}
	}
	if c.Paths == nil {
		c.Paths = []string{}
	}
	return c
}

func setClipboardCookie(c ClipBoard) string {
	return clipCookie + "=" + urlenc(httpx.MarshalString(c)) + "; Path=/; HttpOnly; SameSite=Lax"
}

var fileListQ = []httpx.UField{{Name: "path", Default: true}, {Name: "hidden", Default: true}, {Name: "sort", Default: true}, {Name: "order", Default: true}}

func (a *App) fileList(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	q, ok := httpx.Query(w, r, fileListQ)
	if !ok {
		return
	}
	raw := qstr(q, "path")
	if raw == "" {
		raw = "/"
	}
	cwd, ok := safePath(raw)
	if !ok {
		httpx.Text(w, 400, "잘못된 경로입니다.")
		return
	}
	hidden := qstr(q, "hidden") == "1"
	sortBy, order := "name", "asc"
	if qstr(q, "sort") == "mtime" {
		sortBy = "mtime"
	}
	if qstr(q, "order") == "desc" {
		order = "desc"
	}
	dirs, files, err := listRemote(a.HC, cwd, hidden, sortBy, order == "desc")
	if err != nil {
		httpx.Text(w, 404, "디렉토리를 열 수 없습니다: "+err.Error())
		return
	}
	entries := append(append([]FileEntry{}, dirs...), files...)
	c := base("filemanager", u)
	c["cwd_json"] = httpx.MarshalString(cwd)
	c["breadcrumbs"] = buildBreadcrumbs(cwd)
	c["parent"] = parentDir(cwd)
	c["entries"] = entries
	c["cwd"] = cwd
	c["clip"] = getClipboard(r)
	c["show_hidden"] = hidden
	c["sort"], c["order"] = sortBy, order
	page(w, 200, "filemanager.html", c)
}

type fmEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
}

func (a *App) fileListJSON(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.auth(w, r); !ok {
		return
	}
	q, ok := httpx.Query(w, r, fileListQ)
	if !ok {
		return
	}
	raw := qstr(q, "path")
	if raw == "" {
		raw = "/"
	}
	cwd, ok := safePath(raw)
	if !ok {
		jsonErr(w, 400, "잘못된 경로입니다.")
		return
	}
	dirs, files, err := listRemote(a.HC, cwd, false, "name", false)
	if err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	es := []fmEntry{}
	for _, d := range dirs {
		es = append(es, fmEntry{d.Name, d.Path, true})
	}
	for _, f := range files {
		es = append(es, fmEntry{f.Name, f.Path, false})
	}
	httpx.JSON(w, 200, map[string]any{"cwd": cwd, "parent": parentDir(cwd), "entries": es})
}

func (a *App) fileEdit(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	q, ok := httpx.Query(w, r, []httpx.UField{{Name: "path", Default: true}})
	if !ok {
		return
	}
	fpath, ok := safePath(qstr(q, "path"))
	if !ok {
		httpx.Text(w, 400, "잘못된 경로입니다.")
		return
	}
	size, _, err := statRemote(a.HC, fpath)
	if err != nil {
		httpx.Text(w, 404, "파일을 찾을 수 없습니다.")
		return
	}
	c := base("filemanager", u)
	c["file_path_json"] = httpx.MarshalString(fpath)
	c["file_path"] = fpath
	c["file_dir"] = parentDir(fpath)
	c["content"], c["read_only"], c["too_large"], c["binary"] = "", false, false, false
	if size > 1<<20 {
		c["too_large"] = true
		page(w, 200, "fileedit.html", c)
		return
	}
	data, err := readRemote(a.HC, fpath)
	if err != nil {
		httpx.Text(w, 500, "파일을 읽을 수 없습니다: "+err.Error())
		return
	}
	if isBinary(data) {
		c["binary"] = true
		page(w, 200, "fileedit.html", c)
		return
	}
	c["content"] = lossy(data)
	page(w, 200, "fileedit.html", c)
}

func (a *App) fileSave(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Json(w, r, "FileSaveReq", []httpx.Field{{Name: "path"}, {Name: "content"}})
	if !ok {
		return
	}
	fpath, ok := safePath(q["path"].(string))
	if !ok {
		jsonErr(w, 400, "잘못된 경로입니다.")
		return
	}
	if err := writeRemote(a.HC, fpath, []byte(q["content"].(string))); err != nil {
		jsonErr(w, hostErrStatus(err), err.Error())
		return
	}
	jsonOK(w)
}

func (a *App) fileDownload(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.auth(w, r); !ok {
		return
	}
	q, ok := httpx.Query(w, r, []httpx.UField{{Name: "path", Default: true}})
	if !ok {
		return
	}
	fpath, ok := safePath(qstr(q, "path"))
	if !ok {
		httpx.Text(w, 400, "잘못된 경로입니다.")
		return
	}
	if _, isDir, err := statRemote(a.HC, fpath); err != nil || isDir {
		httpx.Text(w, 404, "파일을 찾을 수 없습니다.")
		return
	}
	data, err := readRemote(a.HC, fpath)
	if err != nil {
		httpx.Text(w, 500, "파일을 열 수 없습니다.")
		return
	}
	name := fpath
	if i := strings.LastIndex(fpath, "/"); i >= 0 {
		name = fpath[i+1:]
	}
	w.Header().Set("Content-Type", mimeGuess(fpath))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(200)
	w.Write(data)
}

func (a *App) fileUpload(w http.ResponseWriter, r *http.Request) {
	mp, ok := httpx.NewMultipart(w, r)
	if !ok {
		return
	}
	var dir *string
	saved := false
	for {
		p, err := mp.Next()
		if err != nil {
			jsonErr(w, 400, "업로드 파싱 실패: "+err.Error())
			return
		}
		if p == nil {
			break
		}
		if p.Name == "dir" {
			s, err := p.Text()
			if err == nil {
				dir = &s
			} else {
				dir = nil
			}
			continue
		}
		if p.Name != "files" {
			continue
		}
		if dir == nil {
			jsonErr(w, 400, "잘못된 경로입니다.")
			return
		}
		sd, ok := safePath(*dir)
		if !ok {
			jsonErr(w, 400, "잘못된 경로입니다.")
			return
		}
		if p.FileName == nil {
			continue
		}
		orig := *p.FileName
		baseName := orig
		if i := strings.LastIndex(orig, "/"); i >= 0 {
			baseName = orig[i+1:]
		}
		b, err := p.Bytes()
		if err != nil {
			// 제한 초과 등으로 바디가 남아 있으면 읽어 버린다(남긴 채 닫으면 클라이언트가 RST를 받음)
			io.Copy(io.Discard, io.LimitReader(r.Body, 64<<20))
			jsonErr(w, 500, "파일 열기 실패: "+err.Error())
			return
		}
		if err := writeRemote(a.HC, strings.TrimRight(sd, "/")+"/"+baseName, b); err != nil {
			jsonErr(w, hostErrStatus(err), "쓰기 실패: "+err.Error())
			return
		}
		saved = true
	}
	if !saved {
		jsonErr(w, 400, "파일이 없습니다.")
		return
	}
	jsonOK(w)
}

func validName(n string) bool {
	return n != "" && n != "." && n != ".." && !strings.ContainsAny(n, "/\x00")
}

var dirNameReq = []httpx.Field{{Name: "dir", Default: true}, {Name: "name", Default: true}}

func (a *App) fileDirName(w http.ResponseWriter, r *http.Request, path string) {
	q, ok := httpx.Json(w, r, "DirNameReq", dirNameReq)
	if !ok {
		return
	}
	dir, ok := safePath(q["dir"].(string))
	name := q["name"].(string)
	if !ok || !validName(name) {
		jsonErr(w, 400, "잘못된 이름입니다.")
		return
	}
	if err := fsPost(a.HC, path, map[string]any{"dir": dir, "name": name}); err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	jsonOK(w)
}

func (a *App) fileMkdir(w http.ResponseWriter, r *http.Request)  { a.fileDirName(w, r, "/fs/mkdir") }
func (a *App) fileCreate(w http.ResponseWriter, r *http.Request) { a.fileDirName(w, r, "/fs/create") }

func (a *App) fileDelete(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Json(w, r, "PathsReq", []httpx.Field{{Name: "paths", Kind: httpx.KStrList, Default: true}})
	if !ok {
		return
	}
	clean := []string{}
	for _, p := range q["paths"].([]string) {
		fp, ok := safePath(p)
		if !ok {
			jsonErr(w, 400, "잘못된 경로: "+p)
			return
		}
		if fp == "/" {
			jsonErr(w, 400, "루트 디렉토리는 삭제할 수 없습니다.")
			return
		}
		clean = append(clean, fp)
	}
	if err := fsPost(a.HC, "/fs/delete", map[string]any{"paths": clean}); err != nil {
		jsonErr(w, hostErrStatus(err), err.Error())
		return
	}
	jsonOK(w)
}

func (a *App) fileRename(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Json(w, r, "RenameReq", []httpx.Field{{Name: "path"}, {Name: "newName", Default: true}})
	if !ok {
		return
	}
	fp, ok := safePath(q["path"].(string))
	if !ok {
		jsonErr(w, 400, "잘못된 이름입니다.")
		return
	}
	nn := q["newName"].(string)
	if nn == "" || strings.ContainsAny(nn, "/\x00") {
		jsonErr(w, 400, "잘못된 이름입니다.")
		return
	}
	target := strings.TrimRight(parentDir(fp), "/") + "/" + nn
	if _, _, err := statRemote(a.HC, target); err == nil {
		jsonErr(w, http.StatusConflict, "같은 이름의 파일이 이미 존재합니다.")
		return
	}
	if err := fsPost(a.HC, "/fs/rename", map[string]any{"path": fp, "newName": nn}); err != nil {
		jsonErr(w, hostErrStatus(err), err.Error())
		return
	}
	jsonOK(w)
}

// parseOctalU32: u32::from_str_radix(s, 8).
func parseOctalU32(s string) bool {
	t := strings.TrimPrefix(s, "+")
	if t == "" || len(s)-len(t) > 1 {
		return false
	}
	n, err := strconv.ParseUint(t, 8, 32)
	return err == nil && n <= 0xffffffff && !strings.ContainsAny(t, "+-")
}

func (a *App) fileChmod(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Json(w, r, "ChmodReq", []httpx.Field{{Name: "paths", Kind: httpx.KStrList, Default: true}, {Name: "mode", Default: true}})
	if !ok {
		return
	}
	mode := q["mode"].(string)
	if !parseOctalU32(mode) {
		jsonErr(w, 400, "잘못된 권한 형식입니다 (예: 644).")
		return
	}
	clean := []string{}
	for _, p := range q["paths"].([]string) {
		fp, ok := safePath(p)
		if !ok {
			jsonErr(w, 400, "잘못된 경로: "+p)
			return
		}
		clean = append(clean, fp)
	}
	if err := fsPost(a.HC, "/fs/chmod", map[string]any{"paths": clean, "mode": mode}); err != nil {
		jsonErr(w, hostErrStatus(err), err.Error())
		return
	}
	jsonOK(w)
}

func (a *App) fileClip(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Json(w, r, "ClipReq", []httpx.Field{{Name: "op", Default: true}, {Name: "paths", Kind: httpx.KStrList, Default: true}})
	if !ok {
		return
	}
	op := q["op"].(string)
	if op != "copy" && op != "cut" {
		jsonErr(w, 400, "op는 copy 또는 cut이어야 합니다.")
		return
	}
	clean := []string{}
	for _, p := range q["paths"].([]string) {
		fp, ok := safePath(p)
		if !ok {
			jsonErr(w, 400, "잘못된 경로: "+p)
			return
		}
		clean = append(clean, fp)
	}
	w.Header().Set("Set-Cookie", setClipboardCookie(ClipBoard{Op: op, Paths: clean}))
	jsonOK(w)
}

func (a *App) filePaste(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Json(w, r, "PasteReq", []httpx.Field{{Name: "destDir", Default: true}})
	if !ok {
		return
	}
	dest, ok := safePath(q["destDir"].(string))
	if !ok {
		jsonErr(w, 400, "잘못된 대상 경로입니다.")
		return
	}
	clip := getClipboard(r)
	if len(clip.Paths) == 0 {
		jsonErr(w, 400, "클립보드가 비어 있습니다.")
		return
	}
	for _, src := range clip.Paths {
		name := src
		if i := strings.LastIndex(src, "/"); i >= 0 {
			name = src[i+1:]
		}
		target := strings.TrimRight(dest, "/") + "/" + name
		if clip.Op == "cut" {
			if err := fsPost(a.HC, "/fs/move", map[string]any{"src": src, "dst": target}); err != nil {
				jsonErr(w, 500, "이동 실패: "+err.Error())
				return
			}
		} else if err := fsPost(a.HC, "/fs/copy", map[string]any{"src": src, "dst": target}); err != nil {
			jsonErr(w, 500, "복사 실패: "+err.Error())
			return
		}
	}
	if clip.Op == "cut" {
		w.Header().Set("Set-Cookie", clipCookie+"=; Path=/; Max-Age=0; HttpOnly")
	}
	jsonOK(w)
}

var _ = url.QueryEscape
