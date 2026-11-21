-- 022_login_pin.sql: a 4-digit login PIN is required after the password (and
-- after TOTP when it is on) before a session is issued, unless the browser
-- presents a trusted-device token for the same user. Only hashes are stored:
-- bcrypt for the PIN, SHA-256 for device tokens and challenges.
ALTER TABLE users ADD COLUMN IF NOT EXISTS login_pin_hash text;
ALTER TABLE users ADD COLUMN IF NOT EXISTS login_pin_set_at timestamptz;

-- "Remember this device for 30 days". A row stops working when it expires
-- (fixed, never extended) or when the user's auth_version changes
-- (password change, suspension, 2FA reset, PIN reset, removal).
CREATE TABLE IF NOT EXISTS trusted_devices (
  id           bigserial PRIMARY KEY,
  uuid         uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE,
  user_id      integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash   varchar(64) NOT NULL UNIQUE,
  auth_version bigint NOT NULL,
  user_agent   text NOT NULL DEFAULT '',
  ip           text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS trusted_devices_user_created ON trusted_devices(user_id, created_at);
CREATE INDEX IF NOT EXISTS trusted_devices_expires ON trusted_devices(expires_at);

-- Wrong PINs per user, for the 5 per 15 minutes and 20 per 24 hours limits.
CREATE TABLE IF NOT EXISTS login_pin_failures (
  id         bigserial PRIMARY KEY,
  user_id    integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  ip         text,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS login_pin_failures_user_created ON login_pin_failures(user_id, created_at);

-- Challenges are either the TOTP step or the PIN step. factor_verified marks
-- a PIN challenge created after a passed TOTP step; trusted_device_id carries
-- a device token presented with the password through the TOTP step.
ALTER TABLE auth_challenges ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'totp';
ALTER TABLE auth_challenges DROP CONSTRAINT IF EXISTS auth_challenges_kind_check;
ALTER TABLE auth_challenges ADD CONSTRAINT auth_challenges_kind_check CHECK (kind IN ('totp','pin'));
ALTER TABLE auth_challenges ADD COLUMN IF NOT EXISTS factor_verified boolean NOT NULL DEFAULT false;
ALTER TABLE auth_challenges ADD COLUMN IF NOT EXISTS trusted_device_id bigint REFERENCES trusted_devices(id) ON DELETE SET NULL;

-- Sessions issued before this migration never passed a PIN step. Nobody has a
-- PIN yet, so this signs every user out once (the same columns the API sets
-- when it revokes a session); the next sign-in asks them to choose a PIN.
UPDATE user_sessions s SET active=false, revoked_at=now()
FROM users u
WHERE u.id=s.user_id AND u.login_pin_hash IS NULL AND s.active;
