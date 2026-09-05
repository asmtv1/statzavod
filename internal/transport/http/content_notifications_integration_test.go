package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This suite intentionally uses TEST_DATABASE_URL (the same disposable,
// migrated database contract as the publishing worker tests). Without it the
// test is skipped; when it is supplied, fixture or schema failures are fatal.
func TestContentNotificationsPostgresIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable database migrated through 00026")
	}
	pool, err := pgxpool.New(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var ready bool
	if err = pool.QueryRow(t.Context(), `SELECT to_regclass('public.content_notifications') IS NOT NULL AND to_regclass('public.manager_company_permissions') IS NOT NULL`).Scan(&ready); err != nil || !ready {
		t.Fatalf("TEST_DATABASE_URL must be migrated through 00026: ready=%v err=%v", ready, err)
	}
	suffix := fmt.Sprintf("notifications-%d", time.Now().UnixNano())
	var org, companyA, companyB, owner, manager, otherManager, creatorUser, creator, item, revision string
	mustScanID(t, pool, `INSERT INTO organizations(name,slug) VALUES($1,$2) RETURNING id`, &org, "Notification test", suffix)
	mustScanID(t, pool, `INSERT INTO companies(organization_id,name) VALUES($1,'A') RETURNING id`, &companyA, org)
	mustScanID(t, pool, `INSERT INTO companies(organization_id,name) VALUES($1,'B') RETURNING id`, &companyB, org)
	insertUser := func(email, membershipRole string, out *string) {
		legacyRole := map[string]string{"OWNER": "ADMIN", "MANAGER": "VIEWER", "CREATOR": "VIEWER"}[membershipRole]
		mustScanID(t, pool, `INSERT INTO users(email,password_hash,role,status) VALUES($1,'test',$2,'ACTIVE') RETURNING id`, out, email, legacyRole)
		if _, e := pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role,membership_role) VALUES($1,$2,$3,$4::membership_role)`, org, *out, legacyRole, membershipRole); e != nil {
			t.Fatal(e)
		}
	}
	insertUser("owner-"+suffix, "OWNER", &owner)
	insertUser("manager-"+suffix, "MANAGER", &manager)
	insertUser("other-"+suffix, "MANAGER", &otherManager)
	insertUser("creator-"+suffix, "CREATOR", &creatorUser)
	var assignment string
	mustScanID(t, pool, `INSERT INTO manager_company_assignments(organization_id,manager_user_id,company_id) VALUES($1,$2,$3) RETURNING id`, &assignment, org, manager, companyA)
	if _, err = pool.Exec(t.Context(), `INSERT INTO manager_company_permissions(assignment_id,permission) VALUES($1,'CONTENT_APPROVE'),($1,'SOCIAL_CONNECT')`, assignment); err != nil {
		t.Fatal(err)
	}
	insertUserCreator := `INSERT INTO creators(organization_id,company_id,login_user_id,created_by,first_name,last_name,display_name) VALUES($1,$2,$3,$4,'Test','Creator','Test Creator') RETURNING id`
	mustScanID(t, pool, insertUserCreator, &creator, org, companyA, creatorUser, owner)
	mustScanID(t, pool, `INSERT INTO content_items(organization_id,company_id,creator_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, &item, org, companyA, creator, owner)
	mustScanID(t, pool, `INSERT INTO content_revisions(content_item_id,organization_id,revision,created_by) VALUES($1,$2,1,$3) RETURNING id`, &revision, item, org, owner)
	s := &Server{pool: pool}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, org) })
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.contentNotificationRecipients(t.Context(), tx, org, companyA, item, "APPROVAL_REQUESTED", "approval:test"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM content_notifications WHERE organization_id=$1 AND content_item_id=$2`, org, item).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("approval recipients=%d, want owner and eligible manager", n)
	}
	var unread int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM content_notifications WHERE organization_id=$1 AND recipient_id=$2 AND read_at IS NULL`, org, owner).Scan(&unread); err != nil || unread != 1 {
		t.Fatalf("owner unread=%d err=%v", unread, err)
	}
	var changed int64
	if tag, e := pool.Exec(t.Context(), `UPDATE content_notifications SET read_at=now() WHERE organization_id=$1 AND recipient_id=$2`, org, otherManager); e != nil {
		t.Fatal(e)
	} else {
		changed = tag.RowsAffected()
	}
	if changed != 0 {
		t.Fatalf("foreign recipient update changed %d rows", changed)
	}
	foreignReq := httptest.NewRequest(http.MethodPost, "/notifications/00000000-0000-0000-0000-000000000001/read", nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("notificationID", "00000000-0000-0000-0000-000000000001")
	foreignReq = foreignReq.WithContext(context.WithValue(context.WithValue(foreignReq.Context(), principalKey, principal{ID: otherManager, OrganizationID: org}), chi.RouteCtxKey, rc))
	foreignRec := httptest.NewRecorder()
	s.markContentNotificationRead(foreignRec, foreignReq)
	if foreignRec.Code != http.StatusNotFound {
		t.Fatalf("foreign mark-read status=%d, want 404", foreignRec.Code)
	}
	var cursorCount int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM content_notifications n WHERE n.organization_id=$1 AND (n.created_at,n.id) < (now()+interval '1 second',n.id)`, org).Scan(&cursorCount); err != nil || cursorCount != 2 {
		t.Fatalf("keyset cursor count=%d err=%v", cursorCount, err)
	}
	// Repeating the same event key is idempotent, while an archived company
	// suppresses future delivery.
	tx, err = pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.contentNotificationRecipients(t.Context(), tx, org, companyA, item, "APPROVAL_REQUESTED", "approval:test"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM content_notifications WHERE organization_id=$1 AND content_item_id=$2`, org, item).Scan(&n); err != nil || n != 2 {
		t.Fatalf("dedupe count=%d err=%v", n, err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE companies SET archived_at=now() WHERE id=$1`, companyA); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.contentNotificationRecipients(t.Context(), tx, org, companyA, item, "PUBLISH_FAILED", "failure:test"); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit(t.Context())
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM content_notifications WHERE organization_id=$1 AND content_item_id=$2`, org, item).Scan(&n); err != nil || n != 2 {
		t.Fatalf("archived suppression count=%d err=%v", n, err)
	}
}
