-- Webhook signing: an optional sealed secret per notification channel. When
-- set (webhook kind only), each delivery carries an HMAC-SHA256 of its body
-- in the X-Spacefleet-Signature-256 header for the receiver to verify.

ALTER TABLE notification_channels ADD COLUMN encrypted_secret BYTEA;
