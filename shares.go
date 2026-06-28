package main

import "encoding/json"

// ── NFS ───────────────────────────────────────────────────────────────────────

type NFSExport struct {
	Name     string `json:"name"`     // identifier / filename without .exports
	Path     string `json:"path"`     // exported path (first field of the line)
	Line     string `json:"line"`     // full export line as written in the file
	Clients  string `json:"clients"`  // for display: everything after path
}

type NFSStatus struct {
	Running bool        `json:"running"`
	Exports []NFSExport `json:"exports"`
}

func getNFSStatus() (NFSStatus, error) {
	var s NFSStatus
	b, err := hostGetAgent("/nfs/status")
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

func createNFSExport(e NFSExport) error {
	body, _ := json.Marshal(e)
	_, err := hostPost("/nfs/export", body)
	return err
}

func deleteNFSExport(name string) error {
	body, _ := json.Marshal(map[string]string{"name": name})
	_, err := hostPost("/nfs/export/delete", body)
	return err
}

func reloadNFS() (string, error) {
	b, err := hostPost("/nfs/reload", nil)
	if err != nil {
		return "", err
	}
	var resp struct {
		Output string `json:"output"`
	}
	json.Unmarshal(b, &resp)
	return resp.Output, nil
}

// ── SMB ───────────────────────────────────────────────────────────────────────

// SMBShare represents one Samba share managed via a drop-in fragment file
// under /usr/local/etc/samba4/shares.d/<name>.conf.
type SMBShare struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Comment    string `json:"comment"`
	ValidUsers string `json:"valid_users"`
	ReadOnly   bool   `json:"read_only"`
	Browseable bool   `json:"browseable"`
	GuestOK    bool   `json:"guest_ok"`
}

// SMBStatus is the JSON payload returned by the host-agent /smb/status endpoint.
type SMBStatus struct {
	Running bool       `json:"running"`
	SetupOK bool       `json:"setup_ok"` // shares.d include line found in smb4.conf
	Shares  []SMBShare `json:"shares"`
}

func getSMBStatus() (SMBStatus, error) {
	var s SMBStatus
	b, err := hostGetAgent("/smb/status")
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

func createSMBShare(share SMBShare) error {
	body, _ := json.Marshal(share)
	_, err := hostPost("/smb/share", body)
	return err
}

func deleteSMBShare(name string) error {
	body, _ := json.Marshal(map[string]string{"name": name})
	_, err := hostPost("/smb/share/delete", body)
	return err
}

func setupSamba() (string, error) {
	b, err := hostPost("/smb/setup", nil)
	if err != nil {
		return "", err
	}
	var resp struct {
		Message string `json:"message"`
	}
	json.Unmarshal(b, &resp)
	return resp.Message, nil
}

func reloadSamba() (string, error) {
	b, err := hostPost("/smb/reload", nil)
	if err != nil {
		return "", err
	}
	var resp struct {
		Output string `json:"output"`
	}
	json.Unmarshal(b, &resp)
	return resp.Output, nil
}
