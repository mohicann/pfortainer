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
			Timeout: 10 * time.Second,
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
	return json.Unmarshal(body, out)
}

func (c *PodmanClient) ListContainers() ([]APIContainer, error) {
	var result []APIContainer
	return result, c.get("/containers/json?all=true", &result)
}

func (c *PodmanClient) ListImages() ([]APIImage, error) {
	var result []APIImage
	return result, c.get("/images/json", &result)
}
