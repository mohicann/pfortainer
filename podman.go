package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type PodmanClient struct {
	hc *http.Client
}

func newPodmanClient(socketPath string) *PodmanClient {
	return &PodmanClient{
		hc: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

type APIContainer struct {
	ID      string    `json:"Id"`
	Names   []string  `json:"Names"`
	Image   string    `json:"Image"`
	State   string    `json:"State"`
	Status  string    `json:"Status"`
	Created int64     `json:"Created"`
	Ports   []APIPort `json:"Ports"`
}

type APIPort struct {
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

type APIImage struct {
	ID       string   `json:"Id"`
	RepoTags []string `json:"RepoTags"`
	Created  int64    `json:"Created"`
	Size     int64    `json:"Size"`
}

type APIContainerDetail struct {
	ID      string   `json:"Id"`
	Name    string   `json:"Name"`
	Created string   `json:"Created"`
	Image   string   `json:"Image"`
	Path    string   `json:"Path"`
	Args    []string `json:"Args"`

	State struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		Paused     bool   `json:"Paused"`
		Restarting bool   `json:"Restarting"`
		Pid        int    `json:"Pid"`
		ExitCode   int    `json:"ExitCode"`
		Error      string `json:"Error"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
	} `json:"State"`

	RestartCount int `json:"RestartCount"`

	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		Mode        string `json:"Mode"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`

	Config struct {
		Hostname   string            `json:"Hostname"`
		Env        []string          `json:"Env"`
		Cmd        []string          `json:"Cmd"`
		Entrypoint []string          `json:"Entrypoint"`
		WorkingDir string            `json:"WorkingDir"`
		Labels     map[string]string `json:"Labels"`
	} `json:"Config"`

	HostConfig struct {
		NetworkMode   string `json:"NetworkMode"`
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`

	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
		Networks map[string]struct {
			IPAddress  string `json:"IPAddress"`
			Gateway    string `json:"Gateway"`
			MacAddress string `json:"MacAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

func (c *PodmanClient) get(path string, out any) error {
	resp, err := c.hc.Get("http://podman" + path)
	if err != nil {
		return fmt.Errorf("podman: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Cause   string `json:"cause"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &apiErr)
		if apiErr.Message == "" {
			apiErr.Message = string(body)
		}
		return &PodmanError{StatusCode: resp.StatusCode, Cause: apiErr.Cause, Message: apiErr.Message}
	}
	return json.Unmarshal(body, out)
}

// PodmanError represents an error response from the Podman API.
type PodmanError struct {
	StatusCode int
	Cause      string
	Message    string
}

func (e *PodmanError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("podman: unexpected status %d", e.StatusCode)
}

// do performs a request against the Podman API and returns a *PodmanError
// if the response status code indicates failure.
func (c *PodmanClient) do(method, path string) error {
	req, err := http.NewRequest(method, "http://podman"+path, nil)
	if err != nil {
		return fmt.Errorf("podman: %w", err)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("podman: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 || resp.StatusCode == http.StatusNotModified {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	var apiErr struct {
		Cause   string `json:"cause"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &apiErr)
	if apiErr.Message == "" {
		apiErr.Message = string(body)
	}
	return &PodmanError{StatusCode: resp.StatusCode, Cause: apiErr.Cause, Message: apiErr.Message}
}

func (c *PodmanClient) ListContainers() ([]APIContainer, error) {
	var result []APIContainer
	return result, c.get("/containers/json?all=true", &result)
}

func (c *PodmanClient) ListImages() ([]APIImage, error) {
	var result []APIImage
	return result, c.get("/images/json", &result)
}

func (c *PodmanClient) InspectContainer(id string) (*APIContainerDetail, error) {
	var result APIContainerDetail
	if err := c.get("/containers/"+id+"/json", &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *PodmanClient) RemoveImage(id string, force bool) error {
	path := "/images/" + id
	if force {
		path += "?force=true"
	}
	return c.do(http.MethodDelete, path)
}

func (c *PodmanClient) StartContainer(id string) error {
	return c.do(http.MethodPost, "/containers/"+id+"/start")
}

func (c *PodmanClient) StopContainer(id string) error {
	return c.do(http.MethodPost, "/containers/"+id+"/stop")
}

func (c *PodmanClient) RestartContainer(id string) error {
	return c.do(http.MethodPost, "/containers/"+id+"/restart")
}

func (c *PodmanClient) KillContainer(id string) error {
	return c.do(http.MethodPost, "/containers/"+id+"/kill")
}

func (c *PodmanClient) PauseContainer(id string) error {
	return c.do(http.MethodPost, "/containers/"+id+"/pause")
}

func (c *PodmanClient) UnpauseContainer(id string) error {
	return c.do(http.MethodPost, "/containers/"+id+"/unpause")
}

func (c *PodmanClient) RemoveContainer(id string) error {
	return c.do(http.MethodDelete, "/containers/"+id)
}
