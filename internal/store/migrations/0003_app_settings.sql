-- Singleton row for instance-wide settings not tied to any one feature
-- area, same convention as sso_config (always keyed by store.AppSettingsID
-- = "app"). First user: the update-check banner's on/off toggle.
CREATE TABLE app_settings (
    id                     TEXT PRIMARY KEY,
    update_check_disabled  INTEGER NOT NULL DEFAULT 0
);
