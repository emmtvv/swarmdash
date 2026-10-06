-- stack_versions: every compose file deployed to a stack (from the deploy
-- form, a GitOps sync or a rollback), so a stack can be edited from what
-- was actually deployed and rolled back to an earlier file. vars_enc holds
-- the ${VAR} values the file was deployed with, encrypted by the admin
-- package (same convention as registry_credentials.password_enc).
CREATE TABLE stack_versions (
    id          TEXT PRIMARY KEY,
    stack_name  TEXT NOT NULL,
    version     INTEGER NOT NULL,
    compose     TEXT NOT NULL,
    vars_enc    BLOB,
    source      TEXT NOT NULL DEFAULT '',
    note        TEXT NOT NULL DEFAULT '',
    created_by  TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL
);
CREATE INDEX idx_stack_versions_stack ON stack_versions (stack_name, version);

-- stack_templates: user-saved compose templates, shown in the template
-- gallery alongside the built-in ones.
CREATE TABLE stack_templates (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    compose      TEXT NOT NULL,
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   DATETIME NOT NULL
);
