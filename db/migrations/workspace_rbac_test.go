package migrations

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestWorkspaceRBACMigrationContract(t *testing.T) {
	normalized := normalizeSQL(migrationUp(t, "00020_workspace_rbac.sql"))

	required := []string{
		"create type membership_role as enum ('owner', 'manager', 'creator')",
		"create type manager_permission as enum ( 'stats_view', 'stats_export', 'creator_create', 'creator_edit', 'creator_archive', 'creator_delete', 'creator_account_manage', 'social_connect', 'sync_manage', 'credential_edit', 'secret_reveal' )",
		"add constraint organization_memberships_one_workspace_key unique (user_id)",
		"when 'admin' then 'owner'::membership_role",
		"when 'analyst' then 'manager'::membership_role",
		"when 'viewer' then 'manager'::membership_role",
		"create trigger organization_memberships_synchronize_roles",
		"create table manager_company_assignments",
		"create table manager_company_permissions",
		"select orphan.organization_id, 'перенесённые данные'",
		"alter column company_id set not null",
		"references companies (id, organization_id) on delete cascade",
		"add column login_user_id uuid",
		"create unique index creators_one_profile_per_user_company_idx on creators (company_id, login_user_id) where login_user_id is not null",
		"add column active_company_id uuid references companies(id) on delete set null",
		"add column active_creator_id uuid references creators(id) on delete set null",
		"add column lifecycle_state company_lifecycle_state not null default 'active'",
		"add column purge_at timestamptz",
		"add column company_id uuid",
		"alter table platform_accounts alter column company_id set not null",
		"alter table sync_targets alter column company_id set not null",
		"create trigger sync_targets_scope",
		"create trigger publications_validate_scope",
		"foreign key (platform_account_id, organization_id) references platform_accounts (id, organization_id) on delete cascade",
		"foreign key (creator_id, organization_id) references creators (id, organization_id) on delete cascade",
		"foreign key (actor_id) references users(id) on delete set null",
		"foreign key (target_id) references sync_targets(id) on delete cascade",
	}
	for _, fragment := range required {
		if !strings.Contains(normalized, fragment) {
			t.Errorf("workspace RBAC migration is missing contract fragment %q", fragment)
		}
	}

	if regexp.MustCompile(`alter table users[^;]*(?:drop|alter) column role`).MatchString(normalized) {
		t.Fatal("expand migration must preserve users.role for legacy API rollback")
	}

	analystGrant := regexp.MustCompile(`when 'analyst' then array\[(.*?)\]::manager_permission\[\]`).FindStringSubmatch(normalized)
	if len(analystGrant) != 2 {
		t.Fatal("legacy ANALYST grants are missing")
	}
	for _, permission := range []string{
		"stats_view", "stats_export", "creator_create", "creator_edit", "creator_archive",
		"creator_delete", "creator_account_manage", "social_connect", "sync_manage", "credential_edit",
	} {
		if !strings.Contains(analystGrant[1], "'"+permission+"'") {
			t.Errorf("legacy ANALYST must receive %s", permission)
		}
	}
	if strings.Contains(analystGrant[1], "secret_reveal") {
		t.Fatal("legacy ANALYST must not receive SECRET_REVEAL")
	}

	viewerGrant := regexp.MustCompile(`when 'viewer' then array\[(.*?)\]::manager_permission\[\]`).FindStringSubmatch(normalized)
	if len(viewerGrant) != 2 || !strings.Contains(viewerGrant[1], "'stats_view'") || !strings.Contains(viewerGrant[1], "'stats_export'") {
		t.Fatal("legacy VIEWER must receive only statistics view/export grants")
	}
}

func TestWorkspaceRBACMigrationIndexesScopeAndForeignKeys(t *testing.T) {
	normalized := normalizeSQL(migrationUp(t, "00020_workspace_rbac.sql"))
	indexes := []string{
		"organization_memberships_workspace_role_idx",
		"companies_lifecycle_purge_idx",
		"creators_organization_company_idx",
		"creators_login_user_idx",
		"sessions_active_company_idx",
		"sessions_active_creator_idx",
		"manager_company_assignments_workspace_manager_idx",
		"manager_company_assignments_company_idx",
		"platform_accounts_company_workspace_idx",
		"sync_targets_company_workspace_idx",
		"audit_logs_organization_idx",
		"oauth_states_organization_idx",
		"sync_targets_organization_idx",
		"content_match_suggestions_publication_b_idx",
		"oauth_account_selections_creator_workspace_idx",
	}
	for _, index := range indexes {
		if !strings.Contains(normalized, "create index "+index) && !strings.Contains(normalized, "create unique index "+index) {
			t.Errorf("scope/FK index %s is missing", index)
		}
	}
}

// This test is opt-in because it creates and drops a schema. Set
// MIGRATIONS_TEST_DATABASE_URL to a disposable PostgreSQL 18 database.
func TestWorkspaceRBACMigrationLiveFresh(t *testing.T) {
	databaseURL := os.Getenv("MIGRATIONS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("MIGRATIONS_TEST_DATABASE_URL is not set")
	}

	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse migration test URL: %v", err)
	}
	schema := fmt.Sprintf("migration_rbac_fresh_%d", time.Now().UnixNano())
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open migration test database: %v", err)
	}
	defer admin.Close()
	if _, err = admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatalf("create fresh migration test schema: %v", err)
	}
	defer func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) }()

	query := parsed.Query()
	// Do not include public as a fallback: a pre-migrated target database may
	// already have public.goose_db_version. Goose must create and mutate only
	// this test's version table and objects inside the disposable schema.
	query.Set("options", "-csearch_path="+schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatalf("open fresh schema-scoped migration database: %v", err)
	}
	defer db.Close()
	if err = goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set Goose dialect: %v", err)
	}
	if err = goose.Up(db, "."); err != nil {
		t.Fatalf("apply migrations to a fresh schema: %v", err)
	}
	assertScalar(t, db, `SELECT count(*) FROM goose_db_version WHERE version_id=20 AND is_applied`, 1)
	assertScalar(t, db, `SELECT count(*) FROM creators WHERE company_id IS NULL`, 0)
	if err = goose.DownTo(db, ".", 19); err != nil {
		t.Fatalf("roll back fresh schema to v19: %v", err)
	}
	if err = goose.UpTo(db, ".", 20); err != nil {
		t.Fatalf("reapply workspace RBAC migration to fresh schema: %v", err)
	}
}

// This test is opt-in because it creates and drops a schema. Set
// MIGRATIONS_TEST_DATABASE_URL to a disposable PostgreSQL 18 database.
func TestWorkspaceRBACMigrationLiveUpgrade(t *testing.T) {
	databaseURL := os.Getenv("MIGRATIONS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("MIGRATIONS_TEST_DATABASE_URL is not set")
	}

	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse migration test URL: %v", err)
	}
	schema := fmt.Sprintf("migration_rbac_%d", time.Now().UnixNano())

	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open migration test database: %v", err)
	}
	defer admin.Close()
	if _, err = admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatalf("create migration test schema: %v", err)
	}
	defer func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) }()

	query := parsed.Query()
	// Keep the Goose version table schema-local even when public is already at
	// a newer version. Otherwise DownTo can roll back the shared public schema.
	query.Set("options", "-csearch_path="+schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatalf("open schema-scoped migration database: %v", err)
	}
	defer db.Close()

	if err = goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set Goose dialect: %v", err)
	}
	if err = goose.UpTo(db, ".", 19); err != nil {
		t.Fatalf("apply legacy schema: %v", err)
	}
	seedLegacyWorkspace(t, db)
	if err = goose.UpTo(db, ".", 20); err != nil {
		t.Fatalf("upgrade workspace RBAC schema: %v", err)
	}

	assertScalar(t, db, `SELECT count(*) FROM creators WHERE company_id IS NULL`, 0)
	assertScalar(t, db, `SELECT count(*) FROM platform_accounts WHERE company_id IS NULL`, 0)
	assertScalar(t, db, `SELECT count(*) FROM sync_targets WHERE company_id IS NULL`, 0)
	assertScalar(t, db, `SELECT count(*) FROM organization_memberships WHERE membership_role='OWNER'`, 1)
	assertScalar(t, db, `SELECT count(*) FROM organization_memberships WHERE membership_role='MANAGER'`, 2)
	assertScalar(t, db, `
		SELECT count(*) FROM sessions session
		JOIN users user_row ON user_row.id=session.user_id
		WHERE user_row.email IN ('migration-analyst@test.local','migration-viewer@test.local')
		  AND session.revoked_at IS NULL
		  AND session.active_company_id IS NULL
		  AND session.active_creator_id IS NULL`, 2)
	assertScalar(t, db, `SELECT count(*) FROM manager_company_permissions WHERE permission='SECRET_REVEAL'`, 0)
	assertScalar(t, db, `
		SELECT count(*)
		FROM manager_company_assignments assignment
		LEFT JOIN manager_company_permissions permission
		  ON permission.assignment_id=assignment.id
		 AND permission.permission='STATS_EXPORT'
		WHERE permission.assignment_id IS NULL`, 0)
	assertScalar(t, db, `
		SELECT count(*)
		FROM creator_account_assignments assignment
		JOIN platform_accounts account ON account.id=assignment.platform_account_id
		WHERE account.external_id='migration-account'
		  AND assignment.valid_to IS NOT NULL`, 1)
	assertScalar(t, db, `
		SELECT count(*)
		FROM creator_account_assignments assignment
		JOIN platform_accounts account ON account.id=assignment.platform_account_id
		WHERE account.external_id='migration-reassigned-account'`, 2)
	assertScalar(t, db, `
		SELECT count(*)
		FROM creator_account_assignments assignment
		JOIN platform_accounts account ON account.id=assignment.platform_account_id
		WHERE account.external_id='migration-reassigned-account'
		  AND assignment.valid_to IS NULL`, 1)
	assertScalar(t, db, `SELECT count(*) FROM sync_targets WHERE operation='DANGLING_IMPORT'`, 0)
	assertScalar(t, db, `SELECT count(*) FROM sync_runs WHERE error_message='dangling migration fixture'`, 0)

	var nullable string
	if err = db.QueryRow(`
		SELECT is_nullable FROM information_schema.columns
		WHERE table_schema=current_schema() AND table_name='creators' AND column_name='company_id'`).Scan(&nullable); err != nil {
		t.Fatalf("inspect creators.company_id: %v", err)
	}
	if nullable != "NO" {
		t.Fatalf("creators.company_id must be NOT NULL, got %s", nullable)
	}
	for _, table := range []string{"platform_accounts", "sync_targets"} {
		if err = db.QueryRow(`
			SELECT is_nullable FROM information_schema.columns
			WHERE table_schema=current_schema() AND table_name=$1 AND column_name='company_id'`, table).Scan(&nullable); err != nil {
			t.Fatalf("inspect %s.company_id: %v", table, err)
		}
		if nullable != "NO" {
			t.Fatalf("%s.company_id must be NOT NULL, got %s", table, nullable)
		}
	}

	if err = goose.DownTo(db, ".", 19); err != nil {
		t.Fatalf("rollback upgraded historical reassignment fixture: %v", err)
	}
	if err = goose.UpTo(db, ".", 20); err != nil {
		t.Fatalf("re-upgrade historical reassignment fixture: %v", err)
	}
	assertScalar(t, db, `
		SELECT count(*)
		FROM creator_account_assignments assignment
		JOIN platform_accounts account ON account.id=assignment.platform_account_id
		WHERE account.external_id='migration-reassigned-account'`, 2)
	assertScalar(t, db, `
		SELECT count(*)
		FROM creator_account_assignments assignment
		JOIN platform_accounts account ON account.id=assignment.platform_account_id
		WHERE account.external_id='migration-account'
		  AND assignment.valid_to IS NOT NULL`, 1)

	testWorkspaceTenantConstraints(t, db)
	testCompanyAndWorkspaceCascades(t, db)
}

func testWorkspaceTenantConstraints(t *testing.T, db *sql.DB) {
	t.Helper()
	expectExecRejected(t, db, "reactivate cross-company historical assignment", `
		UPDATE creator_account_assignments assignment
		SET valid_to=NULL
		FROM platform_accounts account
		WHERE account.id=assignment.platform_account_id
		  AND account.external_id='migration-account'`)
	assertScalar(t, db, `
		SELECT count(*)
		FROM creator_account_assignments assignment
		JOIN platform_accounts account ON account.id=assignment.platform_account_id
		WHERE account.external_id='migration-account'
		  AND assignment.valid_to IS NOT NULL`, 1)

	_, err := db.Exec(`
		WITH legacy_user AS (
			INSERT INTO users(email,password_hash,role)
			VALUES('migration-legacy-write@test.local','hash','ANALYST') RETURNING id
		), workspace AS (
			SELECT id FROM organizations WHERE slug='statzavod'
		)
		INSERT INTO organization_memberships(organization_id,user_id,role)
		SELECT workspace.id,legacy_user.id,'ANALYST'
		FROM workspace CROSS JOIN legacy_user`)
	if err != nil {
		t.Fatalf("legacy membership write compatibility: %v", err)
	}
	assertScalar(t, db, `
		SELECT count(*) FROM organization_memberships membership
		JOIN users user_row ON user_row.id=membership.user_id
		WHERE user_row.email='migration-legacy-write@test.local'
		  AND membership.membership_role='MANAGER'`, 1)

	_, err = db.Exec(`
		WITH second_workspace AS (
			INSERT INTO organizations(name,slug) VALUES('Second workspace','migration-second') RETURNING id
		)
		INSERT INTO companies(organization_id,name)
		SELECT id,'Second company' FROM second_workspace`)
	if err != nil {
		t.Fatalf("create second workspace fixture: %v", err)
	}
	_, err = db.Exec(`
		WITH second_user AS (
		  INSERT INTO users(email,password_hash,role)
		  VALUES('second-workspace-user@test.local','hash','ADMIN') RETURNING id
		), workspace AS (
		  SELECT id FROM organizations WHERE slug='migration-second'
		)
		INSERT INTO organization_memberships(organization_id,user_id,role)
		SELECT workspace.id,second_user.id,'ADMIN'
		FROM workspace CROSS JOIN second_user`)
	if err != nil {
		t.Fatalf("create second workspace user fixture: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name)
		SELECT company.organization_id,company.id,'YOUTUBE','second-workspace-account','second','Second'
		FROM companies company WHERE company.name='Second company'`)
	if err != nil {
		t.Fatalf("create second workspace account fixture: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO platform_accounts(organization_id,platform,external_id,username,display_name)
		SELECT workspace.id,'TIKTOK','fallback-workspace-move-account','fallback-move','Fallback Move'
		FROM organizations workspace WHERE workspace.slug='statzavod'`)
	if err != nil {
		t.Fatalf("create fallback workspace move fixture: %v", err)
	}
	expectExecRejected(t, db, "fallback platform account workspace move", `
		UPDATE platform_accounts account
		SET organization_id=company.organization_id,company_id=company.id
		FROM companies company
		WHERE account.external_id='fallback-workspace-move-account'
		  AND company.name='Second company'`)
	assertScalar(t, db, `
		SELECT count(*) FROM platform_accounts account
		JOIN organizations workspace ON workspace.id=account.organization_id
		WHERE account.external_id='fallback-workspace-move-account'
		  AND workspace.slug='statzavod'`, 1)
	_, err = db.Exec(`
		INSERT INTO creators(organization_id,company_id,first_name,last_name,display_name)
		SELECT company.organization_id,company.id,'Second','Creator','Second Creator'
		FROM companies company WHERE company.name='Second company';

		INSERT INTO company_vk_accounts(organization_id,company_id)
		SELECT company.organization_id,company.id
		FROM companies company WHERE company.name='Second company'`)
	if err != nil {
		t.Fatalf("create second workspace paired-resource fixtures: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO publications(
		  organization_id,creator_id,platform_account_id,platform,external_id,publication_type,published_at
		)
		SELECT creator.organization_id,creator.id,account.id,'YOUTUBE','second-publication-a','VIDEO',now()
		FROM creators creator CROSS JOIN platform_accounts account
		WHERE creator.display_name='Second Creator'
		  AND account.external_id='second-workspace-account';
		INSERT INTO publications(
		  organization_id,creator_id,platform_account_id,platform,external_id,publication_type,published_at
		)
		SELECT creator.organization_id,creator.id,account.id,'YOUTUBE','second-publication-b','VIDEO',now()
		FROM creators creator CROSS JOIN platform_accounts account
		WHERE creator.display_name='Second Creator'
		  AND account.external_id='second-workspace-account';
		INSERT INTO content_groups(creator_id,name)
		SELECT creator.id,'Main workspace group'
		FROM creators creator WHERE creator.display_name='Legacy Creator';

		INSERT INTO publications(
		  organization_id,creator_id,platform_account_id,platform,external_id,publication_type,published_at
		)
		SELECT creator.organization_id,creator.id,account.id,'INSTAGRAM','main-publication-a','VIDEO',now()
		FROM creators creator CROSS JOIN platform_accounts account
		WHERE creator.display_name='Legacy Creator'
		  AND account.external_id='migration-reassigned-account';
		INSERT INTO publications(
		  organization_id,creator_id,platform_account_id,platform,external_id,publication_type,published_at
		)
		SELECT creator.organization_id,creator.id,account.id,'INSTAGRAM','main-publication-b','VIDEO',now()
		FROM creators creator CROSS JOIN platform_accounts account
		WHERE creator.display_name='Legacy Creator'
		  AND account.external_id='migration-reassigned-account';

		INSERT INTO content_group_members(content_group_id,publication_id)
		SELECT content_group.id,publication.id
		FROM content_groups content_group CROSS JOIN publications publication
		WHERE content_group.name='Main workspace group'
		  AND publication.external_id='main-publication-a';
		INSERT INTO content_match_suggestions(creator_id,publication_a_id,publication_b_id,score)
		SELECT creator.id,publication_a.id,publication_b.id,75
		FROM creators creator
		CROSS JOIN publications publication_a
		CROSS JOIN publications publication_b
		WHERE creator.display_name='Legacy Creator'
		  AND publication_a.external_id='main-publication-a'
		  AND publication_b.external_id='main-publication-b'`)
	if err != nil {
		t.Fatalf("create scoped content fixtures: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO companies(organization_id,name)
		SELECT workspace.id,'Legacy Company C'
		FROM organizations workspace WHERE workspace.slug='statzavod';
		INSERT INTO company_vk_accounts(organization_id,company_id)
		SELECT company.organization_id,company.id
		FROM companies company WHERE company.name='Legacy Company A'`)
	if err != nil {
		t.Fatalf("create immutable parent fixtures: %v", err)
	}
	expectExecRejected(t, db, "company VK company move without platform account", `
		UPDATE company_vk_accounts vk
		SET company_id=destination.id
		FROM companies source CROSS JOIN companies destination
		WHERE vk.company_id=source.id
		  AND source.name='Legacy Company A'
		  AND destination.name='Legacy Company C'`)
	expectExecRejected(t, db, "content group creator move", `
		UPDATE content_groups content_group
		SET creator_id=creator.id
		FROM creators creator
		WHERE content_group.name='Main workspace group'
		  AND creator.display_name='Second Creator'`)
	expectExecRejected(t, db, "publication ownership move", `
		UPDATE publications publication
		SET organization_id=creator.organization_id,
		    creator_id=creator.id,
		    platform_account_id=account.id
		FROM creators creator CROSS JOIN platform_accounts account
		WHERE publication.external_id='main-publication-a'
		  AND creator.display_name='Second Creator'
		  AND account.external_id='second-workspace-account'`)

	_, err = db.Exec(`
		INSERT INTO audit_logs(organization_id,actor_id,action,entity_type)
		SELECT workspace.id,actor.id,'SAME_WORKSPACE_ACTOR','TEST'
		FROM organizations workspace CROSS JOIN users actor
		WHERE workspace.slug='statzavod' AND actor.email='migration-admin@test.local';
		INSERT INTO creator_history_events(organization_id,creator_id,actor_id,block)
		SELECT creator.organization_id,creator.id,actor.id,'PROFILE'
		FROM creators creator CROSS JOIN users actor
		WHERE creator.display_name='Legacy Creator' AND actor.email='migration-admin@test.local';
		INSERT INTO oauth_states(organization_id,creator_id,initiated_by,platform,state_hash,nonce,expires_at)
		SELECT creator.organization_id,creator.id,actor.id,'TIKTOK',decode('11','hex'),decode('12','hex'),now()+interval '1 hour'
		FROM creators creator CROSS JOIN users actor
		WHERE creator.display_name='Legacy Creator' AND actor.email='migration-admin@test.local';
		INSERT INTO oauth_account_selections(
		  organization_id,creator_id,initiated_by,platform,payload_ciphertext,nonce,expires_at
		)
		SELECT creator.organization_id,creator.id,actor.id,'INSTAGRAM',decode('13','hex'),decode('14','hex'),now()+interval '1 hour'
		FROM creators creator CROSS JOIN users actor
		WHERE creator.display_name='Legacy Creator' AND actor.email='migration-admin@test.local';
		INSERT INTO user_invitations(organization_id,email,role,token_hash,expires_at,created_by)
		SELECT workspace.id,'same-workspace-invite@test.local','VIEWER',decode('15','hex'),now()+interval '1 hour',actor.id
		FROM organizations workspace CROSS JOIN users actor
		WHERE workspace.slug='statzavod' AND actor.email='migration-admin@test.local';
		UPDATE content_groups content_group SET created_by=actor.id
		FROM users actor
		WHERE content_group.name='Main workspace group'
		  AND actor.email='migration-admin@test.local';
		UPDATE creators creator SET created_by=actor.id
		FROM users actor
		WHERE creator.display_name='Legacy Creator'
		  AND actor.email='migration-admin@test.local';
		UPDATE company_vk_accounts vk SET created_by=actor.id,updated_by=actor.id
		FROM companies company CROSS JOIN users actor
		WHERE company.id=vk.company_id
		  AND company.name='Legacy Company A'
		  AND actor.email='migration-admin@test.local';
		UPDATE creator_account_assignments assignment SET assigned_by=actor.id
		FROM platform_accounts account CROSS JOIN users actor
		WHERE account.id=assignment.platform_account_id
		  AND account.external_id='migration-reassigned-account'
		  AND assignment.valid_to IS NULL
		  AND actor.email='migration-admin@test.local';
		UPDATE manager_company_assignments assignment SET created_by=actor.id
		FROM companies company CROSS JOIN users actor
		WHERE company.id=assignment.company_id
		  AND company.name='Legacy Company B'
		  AND actor.email='migration-admin@test.local';
		UPDATE manager_company_permissions permission SET granted_by=actor.id
		FROM manager_company_assignments assignment
		JOIN companies company ON company.id=assignment.company_id
		CROSS JOIN users actor
		WHERE assignment.id=permission.assignment_id
		  AND company.name='Legacy Company B'
		  AND actor.email='migration-admin@test.local';
		INSERT INTO creator_credentials(
		  creator_id,section,field_key,value_ciphertext,value_nonce,updated_by
		)
		SELECT creator.id,'TEST','same-workspace',decode('21','hex'),decode('22','hex'),actor.id
		FROM creators creator CROSS JOIN users actor
		WHERE creator.display_name='Legacy Creator'
		  AND actor.email='migration-admin@test.local';
		INSERT INTO creator_vk_assignments(
		  creator_id,company_vk_account_id,community_url,updated_by
		)
		SELECT creator.id,vk.id,'https://vk.com/same_workspace',actor.id
		FROM creators creator
		JOIN companies company ON company.id=creator.company_id
		JOIN company_vk_accounts vk ON vk.company_id=company.id
		CROSS JOIN users actor
		WHERE creator.display_name='Second Creator'
		  AND actor.email='second-workspace-user@test.local'`)
	if err != nil {
		t.Fatalf("same-workspace actor references must pass: %v", err)
	}

	expectExecRejected(t, db, "cross-workspace audit actor", `
		INSERT INTO audit_logs(organization_id,actor_id,action,entity_type)
		SELECT workspace.id,actor.id,'CROSS_ACTOR','TEST'
		FROM organizations workspace CROSS JOIN users actor
		WHERE workspace.slug='statzavod' AND actor.email='second-workspace-user@test.local'`)
	expectExecRejected(t, db, "cross-workspace history actor", `
		INSERT INTO creator_history_events(organization_id,creator_id,actor_id,block)
		SELECT creator.organization_id,creator.id,actor.id,'PROFILE'
		FROM creators creator CROSS JOIN users actor
		WHERE creator.display_name='Legacy Creator' AND actor.email='second-workspace-user@test.local'`)
	expectExecRejected(t, db, "cross-workspace OAuth initiator", `
		INSERT INTO oauth_states(organization_id,creator_id,initiated_by,platform,state_hash,nonce,expires_at)
		SELECT creator.organization_id,creator.id,actor.id,'TIKTOK',decode('16','hex'),decode('17','hex'),now()+interval '1 hour'
		FROM creators creator CROSS JOIN users actor
		WHERE creator.display_name='Legacy Creator' AND actor.email='second-workspace-user@test.local'`)
	expectExecRejected(t, db, "cross-workspace OAuth selection initiator", `
		INSERT INTO oauth_account_selections(
		  organization_id,creator_id,initiated_by,platform,payload_ciphertext,nonce,expires_at
		)
		SELECT creator.organization_id,creator.id,actor.id,'INSTAGRAM',decode('18','hex'),decode('19','hex'),now()+interval '1 hour'
		FROM creators creator CROSS JOIN users actor
		WHERE creator.display_name='Legacy Creator' AND actor.email='second-workspace-user@test.local'`)
	expectExecRejected(t, db, "cross-workspace invitation creator", `
		INSERT INTO user_invitations(organization_id,email,role,token_hash,expires_at,created_by)
		SELECT workspace.id,'cross-workspace-invite@test.local','VIEWER',decode('20','hex'),now()+interval '1 hour',actor.id
		FROM organizations workspace CROSS JOIN users actor
		WHERE workspace.slug='statzavod' AND actor.email='second-workspace-user@test.local'`)
	expectExecRejected(t, db, "cross-workspace creator created_by", `
		UPDATE creators creator SET created_by=actor.id
		FROM users actor
		WHERE creator.display_name='Legacy Creator'
		  AND actor.email='second-workspace-user@test.local'`)
	expectExecRejected(t, db, "cross-workspace content group creator", `
		UPDATE content_groups content_group SET created_by=actor.id
		FROM users actor
		WHERE content_group.name='Main workspace group'
		  AND actor.email='second-workspace-user@test.local'`)
	expectExecRejected(t, db, "cross-workspace company VK updater", `
		UPDATE company_vk_accounts vk SET updated_by=actor.id
		FROM companies company CROSS JOIN users actor
		WHERE company.id=vk.company_id
		  AND company.name='Legacy Company A'
		  AND actor.email='second-workspace-user@test.local'`)
	expectExecRejected(t, db, "cross-workspace creator assignment actor", `
		UPDATE creator_account_assignments assignment SET assigned_by=actor.id
		FROM platform_accounts account CROSS JOIN users actor
		WHERE account.id=assignment.platform_account_id
		  AND account.external_id='migration-reassigned-account'
		  AND assignment.valid_to IS NULL
		  AND actor.email='second-workspace-user@test.local'`)
	expectExecRejected(t, db, "cross-workspace manager assignment creator", `
		UPDATE manager_company_assignments assignment SET created_by=actor.id
		FROM companies company CROSS JOIN users actor
		WHERE company.id=assignment.company_id
		  AND company.name='Legacy Company B'
		  AND actor.email='second-workspace-user@test.local'`)
	expectExecRejected(t, db, "cross-workspace manager permission grantor", `
		UPDATE manager_company_permissions permission SET granted_by=actor.id
		FROM manager_company_assignments assignment
		JOIN companies company ON company.id=assignment.company_id
		CROSS JOIN users actor
		WHERE assignment.id=permission.assignment_id
		  AND company.name='Legacy Company B'
		  AND actor.email='second-workspace-user@test.local'`)
	expectExecRejected(t, db, "cross-workspace creator credential updater", `
		UPDATE creator_credentials credential SET updated_by=actor.id
		FROM creators creator CROSS JOIN users actor
		WHERE creator.id=credential.creator_id
		  AND creator.display_name='Legacy Creator'
		  AND credential.field_key='same-workspace'
		  AND actor.email='second-workspace-user@test.local'`)
	expectExecRejected(t, db, "cross-workspace creator VK updater", `
		UPDATE creator_vk_assignments assignment SET updated_by=actor.id
		FROM creators creator CROSS JOIN users actor
		WHERE creator.id=assignment.creator_id
		  AND creator.display_name='Second Creator'
		  AND actor.email='migration-admin@test.local'`)

	if _, err = db.Exec(`
		INSERT INTO creators(organization_id,company_id,first_name,last_name,display_name)
		SELECT first_workspace.id, second_company.id, 'Cross','Workspace','Cross Workspace'
		FROM organizations first_workspace
		CROSS JOIN companies second_company
		WHERE first_workspace.slug='statzavod'
		  AND second_company.name='Second company'`); err == nil {
		t.Fatal("cross-workspace creator/company link must be rejected")
	}

	_, err = db.Exec(`
		WITH creator_user AS (
			INSERT INTO users(email,password_hash,role)
			VALUES('migration-creator@test.local','hash','VIEWER') RETURNING id
		), workspace AS (
			SELECT id FROM organizations WHERE slug='statzavod'
		), membership AS (
			INSERT INTO organization_memberships(organization_id,user_id,membership_role)
			SELECT workspace.id,creator_user.id,'CREATOR'
			FROM workspace CROSS JOIN creator_user
			RETURNING user_id,organization_id
		)
		INSERT INTO creators(organization_id,company_id,login_user_id,first_name,last_name,display_name)
		SELECT membership.organization_id,company.id,membership.user_id,'Portal','Creator','Portal Creator'
		FROM membership
		JOIN companies company ON company.organization_id=membership.organization_id`)
	if err != nil {
		t.Fatalf("create creator login profile fixture: %v", err)
	}
	expectExecRejected(t, db, "legacy role escalation of linked creator", `
		UPDATE organization_memberships membership
		SET role='ADMIN'
		FROM users user_row
		WHERE user_row.id=membership.user_id
		  AND user_row.email='migration-creator@test.local'`)
	assertScalar(t, db, `
		SELECT count(*) FROM organization_memberships membership
		JOIN users user_row ON user_row.id=membership.user_id
		WHERE user_row.email='migration-creator@test.local'
		  AND membership.role='VIEWER'
		  AND membership.membership_role='CREATOR'`, 1)

	_, err = db.Exec(`
		INSERT INTO sessions(user_id,token_hash,expires_at)
		SELECT user_row.id,decode('aa01','hex'),now()+interval '1 hour'
		FROM users user_row WHERE user_row.email='migration-admin@test.local';
		UPDATE sessions session
		SET active_company_id=company.id
		FROM companies company
		WHERE session.token_hash=decode('aa01','hex')
		  AND company.name='Legacy Company A';
		INSERT INTO sessions(user_id,token_hash,expires_at,active_company_id)
		SELECT user_row.id,decode('aa02','hex'),now()+interval '1 hour',company.id
		FROM users user_row CROSS JOIN companies company
		WHERE user_row.email='migration-analyst@test.local'
		  AND company.name='Legacy Company A';
		INSERT INTO sessions(user_id,token_hash,expires_at,active_creator_id)
		SELECT user_row.id,decode('aa03','hex'),now()+interval '1 hour',creator.id
		FROM users user_row CROSS JOIN creators creator
		JOIN companies company ON company.id=creator.company_id
		WHERE user_row.email='migration-creator@test.local'
		  AND creator.login_user_id=user_row.id
		  AND company.name='Legacy Company A';
		INSERT INTO sessions(user_id,token_hash,expires_at)
		SELECT user_row.id,decode('aa05','hex'),now()+interval '1 hour'
		FROM users user_row WHERE user_row.email='migration-analyst@test.local';
		INSERT INTO sessions(user_id,token_hash,expires_at)
		SELECT user_row.id,decode('aa06','hex'),now()+interval '1 hour'
		FROM users user_row WHERE user_row.email='migration-creator@test.local';
		WITH changed_user AS (
		  INSERT INTO users(email,password_hash,role)
		  VALUES('membership-role-change@test.local','hash','ADMIN') RETURNING id
		), membership AS (
		  INSERT INTO organization_memberships(organization_id,user_id,membership_role)
		  SELECT workspace.id,changed_user.id,'OWNER'
		  FROM organizations workspace CROSS JOIN changed_user
		  WHERE workspace.slug='statzavod'
		  RETURNING user_id
		)
		INSERT INTO sessions(user_id,token_hash,expires_at)
		SELECT user_id,decode('aa04','hex'),now()+interval '1 hour' FROM membership`)
	if err != nil {
		t.Fatalf("create role-aware session contexts: %v", err)
	}
	assertScalar(t, db, `
		SELECT count(*) FROM sessions session
		JOIN creators creator ON creator.id=session.active_creator_id
		WHERE session.token_hash=decode('aa03','hex')
		  AND session.active_company_id=creator.company_id
		  AND session.revoked_at IS NULL`, 1)
	assertScalar(t, db, `
		SELECT count(*) FROM sessions
		WHERE token_hash IN (decode('aa05','hex'),decode('aa06','hex'))
		  AND active_company_id IS NULL
		  AND active_creator_id IS NULL
		  AND revoked_at IS NULL`, 2)
	expectExecRejected(t, db, "owner creator context", `
		UPDATE sessions session SET active_creator_id=creator.id
		FROM creators creator
		WHERE session.token_hash=decode('aa01','hex')
		  AND creator.display_name='Legacy Creator'`)
	expectExecRejected(t, db, "manager unassigned company context", `
		UPDATE sessions session SET active_company_id=company.id
		FROM companies company
		WHERE session.token_hash=decode('aa02','hex')
		  AND company.name='Legacy Company C'`)
	expectExecRejected(t, db, "creator mismatched company context", `
		UPDATE sessions session SET active_company_id=company.id
		FROM companies company
		WHERE session.token_hash=decode('aa03','hex')
		  AND company.name='Legacy Company B'`)
	_, err = db.Exec(`
		UPDATE manager_company_assignments assignment
		SET company_id=destination.id
		FROM users user_row, companies source, companies destination
		WHERE assignment.manager_user_id=user_row.id
		  AND assignment.company_id=source.id
		  AND user_row.email='migration-analyst@test.local'
		  AND source.name='Legacy Company A'
		  AND destination.name='Legacy Company C'`)
	if err != nil {
		t.Fatalf("move manager company assignment: %v", err)
	}
	assertScalar(t, db, `
		SELECT count(*) FROM sessions
		WHERE token_hash=decode('aa02','hex')
		  AND revoked_at IS NOT NULL
		  AND active_company_id IS NULL
		  AND active_creator_id IS NULL`, 1)
	_, err = db.Exec(`
		UPDATE organization_memberships membership
		SET membership_role='MANAGER'
		FROM users user_row
		WHERE user_row.id=membership.user_id
		  AND user_row.email='membership-role-change@test.local'`)
	if err != nil {
		t.Fatalf("change membership role and invalidate session: %v", err)
	}
	assertScalar(t, db, `
		SELECT count(*) FROM sessions
		WHERE token_hash=decode('aa04','hex')
		  AND revoked_at IS NOT NULL
		  AND active_company_id IS NULL
		  AND active_creator_id IS NULL`, 1)
	expectExecRejected(t, db, "membership workspace move", `
		UPDATE organization_memberships membership
		SET organization_id=workspace.id
		FROM users user_row CROSS JOIN organizations workspace
		WHERE user_row.id=membership.user_id
		  AND user_row.email='membership-role-change@test.local'
		  AND workspace.slug='migration-second'`)
	testConcurrentMembershipChildCleanup(t, db)
	testConcurrentManagerAssignmentCleanup(t, db)

	if _, err = db.Exec(`
		INSERT INTO creators(organization_id,company_id,login_user_id,first_name,last_name,display_name)
		SELECT membership.organization_id,company.id,membership.user_id,'Duplicate','Creator','Duplicate Creator'
		FROM organization_memberships membership
		JOIN companies company ON company.organization_id=membership.organization_id
		JOIN users creator_user ON creator_user.id=membership.user_id
		WHERE creator_user.email='migration-creator@test.local'`); err == nil {
		t.Fatal("a creator user must not have two profiles in one company")
	}

	if _, err = db.Exec(`
		INSERT INTO creator_account_assignments(creator_id,platform_account_id)
		SELECT creator.id,account.id
		FROM creators creator
		CROSS JOIN platform_accounts account
		WHERE creator.display_name='Legacy Creator'
		  AND account.external_id='second-workspace-account'`); err == nil {
		t.Fatal("creator/platform assignment must reject cross-workspace links")
	}
	expectExecRejected(t, db, "creator company move", `
		UPDATE creators creator
		SET company_id=company.id
		FROM companies company
		WHERE creator.display_name='Legacy Creator'
		  AND company.name='Legacy Company B'`)

	_, err = db.Exec(`
		INSERT INTO platform_accounts(organization_id,platform,external_id,username,display_name)
		SELECT workspace.id,'INSTAGRAM','legacy-compatible-account','legacy-compatible','Legacy compatible'
		FROM organizations workspace WHERE workspace.slug='statzavod'`)
	if err != nil {
		t.Fatalf("legacy platform account insert without company_id: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO sync_targets(organization_id,target_type,target_id,operation)
		SELECT account.organization_id,'PLATFORM_ACCOUNT',account.id,'LEGACY_COMPATIBLE_IMPORT'
		FROM platform_accounts account
		WHERE account.external_id='legacy-compatible-account'`)
	if err != nil {
		t.Fatalf("legacy sync target insert without company_id: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO creator_account_assignments(creator_id,platform_account_id)
		SELECT creator.id,account.id
		FROM creators creator CROSS JOIN platform_accounts account
		WHERE creator.display_name='Legacy Creator'
		  AND account.external_id='legacy-compatible-account'`)
	if err != nil {
		t.Fatalf("claim legacy platform account for creator company: %v", err)
	}
	assertScalar(t, db, `
		SELECT count(*)
		FROM platform_accounts account
		JOIN creators creator ON creator.display_name='Legacy Creator'
		WHERE account.external_id='legacy-compatible-account'
		  AND account.organization_id=creator.organization_id
		  AND account.company_id=creator.company_id`, 1)
	assertScalar(t, db, `
		SELECT count(*)
		FROM sync_targets target
		JOIN platform_accounts account ON account.id=target.target_id
		WHERE target.operation='LEGACY_COMPATIBLE_IMPORT'
		  AND target.organization_id=account.organization_id
		  AND target.company_id=account.company_id`, 1)
	expectExecRejected(t, db, "owned platform account company move", `
		UPDATE platform_accounts account
		SET company_id=company.id
		FROM companies company
		WHERE account.external_id='legacy-compatible-account'
		  AND company.name='Legacy Company B'`)
	expectExecRejected(t, db, "cross-company sync target", `
		INSERT INTO sync_targets(organization_id,company_id,target_type,target_id,operation)
		SELECT account.organization_id,company.id,'PLATFORM_ACCOUNT',account.id,'CROSS_COMPANY_IMPORT'
		FROM platform_accounts account
		JOIN companies company ON company.organization_id=account.organization_id
		WHERE account.external_id='legacy-compatible-account'
		  AND company.name='Legacy Company B'`)

	expectExecRejected(t, db, "cross-workspace sync target", `
		INSERT INTO sync_targets(organization_id,company_id,target_type,target_id,operation)
		SELECT workspace.id,company.id,'PLATFORM_ACCOUNT',account.id,'CROSS_WORKSPACE_IMPORT'
		FROM organizations workspace
		JOIN companies company ON company.organization_id=workspace.id AND company.name='Legacy Company A'
		CROSS JOIN platform_accounts account
		WHERE workspace.slug='statzavod'
		  AND account.external_id='second-workspace-account'`)
	expectExecRejected(t, db, "cross-workspace publication", `
		INSERT INTO publications(organization_id,creator_id,platform_account_id,platform,external_id,publication_type,published_at)
		SELECT workspace.id,creator.id,account.id,'YOUTUBE','cross-workspace-publication','VIDEO',now()
		FROM organizations workspace
		JOIN creators creator ON creator.organization_id=workspace.id AND creator.display_name='Legacy Creator'
		CROSS JOIN platform_accounts account
		WHERE workspace.slug='statzavod'
		  AND account.external_id='second-workspace-account'`)
	expectExecRejected(t, db, "cross-workspace company VK account", `
		INSERT INTO company_vk_accounts(organization_id,company_id,platform_account_id)
		SELECT workspace.id,company.id,account.id
		FROM organizations workspace
		JOIN companies company ON company.organization_id=workspace.id AND company.name='Legacy Company A'
		CROSS JOIN platform_accounts account
		WHERE workspace.slug='statzavod'
		  AND account.external_id='second-workspace-account'`)
	expectExecRejected(t, db, "cross-workspace OAuth connection", `
		INSERT INTO oauth_connections(
		  organization_id,platform_account_id,access_token_ciphertext,nonce,access_token_nonce
		)
		SELECT workspace.id,account.id,decode('01','hex'),decode('02','hex'),decode('02','hex')
		FROM organizations workspace CROSS JOIN platform_accounts account
		WHERE workspace.slug='statzavod'
		  AND account.external_id='second-workspace-account'`)
	expectExecRejected(t, db, "cross-workspace creator OAuth state", `
		INSERT INTO oauth_states(organization_id,creator_id,platform,state_hash,nonce,expires_at)
		SELECT workspace.id,creator.id,'INSTAGRAM',decode('03','hex'),decode('04','hex'),now()+interval '1 hour'
		FROM organizations workspace CROSS JOIN creators creator
		WHERE workspace.slug='statzavod'
		  AND creator.display_name='Second Creator'`)
	expectExecRejected(t, db, "cross-workspace company OAuth state", `
		INSERT INTO oauth_states(organization_id,company_vk_account_id,platform,state_hash,nonce,expires_at)
		SELECT workspace.id,vk.id,'VK',decode('05','hex'),decode('06','hex'),now()+interval '1 hour'
		FROM organizations workspace
		CROSS JOIN company_vk_accounts vk
		JOIN companies company ON company.id=vk.company_id
		WHERE workspace.slug='statzavod'
		  AND company.name='Second company'`)
	expectExecRejected(t, db, "cross-workspace OAuth account selection", `
		INSERT INTO oauth_account_selections(
		  organization_id,creator_id,initiated_by,platform,payload_ciphertext,nonce,expires_at
		)
		SELECT workspace.id,creator.id,actor.id,'INSTAGRAM',decode('07','hex'),decode('08','hex'),now()+interval '1 hour'
		FROM organizations workspace
		CROSS JOIN creators creator
		CROSS JOIN users actor
		WHERE workspace.slug='statzavod'
		  AND creator.display_name='Second Creator'
		  AND actor.email='migration-admin@test.local'`)
	expectExecRejected(t, db, "cross-workspace creator history", `
		INSERT INTO creator_history_events(organization_id,creator_id,block)
		SELECT workspace.id,creator.id,'PROFILE'
		FROM organizations workspace CROSS JOIN creators creator
		WHERE workspace.slug='statzavod'
		  AND creator.display_name='Second Creator'`)
	expectExecRejected(t, db, "cross-workspace creator VK assignment", `
		INSERT INTO creator_vk_assignments(creator_id,company_vk_account_id,community_url)
		SELECT creator.id,vk.id,'https://vk.com/cross_workspace'
		FROM creators creator
		CROSS JOIN company_vk_accounts vk
		JOIN companies company ON company.id=vk.company_id
		WHERE creator.display_name='Legacy Creator'
		  AND company.name='Second company'`)
	expectExecRejected(t, db, "cross-workspace content group member", `
		INSERT INTO content_group_members(content_group_id,publication_id)
		SELECT content_group.id,publication.id
		FROM content_groups content_group CROSS JOIN publications publication
		WHERE content_group.name='Main workspace group'
		  AND publication.external_id='second-publication-a'`)
	expectExecRejected(t, db, "cross-workspace content match suggestion", `
		INSERT INTO content_match_suggestions(
		  creator_id,publication_a_id,publication_b_id,score
		)
		SELECT creator.id,publication_a.id,publication_b.id,50
		FROM creators creator
		CROSS JOIN publications publication_a
		CROSS JOIN publications publication_b
		WHERE creator.display_name='Legacy Creator'
		  AND publication_a.external_id='second-publication-a'
		  AND publication_b.external_id='second-publication-b'`)
	expectExecRejected(t, db, "cross-workspace platform deletion request", `
		INSERT INTO platform_data_deletion_requests(
		  confirmation_code,organization_id,platform_account_id,platform,external_id
		)
		SELECT 'cross-workspace-deletion',workspace.id,account.id,'YOUTUBE','second-workspace-account'
		FROM organizations workspace CROSS JOIN platform_accounts account
		WHERE workspace.slug='statzavod'
		  AND account.external_id='second-workspace-account'`)

	_, err = db.Exec(`
		WITH account AS (
		  INSERT INTO platform_accounts(
		    organization_id,company_id,platform,external_id,username,display_name
		  )
		  SELECT company.organization_id,company.id,'TIKTOK','delete-target-account','delete-target','Delete Target'
		  FROM companies company WHERE company.name='Legacy Company A'
		  RETURNING id,organization_id
		), target AS (
		  INSERT INTO sync_targets(organization_id,target_type,target_id,operation)
		  SELECT organization_id,'PLATFORM_ACCOUNT',id,'DELETE_ACCOUNT_TARGET' FROM account
		  RETURNING id
		)
		INSERT INTO sync_runs(target_id,error_message)
		SELECT id,'delete account target run' FROM target`)
	if err != nil {
		t.Fatalf("create platform delete cleanup fixture: %v", err)
	}
	if _, err = db.Exec(`DELETE FROM platform_accounts WHERE external_id='delete-target-account'`); err != nil {
		t.Fatalf("delete platform target resource: %v", err)
	}
	assertScalar(t, db, `SELECT count(*) FROM sync_targets WHERE operation='DELETE_ACCOUNT_TARGET'`, 0)
	assertScalar(t, db, `SELECT count(*) FROM sync_runs WHERE error_message='delete account target run'`, 0)

	_, err = db.Exec(`
		WITH creator AS (
		  INSERT INTO creators(organization_id,company_id,first_name,last_name,display_name)
		  SELECT company.organization_id,company.id,'Delete','Target','Delete Target Creator'
		  FROM companies company WHERE company.name='Legacy Company A'
		  RETURNING id,organization_id
		), target AS (
		  INSERT INTO sync_targets(organization_id,target_type,target_id,operation)
		  SELECT organization_id,'CREATOR',id,'DELETE_CREATOR_TARGET' FROM creator
		  RETURNING id
		)
		INSERT INTO sync_runs(target_id,error_message)
		SELECT id,'delete creator target run' FROM target`)
	if err != nil {
		t.Fatalf("create creator delete cleanup fixture: %v", err)
	}
	if _, err = db.Exec(`DELETE FROM creators WHERE display_name='Delete Target Creator'`); err != nil {
		t.Fatalf("delete creator target resource: %v", err)
	}
	assertScalar(t, db, `SELECT count(*) FROM sync_targets WHERE operation='DELETE_CREATOR_TARGET'`, 0)
	assertScalar(t, db, `SELECT count(*) FROM sync_runs WHERE error_message='delete creator target run'`, 0)

	if _, err = db.Exec(`
		INSERT INTO sessions(user_id,token_hash,expires_at,active_company_id)
		SELECT creator_user.id,decode('0123456789abcdef','hex'),now()+interval '1 hour',company.id
		FROM users creator_user
		CROSS JOIN companies company
		WHERE creator_user.email='migration-creator@test.local'
		  AND company.name='Second company'`); err == nil {
		t.Fatal("session context must reject a company from another workspace")
	}
}

func testConcurrentMembershipChildCleanup(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
		WITH actor AS (
		  INSERT INTO users(email,password_hash,role)
		  VALUES('concurrent-membership@test.local','hash','ADMIN') RETURNING id
		)
		INSERT INTO organization_memberships(organization_id,user_id,membership_role)
		SELECT workspace.id,actor.id,'OWNER'
		FROM organizations workspace CROSS JOIN actor
		WHERE workspace.slug='statzavod'`)
	if err != nil {
		t.Fatalf("create concurrent membership fixture: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin concurrent membership writer: %v", err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
		INSERT INTO audit_logs(organization_id,actor_id,action,entity_type)
		SELECT membership.organization_id,membership.user_id,'CONCURRENT_MEMBERSHIP','TEST'
		FROM organization_memberships membership
		JOIN users user_row ON user_row.id=membership.user_id
		WHERE user_row.email='concurrent-membership@test.local';
		INSERT INTO sessions(user_id,token_hash,expires_at)
		SELECT id,decode('ac01','hex'),now()+interval '1 hour'
		FROM users WHERE email='concurrent-membership@test.local'`)
	if err != nil {
		t.Fatalf("insert concurrent membership children: %v", err)
	}

	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, deleteErr := db.Exec(`
			DELETE FROM organization_memberships membership
			USING users user_row
			WHERE user_row.id=membership.user_id
			  AND user_row.email='concurrent-membership@test.local'`)
		done <- deleteErr
	}()
	<-started
	select {
	case deleteErr := <-done:
		tx.Rollback()
		t.Fatalf("membership delete did not serialize with child insert: %v", deleteErr)
	case <-time.After(150 * time.Millisecond):
	}
	if err = tx.Commit(); err != nil {
		t.Fatalf("commit concurrent membership children: %v", err)
	}
	select {
	case deleteErr := <-done:
		if deleteErr != nil {
			t.Fatalf("delete membership after child commit: %v", deleteErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("membership delete remained blocked after child commit")
	}
	assertScalar(t, db, `
		SELECT count(*) FROM audit_logs
		WHERE action='CONCURRENT_MEMBERSHIP' AND actor_id IS NULL`, 1)
	assertScalar(t, db, `
		SELECT count(*) FROM sessions
		WHERE token_hash=decode('ac01','hex')
		  AND revoked_at IS NOT NULL
		  AND active_company_id IS NULL
		  AND active_creator_id IS NULL`, 1)
}

func testConcurrentManagerAssignmentCleanup(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin concurrent assignment writer: %v", err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
		INSERT INTO sessions(user_id,token_hash,expires_at,active_company_id)
		SELECT user_row.id,decode('ac02','hex'),now()+interval '1 hour',company.id
		FROM users user_row CROSS JOIN companies company
		WHERE user_row.email='migration-analyst@test.local'
		  AND company.name='Legacy Company B'`)
	if err != nil {
		t.Fatalf("insert concurrent manager session: %v", err)
	}

	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, deleteErr := db.Exec(`
			DELETE FROM manager_company_assignments assignment
			USING users user_row, companies company
			WHERE assignment.manager_user_id=user_row.id
			  AND assignment.company_id=company.id
			  AND user_row.email='migration-analyst@test.local'
			  AND company.name='Legacy Company B'`)
		done <- deleteErr
	}()
	<-started
	select {
	case deleteErr := <-done:
		tx.Rollback()
		t.Fatalf("assignment delete did not serialize with session insert: %v", deleteErr)
	case <-time.After(150 * time.Millisecond):
	}
	if err = tx.Commit(); err != nil {
		t.Fatalf("commit concurrent manager session: %v", err)
	}
	select {
	case deleteErr := <-done:
		if deleteErr != nil {
			t.Fatalf("delete assignment after session commit: %v", deleteErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("assignment delete remained blocked after session commit")
	}
	assertScalar(t, db, `
		SELECT count(*) FROM sessions
		WHERE token_hash=decode('ac02','hex')
		  AND revoked_at IS NOT NULL
		  AND active_company_id IS NULL
		  AND active_creator_id IS NULL`, 1)
}

func testCompanyAndWorkspaceCascades(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO audit_logs(organization_id,actor_id,action,entity_type)
		SELECT membership.organization_id,user_row.id,'VIEW','CREATOR'
		FROM users user_row
		JOIN organization_memberships membership ON membership.user_id=user_row.id
		WHERE user_row.email='migration-analyst@test.local'`)
	if err != nil {
		t.Fatalf("create historical actor fixture: %v", err)
	}
	if _, err = db.Exec(`DELETE FROM users WHERE email='migration-analyst@test.local'`); err != nil {
		t.Fatalf("delete historical actor: %v", err)
	}
	assertScalar(t, db, `SELECT count(*) FROM audit_logs WHERE action='VIEW' AND actor_id IS NULL`, 1)

	if _, err = db.Exec(`
		DELETE FROM companies
		WHERE organization_id=(SELECT id FROM organizations WHERE slug='statzavod')`); err != nil {
		t.Fatalf("delete test company: %v", err)
	}
	assertScalar(t, db, `SELECT count(*) FROM creators WHERE organization_id=(SELECT id FROM organizations WHERE slug='statzavod')`, 0)
	assertScalar(t, db, `SELECT count(*) FROM platform_accounts WHERE organization_id=(SELECT id FROM organizations WHERE slug='statzavod')`, 0)
	assertScalar(t, db, `SELECT count(*) FROM sync_targets WHERE organization_id=(SELECT id FROM organizations WHERE slug='statzavod')`, 0)
	assertScalar(t, db, `SELECT count(*) FROM manager_company_assignments WHERE organization_id=(SELECT id FROM organizations WHERE slug='statzavod')`, 0)

	if _, err = db.Exec(`DELETE FROM organizations WHERE slug='statzavod'`); err != nil {
		t.Fatalf("delete test workspace: %v", err)
	}
	assertScalar(t, db, `SELECT count(*) FROM audit_logs`, 0)
	assertScalar(t, db, `SELECT count(*) FROM organization_memberships WHERE organization_id NOT IN (SELECT id FROM organizations)`, 0)
}

func seedLegacyWorkspace(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
		WITH workspace AS (SELECT id FROM organizations WHERE slug='statzavod'),
		new_users AS (
			INSERT INTO users(email,password_hash,role) VALUES
				('migration-admin@test.local','hash','ADMIN'),
				('migration-analyst@test.local','hash','ANALYST'),
				('migration-viewer@test.local','hash','VIEWER')
			RETURNING id, role
		)
		INSERT INTO organization_memberships(organization_id,user_id,role)
		SELECT workspace.id,new_users.id,new_users.role FROM workspace CROSS JOIN new_users;

		INSERT INTO sessions(user_id,token_hash,expires_at)
		SELECT user_row.id,
		       CASE user_row.role
		         WHEN 'ANALYST' THEN decode('b101','hex')
		         ELSE decode('b102','hex')
		       END,
		       now()+interval '1 hour'
		FROM users user_row
		WHERE user_row.email IN ('migration-analyst@test.local','migration-viewer@test.local');

		WITH workspace AS (SELECT id FROM organizations WHERE slug='statzavod')
		INSERT INTO companies(organization_id,name)
		SELECT id,'Legacy Company A' FROM workspace
		UNION ALL
		SELECT id,'Legacy Company B' FROM workspace;

		INSERT INTO creators(organization_id,company_id,first_name,last_name,display_name)
		SELECT company.organization_id,company.id,'Legacy','Creator','Legacy Creator'
		FROM companies company WHERE company.name='Legacy Company A';
		INSERT INTO creators(organization_id,company_id,first_name,last_name,display_name)
		SELECT company.organization_id,company.id,'Historical','Creator','Historical Creator'
		FROM companies company WHERE company.name='Legacy Company B';

		WITH workspace AS (SELECT id FROM organizations WHERE slug='statzavod'),
		account AS (
			INSERT INTO platform_accounts(organization_id,platform,external_id,username,display_name)
			SELECT id,'TIKTOK','migration-account','migration','Migration Account' FROM workspace
			RETURNING id,organization_id
		)
		INSERT INTO sync_targets(organization_id,target_type,target_id,operation)
		SELECT organization_id,'PLATFORM_ACCOUNT',id,'TIKTOK_IMPORT' FROM account;

		INSERT INTO creator_account_assignments(creator_id,platform_account_id)
		SELECT creator.id,account.id
		FROM creators creator
		CROSS JOIN platform_accounts account
		WHERE creator.display_name='Legacy Creator'
		  AND account.external_id='migration-account';

		INSERT INTO company_vk_accounts(organization_id,company_id,platform_account_id)
		SELECT company.organization_id,company.id,account.id
		FROM companies company
		CROSS JOIN platform_accounts account
		WHERE company.name='Legacy Company B'
		  AND account.external_id='migration-account';

		WITH workspace AS (SELECT id FROM organizations WHERE slug='statzavod'),
		reassigned_account AS (
			INSERT INTO platform_accounts(organization_id,platform,external_id,username,display_name)
			SELECT id,'INSTAGRAM','migration-reassigned-account','reassigned','Reassigned Account' FROM workspace
			RETURNING id
		)
		INSERT INTO creator_account_assignments(creator_id,platform_account_id,valid_from,valid_to)
		SELECT creator.id,reassigned_account.id,now()-interval '2 days',now()-interval '1 day'
		FROM creators creator CROSS JOIN reassigned_account
		WHERE creator.display_name='Historical Creator';

		INSERT INTO creator_account_assignments(creator_id,platform_account_id)
		SELECT creator.id,account.id
		FROM creators creator CROSS JOIN platform_accounts account
		WHERE creator.display_name='Legacy Creator'
		  AND account.external_id='migration-reassigned-account';

		WITH workspace AS (SELECT id FROM organizations WHERE slug='statzavod'),
		orphan_account AS (
			INSERT INTO platform_accounts(organization_id,platform,external_id,username,display_name)
			SELECT id,'YOUTUBE','migration-orphan-account','orphan','Orphan Account' FROM workspace
			RETURNING id,organization_id
		)
		INSERT INTO sync_targets(organization_id,target_type,target_id,operation)
		SELECT organization_id,'PLATFORM_ACCOUNT',id,'YOUTUBE_IMPORT' FROM orphan_account;

		WITH workspace AS (SELECT id FROM organizations WHERE slug='statzavod'),
		dangling_target AS (
			INSERT INTO sync_targets(organization_id,target_type,target_id,operation)
			SELECT id,'PLATFORM_ACCOUNT',uuidv7(),'DANGLING_IMPORT' FROM workspace
			RETURNING id
		)
		INSERT INTO sync_runs(target_id,error_message)
		SELECT id,'dangling migration fixture' FROM dangling_target;
	`)
	if err != nil {
		t.Fatalf("seed legacy migration fixture: %v", err)
	}
}

func assertScalar(t *testing.T, db *sql.DB, query string, expected int) {
	t.Helper()
	var actual int
	if err := db.QueryRow(query).Scan(&actual); err != nil {
		t.Fatalf("query scalar %q: %v", query, err)
	}
	if actual != expected {
		t.Fatalf("query %q: expected %d, got %d", query, expected, actual)
	}
}

func expectExecRejected(t *testing.T, db *sql.DB, name, query string) {
	t.Helper()
	if _, err := db.Exec(query); err == nil {
		t.Fatalf("%s must be rejected", name)
	}
}
