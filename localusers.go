package main

import "encoding/json"

type LocalUser struct {
	Username string   `json:"username"`
	UID      int      `json:"uid"`
	GID      int      `json:"gid"`
	FullName string   `json:"fullname"`
	Home     string   `json:"home"`
	Shell    string   `json:"shell"`
	Groups   []string `json:"groups"`
	HasSMB   bool     `json:"has_smb"`
}

type LocalGroup struct {
	Name    string   `json:"name"`
	GID     int      `json:"gid"`
	Members []string `json:"members"`
}

type LocalUsersStatus struct {
	Users  []LocalUser  `json:"users"`
	Groups []LocalGroup `json:"groups"`
}

func getLocalUsersStatus() (LocalUsersStatus, error) {
	var s LocalUsersStatus
	b, err := hostGetAgent("/localusers/status")
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

func createLocalUser(username, fullname, shell, password, smbPassword string) error {
	body, _ := json.Marshal(map[string]string{
		"username":     username,
		"fullname":     fullname,
		"shell":        shell,
		"password":     password,
		"smb_password": smbPassword,
	})
	_, err := hostPost("/localusers/create", body)
	return err
}

func deleteLocalUser(username string) error {
	body, _ := json.Marshal(map[string]string{"username": username})
	_, err := hostPost("/localusers/delete", body)
	return err
}

func setLocalUserSMBPasswd(username, password string) error {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	_, err := hostPost("/localusers/smbpasswd", body)
	return err
}

func createLocalGroup(name string) error {
	body, _ := json.Marshal(map[string]string{"name": name})
	_, err := hostPost("/localgroups/create", body)
	return err
}

func deleteLocalGroup(name string) error {
	body, _ := json.Marshal(map[string]string{"name": name})
	_, err := hostPost("/localgroups/delete", body)
	return err
}

func updateGroupMember(group, username, action string) error {
	body, _ := json.Marshal(map[string]string{"group": group, "username": username, "action": action})
	_, err := hostPost("/localgroups/member", body)
	return err
}
