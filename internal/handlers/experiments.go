package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/reynerpantou/libra/internal/assign"
	"github.com/reynerpantou/libra/internal/auth"
	"github.com/reynerpantou/libra/internal/middleware"
	"github.com/reynerpantou/libra/internal/serving"
)

// ---- layers ----

type Layer struct {
	ID          int64         `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Diversion   string        `json:"diversion"` // user_id | device_id
	Used        int           `json:"used_buckets"`
	Holders     []LayerHolder `json:"holders"`
}

type LayerHolder struct {
	ExperimentID int64  `json:"experiment_id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Buckets      int    `json:"buckets"`
}

func (s *Server) ListLayers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id, name, description, diversion FROM layers WHERE NOT auto ORDER BY name`)
	if err != nil {
		serverError(w, r, err)
		return
	}
	var out []*Layer
	byID := map[int64]*Layer{}
	for rows.Next() {
		l := &Layer{Holders: []LayerHolder{}}
		if err := rows.Scan(&l.ID, &l.Name, &l.Description, &l.Diversion); err != nil {
			rows.Close()
			serverError(w, r, err)
			return
		}
		out = append(out, l)
		byID[l.ID] = l
	}
	rows.Close()
	rows, err = s.DB.QueryContext(r.Context(), `
		SELECT layer_id, id, name, status, cardinality(buckets) FROM experiments
		WHERE status IN ('active', 'paused') ORDER BY id`)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var lid int64
		var h LayerHolder
		if err := rows.Scan(&lid, &h.ExperimentID, &h.Name, &h.Status, &h.Buckets); err != nil {
			serverError(w, r, err)
			return
		}
		if l := byID[lid]; l != nil {
			l.Holders = append(l.Holders, h)
			l.Used += h.Buckets
		}
	}
	if out == nil {
		out = []*Layer{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) CreateLayer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Diversion   string `json:"diversion"`
	}
	if !decodeOr400(w, r, &req) {
		return
	}
	name, ok := trimmed(req.Name, 80)
	if !ok {
		badRequest(w, "name is required")
		return
	}
	if req.Diversion == "" {
		req.Diversion = assign.DiversionUser
	}
	var n int
	if s.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM diversions WHERE key = $1`, req.Diversion).Scan(&n); n == 0 {
		badRequest(w, "that diversion type doesn't exist")
		return
	}
	salt, err := auth.NewToken()
	if err != nil {
		serverError(w, r, err)
		return
	}
	var id int64
	err = s.DB.QueryRowContext(r.Context(), `INSERT INTO layers (name, description, salt, diversion) VALUES ($1, $2, $3, $4) RETURNING id`,
		name, strings.TrimSpace(req.Description), salt[:16], req.Diversion).Scan(&id)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "conflict", "a layer with that name exists")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	_ = audit(r.Context(), s.DB, user(r).ID, 0, "layer", id, "create", "", "", req)
	writeJSON(w, http.StatusCreated, Layer{ID: id, Name: name, Description: req.Description, Diversion: req.Diversion, Holders: []LayerHolder{}})
}

func (s *Server) UpdateLayer(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decodeOr400(w, r, &req) {
		return
	}
	name, ok := trimmed(req.Name, 80)
	if !ok {
		badRequest(w, "name is required")
		return
	}
	_, err := s.DB.ExecContext(r.Context(), `UPDATE layers SET name = $2, description = $3 WHERE id = $1`, id, name, strings.TrimSpace(req.Description))
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "conflict", "a layer with that name exists")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- experiments ----

type VariantIn struct {
	ID        int64          `json:"id,omitempty"`
	Key       string         `json:"key"`
	Name      string         `json:"name"`
	IsControl bool           `json:"is_control"`
	Weight    int            `json:"weight"` // per mille
	Params    map[string]any `json:"params"`
}

type WhitelistEntry struct {
	UnitID    string    `json:"unit_id"`
	VariantID int64     `json:"variant_id"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

type Experiment struct {
	ID                int64            `json:"id"`
	BusinessID        int64            `json:"business_id"`
	BusinessKey       string           `json:"business_key"`
	BusinessName      string           `json:"business_name"`
	LayerID           int64            `json:"layer_id"`
	LayerName         string           `json:"layer_name"`
	Name              string           `json:"name"`
	Hypothesis        string           `json:"hypothesis"`
	Description       string           `json:"description"`
	OwnerID           *int64           `json:"owner_id"`
	OwnerName         string           `json:"owner_name"`
	Status            string           `json:"status"`
	TrafficTarget     int              `json:"traffic_target"` // per mille
	TrafficHeld       int              `json:"traffic_held"`
	Targeting         assign.Targeting `json:"targeting"`
	LayerDiversion    string           `json:"layer_diversion"`
	MetricGroupIDs    []int64          `json:"metric_group_ids"`
	LayerAuto         bool             `json:"layer_auto"` // a dedicated layer: the experiment can use up to 100%
	ReviewNote        string           `json:"review_note"`
	ReviewerName      string           `json:"reviewer_name"`
	LaunchedVariantID *int64           `json:"launched_variant_id"`
	LaunchRollout     int              `json:"launch_rollout"` // per mille of units the launched variant serves
	StartedAt         *time.Time       `json:"started_at"`
	EndedAt           *time.Time       `json:"ended_at"`
	LaunchedAt        *time.Time       `json:"launched_at"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
	Variants          []VariantIn      `json:"variants,omitempty"`
	Whitelist         []WhitelistEntry `json:"whitelist,omitempty"`
	Units             int64            `json:"units"`
	Actions           []string         `json:"actions,omitempty"`
}

const experimentSelect = `
	SELECT e.id, e.business_id, b.key, b.name, e.layer_id, l.name, l.diversion, l.auto, e.name, e.hypothesis, e.description,
	       e.owner_id, COALESCE(NULLIF(o.display_name, ''), o.username, ''), e.status, e.traffic_target, cardinality(e.buckets),
	       e.targeting, array_to_string(e.metric_group_ids, ','), e.review_note, COALESCE(NULLIF(rv.display_name, ''), rv.username, ''),
	       e.launched_variant_id, e.launch_rollout, e.started_at, e.ended_at, e.launched_at, e.created_at, e.updated_at,
	       (SELECT count(*) FROM assignments a WHERE a.experiment_id = e.id)
	FROM experiments e
	JOIN businesses b ON b.id = e.business_id
	JOIN layers l ON l.id = e.layer_id
	LEFT JOIN users o ON o.id = e.owner_id
	LEFT JOIN users rv ON rv.id = e.reviewer_id`

func scanExperiment(sc interface{ Scan(...any) error }) (Experiment, error) {
	var e Experiment
	var targeting []byte
	var groups string
	err := sc.Scan(&e.ID, &e.BusinessID, &e.BusinessKey, &e.BusinessName, &e.LayerID, &e.LayerName, &e.LayerDiversion, &e.LayerAuto, &e.Name, &e.Hypothesis, &e.Description,
		&e.OwnerID, &e.OwnerName, &e.Status, &e.TrafficTarget, &e.TrafficHeld,
		&targeting, &groups, &e.ReviewNote, &e.ReviewerName,
		&e.LaunchedVariantID, &e.LaunchRollout, &e.StartedAt, &e.EndedAt, &e.LaunchedAt, &e.CreatedAt, &e.UpdatedAt, &e.Units)
	if err != nil {
		return e, err
	}
	_ = json.Unmarshal(targeting, &e.Targeting)
	e.MetricGroupIDs = parseIDs(groups)
	return e, nil
}

func (s *Server) ListExperiments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	where := []string{"true"}
	var args []any
	if b := q.Get("business"); b != "" {
		args = append(args, b)
		where = append(where, fmt.Sprintf("b.key = $%d", len(args)))
	}
	if st := q.Get("status"); st != "" {
		args = append(args, strings.Split(st, ","))
		where = append(where, fmt.Sprintf("e.status = ANY($%d::text[])", len(args)))
	} else {
		where = append(where, "e.status <> 'archived'")
	}
	if text := strings.TrimSpace(q.Get("q")); text != "" {
		args = append(args, "%"+strings.ToLower(text)+"%")
		where = append(where, fmt.Sprintf("(lower(e.name) LIKE $%d OR lower(e.hypothesis) LIKE $%d)", len(args), len(args)))
	}
	if q.Get("mine") == "1" {
		args = append(args, user(r).ID)
		where = append(where, fmt.Sprintf("e.owner_id = $%d", len(args)))
	}
	rows, err := s.DB.QueryContext(r.Context(), experimentSelect+` WHERE `+strings.Join(where, " AND ")+` ORDER BY e.updated_at DESC LIMIT 500`, args...)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	out := []Experiment{}
	for rows.Next() {
		e, err := scanExperiment(rows)
		if err != nil {
			serverError(w, r, err)
			return
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) loadExperiment(ctx context.Context, id int64, full bool) (Experiment, error) {
	e, err := scanExperiment(s.DB.QueryRowContext(ctx, experimentSelect+` WHERE e.id = $1`, id))
	if err != nil || !full {
		return e, err
	}
	e.Variants, err = s.loadVariants(ctx, s.DB, id)
	if err != nil {
		return e, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT unit_id, variant_id, note, created_at FROM whitelist WHERE experiment_id = $1 ORDER BY created_at`, id)
	if err != nil {
		return e, err
	}
	defer rows.Close()
	e.Whitelist = []WhitelistEntry{}
	for rows.Next() {
		var wl WhitelistEntry
		if err := rows.Scan(&wl.UnitID, &wl.VariantID, &wl.Note, &wl.CreatedAt); err != nil {
			return e, err
		}
		e.Whitelist = append(e.Whitelist, wl)
	}
	return e, rows.Err()
}

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Server) loadVariants(ctx context.Context, q querier, expID int64) ([]VariantIn, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, key, name, is_control, weight, params FROM variants WHERE experiment_id = $1 ORDER BY position, id`, expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VariantIn{}
	for rows.Next() {
		var v VariantIn
		var params []byte
		if err := rows.Scan(&v.ID, &v.Key, &v.Name, &v.IsControl, &v.Weight, &params); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(params, &v.Params)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Server) GetExperiment(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	e, err := s.loadExperiment(r.Context(), id, true)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w, "experiment")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	e.Actions = availableActions(e, user(r))
	writeJSON(w, http.StatusOK, e)
}

type experimentRequest struct {
	BusinessID     int64            `json:"business_id"`
	LayerID        int64            `json:"layer_id"`
	Name           string           `json:"name"`
	Hypothesis     string           `json:"hypothesis"`
	Description    string           `json:"description"`
	OwnerID        *int64           `json:"owner_id"`
	TrafficTarget  int              `json:"traffic_target"`
	Targeting      assign.Targeting `json:"targeting"`
	MetricGroupIDs []int64          `json:"metric_group_ids"`
	// AutoDiversion, when set, gives the experiment its own dedicated layer
	// splitting by this diversion instead of a shared LayerID.
	AutoDiversion string      `json:"auto_diversion,omitempty"`
	Variants      []VariantIn `json:"variants"`
}

var variantKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

func (req *experimentRequest) validate() string {
	name, ok := trimmed(req.Name, 120)
	if !ok {
		return "name is required (up to 120 characters)"
	}
	req.Name = name
	req.Hypothesis = strings.TrimSpace(req.Hypothesis)
	req.Description = strings.TrimSpace(req.Description)
	if req.BusinessID == 0 || (req.LayerID == 0 && req.AutoDiversion == "") {
		return "choose a business and a layer (or Auto)"
	}
	if req.MetricGroupIDs == nil {
		req.MetricGroupIDs = []int64{}
	}
	if len(req.MetricGroupIDs) > 50 {
		return "at most 50 metric groups"
	}
	if req.TrafficTarget < 0 || req.TrafficTarget > assign.Buckets {
		return "traffic must be between 0% and 100%"
	}
	if err := assign.ValidateTargeting(req.Targeting, nil); err != nil {
		return err.Error()
	}
	if len(req.Variants) < 2 || len(req.Variants) > 20 {
		return "an experiment needs 2 to 20 variants"
	}
	controls, total := 0, 0
	seen := map[string]bool{}
	for i := range req.Variants {
		v := &req.Variants[i]
		v.Key = strings.TrimSpace(v.Key)
		v.Name = strings.TrimSpace(v.Name)
		if !variantKey.MatchString(v.Key) {
			return "variant keys are lowercase letters, digits, - and _ (e.g. control, treatment_a)"
		}
		if seen[v.Key] {
			return "variant keys must be unique"
		}
		seen[v.Key] = true
		if v.IsControl {
			controls++
		}
		if v.Weight < 0 || v.Weight > assign.Buckets {
			return "variant weights must be between 0% and 100%"
		}
		total += v.Weight
		if v.Params == nil {
			v.Params = map[string]any{}
		}
	}
	if controls != 1 {
		return "exactly one variant must be the control"
	}
	if total != assign.Buckets {
		return fmt.Sprintf("variant weights must add up to 100%% (they add up to %.1f%%)", float64(total)/10)
	}
	return ""
}

func (s *Server) CreateExperiment(w http.ResponseWriter, r *http.Request) {
	var req experimentRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	if msg := req.validate(); msg != "" {
		badRequest(w, msg)
		return
	}
	if msg := s.checkRefs(r.Context(), &req, 0); msg != "" {
		badRequest(w, msg)
		return
	}
	owner := user(r).ID
	if req.OwnerID != nil {
		owner = *req.OwnerID
	}
	salt, err := auth.NewToken()
	if err != nil {
		serverError(w, r, err)
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	if req.AutoDiversion != "" {
		if req.LayerID, err = createAutoLayer(r.Context(), tx, req.AutoDiversion); err != nil {
			serverError(w, r, err)
			return
		}
	}
	targeting, _ := json.Marshal(req.Targeting)
	var id int64
	if err := tx.QueryRowContext(r.Context(), `
		INSERT INTO experiments (business_id, layer_id, name, hypothesis, description, owner_id, salt, traffic_target, targeting, metric_group_ids)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		req.BusinessID, req.LayerID, req.Name, req.Hypothesis, req.Description, owner, salt[:16], req.TrafficTarget, targeting, req.MetricGroupIDs,
	).Scan(&id); err != nil {
		serverError(w, r, err)
		return
	}
	if req.AutoDiversion != "" {
		if _, err := tx.ExecContext(r.Context(), `UPDATE layers SET name = $2 WHERE id = $1`, req.LayerID, fmt.Sprintf("auto-%d", id)); err != nil {
			serverError(w, r, err)
			return
		}
	}
	if err := writeVariants(r.Context(), tx, id, req.Variants); err != nil {
		serverError(w, r, err)
		return
	}
	if err := audit(r.Context(), tx, user(r).ID, id, "experiment", id, "create", "", assign.StatusDraft, req); err != nil {
		serverError(w, r, err)
		return
	}
	if err := serving.Bump(r.Context(), tx); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, r, err)
		return
	}
	s.reload(r.Context())
	e, _ := s.loadExperiment(r.Context(), id, true)
	e.Actions = availableActions(e, user(r))
	writeJSON(w, http.StatusCreated, e)
}

func (s *Server) checkRefs(ctx context.Context, req *experimentRequest, self int64) string {
	var n int
	if s.DB.QueryRowContext(ctx, `SELECT count(*) FROM businesses WHERE id = $1`, req.BusinessID).Scan(&n); n == 0 {
		return "that business doesn't exist"
	}
	if req.AutoDiversion != "" {
		if s.DB.QueryRowContext(ctx, `SELECT count(*) FROM diversions WHERE key = $1`, req.AutoDiversion).Scan(&n); n == 0 {
			return "that diversion type doesn't exist"
		}
	} else if s.DB.QueryRowContext(ctx, `SELECT count(*) FROM layers WHERE id = $1 AND NOT auto`, req.LayerID).Scan(&n); n == 0 {
		// A dedicated layer is only valid for the experiment that owns it.
		if s.DB.QueryRowContext(ctx, `SELECT count(*) FROM experiments WHERE id = $1 AND layer_id = $2`, self, req.LayerID).Scan(&n); n == 0 {
			return "that layer doesn't exist"
		}
	}
	if len(req.MetricGroupIDs) > 0 {
		// Groups can come from any business or platform.
		if s.DB.QueryRowContext(ctx, `SELECT count(DISTINCT id) FROM metric_groups WHERE id = ANY($1::bigint[])`, req.MetricGroupIDs).Scan(&n); n != len(req.MetricGroupIDs) {
			return "every metric group must exist and appear once"
		}
	}
	if req.OwnerID != nil {
		if s.DB.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id = $1`, *req.OwnerID).Scan(&n); n == 0 {
			return "that owner doesn't exist"
		}
	}
	if attrs := req.Targeting.Attrs(); len(attrs) > 0 {
		known, err := s.attributeKeys(ctx)
		if err != nil {
			return "could not check targeting attributes"
		}
		if err := assign.ValidateTargeting(req.Targeting, known); err != nil {
			return err.Error()
		}
	}
	free := assign.Buckets // a dedicated layer is all this experiment's
	if req.AutoDiversion == "" {
		var err error
		if free, err = s.layerFree(ctx, s.DB, req.LayerID, self); err != nil {
			return "could not check the layer's free traffic"
		}
	}
	if req.TrafficTarget > free {
		return fmt.Sprintf("the layer only has %s free; lower the traffic or free the layer first", pct(free))
	}
	return ""
}

// layerFree is how many buckets of a layer aren't held by other running or
// paused experiments.
func (s *Server) layerFree(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, layerID, except int64) (int, error) {
	var used int
	err := q.QueryRowContext(ctx, `
		SELECT COALESCE(sum(cardinality(buckets)), 0) FROM experiments
		WHERE layer_id = $1 AND id <> $2 AND status IN ('active', 'paused')`, layerID, except).Scan(&used)
	return assign.Buckets - used, err
}

// writeVariants replaces an experiment's variants, keeping ids of variants
// whose key survives (so whitelist entries stay attached).
func writeVariants(ctx context.Context, tx *sql.Tx, expID int64, vs []VariantIn) error {
	keys := make([]string, len(vs))
	for i, v := range vs {
		keys[i] = v.Key
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM variants WHERE experiment_id = $1 AND NOT (key = ANY($2::text[]))`, expID, keys); err != nil {
		return err
	}
	for i, v := range vs {
		params, _ := json.Marshal(v.Params)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO variants (experiment_id, key, name, is_control, weight, params, position) VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (experiment_id, key) DO UPDATE SET name = EXCLUDED.name, is_control = EXCLUDED.is_control,
				weight = EXCLUDED.weight, params = EXCLUDED.params, position = EXCLUDED.position`,
			expID, v.Key, v.Name, v.IsControl, v.Weight, params, i); err != nil {
			return err
		}
	}
	return nil
}

// UpdateExperiment edits an experiment. Before it runs everything can change
// (an approved experiment goes back to draft for another review). Once it
// has started, the split and variants are locked — changing them would
// reshuffle units mid-measurement — but descriptive fields, targeting and the
// metric group can still change; traffic changes go through /traffic.
func (s *Server) UpdateExperiment(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	var req experimentRequest
	if !decodeOr400(w, r, &req) {
		return
	}
	cur, err := s.loadExperiment(r.Context(), id, true)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w, "experiment")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	if cur.Status == assign.StatusArchived || cur.Status == assign.StatusLaunched || cur.Status == assign.StatusStopped {
		badRequest(w, "a finished experiment can't be edited; clone it instead")
		return
	}
	if cur.Status == assign.StatusInReview {
		badRequest(w, "withdraw the review before editing")
		return
	}
	locked := cur.Status == assign.StatusActive || cur.Status == assign.StatusPaused
	if locked {
		// Keep what can't change; validate the rest.
		req.BusinessID, req.LayerID, req.Variants, req.TrafficTarget = cur.BusinessID, cur.LayerID, cur.Variants, cur.TrafficTarget
		req.AutoDiversion = ""
	}
	if msg := req.validate(); msg != "" {
		badRequest(w, msg)
		return
	}
	if msg := s.checkRefs(r.Context(), &req, id); msg != "" {
		badRequest(w, msg)
		return
	}
	next := cur.Status
	if cur.Status == assign.StatusApproved || cur.Status == assign.StatusRejected {
		next = assign.StatusDraft
	}
	owner := cur.OwnerID
	if req.OwnerID != nil {
		owner = req.OwnerID
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	// Switching to Auto reuses the experiment's dedicated layer if it has
	// one; switching away from it deletes that layer.
	dropAuto := int64(0)
	switch {
	case req.AutoDiversion != "" && cur.LayerAuto:
		req.LayerID = cur.LayerID
		if _, err := tx.ExecContext(r.Context(), `UPDATE layers SET diversion = $2 WHERE id = $1`, cur.LayerID, req.AutoDiversion); err != nil {
			serverError(w, r, err)
			return
		}
	case req.AutoDiversion != "":
		if req.LayerID, err = createAutoLayer(r.Context(), tx, req.AutoDiversion); err != nil {
			serverError(w, r, err)
			return
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE layers SET name = $2 WHERE id = $1`, req.LayerID, fmt.Sprintf("auto-%d", id)); err != nil {
			serverError(w, r, err)
			return
		}
	case cur.LayerAuto && req.LayerID != cur.LayerID:
		dropAuto = cur.LayerID
	}
	targeting, _ := json.Marshal(req.Targeting)
	if _, err := tx.ExecContext(r.Context(), `
		UPDATE experiments SET business_id = $2, layer_id = $3, name = $4, hypothesis = $5, description = $6, owner_id = $7,
			traffic_target = $8, targeting = $9, metric_group_ids = $10, status = $11, updated_at = now()
		WHERE id = $1`,
		id, req.BusinessID, req.LayerID, req.Name, req.Hypothesis, req.Description, owner, req.TrafficTarget, targeting, req.MetricGroupIDs, next,
	); err != nil {
		serverError(w, r, err)
		return
	}
	if dropAuto != 0 {
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM layers WHERE id = $1 AND auto AND NOT EXISTS (SELECT 1 FROM experiments WHERE layer_id = $1)`, dropAuto); err != nil {
			serverError(w, r, err)
			return
		}
	}
	if !locked {
		if err := writeVariants(r.Context(), tx, id, req.Variants); err != nil {
			serverError(w, r, err)
			return
		}
	}
	if err := audit(r.Context(), tx, user(r).ID, id, "experiment", id, "edit", cur.Status, next, req); err != nil {
		serverError(w, r, err)
		return
	}
	if err := serving.Bump(r.Context(), tx); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, r, err)
		return
	}
	s.reload(r.Context())
	e, _ := s.loadExperiment(r.Context(), id, true)
	e.Actions = availableActions(e, user(r))
	writeJSON(w, http.StatusOK, e)
}

// ---- lifecycle ----

// transitions: action -> allowed source statuses and the target status.
var transitions = map[string]struct {
	from []string
	to   string
}{
	"submit":   {[]string{assign.StatusDraft, assign.StatusRejected}, assign.StatusInReview},
	"withdraw": {[]string{assign.StatusInReview}, assign.StatusDraft},
	"approve":  {[]string{assign.StatusInReview}, assign.StatusApproved},
	"reject":   {[]string{assign.StatusInReview}, assign.StatusRejected},
	"start":    {[]string{assign.StatusApproved}, assign.StatusActive},
	"pause":    {[]string{assign.StatusActive}, assign.StatusPaused},
	"resume":   {[]string{assign.StatusPaused}, assign.StatusActive},
	"stop":     {[]string{assign.StatusActive, assign.StatusPaused}, assign.StatusStopped},
	"launch":   {[]string{assign.StatusActive, assign.StatusPaused, assign.StatusStopped}, assign.StatusLaunched},
	"archive":  {[]string{assign.StatusDraft, assign.StatusRejected, assign.StatusStopped, assign.StatusLaunched}, assign.StatusArchived},
}

func canReview(e Experiment, u middleware.User) bool {
	if middleware.Rank(u.Role) < middleware.Rank("editor") {
		return false
	}
	// Reviewing your own experiment defeats the point; admins may, as a
	// break-glass (it's recorded in the history).
	return e.OwnerID == nil || *e.OwnerID != u.ID || u.Role == "admin"
}

func availableActions(e Experiment, u middleware.User) []string {
	out := []string{}
	if middleware.Rank(u.Role) < middleware.Rank("editor") {
		return out
	}
	for _, a := range []string{"submit", "withdraw", "approve", "reject", "start", "pause", "resume", "stop", "launch", "archive"} {
		t := transitions[a]
		ok := false
		for _, f := range t.from {
			if f == e.Status {
				ok = true
			}
		}
		if !ok {
			continue
		}
		if (a == "approve" || a == "reject") && !canReview(e, u) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// takenBuckets returns buckets held by other experiments in the layer. It
// locks the layer row first so concurrent allocations serialize.
func takenBuckets(ctx context.Context, tx *sql.Tx, layerID, exceptExp int64) (map[int]bool, error) {
	if _, err := tx.ExecContext(ctx, `SELECT id FROM layers WHERE id = $1 FOR UPDATE`, layerID); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT unnest(buckets) FROM experiments WHERE layer_id = $1 AND id <> $2 AND status IN ('active', 'paused')`, layerID, exceptExp)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	taken := map[int]bool{}
	for rows.Next() {
		var b int
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		taken[b] = true
	}
	return taken, rows.Err()
}

func currentBuckets(ctx context.Context, tx *sql.Tx, expID int64) ([]int, string, error) {
	var raw, salt string
	if err := tx.QueryRowContext(ctx, `SELECT array_to_string(buckets, ','), salt FROM experiments WHERE id = $1 FOR UPDATE`, expID).Scan(&raw, &salt); err != nil {
		return nil, "", err
	}
	var out []int
	for _, id := range parseIDs(raw) {
		out = append(out, int(id))
	}
	return out, salt, nil
}

func (s *Server) ExperimentAction(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	action := r.PathValue("action")
	t, ok := transitions[action]
	if !ok {
		notFound(w, "action")
		return
	}
	var req struct {
		Note      string   `json:"note"`
		VariantID int64    `json:"variant_id"`
		Gradual   *Gradual `json:"gradual,omitempty"` // start / launch: ramp up instead of all at once
	}
	if r.ContentLength != 0 && !decodeOr400(w, r, &req) {
		return
	}
	req.Note = strings.TrimSpace(req.Note)
	e, err := s.loadExperiment(r.Context(), id, true)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w, "experiment")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	allowed := false
	for _, a := range availableActions(e, user(r)) {
		if a == action {
			allowed = true
		}
	}
	if !allowed {
		writeError(w, http.StatusConflict, "invalid_transition", fmt.Sprintf("can't %s an experiment that is %s", action, e.Status))
		return
	}
	if action == "reject" && req.Note == "" {
		badRequest(w, "say why it's rejected")
		return
	}
	// Gradual start ramps traffic up to the target; gradual launch ramps the
	// share of units the launched variant serves up to everyone.
	var planKind string
	var planFrom, planFirst, planTarget int
	if req.Gradual != nil {
		switch action {
		case "start":
			planKind, planTarget = "traffic", e.TrafficTarget
		case "launch":
			planKind, planTarget = "launch", assign.Buckets
		default:
			badRequest(w, "only start and launch can be gradual")
			return
		}
		if msg := req.Gradual.check(0, planTarget); msg != "" {
			badRequest(w, msg)
			return
		}
		planFirst = req.Gradual.first(0, planTarget)
	}

	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	// Re-check the status under a row lock so two clicks can't both apply.
	var status string
	if err := tx.QueryRowContext(r.Context(), `SELECT status FROM experiments WHERE id = $1 FOR UPDATE`, id).Scan(&status); err != nil {
		serverError(w, r, err)
		return
	}
	if status != e.Status {
		writeError(w, http.StatusConflict, "invalid_transition", "the experiment changed meanwhile; reload")
		return
	}
	to := t.to
	detail := map[string]any{}
	if req.Note != "" {
		detail["note"] = req.Note
	}
	sets := []string{"status = $2", "updated_at = now()"}
	args := []any{id, to}
	add := func(expr string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf(expr, len(args)))
	}

	switch action {
	case "submit":
		var requireReview bool
		if err := tx.QueryRowContext(r.Context(), `SELECT require_review FROM businesses WHERE id = $1`, e.BusinessID).Scan(&requireReview); err != nil {
			serverError(w, r, err)
			return
		}
		if !requireReview {
			to, args[1] = assign.StatusApproved, assign.StatusApproved
			detail["review"] = "skipped: the business doesn't require review"
		}
		add("review_note = $%d", "")
	case "approve", "reject":
		add("review_note = $%d", req.Note)
		add("reviewer_id = $%d", user(r).ID)
	case "start", "resume":
		taken, err := takenBuckets(r.Context(), tx, e.LayerID, id)
		if err != nil {
			serverError(w, r, err)
			return
		}
		cur, salt, err := currentBuckets(r.Context(), tx, id)
		if err != nil {
			serverError(w, r, err)
			return
		}
		want := e.TrafficTarget
		if planKind == "traffic" {
			want = planFirst
			add("traffic_target = $%d", want)
		}
		next, err := assign.Allocate(cur, want, taken, salt)
		if err != nil {
			badRequest(w, err.Error()+" — lower this experiment's traffic or free the layer")
			return
		}
		add("buckets = $%d", next)
		if action == "start" {
			sets = append(sets, "started_at = COALESCE(started_at, now())")
		}
		detail["buckets"] = len(next)
	case "stop":
		sets = append(sets, "buckets = '{}'", "ended_at = COALESCE(ended_at, now())")
	case "launch":
		found := false
		for _, v := range e.Variants {
			if v.ID == req.VariantID {
				found = true
				detail["variant"] = v.Key
			}
		}
		if !found {
			badRequest(w, "choose the variant to launch")
			return
		}
		add("launched_variant_id = $%d", req.VariantID)
		rollout := assign.Buckets
		if planKind == "launch" {
			rollout = planFirst
			detail["rollout"] = pct(rollout)
		}
		add("launch_rollout = $%d", rollout)
		sets = append(sets, "buckets = '{}'", "launched_at = now()", "ended_at = COALESCE(ended_at, now())")
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE experiments SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...); err != nil {
		serverError(w, r, err)
		return
	}
	if action == "stop" || action == "archive" || action == "launch" {
		err = cancelPlans(r.Context(), tx, id, "", "the experiment was "+to)
	}
	if err == nil && planKind != "" {
		detail["gradual"] = fmt.Sprintf("+%s every %d min up to %s", pct(req.Gradual.Step), req.Gradual.IntervalMinutes, pct(planTarget))
		err = startPlan(r.Context(), tx, id, planKind, planFrom, planFirst, planTarget, req.Gradual, user(r).ID)
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := audit(r.Context(), tx, user(r).ID, id, "experiment", id, action, e.Status, to, detail); err != nil {
		serverError(w, r, err)
		return
	}
	if err := serving.Bump(r.Context(), tx); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, r, err)
		return
	}
	s.reload(r.Context())
	out, _ := s.loadExperiment(r.Context(), id, true)
	out.Actions = availableActions(out, user(r))
	writeJSON(w, http.StatusOK, out)
}

func pct(perMille int) string { return fmt.Sprintf("%.1f%%", float64(perMille)/10) }

// ---- whitelist ----

func (s *Server) AddWhitelist(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	var req struct {
		UnitIDs   []string `json:"unit_ids"`
		VariantID int64    `json:"variant_id"`
		Note      string   `json:"note"`
	}
	if !decodeOr400(w, r, &req) {
		return
	}
	var n int
	if s.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM variants WHERE id = $1 AND experiment_id = $2`, req.VariantID, id).Scan(&n); n == 0 {
		badRequest(w, "choose a variant of this experiment")
		return
	}
	units := []string{}
	for _, u := range req.UnitIDs {
		if u = strings.TrimSpace(u); u != "" && len(u) <= 200 {
			units = append(units, u)
		}
	}
	if len(units) == 0 || len(units) > 500 {
		badRequest(w, "add 1 to 500 unit ids")
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	for _, u := range units {
		if _, err := tx.ExecContext(r.Context(), `
			INSERT INTO whitelist (experiment_id, unit_id, variant_id, note) VALUES ($1, $2, $3, $4)
			ON CONFLICT (experiment_id, unit_id) DO UPDATE SET variant_id = EXCLUDED.variant_id, note = EXCLUDED.note`,
			id, u, req.VariantID, strings.TrimSpace(req.Note)); err != nil {
			serverError(w, r, err)
			return
		}
	}
	if err := audit(r.Context(), tx, user(r).ID, id, "whitelist", id, "whitelist_add", "", "", map[string]any{"units": units, "variant_id": req.VariantID}); err != nil {
		serverError(w, r, err)
		return
	}
	if err := serving.Bump(r.Context(), tx); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, r, err)
		return
	}
	s.reload(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) RemoveWhitelist(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	unit := r.PathValue("unit")
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM whitelist WHERE experiment_id = $1 AND unit_id = $2`, id, unit); err != nil {
		serverError(w, r, err)
		return
	}
	_ = audit(r.Context(), tx, user(r).ID, id, "whitelist", id, "whitelist_remove", "", "", map[string]any{"unit": unit})
	if err := serving.Bump(r.Context(), tx); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, r, err)
		return
	}
	s.reload(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// ---- history & clone ----

type AuditEntry struct {
	ID         int64           `json:"id"`
	Entity     string          `json:"entity"`
	EntityID   *int64          `json:"entity_id"`
	Actor      string          `json:"actor"`
	Action     string          `json:"action"`
	FromStatus *string         `json:"from_status"`
	ToStatus   *string         `json:"to_status"`
	Detail     json.RawMessage `json:"detail"`
	CreatedAt  time.Time       `json:"created_at"`
}

func (s *Server) ExperimentHistory(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT a.id, a.entity, a.entity_id, COALESCE(NULLIF(u.display_name, ''), u.username, 'Libra'), a.action, a.from_status, a.to_status, a.detail, a.created_at
		FROM audit_log a LEFT JOIN users u ON u.id = a.actor_id WHERE a.experiment_id = $1 ORDER BY a.id DESC LIMIT 200`, id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var a AuditEntry
		if err := rows.Scan(&a.ID, &a.Entity, &a.EntityID, &a.Actor, &a.Action, &a.FromStatus, &a.ToStatus, &a.Detail, &a.CreatedAt); err != nil {
			serverError(w, r, err)
			return
		}
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, out)
}

// CloneExperiment copies an experiment's setup into a new draft.
func (s *Server) CloneExperiment(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	e, err := s.loadExperiment(r.Context(), id, true)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w, "experiment")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	salt, err := auth.NewToken()
	if err != nil {
		serverError(w, r, err)
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	layerID := e.LayerID
	if e.LayerAuto {
		// A dedicated layer isn't shared: the copy gets its own.
		if layerID, err = createAutoLayer(r.Context(), tx, e.LayerDiversion); err != nil {
			serverError(w, r, err)
			return
		}
	}
	targeting, _ := json.Marshal(e.Targeting)
	var newID int64
	name := e.Name + " (copy)"
	if len([]rune(name)) > 120 {
		name = string([]rune(name)[:120])
	}
	if err := tx.QueryRowContext(r.Context(), `
		INSERT INTO experiments (business_id, layer_id, name, hypothesis, description, owner_id, salt, traffic_target, targeting, metric_group_ids)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		e.BusinessID, layerID, name, e.Hypothesis, e.Description, user(r).ID, salt[:16], e.TrafficTarget, targeting, e.MetricGroupIDs,
	).Scan(&newID); err != nil {
		serverError(w, r, err)
		return
	}
	if e.LayerAuto {
		if _, err := tx.ExecContext(r.Context(), `UPDATE layers SET name = $2 WHERE id = $1`, layerID, fmt.Sprintf("auto-%d", newID)); err != nil {
			serverError(w, r, err)
			return
		}
	}
	if err := writeVariants(r.Context(), tx, newID, e.Variants); err != nil {
		serverError(w, r, err)
		return
	}
	_ = audit(r.Context(), tx, user(r).ID, newID, "experiment", newID, "clone", "", assign.StatusDraft, map[string]any{"from": id})
	if err := serving.Bump(r.Context(), tx); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, r, err)
		return
	}
	s.reload(r.Context())
	writeJSON(w, http.StatusCreated, map[string]int64{"id": newID})
}

// ExposureDaily shows new units per day per variant — a quick health check
// that assignment and logging are flowing.
func (s *Server) ExposureDaily(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT first_day, variant_id, count(*) FROM assignments WHERE experiment_id = $1 AND NOT multi_variant
		GROUP BY 1, 2 ORDER BY 1`, id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer rows.Close()
	type point struct {
		Day      string           `json:"day"`
		Variants map[string]int64 `json:"variants"`
	}
	byDay := map[string]*point{}
	var days []string
	for rows.Next() {
		var d time.Time
		var vid, n int64
		if err := rows.Scan(&d, &vid, &n); err != nil {
			serverError(w, r, err)
			return
		}
		k := d.Format("2006-01-02")
		if byDay[k] == nil {
			byDay[k] = &point{Day: k, Variants: map[string]int64{}}
			days = append(days, k)
		}
		byDay[k].Variants[fmt.Sprint(vid)] = n
	}
	sort.Strings(days)
	out := []*point{}
	for _, d := range days {
		out = append(out, byDay[d])
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) reload(ctx context.Context) {
	if s.Store != nil {
		_ = s.Store.Reload(ctx, false)
	}
}

// createAutoLayer makes a dedicated layer for one experiment. Its name is
// set to auto-<experiment id> once the experiment exists.
func createAutoLayer(ctx context.Context, tx *sql.Tx, diversion string) (int64, error) {
	salt, err := auth.NewToken()
	if err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO layers (name, description, salt, diversion, auto) VALUES ($1, $2, $3, $4, true) RETURNING id`,
		"auto-new-"+salt[:10], "Dedicated layer of one experiment", salt[:16], diversion).Scan(&id)
	return id, err
}
