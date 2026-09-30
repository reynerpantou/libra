-- Reviewers are invited when an experiment is submitted (at least one; the
-- owner may invite themselves). Only invited reviewers approve or reject.
CREATE TABLE experiment_reviewers (
    experiment_id BIGINT NOT NULL REFERENCES experiments(id) ON DELETE CASCADE ON UPDATE CASCADE,
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invited_by    BIGINT REFERENCES users(id) ON DELETE SET NULL,
    invited_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    decision      TEXT CHECK (decision IN ('approved', 'rejected')),
    note          TEXT NOT NULL DEFAULT '',
    decided_at    TIMESTAMPTZ,
    PRIMARY KEY (experiment_id, user_id)
);
CREATE INDEX experiment_reviewers_user ON experiment_reviewers (user_id);
