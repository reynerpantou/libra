-- Schedules: when an experiment is planned to start (a reminder, not an
-- automatic start: starting stays a reviewed, human decision) and when it
-- ends (it's stopped then unless someone extends it).
ALTER TABLE experiments ADD COLUMN planned_start TIMESTAMPTZ;
ALTER TABLE experiments ADD COLUMN end_at TIMESTAMPTZ;
CREATE INDEX experiments_end_at ON experiments (end_at) WHERE end_at IS NOT NULL;

-- Notifications for people about experiments they own or review.
CREATE TABLE notifications (
    id            BIGSERIAL PRIMARY KEY,
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    experiment_id BIGINT REFERENCES experiments(id) ON DELETE CASCADE ON UPDATE CASCADE,
    kind          TEXT NOT NULL,              -- the action (start, launch, …) or a reminder (start_due, ending_soon, auto_stop)
    actor_id      BIGINT REFERENCES users(id) ON DELETE SET NULL,
    detail        JSONB NOT NULL DEFAULT '{}',
    dedupe        TEXT,                       -- reminders are sent once per key
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_at       TIMESTAMPTZ
);
CREATE INDEX notifications_user ON notifications (user_id, id DESC);
CREATE INDEX notifications_unread ON notifications (user_id) WHERE read_at IS NULL;
CREATE UNIQUE INDEX notifications_dedupe ON notifications (user_id, dedupe) WHERE dedupe IS NOT NULL;

-- Every lifecycle change is audited; the audit trail notifies the owner
-- (and, on submit, the invited reviewers; on a review decision, the owner).
CREATE FUNCTION libra_notify() RETURNS trigger AS $$
BEGIN
    IF NEW.experiment_id IS NULL THEN
        RETURN NEW;
    END IF;
    IF NEW.action IN ('create', 'clone', 'submit', 'withdraw', 'approve', 'reject', 'start', 'pause', 'resume', 'stop',
                      'launch', 'archive', 'extend', 'auto_stop', 'tuning_finished') THEN
        INSERT INTO notifications (user_id, experiment_id, kind, actor_id, detail)
        SELECT e.owner_id, e.id, NEW.action, NEW.actor_id, NEW.detail
        FROM experiments e WHERE e.id = NEW.experiment_id AND e.owner_id IS NOT NULL;
    END IF;
    IF NEW.action IN ('submit', 'invite_reviewers') THEN
        INSERT INTO notifications (user_id, experiment_id, kind, actor_id, detail)
        SELECT r.user_id, NEW.experiment_id, 'review_requested', NEW.actor_id, NEW.detail
        FROM experiment_reviewers r
        WHERE r.experiment_id = NEW.experiment_id AND r.decision IS NULL
          AND NOT EXISTS (SELECT 1 FROM notifications n WHERE n.user_id = r.user_id AND n.experiment_id = NEW.experiment_id
                          AND n.kind = 'review_requested' AND n.created_at > now() - interval '1 minute');
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;

CREATE TRIGGER audit_notify AFTER INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION libra_notify();
