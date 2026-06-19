package main

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

type NetdataClient struct {
	BaseURL    string
	httpClient *http.Client
}

func newNetdataClient(baseURL string) *NetdataClient {
	return &NetdataClient{
		BaseURL:    baseURL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

var validChart = regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`)

func (nc *NetdataClient) proxyData(w http.ResponseWriter, chart, after, points string) {
	if !validChart.MatchString(chart) {
		http.Error(w, "invalid chart name", http.StatusBadRequest)
		return
	}
	url := fmt.Sprintf("%s/api/v1/data?chart=%s&after=%s&points=%s&format=json",
		nc.BaseURL, chart, after, points)
	resp, err := nc.httpClient.Get(url)
	if err != nil {
		http.Error(w, "netdata unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
