package postgres_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"gorm.io/gorm"

	"github.com/tekitla/tekitla-issues/app/infra/postgres"
)

// Base para los tests contra un Postgres de verdad (los que se llaman
// *_Postgres): se saltan sin TEKITLA_PG_TEST_URL, que apunta a una base
// descartable (scripts/test_pg.sh la levanta en docker y la borra al
// terminar; el job `test` del CI usa un service). Nunca contra QA ni prod.
func pgTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	url := os.Getenv("TEKITLA_PG_TEST_URL")
	if url == "" {
		t.Skip("sin TEKITLA_PG_TEST_URL (ver scripts/test_pg.sh)")
	}
	db, err := postgres.NewDB(url, false)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	// Las migraciones se aplican una vez por base (la primera vez que un test
	// la pide): los tests comparten la base de scripts/test_pg.sh.
	var exists bool
	if err := db.Raw(`SELECT to_regclass('issues.agent_session_transitions') IS NOT NULL`).Scan(&exists).Error; err != nil {
		t.Fatalf("check schema: %v", err)
	}
	if !exists {
		files, _ := filepath.Glob("../../../migrations/*.up.sql")
		sort.Strings(files)
		for _, f := range files {
			b, _ := os.ReadFile(f)
			if err := db.Exec(string(b)).Error; err != nil {
				t.Fatalf("%s: %v", f, err)
			}
		}
	}
	return db
}
