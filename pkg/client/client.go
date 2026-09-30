// Package client is a small Go client for Libra's runtime API: resolve a
// unit's experiments and parameters, report exposures, and send business
// events (batched in the background).
//
//	c := client.New("https://example.com/libra", os.Getenv("LIBRA_KEY"))
//	defer c.Close()
//	res, err := c.Resolve(ctx, client.ResolveRequest{UserID: "user-42", DeviceID: "dev-9f3a", Platform: "shop", Business: "search"})
//	formula := res.String("search.ranking.formula", "ctr * cvr")
//	c.Track(client.Event{Platform: "shop", Business: "search", Event: "order", UserID: "user-42", Value: 35.9})
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Client struct {
	base string
	key  string
	http *http.Client

	mu      sync.Mutex
	pending []Event
	flushCh chan struct{}
	done    chan struct{}
	closed  bool

	// BatchSize and FlushInterval control event batching.
	BatchSize     int
	FlushInterval time.Duration
	// OnError receives background send errors (default: log).
	OnError func(error)
}

// New returns a client for the Libra at baseURL, e.g.
// "https://example.com/libra". Call Close to flush pending events.
func New(baseURL, apiKey string) *Client {
	c := &Client{
		base: strings.TrimRight(baseURL, "/"), key: apiKey,
		http:          &http.Client{Timeout: 5 * time.Second},
		flushCh:       make(chan struct{}, 1),
		done:          make(chan struct{}),
		BatchSize:     500,
		FlushInterval: 2 * time.Second,
		OnError:       func(err error) { log.Printf("libra: %v", err) },
	}
	go c.loop()
	return c
}

type ResolveRequest struct {
	UserID      string         `json:"user_id,omitempty"`
	DeviceID    string         `json:"device_id,omitempty"`
	Platform    string         `json:"platform,omitempty"` // required when Libra has several platforms
	Business    string         `json:"business,omitempty"`
	Attrs       map[string]any `json:"attrs,omitempty"`
	LogExposure *bool          `json:"log_exposure,omitempty"`
}

type Hit struct {
	ExperimentID int64  `json:"experiment_id"`
	Experiment   string `json:"experiment"`
	VariantID    int64  `json:"variant_id"`
	Variant      string `json:"variant"`
	Source       string `json:"source"`
	UnitType     string `json:"unit_type"` // user_id | device_id: which id the assignment used
	UnitID       string `json:"unit_id"`
}

type ResolveResponse struct {
	UserID          string         `json:"user_id"`
	DeviceID        string         `json:"device_id"`
	SnapshotVersion int64          `json:"snapshot_version"`
	Params          map[string]any `json:"params"`
	Hits            []Hit          `json:"hits"`
}

// Get returns the parameter at a dot path ("search.ranking.formula").
func (r *ResolveResponse) Get(path string) (any, bool) {
	var cur any = r.Params
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// String returns a string parameter or def.
func (r *ResolveResponse) String(path, def string) string {
	if v, ok := r.Get(path); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

// Float returns a numeric parameter or def.
func (r *ResolveResponse) Float(path string, def float64) float64 {
	if v, ok := r.Get(path); ok {
		if f, ok := v.(float64); ok {
			return f
		}
	}
	return def
}

// Bool returns a boolean parameter or def.
func (r *ResolveResponse) Bool(path string, def bool) bool {
	if v, ok := r.Get(path); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

// Variant returns the variant key the unit got in an experiment, or "".
func (r *ResolveResponse) Variant(experimentID int64) string {
	for _, h := range r.Hits {
		if h.ExperimentID == experimentID {
			return h.Variant
		}
	}
	return ""
}

// Resolve asks Libra which experiments and parameters apply to a unit. On
// error, callers should fall back to their defaults.
func (c *Client) Resolve(ctx context.Context, req ResolveRequest) (*ResolveResponse, error) {
	var out ResolveResponse
	if err := c.post(ctx, "/api/v1/resolve", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type Exposure struct {
	ExperimentID int64          `json:"experiment_id"`
	VariantID    int64          `json:"variant_id"`
	UserID       string         `json:"user_id,omitempty"`
	DeviceID     string         `json:"device_id,omitempty"`
	TS           *time.Time     `json:"ts,omitempty"`
	Attrs        map[string]any `json:"attrs,omitempty"`
}

// LogExposures reports exposures for units resolved with LogExposure=false.
func (c *Client) LogExposures(ctx context.Context, xs []Exposure) error {
	return c.post(ctx, "/api/v1/exposures", map[string]any{"exposures": xs}, nil)
}

type Event struct {
	Platform string         `json:"platform,omitempty"` // needed when the business key exists on several platforms
	Business string         `json:"business"`
	Event    string         `json:"event"`
	UserID   string         `json:"user_id,omitempty"`
	DeviceID string         `json:"device_id,omitempty"`
	TS       *time.Time     `json:"ts,omitempty"`
	Value    float64        `json:"value,omitempty"`
	Props    map[string]any `json:"props,omitempty"`
}

// Track queues an event; it's sent in the background.
func (c *Client) Track(e Event) {
	if e.TS == nil {
		now := time.Now().UTC()
		e.TS = &now
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.pending = append(c.pending, e)
	full := len(c.pending) >= c.BatchSize
	c.mu.Unlock()
	if full {
		select {
		case c.flushCh <- struct{}{}:
		default:
		}
	}
}

// SendEvents sends events synchronously.
func (c *Client) SendEvents(ctx context.Context, events []Event) error {
	for len(events) > 0 {
		n := len(events)
		if n > 5000 {
			n = 5000
		}
		if err := c.post(ctx, "/api/v1/events", map[string]any{"events": events[:n]}, nil); err != nil {
			return err
		}
		events = events[n:]
	}
	return nil
}

// Flush sends queued events now.
func (c *Client) Flush(ctx context.Context) error {
	c.mu.Lock()
	batch := c.pending
	c.pending = nil
	c.mu.Unlock()
	return c.SendEvents(ctx, batch)
}

// Close flushes queued events and stops the background sender.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	close(c.done)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.Flush(ctx)
}

func (c *Client) loop() {
	t := time.NewTicker(c.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
		case <-c.flushCh:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := c.Flush(ctx); err != nil && c.OnError != nil {
			c.OnError(err)
		}
		cancel()
	}
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		var e struct{ Message string }
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(data))
		}
		return fmt.Errorf("libra %s: %d %s", path, resp.StatusCode, e.Message)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
