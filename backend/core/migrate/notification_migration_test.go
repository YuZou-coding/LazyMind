package migrate

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var notificationTables = []string{
	"notification_settings", "schedule_notification_rules",
	"task_notification_events", "task_notification_deliveries",
}

func requiredNotificationMigrations(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("../migrations/dev_mode/v0_3/*_task_notifications.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("notification SQL migration missing: require timestamped *_task_notifications.up.sql and .down.sql")
	}
	for _, up := range files {
		if _, err := os.ReadFile(strings.TrimSuffix(up, ".up.sql") + ".down.sql"); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func TestNotificationMigrationPairsExist(t *testing.T) { requiredNotificationMigrations(t) }

func notificationMigrationDB(t *testing.T, driver, label string) *sql.DB {
	t.Helper()
	if driver == "postgres" {
		dsn := strings.TrimSpace(os.Getenv(migrationPostgresDSNEnv))
		if dsn == "" {
			t.Skip("PostgreSQL not verified: set MIGRATION_TEST_POSTGRES_DSN to an isolated test server")
		}
		return createTemporaryPostgresDatabase(t, dsn, "notifications_"+label)
	}
	db := openRawSQLite(t, filepath.Join(t.TempDir(), label+".db"))
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	return db
}

func assertNotificationTables(t *testing.T, db *sql.DB, present bool) {
	t.Helper()
	for _, table := range notificationTables {
		rows, err := db.Query("SELECT * FROM " + table + " LIMIT 0")
		if err == nil {
			rows.Close()
		}
		if present && err != nil {
			t.Errorf("missing migrated table %s: %v", table, err)
		}
		if !present && err == nil {
			t.Errorf("down retained %s", table)
		}
	}
}

func TestNotificationMigrationUpgradeDownAndDataPreservation(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			db := notificationMigrationDB(t, driver, "upgrade")
			files := requiredNotificationMigrations(t)
			catalog, err := (&Runner{dir: "../migrations"}).loadCatalog()
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range catalog.Modes {
				if mode.Name != "v0_3" {
					execMigrationFileForDriver(t, db, mode.Aggregate.UpPath, driver)
					continue
				}
				for _, migration := range mode.Dev {
					if !strings.HasSuffix(migration.UpPath, "_task_notifications.up.sql") {
						execMigrationFileForDriver(t, db, migration.UpPath, driver)
					}
				}
			}
			if _, err := db.Exec(`INSERT INTO user_schedules(id,user_id,name,cron_expr,timezone,prompt_template,next_run_at,created_at) VALUES ('old-schedule','owner','legacy','0 9 * * *','UTC','report',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`); err != nil {
				t.Fatal(err)
			}
			for _, up := range files {
				execMigrationFileForDriver(t, db, up, driver)
			}
			assertNotificationTables(t, db, true)
			// Upgrading must not manufacture historical delivery jobs.
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM task_notification_events").Scan(&count); err != nil || count != 0 {
				t.Fatalf("upgrade created history or schema missing: count=%d err=%v", count, err)
			}
			for i := len(files) - 1; i >= 0; i-- {
				execMigrationFileForDriver(t, db, strings.TrimSuffix(files[i], ".up.sql")+".down.sql", driver)
			}
			assertNotificationTables(t, db, false)
			var name string
			if err := db.QueryRow("SELECT name FROM user_schedules WHERE id='old-schedule'").Scan(&name); err != nil || name != "legacy" {
				t.Fatalf("down lost preexisting schedule: %q %v", name, err)
			}
			for _, up := range files {
				execMigrationFileForDriver(t, db, up, driver)
			}
			assertNotificationTables(t, db, true)
		})
	}
}

func TestNotificationMigrationAggregateMatchesDevAndRollsBack(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			release := notificationMigrationDB(t, driver, "release")
			dev := notificationMigrationDB(t, driver, "dev")
			requiredNotificationMigrations(t)
			catalog, err := (&Runner{dir: "../migrations"}).loadCatalog()
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range catalog.Modes {
				if mode.Aggregate == nil {
					t.Fatalf("missing aggregate %s", mode.Name)
				}
				fingerprint := sqliteSchemaFingerprint
				if driver == "postgres" {
					fingerprint = postgresSchemaFingerprint
				}
				before := fingerprint(t, release)
				execMigrationFileForDriver(t, release, mode.Aggregate.UpPath, driver)
				if mode.Name != "v0_3" {
					execMigrationFileForDriver(t, dev, mode.Aggregate.UpPath, driver)
					continue
				}
				for _, migration := range mode.Dev {
					execMigrationFileForDriver(t, dev, migration.UpPath, driver)
				}
				assertNotificationTables(t, release, true)
				assertNotificationTables(t, dev, true)
				if a, b := fingerprint(t, release), fingerprint(t, dev); a != b {
					t.Fatalf("notification aggregate/dev schema mismatch\nrelease=%s\ndev=%s", a, b)
				}
				execMigrationFileForDriver(t, release, mode.Aggregate.DownPath, driver)
				assertNotificationTables(t, release, false)
				if after := fingerprint(t, release); after != before {
					t.Fatal("aggregate rollback did not restore previous release schema")
				}
			}
		})
	}
}
