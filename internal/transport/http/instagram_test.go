package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type recordingInstagramDeletionTx struct {
	queries              []string
	platformRowsAffected int64
}

func (tx *recordingInstagramDeletionTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	tx.queries = append(tx.queries, strings.Join(strings.Fields(sql), " "))
	if strings.Contains(sql, "DELETE FROM platform_accounts") {
		return pgconn.NewCommandTag("DELETE " + fmt.Sprint(tx.platformRowsAffected)), nil
	}
	return pgconn.NewCommandTag("DELETE 1"), nil
}

func TestParseInstagramTimestamp(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Time
	}{
		{
			name:  "RFC3339 offset",
			value: "2026-07-30T18:32:10+00:00",
			want:  time.Date(2026, 7, 30, 18, 32, 10, 0, time.UTC),
		},
		{
			name:  "Meta compact offset",
			value: "2026-07-30T18:32:10+0000",
			want:  time.Date(2026, 7, 30, 18, 32, 10, 0, time.UTC),
		},
		{
			name:  "Meta compact offset with fractional seconds",
			value: "2026-07-30T22:32:10.125+0400",
			want:  time.Date(2026, 7, 30, 18, 32, 10, 125000000, time.UTC),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseInstagramTimestamp(test.value)
			if err != nil {
				t.Fatalf("parseInstagramTimestamp(%q): %v", test.value, err)
			}
			if !got.Equal(test.want) {
				t.Fatalf("parseInstagramTimestamp(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestParseInstagramTimestampRejectsInvalidValue(t *testing.T) {
	if _, err := parseInstagramTimestamp("not-a-date"); err == nil {
		t.Fatal("parseInstagramTimestamp accepted an invalid value")
	}
}

func TestFetchInstagramMediaInsightsBatchesMetrics(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/media-1/insights" {
			t.Fatalf("request path = %q", r.URL.Path)
		}
		metrics := strings.Split(r.URL.Query().Get("metric"), ",")
		if len(metrics) != 7 {
			t.Fatalf("metric count = %d, want 7", len(metrics))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"name":"views","total_value":{"value":120}},{"name":"reach","total_value":{"value":80}},{"name":"shares","total_value":{"value":5}}]}`))
	}))
	defer server.Close()

	provider := newProviderClient("Instagram")
	provider.client = server.Client()
	app := &Server{}
	app.config.InstagramAPIBase = server.URL
	got, err := app.fetchInstagramMediaInsights(context.Background(), provider, "token", instagramMedia{
		ID:            "media-1",
		LikeCount:     11,
		CommentsCount: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1", requests)
	}
	if got.Views == nil || *got.Views != 120 || got.Reach == nil || *got.Reach != 80 || got.Shares == nil || *got.Shares != 5 {
		t.Fatalf("unexpected insight metrics: %+v", got)
	}
	if got.Likes == nil || *got.Likes != 11 || got.Comments == nil || *got.Comments != 3 {
		t.Fatalf("media counters were not preserved: %+v", got)
	}
}

func TestDeleteInstagramAccountDataOrdersSyncCleanupBeforeTarget(t *testing.T) {
	tx := &recordingInstagramDeletionTx{platformRowsAffected: 1}
	if err := deleteInstagramAccountDataInTx(t.Context(), tx, "account-id", "organization-id"); err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{
		"DELETE FROM sync_runs",
		"DELETE FROM sync_targets",
		"DELETE FROM creator_account_assignments",
		"DELETE FROM publications",
		"DELETE FROM platform_accounts",
		"INSERT INTO audit_logs",
	}
	if len(tx.queries) != len(wantOrder) {
		t.Fatalf("query count = %d, want %d: %#v", len(tx.queries), len(wantOrder), tx.queries)
	}
	for index, fragment := range wantOrder {
		if !strings.Contains(tx.queries[index], fragment) {
			t.Fatalf("query %d = %q, want fragment %q", index, tx.queries[index], fragment)
		}
	}
	if !strings.Contains(tx.queries[2], "account.organization_id=$2") || !strings.Contains(tx.queries[3], "organization_id=$2") || !strings.Contains(tx.queries[4], "organization_id=$2") {
		t.Fatalf("tenant filters are missing from deletion queries: %#v", tx.queries)
	}
}

func TestDeleteInstagramAccountDataStopsBeforeAuditWhenAccountIsAbsent(t *testing.T) {
	tx := &recordingInstagramDeletionTx{platformRowsAffected: 0}
	err := deleteInstagramAccountDataInTx(t.Context(), tx, "account-id", "organization-id")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("error = %v, want pgx.ErrNoRows", err)
	}
	if len(tx.queries) != 5 {
		t.Fatalf("query count = %d, want 5 without audit insert: %#v", len(tx.queries), tx.queries)
	}
}

func TestDeleteInstagramAccountCopiesDeletesEveryTenantCopy(t *testing.T) {
	accounts := []instagramAccountCopy{
		{AccountID: "account-a", OrganizationID: "organization-a"},
		{AccountID: "account-b", OrganizationID: "organization-b"},
		{AccountID: "account-c", OrganizationID: "organization-c"},
	}
	deleted := make([]instagramAccountCopy, 0, len(accounts))
	err := deleteInstagramAccountCopies(t.Context(), accounts, func(_ context.Context, accountID, organizationID string) error {
		deleted = append(deleted, instagramAccountCopy{AccountID: accountID, OrganizationID: organizationID})
		if accountID == "account-b" {
			// A concurrent retry may observe one copy as already gone. The
			// remaining tenant copies must still be deleted.
			return pgx.ErrNoRows
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(deleted, accounts) {
		t.Fatalf("deleted copies = %#v, want %#v", deleted, accounts)
	}
}

func TestInstagramDeletionRequestOwnerAvoidsArbitraryTenantForMultipleCopies(t *testing.T) {
	accountID, organizationID := instagramDeletionRequestOwner(nil)
	if accountID != nil || organizationID != nil {
		t.Fatalf("empty owner = %#v, %#v; want nil, nil", accountID, organizationID)
	}

	accountID, organizationID = instagramDeletionRequestOwner([]instagramAccountCopy{{AccountID: "account-a", OrganizationID: "organization-a"}})
	if accountID != "account-a" || organizationID != "organization-a" {
		t.Fatalf("single owner = %#v, %#v", accountID, organizationID)
	}

	accountID, organizationID = instagramDeletionRequestOwner([]instagramAccountCopy{
		{AccountID: "account-a", OrganizationID: "organization-a"},
		{AccountID: "account-b", OrganizationID: "organization-b"},
	})
	if accountID != nil || organizationID != nil {
		t.Fatalf("multi-tenant owner = %#v, %#v; want nil, nil", accountID, organizationID)
	}
}
