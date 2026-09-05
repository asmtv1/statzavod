package migrations

import (
	"context"
	"database/sql"
	"fmt"
	urlpkg "net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestContentPublishingOwnerKeysetIndexDeclared(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("00025_content_publishing.sql"))
	if err != nil {
		t.Fatal(err)
	}
	want := "CREATE INDEX content_items_owner_updated_idx ON content_items (organization_id, updated_at DESC, id DESC) WHERE cancelled_at IS NULL;"
	if !strings.Contains(string(contents), want) {
		t.Fatalf("00025 is missing owner keyset index: %s", want)
	}
	for _, declaration := range []string{
		"CREATE INDEX content_items_company_updated_idx ON content_items (organization_id, company_id, updated_at DESC, id DESC) WHERE cancelled_at IS NULL;",
		"CREATE INDEX content_publish_targets_media_asset_idx ON content_publish_targets (media_asset_id, company_id, creator_id, organization_id) WHERE media_asset_id IS NOT NULL;",
		"CREATE INDEX media_upload_sessions_media_asset_idx ON media_upload_sessions (media_asset_id, organization_id);",
	} {
		migration := "00025_content_publishing.sql"
		if strings.Contains(declaration, "media_") {
			migration = "00026_content_media.sql"
		}
		body, readErr := os.ReadFile(filepath.Join(migration))
		if readErr != nil || !strings.Contains(string(body), declaration) {
			t.Fatalf("%s is missing index %q (read error %v)", migration, declaration, readErr)
		}
	}
}

// This opt-in PostgreSQL test proves both migration reversibility and the
// access paths used by the three content-list scopes. enable_seqscan is
// disabled only inside the test transaction: with a small deterministic test
// fixture PostgreSQL can otherwise prefer a sequential scan despite the index
// being the production access path for large workspaces.
func TestContentPublishingKeysetPlansPostgres(t *testing.T) {
	databaseURL := os.Getenv("MIGRATIONS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("MIGRATIONS_TEST_DATABASE_URL is not set")
	}
	parsed, err := urlpkg.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("content_keyset_%d", time.Now().UnixNano())
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err = admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) }()
	query := parsed.Query()
	query.Set("options", "-csearch_path="+schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err = goose.Up(db, "."); err != nil {
		t.Fatalf("fresh up: %v", err)
	}
	assertMigrationIndexExists(t, db, "content_items_owner_updated_idx", true)
	assertMigrationIndexExists(t, db, "content_items_company_updated_idx", true)
	if err = goose.DownTo(db, ".", 24); err != nil {
		t.Fatalf("down to 00024: %v", err)
	}
	assertMigrationIndexExists(t, db, "content_items_owner_updated_idx", false)
	if err = goose.Up(db, "."); err != nil {
		t.Fatalf("reapply through latest: %v", err)
	}
	assertMigrationIndexExists(t, db, "content_items_owner_updated_idx", true)
	assertMigrationIndexExists(t, db, "content_items_company_updated_idx", true)
	assertMigrationIndexExists(t, db, "content_publish_targets_media_asset_idx", true)
	assertMigrationIndexExists(t, db, "media_upload_sessions_media_asset_idx", true)
	assertAllForeignKeysHaveLeadingIndexes(t, db)

	ctx := context.Background()
	suffix := time.Now().UTC().Format("20060102150405.000000000")
	var org, companyA, companyB, user, creatorA, creatorA2, creatorB, creatorB2 string
	mustMigrationID(t, db, `INSERT INTO organizations(name,slug) VALUES('keyset',$1) RETURNING id`, &org, "content-keyset-"+suffix)
	mustMigrationID(t, db, `INSERT INTO companies(organization_id,name) VALUES($1,'A') RETURNING id`, &companyA, org)
	mustMigrationID(t, db, `INSERT INTO companies(organization_id,name) VALUES($1,'B') RETURNING id`, &companyB, org)
	mustMigrationID(t, db, `INSERT INTO users(email,password_hash,role,status) VALUES($1,'x','ADMIN','ACTIVE') RETURNING id`, &user, "content-keyset-"+suffix+"@test")
	if _, err = db.ExecContext(ctx, `INSERT INTO organization_memberships(organization_id,user_id,membership_role) VALUES($1,$2,'OWNER')`, org, user); err != nil {
		t.Fatalf("seed owner membership: %v", err)
	}
	for _, row := range []struct {
		company, name string
		id            *string
	}{
		{companyA, "A1", &creatorA}, {companyA, "A2", &creatorA2},
		{companyB, "B1", &creatorB}, {companyB, "B2", &creatorB2},
	} {
		mustMigrationID(t, db, `INSERT INTO creators(organization_id,company_id,created_by,first_name,last_name,display_name) VALUES($1,$2,$3,$4,'Creator',$4) RETURNING id`, row.id, org, row.company, user, row.name)
	}
	for _, row := range []struct{ company, creator string }{
		{companyA, creatorA}, {companyA, creatorA2}, {companyB, creatorB}, {companyB, creatorB2},
	} {
		if _, err = db.ExecContext(ctx, `
			INSERT INTO content_items(organization_id,company_id,creator_id,created_by,updated_at,cancelled_at)
			SELECT $1,$2,$3,$4,now()-(n*interval '1 second'),
			       CASE WHEN n%17=0 THEN now() END
			FROM generate_series(1,1500) AS n`, org, row.company, row.creator, user); err != nil {
			t.Fatalf("seed representative content rows: %v", err)
		}
	}
	if _, err = db.ExecContext(ctx, `ANALYZE content_items`); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	cursorTime := time.Now().Add(24 * time.Hour)
	cursorID := "ffffffff-ffff-ffff-ffff-ffffffffffff"
	assertMigrationPlanUsesIndex(t, tx, "owner", "content_items_owner_updated_idx", `
		SELECT id FROM content_items
		WHERE organization_id=$1 AND cancelled_at IS NULL
		  AND (updated_at,id)<($2,$3)
		ORDER BY updated_at DESC,id DESC LIMIT 51`, org, cursorTime, cursorID)
	assertMigrationPlanUsesIndex(t, tx, "company", "content_items_company_updated_idx", `
		SELECT id FROM content_items
		WHERE organization_id=$1 AND company_id=$2 AND cancelled_at IS NULL
		  AND (updated_at,id)<($3,$4)
		ORDER BY updated_at DESC,id DESC LIMIT 51`, org, companyA, cursorTime, cursorID)
	assertMigrationPlanUsesIndex(t, tx, "creator", "content_items_scope_updated_idx", `
		SELECT id FROM content_items
		WHERE organization_id=$1 AND company_id=$2 AND creator_id=$3 AND cancelled_at IS NULL
		  AND (updated_at,id)<($4,$5)
		ORDER BY updated_at DESC,id DESC LIMIT 51`, org, companyA, creatorA, cursorTime, cursorID)
}

func assertAllForeignKeysHaveLeadingIndexes(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`
		SELECT constraint_row.conrelid::regclass::text,constraint_row.conname
		FROM pg_constraint constraint_row
		WHERE constraint_row.contype='f'
		  AND constraint_row.connamespace=current_schema()::regnamespace
		  AND NOT EXISTS (
		    SELECT 1 FROM pg_index index_row
		    WHERE index_row.indrelid=constraint_row.conrelid
		      AND index_row.indisvalid AND index_row.indisready
		      AND index_row.indnkeyatts>=cardinality(constraint_row.conkey)
		      AND NOT EXISTS (
		        SELECT 1 FROM generate_subscripts(constraint_row.conkey,1) position
		        WHERE (index_row.indkey::smallint[])[position-1]<>constraint_row.conkey[position]
		      )
		  )
		ORDER BY 1,2`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var missing []string
	for rows.Next() {
		var table, constraint string
		if err = rows.Scan(&table, &constraint); err != nil {
			t.Fatal(err)
		}
		missing = append(missing, table+"."+constraint)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("foreign keys without exact leading indexes: %s", strings.Join(missing, ", "))
	}
}

func assertMigrationIndexExists(t *testing.T, db *sql.DB, name string, want bool) {
	t.Helper()
	var exists bool
	if err := db.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != want {
		t.Fatalf("index %s exists=%t want=%t", name, exists, want)
	}
}

func assertMigrationPlanUsesIndex(t *testing.T, tx *sql.Tx, scope, index, query string, args ...any) {
	t.Helper()
	rows, err := tx.Query("EXPLAIN (COSTS OFF) "+query, args...)
	if err != nil {
		t.Fatalf("explain %s keyset: %v", scope, err)
	}
	defer rows.Close()
	var planLines []string
	for rows.Next() {
		var line string
		if err = rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		planLines = append(planLines, line)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(planLines, "\n")
	if !strings.Contains(plan, index) {
		t.Fatalf("%s keyset did not use %s:\n%s", scope, index, plan)
	}
	t.Logf("%s keyset plan:\n%s", scope, plan)
}
