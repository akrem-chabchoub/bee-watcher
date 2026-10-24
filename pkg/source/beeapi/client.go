// Package beeapi is a small, read-only client for the Bee HTTP API.
//
// Only cheap GET endpoints are exposed for periodic polling. Expensive or
// state-changing endpoints are deliberately absent; see pkg/server for the
// explicit, user-confirmed actions.
package beeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"beescope/pkg/model"
)

// maxBody bounds every response body read from a node.
const maxBody = 32 << 20

// HTTPError is returned for non-2xx responses.
type HTTPError struct {
	Code int
	Body string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("http %d: %s", e.Code, e.Body)
}

// Code returns the HTTP status of err, or 0.
func Code(err error) int {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Code
	}
	return 0
}

// Client talks to one Bee node.
type Client struct {
	base string
	hc   *http.Client
}

// New returns a client for the node at base (no trailing slash).
func New(base string, hc *http.Client) *Client {
	return &Client{base: strings.TrimRight(base, "/"), hc: hc}
}

// Base returns the node base URL.
func (c *Client) Base() string { return c.base }

// Get performs a GET and returns the raw body.
func (c *Client) Get(ctx context.Context, path string, hdr http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, &HTTPError{Code: resp.StatusCode, Body: msg}
	}
	return body, nil
}

func getJSON[T any](ctx context.Context, c *Client, path string) (T, []byte, error) {
	var out T
	body, err := c.Get(ctx, path, nil)
	if err != nil {
		return out, nil, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, body, fmt.Errorf("decode %s: %w", path, err)
	}
	return out, body, nil
}

// Health is GET /health.
func (c *Client) Health(ctx context.Context) (*model.Health, []byte, error) {
	h, raw, err := getJSON[model.Health](ctx, c, "/health")
	return &h, raw, err
}

// Status is GET /status.
func (c *Client) Status(ctx context.Context) (*model.Status, []byte, error) {
	type resp struct {
		Snapshots []model.Status `json:"snapshots"`
	}
	// Older nodes return the snapshot directly; newer ones wrap it.
	body, err := c.Get(ctx, "/status", nil)
	if err != nil {
		return nil, nil, err
	}
	var r resp
	if err := json.Unmarshal(body, &r); err == nil && len(r.Snapshots) > 0 {
		return &r.Snapshots[0], body, nil
	}
	var s model.Status
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, body, fmt.Errorf("decode /status: %w", err)
	}
	return &s, body, nil
}

type peerInfo struct {
	Address string `json:"address"`
	Metrics *struct {
		LatencyEWMA                int64   `json:"latencyEWMA"`
		Reachability               string  `json:"reachability"`
		Healthy                    bool    `json:"healthy"`
		SessionConnectionDirection string  `json:"sessionConnectionDirection"`
		SessionConnectionDuration  float64 `json:"sessionConnectionDuration"`
	} `json:"metrics"`
}

type binInfo struct {
	Population     int        `json:"population"`
	Connected      int        `json:"connected"`
	ConnectedPeers []peerInfo `json:"connectedPeers"`
}

type kadParams struct {
	BaseAddr            string             `json:"baseAddr"`
	Population          int                `json:"population"`
	Connected           int                `json:"connected"`
	Depth               uint8              `json:"depth"`
	Reachability        string             `json:"reachability"`
	NetworkAvailability string             `json:"networkAvailability"`
	Bins                map[string]binInfo `json:"bins"`
	LightNodes          binInfo            `json:"lightNodes"`
}

// DecodeTopology converts a /topology body into the flattened model.
func DecodeTopology(body []byte) (*model.Topology, error) {
	var kp kadParams
	if err := json.Unmarshal(body, &kp); err != nil {
		return nil, fmt.Errorf("decode /topology: %w", err)
	}
	t := &model.Topology{
		BaseAddr:            kp.BaseAddr,
		Population:          kp.Population,
		Connected:           kp.Connected,
		Depth:               kp.Depth,
		Reachability:        kp.Reachability,
		NetworkAvailability: kp.NetworkAvailability,
		LightConnected:      kp.LightNodes.Connected,
	}
	for name, b := range kp.Bins {
		n, err := strconv.Atoi(strings.TrimPrefix(name, "bin_"))
		if err != nil || n < 0 || n > 31 {
			continue
		}
		t.BinConnected[n] = b.Connected
		t.BinPopulation[n] = b.Population
		for _, p := range b.ConnectedPeers {
			kp := model.KadPeer{Address: p.Address, Bin: n, Healthy: true}
			if p.Metrics != nil {
				kp.Direction = p.Metrics.SessionConnectionDirection
				kp.LatencyMS = float64(p.Metrics.LatencyEWMA) // already milliseconds
				kp.Reachability = p.Metrics.Reachability
				kp.Healthy = p.Metrics.Healthy
				kp.SessionS = p.Metrics.SessionConnectionDuration
			}
			t.Peers = append(t.Peers, kp)
		}
	}
	return t, nil
}

// Topology is GET /topology.
func (c *Client) Topology(ctx context.Context) (*model.Topology, []byte, error) {
	body, err := c.Get(ctx, "/topology", nil)
	if err != nil {
		return nil, nil, err
	}
	t, err := DecodeTopology(body)
	return t, body, err
}

// Stamps is GET /stamps.
func (c *Client) Stamps(ctx context.Context) ([]model.Stamp, []byte, error) {
	type resp struct {
		Stamps []model.Stamp `json:"stamps"`
	}
	r, raw, err := getJSON[resp](ctx, c, "/stamps")
	return r.Stamps, raw, err
}

// ChainState is GET /chainstate.
func (c *Client) ChainState(ctx context.Context) (*model.ChainState, []byte, error) {
	type resp struct {
		ChainTip     uint64          `json:"chainTip"`
		Block        uint64          `json:"block"`
		CurrentPrice json.RawMessage `json:"currentPrice"`
	}
	r, raw, err := getJSON[resp](ctx, c, "/chainstate")
	if err != nil {
		return nil, raw, err
	}
	return &model.ChainState{ChainTip: r.ChainTip, Block: r.Block, CurrentPrice: strings.Trim(string(r.CurrentPrice), `"`)}, raw, nil
}

// Redistribution is GET /redistributionstate. It performs a chain call on
// the node, so callers must poll it slowly.
func (c *Client) Redistribution(ctx context.Context) (*model.Redistribution, []byte, error) {
	r, raw, err := getJSON[model.Redistribution](ctx, c, "/redistributionstate")
	return &r, raw, err
}

// Tags is GET /tags.
func (c *Client) Tags(ctx context.Context) ([]model.Tag, []byte, error) {
	type resp struct {
		Tags []model.Tag `json:"tags"`
	}
	r, raw, err := getJSON[resp](ctx, c, "/tags?limit=100")
	return r.Tags, raw, err
}

// Metrics is GET /metrics, returned as raw Prometheus text.
func (c *Client) Metrics(ctx context.Context) ([]byte, error) {
	return c.Get(ctx, "/metrics", nil)
}

// Request performs an arbitrary request and returns the status and body
// without treating non-2xx as an error. It is used only by explicit,
// user-confirmed actions; pollers use the typed GET methods above.
func (c *Client) Request(ctx context.Context, method, path string, hdr http.Header) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
	if err != nil {
		return 0, nil, err
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read body: %w", err)
	}
	return resp.StatusCode, body, nil
}

// Do sends a request with a body and returns the response unread, for
// streaming uploads and downloads. size is the body length, or -1 when
// unknown. The caller closes the response body. Use it only for explicit
// user actions, never for polling.
func (c *Client) Do(ctx context.Context, method, path string, hdr http.Header, body io.Reader, size int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return c.hc.Do(req)
}
