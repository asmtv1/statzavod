package migrations

import (
	"context"
	"database/sql"
	"fmt"
	urlpkg "net/url"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// This covers the ownership invariants in PostgreSQL itself. It is opt-in
// because composite FKs, trigger updates, and partial assignment indexes are
// PostgreSQL behaviour rather than portable SQL mocks.
func TestContentPublishingScopePostgres(t *testing.T) {
	url := os.Getenv("MIGRATIONS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("MIGRATIONS_TEST_DATABASE_URL is not set")
	}
	parsed, err := urlpkg.Parse(url)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("content_scope_%d", time.Now().UnixNano())
	admin, err := sql.Open("pgx", url)
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
		t.Fatalf("apply migrations: %v", err)
	}
	ctx := context.Background()
	var ready bool
	if err = db.QueryRowContext(ctx, `SELECT to_regclass('media_assets') IS NOT NULL AND to_regclass('content_publish_targets') IS NOT NULL`).Scan(&ready); err != nil || !ready {
		t.Fatalf("database must be migrated through 00026: ready=%v err=%v", ready, err)
	}
	suffix := time.Now().UTC().Format("20060102150405.000000000")
	var org, companyA, companyB, user, creatorA, creatorAlt, creatorB string
	mustMigrationID(t, db, `INSERT INTO organizations(name,slug) VALUES('scope',$1) RETURNING id`, &org, "content-scope-"+suffix)
	mustMigrationID(t, db, `INSERT INTO companies(organization_id,name) VALUES($1,'A') RETURNING id`, &companyA, org)
	mustMigrationID(t, db, `INSERT INTO companies(organization_id,name) VALUES($1,'B') RETURNING id`, &companyB, org)
	mustMigrationID(t, db, `INSERT INTO users(email,password_hash,role,status) VALUES($1,'x','ADMIN','ACTIVE') RETURNING id`, &user, "content-scope-"+suffix+"@test")
	if _, err = db.ExecContext(ctx, `INSERT INTO organization_memberships(organization_id,user_id,membership_role) VALUES($1,$2,'OWNER')`, org, user); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		company, name string
		id            *string
	}{
		{companyA, "A", &creatorA}, {companyA, "Alt", &creatorAlt}, {companyB, "B", &creatorB},
	} {
		mustMigrationID(t, db, `INSERT INTO creators(organization_id,company_id,created_by,first_name,last_name,display_name) VALUES($1,$2,$3,$4,'Creator',$4) RETURNING id`, row.id, org, row.company, user, row.name)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), `DELETE FROM organizations WHERE id=$1`, org) })

	var accountA, accountAlt, accountB, mediaA, mediaAlt, mediaB, item, revision, target string
	for _, row := range []struct {
		company, creator, platform string
		id                         *string
	}{
		{companyA, creatorA, "YOUTUBE", &accountA}, {companyA, creatorAlt, "VK", &accountAlt}, {companyB, creatorB, "YOUTUBE", &accountB},
	} {
		mustMigrationID(t, db, `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status) VALUES($1,$2,$3,$4,$4,$4,'ACTIVE') RETURNING id`, row.id, org, row.company, row.platform, suffix+row.platform+row.creator)
		if _, err = db.ExecContext(ctx, `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2)`, row.creator, *row.id); err != nil {
			t.Fatalf("assign account: %v", err)
		}
	}
	for _, row := range []struct {
		company, creator, key string
		id                    *string
	}{
		{companyA, creatorA, "a", &mediaA}, {companyA, creatorAlt, "alt", &mediaAlt}, {companyB, creatorB, "b", &mediaB},
	} {
		mustMigrationID(t, db, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,content_sha256,original_filename,width,height) VALUES($1,$2,$3,'READY',$4,decode(md5($4),'hex'),'x.mp4',9,16) RETURNING id`, row.id, org, row.company, row.creator, "scope/"+suffix+row.key)
	}

	expectMigrationRejected(t, db, "cross-company content item", `INSERT INTO content_items(organization_id,company_id,creator_id,created_by) VALUES($1,$2,$3,$4)`, org, companyB, creatorA, user)
	mustMigrationID(t, db, `INSERT INTO content_items(organization_id,company_id,creator_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, &item, org, companyA, creatorA, user)
	mustMigrationID(t, db, `INSERT INTO content_revisions(content_item_id,organization_id,revision,description,status,created_by) VALUES($1,$2,1,'scope','DRAFT',$3) RETURNING id`, &revision, item, org, user)
	mustMigrationID(t, db, `INSERT INTO content_publish_targets(content_revision_id,organization_id,company_id,creator_id,platform_account_id,platform,media_asset_id,platform_options,status) VALUES($1,$2,$3,$4,$5,'YOUTUBE',$6,'{}','READY') RETURNING id`, &target, revision, org, companyA, creatorA, accountA, mediaA)

	// Same-company media of another creator is intentionally rejected; a second
	// valid asset owned by the target creator remains accepted.
	expectMigrationRejected(t, db, "same-company foreign media", `UPDATE content_publish_targets SET media_asset_id=$1 WHERE id=$2`, mediaAlt, target)
	expectMigrationRejected(t, db, "cross-company media", `UPDATE content_publish_targets SET media_asset_id=$1 WHERE id=$2`, mediaB, target)
	var mediaA2 string
	mustMigrationID(t, db, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,content_sha256,original_filename,width,height) VALUES($1,$2,$3,'READY',$4,decode('cc','hex'),'x2.mp4',9,16) RETURNING id`, &mediaA2, org, companyA, creatorA, "scope/"+suffix+"a2")
	if _, err = db.ExecContext(ctx, `UPDATE content_publish_targets SET media_asset_id=$1 WHERE id=$2`, mediaA2, target); err != nil {
		t.Fatalf("valid alternate creator media: %v", err)
	}
	expectMigrationRejected(t, db, "foreign account assignment", `UPDATE content_publish_targets SET platform_account_id=$1,platform='VK' WHERE id=$2`, accountAlt, target)
	expectMigrationRejected(t, db, "cross-company account", `UPDATE content_publish_targets SET platform_account_id=$1 WHERE id=$2`, accountB, target)
	expectMigrationRejected(t, db, "target creator update", `UPDATE content_publish_targets SET creator_id=$1 WHERE id=$2`, creatorAlt, target)
}

func mustMigrationID(t *testing.T, db *sql.DB, query string, target *string, args ...any) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(target); err != nil {
		t.Fatal(err)
	}
}

func expectMigrationRejected(t *testing.T, db *sql.DB, name, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err == nil {
		t.Fatalf("%s unexpectedly succeeded", name)
	}
}
