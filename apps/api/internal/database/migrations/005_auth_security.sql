-- Counters are stored on the key so rate limits work across API replicas.
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS request_window timestamptz;
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS request_count integer NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_last_step bigint;
ALTER TABLE users ADD COLUMN IF NOT EXISTS auth_version bigint NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS auth_challenges (
 token_hash varchar(64) PRIMARY KEY,
 user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 auth_version bigint NOT NULL DEFAULT 0,
 expires_at timestamptz NOT NULL,
 attempts integer NOT NULL DEFAULT 0,
 consumed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS auth_challenges_user_created ON auth_challenges(user_id, created_at);
CREATE INDEX IF NOT EXISTS user_sessions_token_active ON user_sessions(token_hash) WHERE active;
