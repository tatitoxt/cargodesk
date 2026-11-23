-- Public tracking links: each shipment gets an unguessable token used by the
-- unauthenticated /api/v1/track/{token} endpoint. Rotating the token
-- invalidates previously shared links.
ALTER TABLE shipments ADD COLUMN track_token text;
UPDATE shipments SET track_token = replace(gen_random_uuid()::text || gen_random_uuid()::text, '-', '');
ALTER TABLE shipments ALTER COLUMN track_token SET NOT NULL;
CREATE UNIQUE INDEX shipments_track_token_idx ON shipments (track_token);
