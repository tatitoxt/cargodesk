ALTER TABLE users ADD COLUMN password_hash bytea;
ALTER TABLE users ADD COLUMN must_change_password boolean NOT NULL DEFAULT false;
