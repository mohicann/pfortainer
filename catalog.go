package main

// CatalogField describes a user-configurable install parameter.
type CatalogField struct {
	Name     string
	Label    string
	Type     string // "text"|"path"|"port"|"password"
	Default  string
	Required bool
}

// CatalogApp defines a pre-packaged single-container application.
type CatalogApp struct {
	ID          string
	Name        string
	Description string
	Icon        string // Bootstrap Icons name without "bi-"
	Category    string
	Image       string // docker image:tag
	Fields      []CatalogField
	// Ports[i] = "hostPort:containerPort" template — field references use {{.fieldname}}
	Ports   []string
	Volumes []string
	Env     map[string]string
	Cmd     []string
}

// ContainerName returns the managed container name for this app.
func (a CatalogApp) ContainerName() string { return "pf-" + a.ID }

// CatalogApps is the built-in app catalog.
var CatalogApps = []CatalogApp{
	{
		ID:          "filebrowser",
		Name:        "FileBrowser",
		Description: "웹 기반 파일 관리자. 브라우저에서 파일 업로드·다운로드·편집 가능.",
		Icon:        "folder2-open",
		Category:    "유틸리티",
		Image:       "filebrowser/filebrowser:latest",
		Fields: []CatalogField{
			{Name: "port", Label: "웹 포트", Type: "port", Default: "8080", Required: true},
			{Name: "data", Label: "루트 경로", Type: "path", Default: "/zdata", Required: true},
		},
		Ports:   []string{"{{port}}:80"},
		Volumes: []string{"{{data}}:/srv", "/var/db/pfortainer/filebrowser.db:/database.db"},
	},
	{
		ID:          "minio",
		Name:        "MinIO",
		Description: "S3 호환 오브젝트 스토리지. AWS S3 API와 호환.",
		Icon:        "database",
		Category:    "스토리지",
		Image:       "minio/minio:latest",
		Fields: []CatalogField{
			{Name: "api_port", Label: "API 포트", Type: "port", Default: "9000", Required: true},
			{Name: "console_port", Label: "콘솔 포트", Type: "port", Default: "9001", Required: true},
			{Name: "data", Label: "데이터 경로", Type: "path", Default: "/zdata/minio", Required: true},
			{Name: "user", Label: "Root 사용자", Type: "text", Default: "minioadmin", Required: true},
			{Name: "password", Label: "Root 비밀번호", Type: "password", Default: "", Required: true},
		},
		Ports:   []string{"{{api_port}}:9000", "{{console_port}}:9001"},
		Volumes: []string{"{{data}}:/data"},
		Env:     map[string]string{"MINIO_ROOT_USER": "{{user}}", "MINIO_ROOT_PASSWORD": "{{password}}"},
		Cmd:     []string{"server", "/data", "--console-address", ":9001"},
	},
	{
		ID:          "syncthing",
		Name:        "Syncthing",
		Description: "P2P 파일 동기화. 클라우드 없이 기기 간 실시간 동기화.",
		Icon:        "arrow-repeat",
		Category:    "동기화",
		Image:       "syncthing/syncthing:latest",
		Fields: []CatalogField{
			{Name: "web_port", Label: "웹 포트", Type: "port", Default: "8384", Required: true},
			{Name: "data", Label: "데이터 경로", Type: "path", Default: "/zdata/syncthing", Required: true},
		},
		Ports:   []string{"{{web_port}}:8384", "22000:22000/tcp", "22000:22000/udp", "21027:21027/udp"},
		Volumes: []string{"{{data}}:/var/syncthing"},
	},
	{
		ID:          "jellyfin",
		Name:        "Jellyfin",
		Description: "오픈소스 미디어 서버. 영화·TV·음악 스트리밍.",
		Icon:        "play-circle",
		Category:    "미디어",
		Image:       "jellyfin/jellyfin:latest",
		Fields: []CatalogField{
			{Name: "port", Label: "웹 포트", Type: "port", Default: "8096", Required: true},
			{Name: "config", Label: "설정 경로", Type: "path", Default: "/var/db/jellyfin", Required: true},
			{Name: "media", Label: "미디어 경로", Type: "path", Default: "/zdata/media", Required: true},
		},
		Ports:   []string{"{{port}}:8096"},
		Volumes: []string{"{{config}}:/config", "{{media}}:/media:ro"},
	},
	{
		ID:          "vaultwarden",
		Name:        "Vaultwarden",
		Description: "Bitwarden 호환 비밀번호 관리자 서버.",
		Icon:        "shield-lock",
		Category:    "보안",
		Image:       "vaultwarden/server:latest",
		Fields: []CatalogField{
			{Name: "port", Label: "웹 포트", Type: "port", Default: "8070", Required: true},
			{Name: "data", Label: "데이터 경로", Type: "path", Default: "/var/db/vaultwarden", Required: true},
		},
		Ports:   []string{"{{port}}:80"},
		Volumes: []string{"{{data}}:/data"},
	},
	{
		ID:          "uptime-kuma",
		Name:        "Uptime Kuma",
		Description: "셀프호스티드 서비스 모니터링. 웹사이트·포트·컨테이너 가동 감시.",
		Icon:        "heart-pulse",
		Category:    "모니터링",
		Image:       "louislam/uptime-kuma:latest",
		Fields: []CatalogField{
			{Name: "port", Label: "웹 포트", Type: "port", Default: "3001", Required: true},
			{Name: "data", Label: "데이터 경로", Type: "path", Default: "/var/db/uptime-kuma", Required: true},
		},
		Ports:   []string{"{{port}}:3001"},
		Volumes: []string{"{{data}}:/app/data"},
	},
	{
		ID:          "gitea",
		Name:        "Gitea",
		Description: "경량 셀프호스티드 Git 서비스. GitHub 대체.",
		Icon:        "git",
		Category:    "개발",
		Image:       "gitea/gitea:latest",
		Fields: []CatalogField{
			{Name: "web_port", Label: "웹 포트", Type: "port", Default: "3000", Required: true},
			{Name: "ssh_port", Label: "SSH 포트", Type: "port", Default: "2222", Required: true},
			{Name: "data", Label: "데이터 경로", Type: "path", Default: "/zdata/gitea", Required: true},
		},
		Ports:   []string{"{{web_port}}:3000", "{{ssh_port}}:22"},
		Volumes: []string{"{{data}}:/data"},
	},
}
