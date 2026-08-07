package migrations

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestCreatorDeletionMigrationContract(t *testing.T) {
	up := migrationUp(t, "00017_creator_deletion.sql")
	normalized := normalizeSQL(up)

	creatorOwnedForeignKeys := []struct {
		table      string
		constraint string
	}{
		{table: "creator_contacts", constraint: "creator_contacts_creator_id_fkey"},
		{table: "creator_account_assignments", constraint: "creator_account_assignments_creator_id_fkey"},
		{table: "publications", constraint: "publications_creator_id_fkey"},
		{table: "content_groups", constraint: "content_groups_creator_id_fkey"},
		{table: "content_match_suggestions", constraint: "content_match_suggestions_creator_id_fkey"},
		{table: "oauth_states", constraint: "oauth_states_creator_id_fkey"},
	}

	for _, foreignKey := range creatorOwnedForeignKeys {
		t.Run(foreignKey.table+"_cascades", func(t *testing.T) {
			pattern := `alter table ` + regexp.QuoteMeta(foreignKey.table) +
				` [^;]*add constraint ` + regexp.QuoteMeta(foreignKey.constraint) +
				` foreign key \(creator_id\) references creators\(id\) on delete cascade;`
			if !regexp.MustCompile(pattern).MatchString(normalized) {
				t.Fatalf("migration must cascade %s.creator_id when its creator is deleted", foreignKey.table)
			}
		})
	}

	forbiddenPlatformAccountMutations := regexp.MustCompile(`(?:alter table|delete from|drop table) platform_accounts\b`)
	if forbiddenPlatformAccountMutations.MatchString(normalized) {
		t.Fatal("creator deletion migration must preserve platform_accounts; only assignments belong to a creator")
	}
}

func TestCreatorOwnershipMigrationContract(t *testing.T) {
	normalized := normalizeSQL(migrationUp(t, "00017_creator_deletion.sql"))

	createdByForeignKey := regexp.MustCompile(`alter table creators [^;]*add column created_by uuid references users\(id\) on delete set null;`)
	if !createdByForeignKey.MatchString(normalized) {
		t.Fatal("creators.created_by must reference users with ON DELETE SET NULL")
	}

	backfillRequirements := []string{
		"update creators as creator set created_by = creator_create.actor_id",
		"from ( select distinct on (organization_id, entity_id)",
		"from audit_logs where action = 'create' and entity_type = 'creator'",
		"and actor_id is not null",
		"where creator.id = creator_create.entity_id and creator.organization_id = creator_create.organization_id",
	}
	for _, requirement := range backfillRequirements {
		if !strings.Contains(normalized, requirement) {
			t.Fatalf("creator ownership backfill is missing contract fragment %q", requirement)
		}
	}
}

func TestExistingCreatorOwnedForeignKeysCascade(t *testing.T) {
	tests := []struct {
		name       string
		migration  string
		table      string
		column     string
		references string
	}{
		{name: "credentials", migration: "00006_creator_profiles.sql", table: "creator_credentials", column: "creator_id", references: "creators"},
		{name: "history events", migration: "00012_creator_history.sql", table: "creator_history_events", column: "creator_id", references: "creators"},
		{name: "history changes", migration: "00012_creator_history.sql", table: "creator_history_changes", column: "event_id", references: "creator_history_events"},
		{name: "VK assignment", migration: "00013_company_vk_accounts.sql", table: "creator_vk_assignments", column: "creator_id", references: "creators"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized := normalizeSQL(migrationUp(t, test.migration))
			pattern := `create table ` + regexp.QuoteMeta(test.table) + ` [^;]*` +
				regexp.QuoteMeta(test.column) + ` [^,;]*references ` + regexp.QuoteMeta(test.references) +
				`\(id\) on delete cascade`
			if !regexp.MustCompile(pattern).MatchString(normalized) {
				t.Fatalf("%s.%s must cascade from %s", test.table, test.column, test.references)
			}
		})
	}
}

func TestPlatformAccountsHaveNoCreatorOwnershipForeignKey(t *testing.T) {
	filenames, err := filepath.Glob("0*.sql")
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}

	var schema strings.Builder
	for _, filename := range filenames {
		schema.WriteString(" ")
		schema.WriteString(normalizeSQL(migrationUp(t, filename)))
	}
	normalized := schema.String()
	foreignKeyToCreator := regexp.MustCompile(`(?:create table|alter table) platform_accounts [^;]*references creators\(id\)`)
	if foreignKeyToCreator.MatchString(normalized) {
		t.Fatal("platform_accounts must not be owned by creators or cascade from creator deletion")
	}
}

func migrationUp(t *testing.T, filename string) string {
	t.Helper()
	contents, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read migration %s: %v", filename, err)
	}
	parts := strings.SplitN(string(contents), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatalf("migration %s has no goose Down section", filename)
	}
	return parts[0]
}

func normalizeSQL(sql string) string {
	return strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(strings.ToLower(sql), " "))
}
