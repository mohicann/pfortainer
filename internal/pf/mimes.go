package pf

import "strings"

// mimeGuess: mime_guess::from_path(...).first_or_octet_stream() — 흔한 확장자만.
func mimeGuess(p string) string {
	name := p
	if i := strings.LastIndex(p, "/"); i >= 0 {
		name = p[i+1:]
	}
	i := strings.LastIndex(name, ".")
	if i <= 0 {
		return "application/octet-stream"
	}
	if t, ok := mimeTable[strings.ToLower(name[i+1:])]; ok {
		return t
	}
	return "application/octet-stream"
}

var mimeTable = map[string]string{
	"txt": "text/plain", "log": "text/plain", "conf": "text/plain", "ini": "text/plain", "cfg": "text/plain", "text": "text/plain", "md": "text/markdown",
	"html": "text/html", "htm": "text/html", "css": "text/css", "csv": "text/csv", "xml": "text/xml", "js": "text/javascript", "mjs": "text/javascript",
	"json": "application/json", "pdf": "application/pdf", "zip": "application/zip", "gz": "application/gzip", "tar": "application/x-tar",
	"tgz": "application/x-compressed", "bz2": "application/x-bzip2", "xz": "application/x-xz", "7z": "application/x-7z-compressed", "rar": "application/vnd.rar",
	"jpg": "image/jpeg", "jpeg": "image/jpeg", "jpe": "image/jpeg", "png": "image/png", "gif": "image/gif", "svg": "image/svg+xml", "webp": "image/webp",
	"ico": "image/x-icon", "bmp": "image/bmp", "tif": "image/tiff", "tiff": "image/tiff",
	"mp3": "audio/mpeg", "wav": "audio/wav", "ogg": "audio/ogg", "flac": "audio/flac", "aac": "audio/aac", "m4a": "audio/m4a",
	"mp4": "video/mp4", "mkv": "video/x-matroska", "avi": "video/x-msvideo", "mov": "video/quicktime", "webm": "video/webm",
	"yaml": "text/x-yaml", "yml": "text/x-yaml", "toml": "text/x-toml", "sh": "application/x-sh", "py": "text/x-python", "rs": "text/x-rust",
	"go": "text/x-go", "c": "text/x-c", "h": "text/x-c", "cpp": "text/x-c", "java": "text/x-java-source", "pem": "application/x-x509-ca-cert",
	"crt": "application/x-x509-ca-cert", "db": "application/octet-stream", "sqlite": "application/vnd.sqlite3", "wasm": "application/wasm",
	"woff": "font/woff", "woff2": "font/woff2", "ttf": "font/ttf", "otf": "font/otf", "deb": "application/vnd.debian.binary-package",
	"rpm": "application/x-redhat-package-manager", "iso": "application/x-iso9660-image", "exe": "application/octet-stream", "bin": "application/octet-stream",
	"dat": "application/octet-stream",
}
