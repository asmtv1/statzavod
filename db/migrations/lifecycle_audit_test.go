package migrations

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

func TestLifecycleAuditMigrationContract(t *testing.T) {
	normalized := normalizeSQL(migrationUp(t, "00022_lifecycle_audit.sql"))
	required := []string{
		"create type workspace_lifecycle_state as enum ('active', 'deleting')",
		"create table operational_receipts",
		"add column delete_lease_owner text",
		"add column delete_lease_until timestamptz",
		"create index organizations_deleting_queue_idx",
		"add column purge_lease_owner text",
		"add column purge_lease_until timestamptz",
		"create index companies_lifecycle_purge_idx on companies (purge_at, purge_lease_until, id) where lifecycle_state = 'archived'",
		"add column suspended_for_company_archive boolean not null default false",
		"add column endpoint text",
		"add column http_method text",
		"add column response_status integer",
		"references companies (id, organization_id) on delete set null (company_id)",
		"create index audit_logs_workspace_created_idx",
	}
	for _, fragment := range required {
		if !strings.Contains(normalized, fragment) {
			t.Errorf("lifecycle migration is missing contract fragment %q", fragment)
		}
	}
	if strings.Contains(normalized, "drop column role") || strings.Contains(normalized, "drop table audit_logs") {
		t.Fatal("expand migration contracts or removes legacy authorization/audit data")
	}
}

func TestLifecycleWorkerLeaseContract(t *testing.T) {
	source, err := os.ReadFile("../../internal/transport/http/lifecycle.go")
	if err != nil {
		t.Fatal(err)
	}
	normalized := normalizeSQL(string(source))
	for _, fragment := range []string{
		"for update skip locked",
		"purge_lease_owner=$2,purge_lease_until=$3",
		"delete_lease_owner=$2,delete_lease_until=$3",
		"purge_lease_owner=$4 and purge_lease_until>$3",
		"delete_lease_owner=$2 and delete_lease_until>$3",
		"context.withtimeout",
		"sha256.sum256",
	} {
		if !strings.Contains(normalized, fragment) {
			t.Errorf("lifecycle worker is missing concurrency/safety fragment %q", fragment)
		}
	}
}

func TestLifecycleAuditProductionLikeUpgradeLive(t *testing.T) {
	databaseURL := os.Getenv("MIGRATIONS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("MIGRATIONS_TEST_DATABASE_URL is not set")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("migration_lifecycle_upgrade_%d", time.Now().UnixNano())
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
	if err = goose.UpTo(db, ".", 21); err != nil {
		t.Fatalf("migrate legacy production schema through v21: %v", err)
	}
	var organizationID, companyID string
	if err = db.QueryRow(`INSERT INTO organizations(name,slug) VALUES('Upgrade','upgrade-lifecycle') RETURNING id::text`).Scan(&organizationID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`INSERT INTO companies(organization_id,name) VALUES($1,'Existing active company') RETURNING id::text`, organizationID).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	if err = goose.UpTo(db, ".", 22); err != nil {
		t.Fatalf("expand production schema from v21 to v22: %v", err)
	}
	var workspaceState, companyState string
	var suspendedOAuth, suspendedSync bool
	if err = db.QueryRow(`SELECT lifecycle_state::text FROM organizations WHERE id=$1`, organizationID).Scan(&workspaceState); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT lifecycle_state::text FROM companies WHERE id=$1`, companyID).Scan(&companyState); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT column_default::text LIKE '%false%' FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='oauth_connections' AND column_name='suspended_for_company_archive'`).Scan(&suspendedOAuth); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT column_default::text LIKE '%false%' FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='sync_targets' AND column_name='suspended_for_company_archive'`).Scan(&suspendedSync); err != nil {
		t.Fatal(err)
	}
	if workspaceState != "ACTIVE" || companyState != "ACTIVE" || !suspendedOAuth || !suspendedSync {
		t.Fatalf("upgrade defaults workspace=%s company=%s oauth=%v sync=%v", workspaceState, companyState, suspendedOAuth, suspendedSync)
	}
	assertScalar(t, db, `SELECT count(*) FROM goose_db_version WHERE version_id=22 AND is_applied`, 1)
}
