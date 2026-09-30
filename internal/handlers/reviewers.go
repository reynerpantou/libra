package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/reynerpantou/libra/internal/assign"
)

type Reviewer struct {
	UserID    int64      `json:"user_id"`
	Name      string     `json:"name"`
	Decision  string     `json:"decision,omitempty"` // approved | rejected; empty while pending
	Note      string     `json:"note,omitempty"`
	InvitedBy string     `json:"invited_by"`
	InvitedAt time.Time  `json:"invited_at"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
}

func (s *Server) loadReviewers(ctx context.Context, expID int64) ([]Reviewer, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT r.user_id, COALESCE(NULLIF(u.display_name, ''), u.username), COALESCE(r.decision, ''), r.note,
		       COALESCE(NULLIF(ib.display_name, ''), ib.username, ''), r.invited_at, r.decided_at
		FROM experiment_reviewers r JOIN users u ON u.id = r.user_id LEFT JOIN users ib ON ib.id = r.invited_by
		WHERE r.experiment_id = $1 ORDER BY r.invited_at, u.username`, expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Reviewer{}
	for rows.Next() {
		var x Reviewer
		if err := rows.Scan(&x.UserID, &x.Name, &x.Decision, &x.Note, &x.InvitedBy, &x.InvitedAt, &x.DecidedAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// inviteReviewers adds reviewers (replacing the previous review's when
// fresh). Reviewers must be editors or admins — they approve. It returns
// their names, or a message saying what's wrong.
func inviteReviewers(ctx context.Context, tx *sql.Tx, expID int64, ids []int64, by int64, fresh bool) ([]string, string, error) {
	seen := map[int64]bool{}
	var uniq []int64
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if fresh && len(uniq) == 0 {
		return nil, "invite at least one reviewer (you can invite yourself)", nil
	}
	if len(uniq) > 20 {
		return nil, "at most 20 reviewers", nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, COALESCE(NULLIF(display_name, ''), username) FROM users WHERE id = ANY($1::bigint[]) AND role IN ('editor', 'admin')`, uniq)
	if err != nil {
		return nil, "", err
	}
	var names []string
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return nil, "", err
		}
		names = append(names, name)
	}
	rows.Close()
	if len(names) != len(uniq) {
		return nil, "reviewers must be existing editors or admins", nil
	}
	if fresh {
		if _, err := tx.ExecContext(ctx, `DELETE FROM experiment_reviewers WHERE experiment_id = $1`, expID); err != nil {
			return nil, "", err
		}
	}
	var inviter any
	if by > 0 {
		inviter = by
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO experiment_reviewers (experiment_id, user_id, invited_by)
		SELECT $1, unnest($2::bigint[]), $3 ON CONFLICT DO NOTHING`, expID, uniq, inviter); err != nil {
		return nil, "", err
	}
	return names, "", nil
}

// InviteReviewers adds reviewers to an experiment that's in review.
func (s *Server) InviteReviewers(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	var req struct {
		UserIDs []int64 `json:"user_ids"`
	}
	if !decodeOr400(w, r, &req) {
		return
	}
	if len(req.UserIDs) == 0 {
		badRequest(w, "choose someone to invite")
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(r.Context(), `SELECT status FROM experiments WHERE id = $1 FOR UPDATE`, id).Scan(&status); err != nil {
		notFound(w, "experiment")
		return
	}
	if status != assign.StatusInReview {
		badRequest(w, "reviewers are invited when the experiment is submitted for review")
		return
	}
	names, msg, err := inviteReviewers(r.Context(), tx, id, req.UserIDs, user(r).ID, false)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if msg != "" {
		badRequest(w, msg)
		return
	}
	if err := audit(r.Context(), tx, user(r).ID, id, "experiment", id, "invite_reviewers", "", "", map[string]any{"reviewers": names}); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, r, err)
		return
	}
	out, _ := s.loadExperiment(r.Context(), id, true)
	out.Actions = availableActions(out, user(r))
	writeJSON(w, http.StatusOK, out)
}
