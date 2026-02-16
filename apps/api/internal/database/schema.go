package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// InitSchema creates all required database tables if they don't exist.
// This is called on API startup to ensure the database is ready.
func InitSchema(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := db.ExecContext(ctx, schemaSQL)
	if err != nil {
		return fmt.Errorf("failed to initialize schema: %w", err)
	}

	return nil
}

const schemaSQL = `
-- Enable UUID extension
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- Organizations
CREATE TABLE IF NOT EXISTS organizations (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	name VARCHAR(255) NOT NULL,
	slug VARCHAR(100) UNIQUE NOT NULL,
	settings JSONB DEFAULT '{}',
	max_domains INT DEFAULT 5,
	max_users INT DEFAULT 10,
	max_contacts INT DEFAULT 1000,
	max_identities INT DEFAULT 10,
	monthly_email_limit INT DEFAULT 10000,
	plan VARCHAR(50) DEFAULT 'free',
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);

-- Users
CREATE TABLE IF NOT EXISTS users (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	org_id INT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
	email VARCHAR(255) UNIQUE NOT NULL,
	password_hash VARCHAR(255) NOT NULL,
	name VARCHAR(255),
	role VARCHAR(50) DEFAULT 'member',
	email_verified BOOLEAN DEFAULT false,
	email_verified_at TIMESTAMPTZ(6),
	status VARCHAR(50) DEFAULT 'active',
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW(),
	last_login_at TIMESTAMPTZ(6),
	backup_codes TEXT[] DEFAULT '{}',
	totp_enabled BOOLEAN DEFAULT false,
	totp_secret VARCHAR(64),
	totp_verified_at TIMESTAMPTZ(6)
);

-- API Keys
CREATE TABLE IF NOT EXISTS api_keys (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	org_id INT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
	user_id INT,
	name VARCHAR(255) NOT NULL,
	key_prefix VARCHAR(10) NOT NULL,
	key_hash VARCHAR(255) NOT NULL,
	permissions TEXT[] DEFAULT '{}',
	rate_limit INT DEFAULT 100,
	last_used_at TIMESTAMPTZ(6),
	expires_at TIMESTAMPTZ(6),
	created_at TIMESTAMPTZ(6) DEFAULT NOW()
);

-- Domains
CREATE TABLE IF NOT EXISTS domains (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	org_id INT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
	name VARCHAR(255) NOT NULL,
	verification_token VARCHAR(100) NOT NULL,
	verified_at TIMESTAMPTZ(6),
	mx_verified BOOLEAN DEFAULT false,
	spf_verified BOOLEAN DEFAULT false,
	dkim_verified BOOLEAN DEFAULT false,
	dmarc_verified BOOLEAN DEFAULT false,
	dkim_selector VARCHAR(50) DEFAULT 'mail',
	dkim_private_key TEXT,
	dkim_public_key TEXT,
	default_mailbox_quota BIGINT DEFAULT 1073741824,
	max_message_size INT DEFAULT 26214400,
	open_tracking BOOLEAN DEFAULT true,
	click_tracking BOOLEAN DEFAULT true,
	status VARCHAR(50) DEFAULT 'pending',
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW(),
	last_dns_check_at TIMESTAMPTZ(6),
	ses_verified BOOLEAN DEFAULT false,
	ses_dkim_tokens TEXT[] DEFAULT '{}',
	ses_identity_arn VARCHAR(255),
	email_provider VARCHAR(20) DEFAULT 'ses',
	receiving_enabled BOOLEAN DEFAULT false,
	receiving_s3_bucket VARCHAR(255),
	receiving_sns_topic_arn VARCHAR(255),
	receiving_rule_set_name VARCHAR(255),
	receiving_rule_name VARCHAR(255),
	receiving_setup_at TIMESTAMPTZ(6),
	UNIQUE(org_id, name)
);

-- Domain DNS Records
CREATE TABLE IF NOT EXISTS domain_dns_records (
	id SERIAL PRIMARY KEY,
	domain_id INT NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
	record_type VARCHAR(10) NOT NULL,
	hostname VARCHAR(255) NOT NULL,
	expected_value TEXT NOT NULL,
	actual_value TEXT,
	verified BOOLEAN DEFAULT false,
	last_checked_at TIMESTAMPTZ(6),
	UNIQUE(domain_id, record_type, hostname)
);

-- Identities (email mailboxes)
CREATE TABLE IF NOT EXISTS identities (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	domain_id INT NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
	email VARCHAR(255) UNIQUE NOT NULL,
	display_name VARCHAR(255),
	signature_html TEXT,
	signature_text TEXT,
	is_default BOOLEAN DEFAULT false,
	can_send BOOLEAN DEFAULT true,
	can_receive BOOLEAN DEFAULT true,
	is_catch_all BOOLEAN DEFAULT false,
	color VARCHAR(7),
	password_hash VARCHAR(255),
	encrypted_password TEXT,
	quota_bytes BIGINT DEFAULT 1073741824,
	used_bytes BIGINT DEFAULT 0,
	stalwart_account_id VARCHAR(255),
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_identities_stalwart ON identities(stalwart_account_id);
CREATE INDEX IF NOT EXISTS idx_identities_domain_catchall ON identities(domain_id, is_catch_all);

-- Contacts
CREATE TABLE IF NOT EXISTS contacts (
	id BIGSERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	org_id INT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
	email VARCHAR(255) NOT NULL,
	first_name VARCHAR(100),
	last_name VARCHAR(100),
	attributes JSONB DEFAULT '{}',
	status VARCHAR(50) DEFAULT 'active',
	consent_source VARCHAR(100),
	consent_timestamp TIMESTAMPTZ(6),
	consent_ip VARCHAR(45),
	consent_user_agent TEXT,
	last_engaged_at TIMESTAMPTZ(6),
	engagement_score DOUBLE PRECISION DEFAULT 0,
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW(),
	UNIQUE(org_id, email)
);
CREATE INDEX IF NOT EXISTS idx_contacts_org_status ON contacts(org_id, status);

-- Lists
CREATE TABLE IF NOT EXISTS lists (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	org_id INT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
	name VARCHAR(255) NOT NULL,
	description TEXT,
	type VARCHAR(50) DEFAULT 'static',
	segment_rules JSONB,
	contact_count INT DEFAULT 0,
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);

-- List Contacts
CREATE TABLE IF NOT EXISTS list_contacts (
	id BIGSERIAL PRIMARY KEY,
	list_id INT NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
	contact_id BIGINT NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	UNIQUE(list_id, contact_id)
);

-- Campaigns
CREATE TABLE IF NOT EXISTS campaigns (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	org_id INT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
	name VARCHAR(255) NOT NULL,
	subject VARCHAR(500) NOT NULL,
	html_content TEXT,
	text_content TEXT,
	template_id INT,
	from_name VARCHAR(255) NOT NULL,
	from_email VARCHAR(255) NOT NULL,
	reply_to VARCHAR(255),
	list_id INT NOT NULL,
	status VARCHAR(50) DEFAULT 'draft',
	scheduled_at TIMESTAMPTZ(6),
	started_at TIMESTAMPTZ(6),
	completed_at TIMESTAMPTZ(6),
	total_recipients INT DEFAULT 0,
	sent_count INT DEFAULT 0,
	delivered_count INT DEFAULT 0,
	open_count INT DEFAULT 0,
	click_count INT DEFAULT 0,
	bounce_count INT DEFAULT 0,
	unsubscribe_count INT DEFAULT 0,
	complaint_count INT DEFAULT 0,
	is_ab_test BOOLEAN DEFAULT false,
	ab_test_settings JSONB,
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);

-- Templates
CREATE TABLE IF NOT EXISTS templates (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	org_id INT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
	name VARCHAR(255) NOT NULL,
	description TEXT,
	html_content TEXT NOT NULL,
	text_content TEXT,
	category VARCHAR(50) DEFAULT 'general',
	variables_schema JSONB,
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);

-- Transactional Emails
CREATE TABLE IF NOT EXISTS transactional_emails (
	id BIGSERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	org_id INT NOT NULL,
	identity_id INT DEFAULT 0,
	message_id VARCHAR(500) UNIQUE NOT NULL,
	from_address VARCHAR(255) NOT NULL,
	to_addresses TEXT NOT NULL,
	cc_addresses TEXT,
	bcc_addresses TEXT,
	reply_to VARCHAR(255),
	subject VARCHAR(500) NOT NULL,
	html_body TEXT,
	text_body TEXT,
	template_id INT,
	tags TEXT,
	metadata TEXT,
	status VARCHAR(50) DEFAULT 'queued',
	scheduled_for TIMESTAMPTZ(6),
	sent_at TIMESTAMPTZ(6),
	delivered_at TIMESTAMPTZ(6),
	opened_at TIMESTAMPTZ(6),
	clicked_at TIMESTAMPTZ(6),
	bounced_at TIMESTAMPTZ(6),
	bounce_type VARCHAR(20),
	bounce_reason TEXT,
	idempotency_key VARCHAR(255),
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW(),
	provider_message_id VARCHAR(255),
	email_provider VARCHAR(20) DEFAULT 'ses'
);
CREATE INDEX IF NOT EXISTS idx_trans_emails_org ON transactional_emails(org_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_trans_emails_status ON transactional_emails(status);
CREATE INDEX IF NOT EXISTS idx_trans_emails_idempotency ON transactional_emails(idempotency_key);
CREATE INDEX IF NOT EXISTS idx_trans_emails_provider ON transactional_emails(provider_message_id);

-- Transactional Templates
CREATE TABLE IF NOT EXISTS email_templates (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	org_id INT NOT NULL,
	name VARCHAR(255) NOT NULL,
	description TEXT,
	subject VARCHAR(500) NOT NULL,
	html_body TEXT NOT NULL,
	text_body TEXT,
	variables TEXT,
	is_active BOOLEAN DEFAULT true,
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_email_templates_org ON email_templates(org_id);

-- Transactional Delivery Events
CREATE TABLE IF NOT EXISTS transactional_delivery_events (
	id BIGSERIAL PRIMARY KEY,
	email_id BIGINT NOT NULL REFERENCES transactional_emails(id) ON DELETE CASCADE,
	event_type VARCHAR(50) NOT NULL,
	details TEXT,
	ip_address VARCHAR(45),
	user_agent TEXT,
	created_at TIMESTAMPTZ(6) DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_trans_delivery_email ON transactional_delivery_events(email_id, created_at DESC);

-- Emails (marketing/campaign)
CREATE TABLE IF NOT EXISTS emails (
	id BIGSERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	org_id INT NOT NULL,
	message_id VARCHAR(255) UNIQUE NOT NULL,
	identity_id INT NOT NULL,
	from_email VARCHAR(255) NOT NULL,
	from_name VARCHAR(255),
	to_emails TEXT[] NOT NULL,
	cc_emails TEXT[] DEFAULT '{}',
	bcc_emails TEXT[] DEFAULT '{}',
	reply_to VARCHAR(255),
	subject VARCHAR(500) NOT NULL,
	html_content TEXT,
	text_content TEXT,
	source VARCHAR(50) DEFAULT 'api',
	domain_id INT NOT NULL,
	campaign_id INT,
	template_id INT,
	contact_id BIGINT,
	tags TEXT[] DEFAULT '{}',
	metadata JSONB DEFAULT '{}',
	headers JSONB DEFAULT '{}',
	status VARCHAR(50) DEFAULT 'queued',
	scheduled_at TIMESTAMPTZ(6),
	sent_at TIMESTAMPTZ(6),
	delivered_at TIMESTAMPTZ(6),
	open_count INT DEFAULT 0,
	click_count INT DEFAULT 0,
	idempotency_key VARCHAR(255) UNIQUE,
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW(),
	provider_message_id VARCHAR(255),
	email_provider VARCHAR(20) DEFAULT 'ses'
);
CREATE INDEX IF NOT EXISTS idx_emails_org ON emails(org_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_emails_status ON emails(status);
CREATE INDEX IF NOT EXISTS idx_emails_message ON emails(message_id);
CREATE INDEX IF NOT EXISTS idx_emails_provider ON emails(provider_message_id);

-- Delivery Events
CREATE TABLE IF NOT EXISTS delivery_events (
	id BIGSERIAL PRIMARY KEY,
	email_id BIGINT NOT NULL REFERENCES emails(id) ON DELETE CASCADE,
	event_type VARCHAR(50) NOT NULL,
	stalwart_message_id VARCHAR(255),
	data JSONB DEFAULT '{}',
	occurred_at TIMESTAMPTZ(6) DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_delivery_events_email ON delivery_events(email_id, occurred_at DESC);

-- Webhooks
CREATE TABLE IF NOT EXISTS webhooks (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	org_id INT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
	name VARCHAR(255) NOT NULL,
	url VARCHAR(2048) NOT NULL,
	secret VARCHAR(255) NOT NULL,
	events TEXT[] DEFAULT '{}',
	active BOOLEAN DEFAULT true,
	success_count INT DEFAULT 0,
	failure_count INT DEFAULT 0,
	last_triggered_at TIMESTAMPTZ(6),
	last_success_at TIMESTAMPTZ(6),
	last_failure_at TIMESTAMPTZ(6),
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);

-- Webhook Calls
CREATE TABLE IF NOT EXISTS webhook_calls (
	id BIGSERIAL PRIMARY KEY,
	webhook_id INT NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
	event_type VARCHAR(50) NOT NULL,
	payload JSONB NOT NULL,
	response_status INT,
	response_body TEXT,
	response_time_ms INT,
	status VARCHAR(50) DEFAULT 'pending',
	attempts INT DEFAULT 0,
	error TEXT,
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	completed_at TIMESTAMPTZ(6)
);
CREATE INDEX IF NOT EXISTS idx_webhook_calls ON webhook_calls(webhook_id, created_at DESC);

-- Suppressions
CREATE TABLE IF NOT EXISTS suppressions (
	id BIGSERIAL PRIMARY KEY,
	org_id INT NOT NULL,
	email VARCHAR(255) NOT NULL,
	reason VARCHAR(50) NOT NULL,
	source_type VARCHAR(50) NOT NULL,
	source_id VARCHAR(255),
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	UNIQUE(org_id, email)
);
CREATE INDEX IF NOT EXISTS idx_suppressions ON suppressions(org_id, email);

-- Suppression List
CREATE TABLE IF NOT EXISTS suppression_list (
	id BIGSERIAL PRIMARY KEY,
	org_id INT NOT NULL,
	email VARCHAR(255) NOT NULL,
	reason TEXT,
	source VARCHAR(50) DEFAULT 'manual',
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	UNIQUE(org_id, email)
);
CREATE INDEX IF NOT EXISTS idx_suppression_list ON suppression_list(org_id, email);

-- Audit Logs
CREATE TABLE IF NOT EXISTS audit_logs (
	id BIGSERIAL PRIMARY KEY,
	org_id INT NOT NULL,
	user_id INT,
	action VARCHAR(100) NOT NULL,
	resource VARCHAR(100) NOT NULL,
	resource_id VARCHAR(255),
	description TEXT,
	ip_address VARCHAR(45),
	user_agent TEXT,
	request_id VARCHAR(100),
	old_values JSONB,
	new_values JSONB,
	status VARCHAR(20) DEFAULT 'success',
	created_at TIMESTAMPTZ(6) DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_audit_org ON audit_logs(org_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_user ON audit_logs(user_id, created_at DESC);

-- User Sessions
CREATE TABLE IF NOT EXISTS user_sessions (
	id BIGSERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	org_id INT NOT NULL,
	token_hash VARCHAR(64) UNIQUE NOT NULL,
	device_name VARCHAR(255),
	device_type VARCHAR(50),
	browser VARCHAR(100),
	os VARCHAR(100),
	ip_address VARCHAR(45),
	location VARCHAR(255),
	active BOOLEAN DEFAULT true,
	last_seen_at TIMESTAMPTZ(6) DEFAULT NOW(),
	expires_at TIMESTAMPTZ(6) NOT NULL,
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	revoked_at TIMESTAMPTZ(6)
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON user_sessions(user_id, active);
CREATE INDEX IF NOT EXISTS idx_sessions_token ON user_sessions(token_hash);

-- User Settings
CREATE TABLE IF NOT EXISTS user_settings (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	user_id INT UNIQUE NOT NULL,
	org_id INT NOT NULL,
	display_name VARCHAR(255),
	show_snippets BOOLEAN DEFAULT true,
	conversation_view BOOLEAN DEFAULT true,
	auto_advance BOOLEAN DEFAULT false,
	new_email_notifications BOOLEAN DEFAULT true,
	campaign_reports BOOLEAN DEFAULT true,
	weekly_digest BOOLEAN DEFAULT false,
	blacklist_alerts BOOLEAN DEFAULT true,
	bounce_rate_warnings BOOLEAN DEFAULT true,
	quota_warnings BOOLEAN DEFAULT true,
	browser_notifications BOOLEAN DEFAULT false,
	theme VARCHAR(20) DEFAULT 'light',
	density VARCHAR(20) DEFAULT 'comfortable',
	inbox_layout VARCHAR(20) DEFAULT 'default',
	two_factor_enabled BOOLEAN DEFAULT false,
	two_factor_method VARCHAR(20),
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_user_settings_user ON user_settings(user_id);
CREATE INDEX IF NOT EXISTS idx_user_settings_org ON user_settings(org_id);

-- Message Metadata
CREATE TABLE IF NOT EXISTS message_metadata (
	id BIGSERIAL PRIMARY KEY,
	stalwart_message_id VARCHAR(255) UNIQUE NOT NULL,
	stalwart_account_id VARCHAR(255) NOT NULL,
	identity_id INT,
	labels TEXT[] DEFAULT '{}',
	tags TEXT[] DEFAULT '{}',
	source VARCHAR(50),
	campaign_id INT,
	contact_id BIGINT,
	thread_group_id BIGINT,
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_msg_meta_identity ON message_metadata(identity_id);
CREATE INDEX IF NOT EXISTS idx_msg_meta_campaign ON message_metadata(campaign_id);

-- Thread Groups
CREATE TABLE IF NOT EXISTS thread_groups (
	id BIGSERIAL PRIMARY KEY,
	user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	subject_hash VARCHAR(64),
	participants_hash VARCHAR(64),
	message_count INT DEFAULT 1,
	last_message_at TIMESTAMPTZ(6) DEFAULT NOW(),
	created_at TIMESTAMPTZ(6) DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_thread_groups ON thread_groups(user_id, last_message_at DESC);

-- Received Emails
CREATE TABLE IF NOT EXISTS received_emails (
	id BIGSERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	org_id INT NOT NULL,
	domain_id INT NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
	identity_id INT NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
	message_id VARCHAR(500) UNIQUE NOT NULL,
	in_reply_to VARCHAR(500),
	"references" TEXT[] DEFAULT '{}',
	thread_id VARCHAR(100),
	from_email VARCHAR(255) NOT NULL,
	from_name VARCHAR(255),
	to_emails TEXT[] NOT NULL,
	cc_emails TEXT[] DEFAULT '{}',
	bcc_emails TEXT[] DEFAULT '{}',
	reply_to VARCHAR(255),
	subject VARCHAR(1000) NOT NULL,
	text_body TEXT,
	html_body TEXT,
	snippet VARCHAR(500),
	raw_s3_key VARCHAR(500),
	raw_s3_bucket VARCHAR(255),
	size_bytes INT DEFAULT 0,
	has_attachments BOOLEAN DEFAULT false,
	folder VARCHAR(50) DEFAULT 'inbox',
	is_read BOOLEAN DEFAULT false,
	is_starred BOOLEAN DEFAULT false,
	is_archived BOOLEAN DEFAULT false,
	is_trashed BOOLEAN DEFAULT false,
	is_spam BOOLEAN DEFAULT false,
	labels TEXT[] DEFAULT '{}',
	spam_score DOUBLE PRECISION,
	spam_verdict VARCHAR(20),
	virus_verdict VARCHAR(20),
	spf_verdict VARCHAR(20),
	dkim_verdict VARCHAR(20),
	dmarc_verdict VARCHAR(20),
	ses_message_id VARCHAR(255),
	sns_notification_id VARCHAR(255),
	received_at TIMESTAMPTZ(6) DEFAULT NOW(),
	read_at TIMESTAMPTZ(6),
	trashed_at TIMESTAMPTZ(6),
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_recv_emails_folder ON received_emails(org_id, identity_id, folder, received_at DESC);
CREATE INDEX IF NOT EXISTS idx_recv_emails_read ON received_emails(org_id, identity_id, is_read);
CREATE INDEX IF NOT EXISTS idx_recv_emails_thread ON received_emails(thread_id);

-- Email Attachments
CREATE TABLE IF NOT EXISTS email_attachments (
	id BIGSERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	received_email_id BIGINT NOT NULL REFERENCES received_emails(id) ON DELETE CASCADE,
	filename VARCHAR(255) NOT NULL,
	content_type VARCHAR(255) NOT NULL,
	size_bytes INT NOT NULL,
	s3_key VARCHAR(500) NOT NULL,
	s3_bucket VARCHAR(255) NOT NULL,
	content_id VARCHAR(255),
	is_inline BOOLEAN DEFAULT false,
	checksum VARCHAR(64),
	created_at TIMESTAMPTZ(6) DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_email_attachments ON email_attachments(received_email_id);

-- Email Labels
CREATE TABLE IF NOT EXISTS email_labels (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	org_id INT NOT NULL,
	user_id INT NOT NULL,
	name VARCHAR(100) NOT NULL,
	color VARCHAR(7) DEFAULT '#6366f1',
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW(),
	UNIQUE(user_id, name)
);
CREATE INDEX IF NOT EXISTS idx_email_labels ON email_labels(org_id, user_id);

-- Email Rules
CREATE TABLE IF NOT EXISTS email_rules (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	user_id INT NOT NULL,
	org_id INT NOT NULL,
	name VARCHAR(255) NOT NULL,
	description TEXT,
	priority INT DEFAULT 0,
	conditions JSONB NOT NULL,
	condition_logic VARCHAR(10) DEFAULT 'all',
	actions JSONB NOT NULL,
	identity_ids INT[] DEFAULT '{}',
	active BOOLEAN DEFAULT true,
	match_count INT DEFAULT 0,
	last_matched_at TIMESTAMPTZ(6),
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_email_rules ON email_rules(user_id, active, priority);

-- Automations
CREATE TABLE IF NOT EXISTS automations (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	org_id INT NOT NULL,
	name VARCHAR(255) NOT NULL,
	description TEXT,
	trigger_type VARCHAR(50) NOT NULL,
	trigger_config JSONB DEFAULT '{}',
	workflow JSONB DEFAULT '{"edges": [], "nodes": []}',
	status VARCHAR(50) DEFAULT 'draft',
	enrolled_count INT DEFAULT 0,
	completed_count INT DEFAULT 0,
	in_progress_count INT DEFAULT 0,
	error_count INT DEFAULT 0,
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_automations ON automations(org_id, status);

-- Automation Enrollments
CREATE TABLE IF NOT EXISTS automation_enrollments (
	id BIGSERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	automation_id INT NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
	contact_id BIGINT NOT NULL,
	org_id INT NOT NULL,
	status VARCHAR(50) DEFAULT 'active',
	step_index INT DEFAULT 0,
	step_data JSONB DEFAULT '{}',
	next_run_at TIMESTAMPTZ(6),
	completed_at TIMESTAMPTZ(6),
	error_message TEXT,
	retry_count INT DEFAULT 0,
	enrolled_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW(),
	UNIQUE(automation_id, contact_id)
);
CREATE INDEX IF NOT EXISTS idx_auto_enroll_status ON automation_enrollments(automation_id, status);
CREATE INDEX IF NOT EXISTS idx_auto_enroll_next ON automation_enrollments(next_run_at);

-- Automation Logs
CREATE TABLE IF NOT EXISTS automation_logs (
	id BIGSERIAL PRIMARY KEY,
	enrollment_id BIGINT NOT NULL,
	automation_id INT NOT NULL,
	step_index INT NOT NULL,
	step_type VARCHAR(50) NOT NULL,
	status VARCHAR(50) DEFAULT 'success',
	message TEXT,
	data JSONB,
	created_at TIMESTAMPTZ(6) DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_auto_logs_enroll ON automation_logs(enrollment_id, created_at DESC);

-- Warmup Progress
CREATE TABLE IF NOT EXISTS warmup_progress (
	id SERIAL PRIMARY KEY,
	org_id INT NOT NULL,
	ip_address VARCHAR(45) NOT NULL,
	schedule_name VARCHAR(50) DEFAULT 'conservative',
	current_day INT DEFAULT 1,
	status VARCHAR(20) DEFAULT 'active',
	pause_reason TEXT,
	started_at TIMESTAMPTZ(6) DEFAULT NOW(),
	completed_at TIMESTAMPTZ(6),
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW(),
	UNIQUE(org_id, ip_address)
);

-- Alerts
CREATE TABLE IF NOT EXISTS alerts (
	id BIGSERIAL PRIMARY KEY,
	org_id INT NOT NULL,
	type VARCHAR(50) NOT NULL,
	severity VARCHAR(20) NOT NULL,
	title VARCHAR(255) NOT NULL,
	message TEXT NOT NULL,
	data JSONB,
	acknowledged BOOLEAN DEFAULT false,
	created_at TIMESTAMPTZ(6) DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_alerts ON alerts(org_id, acknowledged, created_at DESC);

-- Blacklist Checks
CREATE TABLE IF NOT EXISTS blacklist_checks (
	id BIGSERIAL PRIMARY KEY,
	ip_address VARCHAR(45) NOT NULL,
	listed_count INT DEFAULT 0,
	results JSONB NOT NULL,
	checked_at TIMESTAMPTZ(6) DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_blacklist ON blacklist_checks(ip_address, checked_at DESC);

-- Receiving Config
CREATE TABLE IF NOT EXISTS receiving_configs (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	org_id INT UNIQUE NOT NULL,
	s3_bucket VARCHAR(255) NOT NULL,
	s3_region VARCHAR(50) NOT NULL,
	sns_topic_arn VARCHAR(255) NOT NULL,
	ses_rule_set_name VARCHAR(255) NOT NULL,
	webhook_secret VARCHAR(255) NOT NULL,
	status VARCHAR(50) DEFAULT 'pending',
	last_health_check TIMESTAMPTZ(6),
	setup_completed_at TIMESTAMPTZ(6),
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);

-- Tenant Branding
CREATE TABLE IF NOT EXISTS tenant_brandings (
	id SERIAL PRIMARY KEY,
	org_id INT UNIQUE NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
	logo_url TEXT,
	logo_light_url TEXT,
	favicon_url TEXT,
	primary_color VARCHAR(7),
	accent_color VARCHAR(7),
	custom_domain VARCHAR(255) UNIQUE,
	custom_domain_verified BOOLEAN DEFAULT false,
	email_footer_html TEXT,
	email_header_html TEXT,
	hide_powered_by BOOLEAN DEFAULT false,
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);

-- OAuth Connections
CREATE TABLE IF NOT EXISTS oauth_connections (
	id SERIAL PRIMARY KEY,
	user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	provider VARCHAR(50) NOT NULL,
	provider_user_id VARCHAR(255) NOT NULL,
	access_token TEXT,
	refresh_token TEXT,
	token_expiry TIMESTAMPTZ(6),
	email VARCHAR(255),
	name VARCHAR(255),
	avatar_url TEXT,
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW(),
	UNIQUE(provider, provider_user_id),
	UNIQUE(user_id, provider)
);

-- Consent Audit
CREATE TABLE IF NOT EXISTS consent_audit (
	id BIGSERIAL PRIMARY KEY,
	contact_id BIGINT NOT NULL,
	org_id INT NOT NULL,
	action VARCHAR(50) NOT NULL,
	source VARCHAR(50) NOT NULL,
	list_id INT,
	ip_address VARCHAR(45),
	user_agent TEXT,
	details TEXT,
	created_at TIMESTAMPTZ(6) DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_consent_contact ON consent_audit(contact_id, created_at DESC);

-- Inbox Filters
CREATE TABLE IF NOT EXISTS inbox_filters (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	org_id INT NOT NULL,
	user_id INT NOT NULL,
	identity_id INT,
	name VARCHAR(255) NOT NULL,
	priority INT DEFAULT 0,
	active BOOLEAN DEFAULT true,
	conditions JSONB DEFAULT '[]',
	condition_logic VARCHAR(10) DEFAULT 'all',
	action_labels TEXT[] DEFAULT '{}',
	action_folder VARCHAR(50),
	action_star BOOLEAN DEFAULT false,
	action_mark_read BOOLEAN DEFAULT false,
	action_archive BOOLEAN DEFAULT false,
	action_trash BOOLEAN DEFAULT false,
	action_forward VARCHAR(255),
	match_count INT DEFAULT 0,
	last_matched_at TIMESTAMPTZ(6),
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_inbox_filters ON inbox_filters(user_id, active, priority);

-- Auto Replies
CREATE TABLE IF NOT EXISTS auto_replies (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	user_id INT NOT NULL,
	org_id INT NOT NULL,
	name VARCHAR(255) NOT NULL,
	start_date TIMESTAMPTZ(6) NOT NULL,
	end_date TIMESTAMPTZ(6),
	subject VARCHAR(500) NOT NULL,
	html_content TEXT NOT NULL,
	text_content TEXT,
	reply_once BOOLEAN DEFAULT true,
	reply_to_all BOOLEAN DEFAULT true,
	exclude_patterns TEXT[] DEFAULT '{}',
	identity_ids INT[] DEFAULT '{}',
	active BOOLEAN DEFAULT true,
	reply_count INT DEFAULT 0,
	last_replied_at TIMESTAMPTZ(6),
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);

-- Auto Reply Senders
CREATE TABLE IF NOT EXISTS auto_reply_senders (
	id BIGSERIAL PRIMARY KEY,
	auto_reply_id INT NOT NULL REFERENCES auto_replies(id) ON DELETE CASCADE,
	sender_email VARCHAR(255) NOT NULL,
	replied_at TIMESTAMPTZ(6) DEFAULT NOW(),
	UNIQUE(auto_reply_id, sender_email)
);

-- Email Forwards
CREATE TABLE IF NOT EXISTS email_forwards (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	user_id INT NOT NULL,
	org_id INT NOT NULL,
	identity_id INT NOT NULL,
	forward_to VARCHAR(255) NOT NULL,
	keep_copy BOOLEAN DEFAULT true,
	active BOOLEAN DEFAULT true,
	verified BOOLEAN DEFAULT false,
	verify_token VARCHAR(100),
	verified_at TIMESTAMPTZ(6),
	forward_count INT DEFAULT 0,
	last_forwarded_at TIMESTAMPTZ(6),
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW(),
	UNIQUE(identity_id, forward_to)
);

-- Shared Mailboxes
CREATE TABLE IF NOT EXISTS shared_mailboxes (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	org_id INT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
	name VARCHAR(255) NOT NULL,
	email VARCHAR(255) UNIQUE NOT NULL,
	description TEXT,
	auto_reply_enabled BOOLEAN DEFAULT false,
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);

-- Shared Mailbox Members
CREATE TABLE IF NOT EXISTS shared_mailbox_members (
	id SERIAL PRIMARY KEY,
	shared_mailbox_id INT NOT NULL REFERENCES shared_mailboxes(id) ON DELETE CASCADE,
	user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	can_read BOOLEAN DEFAULT true,
	can_send BOOLEAN DEFAULT false,
	can_manage BOOLEAN DEFAULT false,
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	UNIQUE(shared_mailbox_id, user_id)
);

-- Sieve Scripts
CREATE TABLE IF NOT EXISTS sieve_scripts (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	org_id INT NOT NULL,
	name VARCHAR(255) NOT NULL,
	script TEXT NOT NULL,
	active BOOLEAN DEFAULT false,
	is_default BOOLEAN DEFAULT false,
	is_valid BOOLEAN DEFAULT true,
	last_error TEXT,
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);

-- Webhook Triggers
CREATE TABLE IF NOT EXISTS webhook_triggers (
	id SERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	org_id INT NOT NULL,
	user_id INT NOT NULL,
	name VARCHAR(255) NOT NULL,
	description TEXT,
	trigger_type VARCHAR(50) NOT NULL,
	filters JSONB,
	webhook_url TEXT NOT NULL,
	secret VARCHAR(255),
	active BOOLEAN DEFAULT true,
	last_triggered_at TIMESTAMPTZ(6),
	trigger_count INT DEFAULT 0,
	created_at TIMESTAMPTZ(6) DEFAULT NOW(),
	updated_at TIMESTAMPTZ(6) DEFAULT NOW()
);

-- Push Subscriptions
CREATE TABLE IF NOT EXISTS push_subscriptions (
	id BIGSERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	endpoint TEXT UNIQUE NOT NULL,
	p256dh_key TEXT NOT NULL,
	auth_key VARCHAR(255) NOT NULL,
	user_agent TEXT,
	device_name VARCHAR(255),
	notify_new_email BOOLEAN DEFAULT true,
	notify_campaign BOOLEAN DEFAULT false,
	notify_mentions BOOLEAN DEFAULT true,
	active BOOLEAN DEFAULT true,
	last_used_at TIMESTAMPTZ(6),
	created_at TIMESTAMPTZ(6) DEFAULT NOW()
);

-- WebAuthn Credentials
CREATE TABLE IF NOT EXISTS webauthn_credentials (
	id BIGSERIAL PRIMARY KEY,
	uuid UUID UNIQUE DEFAULT gen_random_uuid(),
	user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	credential_id BYTEA UNIQUE NOT NULL,
	public_key BYTEA NOT NULL,
	sign_count INT DEFAULT 0,
	transports TEXT[] DEFAULT '{}',
	aaguid VARCHAR(36),
	attestation_type VARCHAR(50),
	name VARCHAR(255) NOT NULL,
	device_type VARCHAR(50),
	last_used_at TIMESTAMPTZ(6),
	created_at TIMESTAMPTZ(6) DEFAULT NOW()
);
`
