-- Replace the two-role membership model (owner/member) with the three-role
-- model (admin/editor/viewer). Existing data is migrated in place: an owner
-- becomes an admin (full org management), and a plain member becomes an editor
-- (could already take any action in the app, so editor preserves that). The new
-- viewer role is read-only and is now the column default (least privilege); the
-- org creator is promoted to admin and invited users get their invitation's
-- role explicitly, so the default is rarely hit. role is a plain TEXT column,
-- so there is no Postgres enum type to alter.

UPDATE memberships SET role = 'admin'  WHERE role = 'owner';
UPDATE memberships SET role = 'editor' WHERE role = 'member';

ALTER TABLE memberships ALTER COLUMN role SET DEFAULT 'viewer';
