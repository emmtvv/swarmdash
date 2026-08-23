package store

import (
	"database/sql"
	"errors"
	"testing"
	"testing/fstest"
	"time"
)

func openTestSQLite(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := OpenSQLite(t.TempDir())
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSQLiteStoreUserCRUD(t *testing.T) {
	s := openTestSQLite(t)

	if has, err := s.HasAnyUser(); err != nil || has {
		t.Fatalf("HasAnyUser on empty store = %v, %v; want false, nil", has, err)
	}

	u := User{Username: "alice", PasswordHash: "hash", Role: "admin", CreatedAt: time.Now()}
	if err := s.PutUser(u); err != nil {
		t.Fatalf("PutUser: %v", err)
	}

	got, err := s.GetUser("alice")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Username != u.Username || got.Role != u.Role {
		t.Fatalf("GetUser = %+v, want %+v", got, u)
	}

	if has, err := s.HasAnyUser(); err != nil || !has {
		t.Fatalf("HasAnyUser = %v, %v; want true, nil", has, err)
	}

	if _, err := s.GetUser("bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetUser(missing) err = %v, want ErrNotFound", err)
	}

	// PutUser on an existing username upserts rather than erroring.
	u.Role = "viewer"
	if err := s.PutUser(u); err != nil {
		t.Fatalf("PutUser (update): %v", err)
	}
	got, err = s.GetUser("alice")
	if err != nil || got.Role != "viewer" {
		t.Fatalf("GetUser after upsert = %+v, %v; want role viewer", got, err)
	}

	if err := s.PutUser(User{Username: "bob"}); err != nil {
		t.Fatalf("PutUser: %v", err)
	}
	users, err := s.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 2 || users[0].Username != "alice" || users[1].Username != "bob" {
		t.Fatalf("ListUsers = %+v, want [alice, bob] sorted", users)
	}

	if err := s.DeleteUser("alice"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := s.GetUser("alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetUser(deleted) err = %v, want ErrNotFound", err)
	}
}

func TestSQLiteStoreSessionExpiry(t *testing.T) {
	s := openTestSQLite(t)

	live := Session{Token: "live", Username: "alice", ExpiresAt: time.Now().Add(time.Hour)}
	expired := Session{Token: "expired", Username: "alice", ExpiresAt: time.Now().Add(-time.Hour)}
	if err := s.PutSession(live); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	if err := s.PutSession(expired); err != nil {
		t.Fatalf("PutSession: %v", err)
	}

	if _, err := s.GetSession("live"); err != nil {
		t.Fatalf("GetSession(live): %v", err)
	}
	if _, err := s.GetSession("expired"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSession(expired) err = %v, want ErrNotFound", err)
	}

	// pruneExpired (normally run on a timer) should physically delete the
	// expired row, not just filter it out on read.
	s.pruneExpired()
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token = 'expired'`).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Fatalf("expired session still present after pruneExpired")
	}
}

func TestSQLiteStoreAuditPagination(t *testing.T) {
	s := openTestSQLite(t)

	for i := 0; i < 5; i++ {
		if err := s.AppendAudit(AuditEntry{Username: "alice", Action: "test"}); err != nil {
			t.Fatalf("AppendAudit: %v", err)
		}
	}

	count, err := s.CountAudit()
	if err != nil || count != 5 {
		t.Fatalf("CountAudit = %d, %v; want 5, nil", count, err)
	}

	page, err := s.ListAudit(0, 2)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(page) != 2 || page[0].ID != 5 || page[1].ID != 4 {
		t.Fatalf("ListAudit(0,2) = %+v, want IDs [5 4]", page)
	}

	page2, err := s.ListAudit(2, 2)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(page2) != 2 || page2[0].ID != 3 || page2[1].ID != 2 {
		t.Fatalf("ListAudit(2,2) = %+v, want IDs [3 2]", page2)
	}
}

func TestSQLiteStorePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()

	s, err := OpenSQLite(dir)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	if err := s.PutUser(User{Username: "alice", Role: "admin"}); err != nil {
		t.Fatalf("PutUser: %v", err)
	}
	if err := s.AppendAudit(AuditEntry{Username: "alice", Action: "login"}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	if err := s.PutSSOConfig(SSOConfig{Enabled: true, Label: "Okta"}); err != nil {
		t.Fatalf("PutSSOConfig: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenSQLite(dir)
	if err != nil {
		t.Fatalf("OpenSQLite (reopen): %v", err)
	}
	defer func() { _ = reopened.Close() }()

	u, err := reopened.GetUser("alice")
	if err != nil || u.Role != "admin" {
		t.Fatalf("GetUser after reopen = %+v, %v", u, err)
	}

	count, err := reopened.CountAudit()
	if err != nil || count != 1 {
		t.Fatalf("CountAudit after reopen = %d, %v; want 1, nil", count, err)
	}

	// A fresh append should continue the sequence rather than restart it.
	if err := reopened.AppendAudit(AuditEntry{Username: "alice", Action: "logout"}); err != nil {
		t.Fatalf("AppendAudit after reopen: %v", err)
	}
	entries, err := reopened.ListAudit(0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 2 || entries[0].ID != 2 || entries[1].ID != 1 {
		t.Fatalf("ListAudit after reopen = %+v, want IDs [2 1]", entries)
	}

	sso, err := reopened.GetSSOConfig()
	if err != nil || !sso.Enabled || sso.Label != "Okta" {
		t.Fatalf("GetSSOConfig after reopen = %+v, %v", sso, err)
	}

	// Re-opening an already-migrated database must be a no-op, not an error
	// (OpenSQLite runs migrate() on every start).
	reopened2, err := OpenSQLite(dir)
	if err != nil {
		t.Fatalf("OpenSQLite (second reopen): %v", err)
	}
	defer func() { _ = reopened2.Close() }()
}

func TestSQLiteStoreDeployHookLookups(t *testing.T) {
	s := openTestSQLite(t)

	h := DeployHook{ID: "h1", Hash: "hash1", ServiceName: "web"}
	if err := s.PutDeployHook(h); err != nil {
		t.Fatalf("PutDeployHook: %v", err)
	}

	byHash, err := s.FindDeployHookByHash("hash1")
	if err != nil || byHash.ID != "h1" {
		t.Fatalf("FindDeployHookByHash = %+v, %v", byHash, err)
	}

	forService, err := s.ListDeployHooksForService("web")
	if err != nil || len(forService) != 1 {
		t.Fatalf("ListDeployHooksForService = %+v, %v", forService, err)
	}

	if _, err := s.FindDeployHookByHash("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("FindDeployHookByHash(missing) err = %v, want ErrNotFound", err)
	}

	if err := s.TouchDeployHook("h1"); err != nil {
		t.Fatalf("TouchDeployHook: %v", err)
	}
	touched, err := s.FindDeployHookByHash("hash1")
	if err != nil || touched.LastUsedAt.IsZero() {
		t.Fatalf("FindDeployHookByHash after touch = %+v, %v; want non-zero LastUsedAt", touched, err)
	}
}

func TestSQLiteStoreClusterSamplePruning(t *testing.T) {
	s := openTestSQLite(t)

	if err := s.AppendClusterSample(ClusterSample{NodeCount: 1}); err != nil {
		t.Fatalf("AppendClusterSample: %v", err)
	}

	time.Sleep(5 * time.Millisecond)
	cutoff := time.Now()
	time.Sleep(5 * time.Millisecond)

	if err := s.AppendClusterSample(ClusterSample{NodeCount: 2}); err != nil {
		t.Fatalf("AppendClusterSample: %v", err)
	}

	if err := s.PruneClusterSamples(cutoff); err != nil {
		t.Fatalf("PruneClusterSamples: %v", err)
	}

	samples, err := s.ListClusterSamples(time.Time{})
	if err != nil {
		t.Fatalf("ListClusterSamples: %v", err)
	}
	if len(samples) != 1 || samples[0].NodeCount != 2 {
		t.Fatalf("ListClusterSamples after prune = %+v, want just the sample after cutoff", samples)
	}
}

func TestSQLiteStoreRegistryCredentialNilBlob(t *testing.T) {
	s := openTestSQLite(t)

	// PasswordEnc has no omitempty in Mongo but there's no reason a
	// zero-length ciphertext couldn't happen; make sure the round trip
	// through a BLOB column doesn't choke on it.
	c := RegistryCredential{Server: "registry.example.com", Username: "bot", PasswordEnc: nil, CreatedAt: time.Now()}
	if err := s.PutRegistryCredential(c); err != nil {
		t.Fatalf("PutRegistryCredential: %v", err)
	}
	got, err := s.GetRegistryCredential("registry.example.com")
	if err != nil {
		t.Fatalf("GetRegistryCredential: %v", err)
	}
	if got.Username != "bot" {
		t.Fatalf("GetRegistryCredential = %+v", got)
	}
}

func TestMigrateIsIdempotentAndForwardOnly(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSQLite(dir)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() { _ = s.Close() }()

	var versions []int
	rows, err := s.db.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		versions = append(versions, v)
	}
	_ = rows.Close()
	if len(versions) != 1 || versions[0] != 1 {
		t.Fatalf("schema_migrations = %v, want [1]", versions)
	}

	// Running migrate again against an already-migrated DB must not
	// re-apply anything (re-running 0001_init.sql's CREATE TABLE would
	// error, since the tables already exist).
	if err := migrate(s.db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// TestMigrateFSAppliesNewVersionsIncrementally is the scenario the whole
// migration runner exists for: a database created under an older schema
// version, then opened by a newer swarmdash build that ships an additional
// migration, ends up with both the old data intact and the new schema
// applied - without re-running (and erroring on) the migration it already
// has recorded in schema_migrations.
func TestMigrateFSAppliesNewVersionsIncrementally(t *testing.T) {
	dir := t.TempDir() + "/test.db"

	v1 := fstest.MapFS{
		"migrations/0001_init.sql": &fstest.MapFile{Data: []byte(
			`CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT NOT NULL);`,
		)},
	}

	db, err := sql.Open("sqlite", dir)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := migrateFS(db, v1, "migrations"); err != nil {
		t.Fatalf("migrateFS (v1): %v", err)
	}
	if _, err := db.Exec(`INSERT INTO widgets (name) VALUES ('sprocket')`); err != nil {
		t.Fatalf("insert into v1 schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// A later swarmdash build ships a second migration that adds a column.
	v2 := fstest.MapFS{
		"migrations/0001_init.sql": v1["migrations/0001_init.sql"],
		"migrations/0002_add_widget_color.sql": &fstest.MapFile{Data: []byte(
			`ALTER TABLE widgets ADD COLUMN color TEXT NOT NULL DEFAULT '';`,
		)},
	}

	db2, err := sql.Open("sqlite", dir)
	if err != nil {
		t.Fatalf("sql.Open (reopen): %v", err)
	}
	defer func() { _ = db2.Close() }()
	if err := migrateFS(db2, v2, "migrations"); err != nil {
		t.Fatalf("migrateFS (v2): %v", err)
	}

	// Pre-existing data survived the schema change, with the new column
	// defaulted.
	var name, color string
	if err := db2.QueryRow(`SELECT name, color FROM widgets WHERE id = 1`).Scan(&name, &color); err != nil {
		t.Fatalf("query after migration: %v", err)
	}
	if name != "sprocket" || color != "" {
		t.Fatalf("got name=%q color=%q, want name=sprocket color=\"\"", name, color)
	}

	var versions []int
	rows, err := db2.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		versions = append(versions, v)
	}
	_ = rows.Close()
	if len(versions) != 2 || versions[0] != 1 || versions[1] != 2 {
		t.Fatalf("schema_migrations = %v, want [1 2]", versions)
	}
}

func TestMigrationVersion(t *testing.T) {
	cases := []struct {
		name    string
		want    int
		wantErr bool
	}{
		{"0001_init.sql", 1, false},
		{"0042_add_widgets.sql", 42, false},
		{"nope.sql", 0, true},
	}
	for _, c := range cases {
		got, err := migrationVersion(c.name)
		if c.wantErr {
			if err == nil {
				t.Errorf("migrationVersion(%q) = %d, nil; want error", c.name, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("migrationVersion(%q) = %d, %v; want %d, nil", c.name, got, err, c.want)
		}
	}
}
