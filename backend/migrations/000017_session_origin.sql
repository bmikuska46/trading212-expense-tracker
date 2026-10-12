-- Each session remembers where it was signed in from, so users can see their
-- sign-ins in the settings. The location comes from Cloudflare's visitor
-- headers and stays empty when the request did not pass through Cloudflare.
-- Sessions are deleted when they expire, and these details with them.
ALTER TABLE sessions ADD COLUMN ip TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN country TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN region TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN city TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN user_agent TEXT NOT NULL DEFAULT '';
