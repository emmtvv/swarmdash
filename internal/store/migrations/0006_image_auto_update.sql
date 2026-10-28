-- Cluster-wide default for automatic image updates ("", "off", "patch", "minor" or
-- "major" - see internal/imageupdate); a service's swarmdash.autoupdate
-- label overrides it.
ALTER TABLE app_settings ADD COLUMN image_auto_update TEXT NOT NULL DEFAULT '';
