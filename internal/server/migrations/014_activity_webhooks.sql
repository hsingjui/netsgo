-- Name: 014_activity_webhooks
-- Description: Persist user-owned activity Webhooks and durable delivery attempts.
-- CreatedAt: 2026-08-25T00:00:00Z

-- Up:
CREATE TABLE activity_webhooks (
	owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	id TEXT NOT NULL,
	revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
	name TEXT NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
	target_kind TEXT NOT NULL CHECK (target_kind IN ('client', 'tunnel')),
	target_mode TEXT NOT NULL CHECK (target_mode IN ('all', 'selected')),
	method TEXT NOT NULL CHECK (method IN ('GET', 'POST')),
	url_template TEXT NOT NULL,
	headers_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(headers_json)),
	body_template TEXT NOT NULL DEFAULT '',
	last_status TEXT NOT NULL DEFAULT 'idle' CHECK (last_status IN ('idle', 'success', 'failed', 'retrying')),
	consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
	last_called_at_ns INTEGER,
	created_at_ns INTEGER NOT NULL,
	updated_at_ns INTEGER NOT NULL,
	PRIMARY KEY (owner_user_id, id)
);
CREATE INDEX idx_activity_webhooks_owner_updated
	ON activity_webhooks(owner_user_id, updated_at_ns DESC, id DESC);

CREATE TABLE activity_webhook_events (
	owner_user_id TEXT NOT NULL,
	webhook_id TEXT NOT NULL,
	event_type TEXT NOT NULL,
	PRIMARY KEY (owner_user_id, webhook_id, event_type),
	FOREIGN KEY (owner_user_id, webhook_id)
		REFERENCES activity_webhooks(owner_user_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_activity_webhook_events_match
	ON activity_webhook_events(owner_user_id, event_type, webhook_id);

CREATE TABLE activity_webhook_targets (
	owner_user_id TEXT NOT NULL,
	webhook_id TEXT NOT NULL,
	target_id TEXT NOT NULL,
	PRIMARY KEY (owner_user_id, webhook_id, target_id),
	FOREIGN KEY (owner_user_id, webhook_id)
		REFERENCES activity_webhooks(owner_user_id, id) ON DELETE CASCADE
);

CREATE TABLE activity_webhook_deliveries (
	id TEXT PRIMARY KEY,
	owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	webhook_id TEXT NOT NULL,
	webhook_name TEXT NOT NULL,
	origin TEXT NOT NULL CHECK (origin IN ('event', 'test', 'replay')),
	source_event_id INTEGER REFERENCES activity_events(id) ON DELETE SET NULL,
	event_type TEXT NOT NULL,
	event_occurred_at_ns INTEGER NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('queued', 'retrying', 'success', 'failed', 'canceled')),
	attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
	max_attempts INTEGER NOT NULL CHECK (max_attempts BETWEEN 1 AND 3),
	next_attempt_at_ns INTEGER NOT NULL,
	lease_until_ns INTEGER,
	config_revision INTEGER NOT NULL CHECK (config_revision >= 0),
	config_snapshot_json TEXT NOT NULL CHECK (json_valid(config_snapshot_json)),
	event_snapshot_json TEXT NOT NULL CHECK (json_valid(event_snapshot_json)),
	values_snapshot_json TEXT NOT NULL CHECK (json_valid(values_snapshot_json)),
	request_method TEXT NOT NULL CHECK (request_method IN ('GET', 'POST')),
	request_url TEXT NOT NULL,
	request_headers_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(request_headers_json)),
	request_body TEXT,
	response_status INTEGER,
	response_headers_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(response_headers_json)),
	response_body TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	duration_ms INTEGER,
	created_at_ns INTEGER NOT NULL,
	started_at_ns INTEGER,
	completed_at_ns INTEGER,
	updated_at_ns INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_activity_webhook_deliveries_event_dedupe
	ON activity_webhook_deliveries(owner_user_id, webhook_id, source_event_id)
	WHERE origin = 'event' AND source_event_id IS NOT NULL;
CREATE INDEX idx_activity_webhook_deliveries_due
	ON activity_webhook_deliveries(status, next_attempt_at_ns, created_at_ns, id);
CREATE INDEX idx_activity_webhook_deliveries_owner_webhook_page
	ON activity_webhook_deliveries(owner_user_id, webhook_id, created_at_ns DESC, id DESC);
CREATE INDEX idx_activity_webhook_deliveries_terminal_cleanup
	ON activity_webhook_deliveries(owner_user_id, webhook_id, completed_at_ns DESC, id DESC)
	WHERE status IN ('success', 'failed', 'canceled');

CREATE TABLE activity_webhook_delivery_attempts (
	delivery_id TEXT NOT NULL REFERENCES activity_webhook_deliveries(id) ON DELETE CASCADE,
	attempt_number INTEGER NOT NULL CHECK (attempt_number >= 1),
	status TEXT NOT NULL CHECK (status IN ('pending', 'success', 'failed')),
	started_at_ns INTEGER NOT NULL,
	completed_at_ns INTEGER,
	duration_ms INTEGER,
	response_status INTEGER,
	response_headers_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(response_headers_json)),
	response_body TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (delivery_id, attempt_number)
);

CREATE TABLE activity_webhook_dispatch_slots (
	owner_user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
	next_allowed_at_ns INTEGER NOT NULL DEFAULT 0
);

-- Down:
DROP TABLE activity_webhook_dispatch_slots;
DROP TABLE activity_webhook_delivery_attempts;
DROP TABLE activity_webhook_deliveries;
DROP TABLE activity_webhook_targets;
DROP TABLE activity_webhook_events;
DROP TABLE activity_webhooks;
