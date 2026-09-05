package migrations

import (
	"strings"
	"testing"
)

func TestPlatformDeletionRequestRetentionMigration(t *testing.T) {
	normalized := normalizeSQL(migrationUp(t, "00023_platform_deletion_request_retention.sql"))
	for _, required := range []string{
		"delete from platform_data_deletion_requests where organization_id is null",
		"references organizations(id) on delete cascade",
		"create table platform_data_deletion_request_workspaces",
		"before delete on organizations",
		"delete from platform_data_deletion_requests request",
		"before delete on platform_accounts",
		"external_id=''",
		"error_message=null",
	} {
		if !strings.Contains(normalized, required) {
			t.Errorf("retention migration is missing %q", required)
		}
	}
}
