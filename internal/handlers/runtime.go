package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/reynerpantou/libra/internal/assign"
	"github.com/reynerpantou/libra/internal/serving"
)

// MaxBatch caps items per ingestion request.
const MaxBatch = 5000

// selector is a platform or business selection: "" / "all" for everything,
// one key, a comma-separated list, or a JSON array of keys.
type resolveRequest struct {
	UserID   string            `json:"user_id,omitempty"`
	DeviceID string            `json:"device_id,omitempty"`
	IDs      map[string]string `json:"ids,omitempty"`     // other diversions, e.g. {"shop_id": "s-1"}
	UnitID   string            `json:"unit_id,omitempty"` // older name for user_id
	// Scope says which platforms and businesses the caller wants, as
	// {"<platform>": "all" | "<business>" | ["<business>", …]}, or "all"
	// for every platform. Required: a caller never gets every experiment
	// by accident.
	Scope json.RawMessage `json:"scope,omitempty"`
	// Platform and Business were replaced by Scope; sending them is an
	// error that says how to migrate.
	Platform json.RawMessage `json:"platform,omitempty"`
	Business json.RawMessage `json:"business,omitempty"`
	Attrs    map[string]any  `json:"attrs,omitempty"`
	// LogExposure records exposures (default true). Set false when the
	// caller only prefetches config and will report exposures itself.
	LogExposure *bool `json:"log_exposure,omitempty"`
	Debug       bool  `json:"debug,omitempty"`
}

type resolveResponse struct {
	UserID          string            `json:"user_id,omitempty"`
	DeviceID        string            `json:"device_id,omitempty"`
	Scope           map[string]any    `json:"scope"` // the scope served: platform -> "all" or its businesses
	SnapshotVersion int64             `json:"snapshot_version"`
	Params          map[string]any    `json:"params"`
	VariantIDs      []int64           `json:"variant_ids"` // every variant this unit got, for logging and debugging
	Hits            []assign.Hit      `json:"hits"`
	Conflicts       []assign.Conflict `json:"conflicts,omitempty"`
	Trace           []assign.Step     `json:"trace,omitempty"`
}

// resolveFor answers a resolve request against a snapshot. The message is
// set when the request is invalid.
// appURL is Libra's public address (set at startup) for links in responses.
var appURL string

func experimentURL(id int64, kind string) string {
	if kind == "tuning" {
		return fmt.Sprintf("%s/tuning/%d", appURL, id)
	}
	return fmt.Sprintf("%s/experiments/%d", appURL, id)
}

func resolveFor(snap *assign.Snapshot, req resolveRequest, trace bool) (resolveResponse, assign.Result, string) {
	req.UserID, req.DeviceID = strings.TrimSpace(req.UserID), strings.TrimSpace(req.DeviceID)
	if req.UserID == "" {
		req.UserID = strings.TrimSpace(req.UnitID)
	}
	if msg := checkIDs(req.UserID, req.DeviceID, req.IDs, false); msg != "" {
		return resolveResponse{}, assign.Result{}, msg
	}
	scope, msg := parseScope(snap, req)
	if msg != "" {
		return resolveResponse{}, assign.Result{}, msg
	}
	res := snap.Resolve(assign.Request{UserID: req.UserID, DeviceID: req.DeviceID, IDs: req.IDs, Scope: scope, Attrs: req.Attrs}, trace)
	out := resolveResponse{
		UserID: req.UserID, DeviceID: req.DeviceID, Scope: scopeEcho(snap, scope), SnapshotVersion: snap.Version,
		Params: res.Params, VariantIDs: []int64{}, Hits: res.Hits, Conflicts: res.Conflicts, Trace: res.Trace,
	}
	for i, h := range res.Hits {
		out.VariantIDs = append(out.VariantIDs, h.VariantID)
		out.Hits[i].URL = experimentURL(h.ExperimentID, snap.ExperimentKind(h.ExperimentID))
	}
	return out, res, ""
}

const scopeExample = `"scope": {"shop": ["search", "reco"], "market": "all"}`

// parseScope reads the request's scope. It accepts "all", or an object of
// platform -> "all" | business | [businesses]; it returns nil for "all".
func parseScope(snap *assign.Snapshot, req resolveRequest) (map[string][]string, string) {
	if len(req.Platform) > 0 || len(req.Business) > 0 {
		return nil, "platform and business were replaced by scope: send " + scopeExample + ` ("all" for every platform)`
	}
	raw := strings.TrimSpace(string(req.Scope))
	if raw == "" || raw == "null" {
		return nil, "scope is required — say which platforms and businesses you want, e.g. " + scopeExample + ` (or "scope": "all")`
	}
	var all string
	if json.Unmarshal(req.Scope, &all) == nil {
		if all == "all" || all == "*" {
			return nil, ""
		}
		return nil, `scope is "all" or an object like ` + scopeExample
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(req.Scope, &obj); err != nil || len(obj) == 0 {
		return nil, "scope must name at least one platform, like " + scopeExample
	}
	scope := map[string][]string{}
	for p, v := range obj {
		p = strings.TrimSpace(p)
		var one string
		var many []string
		switch {
		case json.Unmarshal(v, &one) == nil:
			if one == "all" || one == "*" {
				scope[p] = []string{}
			} else if one = strings.TrimSpace(one); one != "" {
				scope[p] = []string{one}
			} else {
				return nil, fmt.Sprintf(`scope.%s is empty: use "all" or business keys`, p)
			}
		case json.Unmarshal(v, &many) == nil:
			if len(many) == 0 {
				return nil, fmt.Sprintf(`scope.%s is an empty list: use "all" or business keys`, p)
			}
			bs := []string{}
			for _, b := range many {
				if b = strings.TrimSpace(b); b == "all" || b == "*" {
					return nil, fmt.Sprintf(`scope.%s: "all" goes alone, not in a list`, p)
				} else if b != "" && !slices.Contains(bs, b) {
					bs = append(bs, b)
				}
			}
			scope[p] = bs
		default:
			return nil, fmt.Sprintf(`scope.%s must be "all", a business key or a list of them`, p)
		}
	}
	if msg := snap.CheckScope(scope); msg != "" {
		return nil, msg
	}
	return scope, ""
}

// scopeEcho is the scope as served, for the response.
func scopeEcho(snap *assign.Snapshot, scope map[string][]string) map[string]any {
	out := map[string]any{}
	if scope == nil {
		for p := range snap.Platforms {
			out[p] = "all"
		}
		return out
	}
	for p, bs := range scope {
		if len(bs) == 0 {
			out[p] = "all"
		} else {
			out[p] = bs
		}
	}
	return out
}

func orEmpty(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// Resolve returns the unit's experiments and merged parameters, and logs
// exposures for units assigned by traffic (not whitelist or launches).
func (s *Server) Resolve(w http.ResponseWriter, r *http.Request) {
	var req resolveRequest
	if err := decodeLimit(r, &req, 64<<10); err != nil {
		badRequest(w, "could not read the request: "+err.Error())
		return
	}
	out, res, msg := resolveFor(s.Store.Snapshot(), req, req.Debug)
	if msg != "" {
		badRequest(w, msg)
		return
	}
	if req.LogExposure == nil || *req.LogExposure {
		now := time.Now().UTC()
		attrs := serving.DimensionAttrs(req.Attrs)
		for _, h := range res.Hits {
			if h.Reason == assign.ReasonInExperiment {
				s.Exposures.Log(serving.Exposure{ExperimentID: h.ExperimentID, VariantID: h.VariantID, UnitID: h.UnitID, UnitType: h.UnitType, TS: now, Attrs: attrs})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type exposureIn struct {
	ExperimentID int64             `json:"experiment_id"`
	VariantID    int64             `json:"variant_id"`
	UserID       string            `json:"user_id,omitempty"`
	DeviceID     string            `json:"device_id,omitempty"`
	IDs          map[string]string `json:"ids,omitempty"`
	UnitID       string            `json:"unit_id,omitempty"` // older name for user_id
	TS           *time.Time        `json:"ts,omitempty"`
	Attrs        map[string]any    `json:"attrs,omitempty"`
}

// IngestExposures accepts exposures reported by clients that resolved
// earlier with log_exposure=false (e.g. logging only when a screen renders).
// Only running experiments' real variants are accepted.
func (s *Server) IngestExposures(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Exposures []exposureIn `json:"exposures"`
	}
	if err := decodeLimit(r, &req, 16<<20); err != nil {
		badRequest(w, "could not read the request: "+err.Error())
		return
	}
	if len(req.Exposures) == 0 || len(req.Exposures) > MaxBatch {
		badRequest(w, fmt.Sprintf("send 1 to %d exposures", MaxBatch))
		return
	}
	// Running variants, and which id each experiment's layer splits on.
	snap := s.Store.Snapshot()
	valid := map[[2]int64]string{}
	for _, e := range snap.Experiments {
		if e.Status == assign.StatusActive || e.Status == assign.StatusPaused {
			diversion := assign.DiversionUser
			if l := snap.Layers[e.LayerID]; l != nil && l.Diversion != "" {
				diversion = l.Diversion
			}
			for _, v := range e.Variants {
				valid[[2]int64{e.ID, v.ID}] = diversion
			}
		}
	}
	now := time.Now().UTC()
	var batch []serving.Exposure
	var rejected []map[string]any
	for i, x := range req.Exposures {
		diversion, ok := valid[[2]int64{x.ExperimentID, x.VariantID}]
		unit := strings.TrimSpace(assign.Request{UserID: x.UserID, DeviceID: x.DeviceID, IDs: x.IDs, UnitID: x.UnitID}.ID(diversion))
		switch {
		case !ok:
			rejected = append(rejected, map[string]any{"index": i, "error": "not a variant of a running experiment"})
		case unit == "" || len(unit) > 200:
			rejected = append(rejected, map[string]any{"index": i, "error": "this experiment splits by " + diversion + "; send it"})
		default:
			ts := now
			if x.TS != nil && x.TS.Before(now.Add(time.Minute)) && x.TS.After(now.AddDate(0, 0, -7)) {
				ts = x.TS.UTC()
			}
			batch = append(batch, serving.Exposure{ExperimentID: x.ExperimentID, VariantID: x.VariantID, UnitID: unit, UnitType: diversion, TS: ts, Attrs: serving.DimensionAttrs(x.Attrs)})
		}
	}
	if err := serving.WriteExposures(r.Context(), s.DB, batch); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": len(batch), "rejected": rejectedOrEmpty(rejected)})
}

type eventIn struct {
	Platform string            `json:"platform,omitempty"` // needed when the business key exists on several platforms
	Business string            `json:"business"`
	Event    string            `json:"event"`
	UserID   string            `json:"user_id,omitempty"`
	DeviceID string            `json:"device_id,omitempty"`
	IDs      map[string]string `json:"ids,omitempty"`     // other diversion ids
	UnitID   string            `json:"unit_id,omitempty"` // older name for user_id
	TS       *time.Time        `json:"ts,omitempty"`
	Value    *float64          `json:"value,omitempty"`
	Props    json.RawMessage   `json:"props,omitempty"`
}

// IngestEvents accepts business events: the raw material measures are
// computed from. Timestamps up to 30 days old are accepted (backfills);
// the pipeline recomputes whichever days receive events.
func (s *Server) IngestEvents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Events []eventIn `json:"events"`
	}
	if err := decodeLimit(r, &req, 16<<20); err != nil {
		badRequest(w, "could not read the request: "+err.Error())
		return
	}
	if len(req.Events) == 0 || len(req.Events) > MaxBatch {
		badRequest(w, fmt.Sprintf("send 1 to %d events", MaxBatch))
		return
	}
	now := time.Now().UTC()
	var batch []serving.Event
	var rejected []map[string]any
	for i, x := range req.Events {
		bid, bizErr, err := s.businessID(r.Context(), strings.TrimSpace(x.Platform), strings.TrimSpace(x.Business))
		if err != nil {
			serverError(w, r, err)
			return
		}
		unit := strings.TrimSpace(x.UserID)
		if unit == "" {
			unit = strings.TrimSpace(x.UnitID)
		}
		device := strings.TrimSpace(x.DeviceID)
		name := strings.TrimSpace(x.Event)
		props := []byte(x.Props)
		var obj map[string]any
		switch {
		case bid == 0:
			rejected = append(rejected, map[string]any{"index": i, "error": bizErr})
			continue
		case name == "" || len(name) > 120:
			rejected = append(rejected, map[string]any{"index": i, "error": "event is required"})
			continue
		case checkIDs(unit, device, x.IDs, true) != "":
			rejected = append(rejected, map[string]any{"index": i, "error": checkIDs(unit, device, x.IDs, true)})
			continue
		case len(props) > 0 && (json.Unmarshal(props, &obj) != nil || obj == nil):
			rejected = append(rejected, map[string]any{"index": i, "error": "props must be a JSON object"})
			continue
		case len(props) > 8<<10:
			rejected = append(rejected, map[string]any{"index": i, "error": "props larger than 8 KB"})
			continue
		}
		ts := now
		if x.TS != nil {
			if x.TS.After(now.Add(5*time.Minute)) || x.TS.Before(now.AddDate(0, 0, -30)) {
				rejected = append(rejected, map[string]any{"index": i, "error": "ts must be within the last 30 days"})
				continue
			}
			ts = x.TS.UTC()
		}
		val := 0.0
		if x.Value != nil {
			val = *x.Value
		}
		batch = append(batch, serving.Event{BusinessID: bid, Name: name, UnitID: unit, DeviceID: device, IDs: idsJSON(x.IDs), TS: ts, Value: val, Props: props})
	}
	if err := serving.WriteEvents(r.Context(), s.DB, batch); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": len(batch), "rejected": rejectedOrEmpty(rejected)})
}

func rejectedOrEmpty(r []map[string]any) []map[string]any {
	if r == nil {
		return []map[string]any{}
	}
	if len(r) > 100 {
		return r[:100]
	}
	return r
}

// Businesses are cached briefly: ingestion is hot, businesses rarely change.
var bizCache struct {
	sync.Mutex
	ids    map[string]int64   // "platform/business" -> id
	byKey  map[string][]int64 // business key -> ids (one per platform that has it)
	loaded time.Time
}

// businessID finds the business an event belongs to. A key alone works
// when only one platform has it; otherwise the platform is needed. The
// string is why it wasn't found.
func (s *Server) businessID(ctx context.Context, platform, key string) (int64, string, error) {
	if p, k, ok := strings.Cut(key, "/"); ok && platform == "" {
		platform, key = p, k // "tokopedia/search"
	}
	bizCache.Lock()
	defer bizCache.Unlock()
	full := platform + "/" + key
	missing := (platform != "" && bizCache.ids[full] == 0) || (platform == "" && len(bizCache.byKey[key]) == 0)
	if bizCache.ids == nil || time.Since(bizCache.loaded) > 30*time.Second || (missing && time.Since(bizCache.loaded) > 2*time.Second) {
		rows, err := s.DB.QueryContext(ctx, `SELECT p.key, b.key, b.id FROM businesses b JOIN platforms p ON p.id = b.platform_id`)
		if err != nil {
			return 0, "", err
		}
		defer rows.Close()
		ids, byKey := map[string]int64{}, map[string][]int64{}
		for rows.Next() {
			var p, k string
			var id int64
			if err := rows.Scan(&p, &k, &id); err != nil {
				return 0, "", err
			}
			ids[p+"/"+k] = id
			byKey[k] = append(byKey[k], id)
		}
		bizCache.ids, bizCache.byKey, bizCache.loaded = ids, byKey, time.Now()
	}
	if key == "" {
		return 0, "business is required", nil
	}
	if platform != "" {
		if id := bizCache.ids[full]; id != 0 {
			return id, "", nil
		}
		return 0, fmt.Sprintf("platform %s has no business %s", platform, key), nil
	}
	switch ids := bizCache.byKey[key]; len(ids) {
	case 0:
		return 0, "unknown business " + key, nil
	case 1:
		return ids[0], "", nil
	}
	return 0, "business " + key + " exists on several platforms; send platform too", nil
}

// checkIDs validates a request's ids. requireOne: events and exposures
// belong to a unit; resolve doesn't need one — without ids it returns just
// the launched config, which applies to everyone.
func checkIDs(user, device string, ids map[string]string, requireOne bool) string {
	if len(ids) > 10 {
		return "at most 10 extra ids"
	}
	any := user != "" || device != ""
	for k, v := range ids {
		if len(k) > 40 || len(v) > 200 {
			return "ids: keys up to 40 and values up to 200 characters"
		}
		if v != "" {
			any = true
		}
	}
	if !any && requireOne {
		return "send user_id, device_id, or another id in ids"
	}
	if len(user) > 200 || len(device) > 200 {
		return "ids can be at most 200 characters"
	}
	return ""
}

func idsJSON(ids map[string]string) []byte {
	clean := map[string]string{}
	for k, v := range ids {
		if v = strings.TrimSpace(v); v != "" && k != assign.DiversionUser && k != assign.DiversionDevice {
			clean[k] = v
		}
	}
	if len(clean) == 0 {
		return nil
	}
	b, _ := json.Marshal(clean)
	return b
}
