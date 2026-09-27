-- Name: 015_webhook_delivery_policy
-- Description: Persist server-wide outbound webhook delivery policy.
-- CreatedAt: 2026-08-28T00:00:00Z

-- Up:
ALTER TABLE server_config ADD COLUMN webhook_allow_private_targets INTEGER NOT NULL DEFAULT 0 CHECK (webhook_allow_private_targets IN (0, 1));
ALTER TABLE server_config ADD COLUMN webhook_daily_delivery_cap INTEGER NOT NULL DEFAULT 50 CHECK (webhook_daily_delivery_cap BETWEEN 1 AND 10000);

-- Down:
ALTER TABLE server_config DROP COLUMN webhook_daily_delivery_cap;
ALTER TABLE server_config DROP COLUMN webhook_allow_private_targets;
