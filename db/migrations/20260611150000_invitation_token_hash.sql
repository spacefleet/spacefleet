-- Invitation tokens are now stored hashed (SHA-256, hex) instead of in
-- plaintext, so reading the database (or a backup) no longer yields live
-- invite links. The application hashes the presented token on lookup.
-- Existing plaintext tokens are hashed in place, which keeps already-issued
-- invite links working.

UPDATE invitations SET token = encode(sha256(convert_to(token, 'UTF8')), 'hex');

ALTER TABLE invitations RENAME COLUMN token TO token_hash;
