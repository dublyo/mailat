-- Public signup requests are separate from campaign membership until accepted.
ALTER TABLE lists ADD COLUMN confirmation_mode text NOT NULL DEFAULT 'single' CHECK (confirmation_mode IN ('single','double'));
ALTER TABLE lists ADD CONSTRAINT lists_id_org_unique UNIQUE (id,org_id);
CREATE TABLE signup_forms (
 id bigserial PRIMARY KEY, uuid uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
 org_id integer NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
 list_id integer NOT NULL, created_by integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 identity_id integer REFERENCES identities(id) ON DELETE SET NULL,
 name varchar(100) NOT NULL, title varchar(150) NOT NULL, description varchar(1000) NOT NULL DEFAULT '',
 consent_text varchar(1000) NOT NULL, button_text varchar(60) NOT NULL DEFAULT 'Subscribe',
 privacy_url varchar(2048) NOT NULL DEFAULT '', collect_name boolean NOT NULL DEFAULT false,
 published boolean NOT NULL DEFAULT false, version integer NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(list_id,org_id) REFERENCES lists(id,org_id) ON DELETE CASCADE
);
CREATE INDEX signup_forms_org ON signup_forms(org_id,id);
CREATE TABLE signup_requests (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), form_id bigint NOT NULL REFERENCES signup_forms(id) ON DELETE CASCADE,
 email varchar(255) NOT NULL, first_name varchar(100) NOT NULL DEFAULT '',
 status text NOT NULL CHECK(status IN ('pending','subscribed','blocked')),
 confirmation_mode text NOT NULL CHECK(confirmation_mode IN ('single','double')),
 disclosure text NOT NULL, form_version integer NOT NULL,
 token_hash text UNIQUE, expires_at timestamptz, sent_at timestamptz,
 confirmed_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(form_id,email)
);
CREATE INDEX signup_requests_recent ON signup_requests(form_id,created_at DESC,id);
CREATE TABLE signup_rate_limits (key text PRIMARY KEY, window_start timestamptz NOT NULL, hits integer NOT NULL);
CREATE INDEX signup_rate_expiry ON signup_rate_limits(window_start);
