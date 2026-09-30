-- The owner is claimed once through a setup link, not configured.
--
-- While no owner can sign in (a brand-new install, or an owner with no email
-- and no linked Google/Apple account), the server keeps one setup token and
-- prints a link with it to its own log. Whoever opens that link and signs in
-- with Google or Apple becomes the owner, and the token is deleted. Only
-- someone who can read the server's log can claim it.
CREATE TABLE owner_setup (
    id         BOOLEAN PRIMARY KEY DEFAULT true CHECK (id), -- at most one row
    token      TEXT NOT NULL,                               -- SHA-256 of the token
    expires_at TIMESTAMPTZ NOT NULL
);

-- A sign-in in progress is either a normal sign-in or an owner claim.
ALTER TABLE auth_flows ADD COLUMN purpose TEXT NOT NULL DEFAULT 'signin';
