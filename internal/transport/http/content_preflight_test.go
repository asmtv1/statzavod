package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/statzavod/statzavod/internal/config"
)

func TestPublishingReadinessRequiresPublishScopesPerTarget(t *testing.T) {
	tests := []struct {
		platform string
		scopes   []string
		missing  string
	}{
		{"INSTAGRAM", []string{"instagram_business_basic"}, "instagram_content_publish"},
		{"TIKTOK", []string{"video.list"}, "video.publish"},
		{"YOUTUBE", []string{"https://www.googleapis.com/auth/youtube.readonly"}, "https://www.googleapis.com/auth/youtube.upload"},
		{"VK", []string{"wall", "groups"}, "video"},
	}
	for _, test := range tests {
		t.Run(test.platform, func(t *testing.T) {
			got := publishingReadiness(test.platform, "account", "ACTIVE", "ACTIVE", test.scopes)
			if got.Compatible || !got.Reauth || got.Reconnect != "RECONNECT_PLATFORM_ACCOUNT" || len(got.MissingScopes) != 1 || got.MissingScopes[0] != test.missing {
				t.Fatalf("unexpected readiness: %#v", got)
			}
		})
	}
}

func TestInstagramPublishingReadinessUsesLoginModeAndProfessionalAccount(t *testing.T) {
	login := instagramPublishingReadiness("BUSINESS", "", "ACTIVE", "ACTIVE", []string{"instagram_business_content_publish"})
	if !login.Compatible {
		t.Fatalf("Instagram Login was rejected: %#v", login)
	}
	facebook := instagramPublishingReadiness("PROFESSIONAL", "FACEBOOK", "ACTIVE", "ACTIVE", []string{"instagram_content_publish"})
	if !facebook.Compatible {
		t.Fatalf("Facebook Login was rejected: %#v", facebook)
	}
	personal := instagramPublishingReadiness("PERSONAL", "", "ACTIVE", "ACTIVE", []string{"instagram_business_content_publish"})
	if personal.Compatible || personal.Reauth {
		t.Fatalf("personal account was publishable: %#v", personal)
	}
}

func TestPublishingReadinessRejectsRevokedConnection(t *testing.T) {
	got := publishingReadiness("YOUTUBE", "account", "ACTIVE", "REVOKED", []string{"https://www.googleapis.com/auth/youtube.upload"})
	if got.Compatible || !got.Reauth || got.Reconnect != "RECONNECT_PLATFORM_ACCOUNT" {
		t.Fatalf("revoked connection became selectable: %#v", got)
	}
}

func TestPublishingReadinessAcceptsActivePublishingConnection(t *testing.T) {
	got := publishingReadiness("TIKTOK", "account", "ACTIVE", "ACTIVE", []string{"video.publish"})
	if !got.Compatible || got.Reauth || got.Reconnect != "" || len(got.MissingScopes) != 0 {
		t.Fatalf("active publishing connection was not ready: %#v", got)
	}
}

func TestTransactionalPublishPreflightPostgresIntegration(t *testing.T) {
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
	if err = pool.QueryRow(t.Context(), `SELECT to_regclass('public.content_publish_jobs') IS NOT NULL AND to_regclass('public.media_assets') IS NOT NULL`).Scan(&ready); err != nil || !ready {
		t.Fatalf("TEST_DATABASE_URL must be migrated through 00026: ready=%v err=%v", ready, err)
	}

	tests := []struct {
		name             string
		action           string
		body             string
		accountStatus    string
		connectionStatus string
		scopes           []string
		enabled          bool
		assignment       string
	}{
		{name: "publish empty scopes", action: "publish", body: `{}`, accountStatus: "ACTIVE", connectionStatus: "ACTIVE", scopes: []string{}, enabled: true, assignment: "active"},
		{name: "schedule revoked connection", action: "schedule", body: `{"scheduledAt":"2035-01-01T12:00:00Z"}`, accountStatus: "ACTIVE", connectionStatus: "REVOKED", scopes: []string{"https://www.googleapis.com/auth/youtube.upload"}, enabled: true, assignment: "active"},
		{name: "retry reauth required", action: "retry", accountStatus: "REAUTH_REQUIRED", connectionStatus: "REAUTH_REQUIRED", scopes: []string{"https://www.googleapis.com/auth/youtube.upload"}, enabled: true, assignment: "active"},
		{name: "publish disabled platform", action: "publish", body: `{}`, accountStatus: "ACTIVE", connectionStatus: "ACTIVE", scopes: []string{"https://www.googleapis.com/auth/youtube.upload"}, enabled: false, assignment: "active"},
		{name: "schedule expired assignment", action: "schedule", body: `{"scheduledAt":"2035-01-01T12:00:00Z"}`, accountStatus: "ACTIVE", connectionStatus: "ACTIVE", scopes: []string{"https://www.googleapis.com/auth/youtube.upload"}, enabled: true, assignment: "expired"},
		{name: "retry reassigned account", action: "retry", accountStatus: "ACTIVE", connectionStatus: "ACTIVE", scopes: []string{"https://www.googleapis.com/auth/youtube.upload"}, enabled: true, assignment: "reassigned"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			suffix := strings.NewReplacer(" ", "-", ".", "-").Replace(test.name) + "-" + time.Now().UTC().Format("20060102150405.000000000")
			var organizationID, companyID, userID, creatorID, otherCreatorID, accountID, assignmentID, mediaID, itemID, revisionID, targetID string
			mustScanID(t, pool, `INSERT INTO organizations(name,slug) VALUES('preflight',$1) RETURNING id`, &organizationID, "preflight-"+suffix)
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, organizationID)
			})
			mustScanID(t, pool, `INSERT INTO companies(organization_id,name) VALUES($1,'Preflight') RETURNING id`, &companyID, organizationID)
			mustScanID(t, pool, `INSERT INTO users(email,password_hash,role,status) VALUES($1,'x','ADMIN','ACTIVE') RETURNING id`, &userID, "preflight-"+suffix+"@test.local")
			if _, err = pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role,membership_role) VALUES($1,$2,'ADMIN','OWNER')`, organizationID, userID); err != nil {
				t.Fatal(err)
			}
			mustScanID(t, pool, `INSERT INTO creators(organization_id,company_id,created_by,first_name,last_name,display_name) VALUES($1,$2,$3,'Target','Creator','Target Creator') RETURNING id`, &creatorID, organizationID, companyID, userID)
			mustScanID(t, pool, `INSERT INTO creators(organization_id,company_id,created_by,first_name,last_name,display_name) VALUES($1,$2,$3,'Other','Creator','Other Creator') RETURNING id`, &otherCreatorID, organizationID, companyID, userID)
			mustScanID(t, pool, `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status) VALUES($1,$2,'YOUTUBE',$3,'preflight','Preflight',$4) RETURNING id`, &accountID, organizationID, companyID, "youtube-"+suffix, test.accountStatus)
			mustScanID(t, pool, `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2) RETURNING id`, &assignmentID, creatorID, accountID)
			if _, err = pool.Exec(t.Context(), `INSERT INTO oauth_connections(organization_id,platform_account_id,access_token_ciphertext,nonce,access_token_nonce,status,scopes) VALUES($1,$2,decode('01','hex'),decode('02','hex'),decode('02','hex'),$3,$4)`, organizationID, accountID, test.connectionStatus, test.scopes); err != nil {
				t.Fatal(err)
			}
			mustScanID(t, pool, `INSERT INTO media_assets(organization_id,company_id,creator_id,status,object_key,content_sha256,original_filename,width,height) VALUES($1,$2,$3,'READY',$4,decode('aa','hex'),'preflight.mp4',9,16) RETURNING id`, &mediaID, organizationID, companyID, creatorID, "preflight/"+suffix)
			mustScanID(t, pool, `INSERT INTO content_items(organization_id,company_id,creator_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, &itemID, organizationID, companyID, creatorID, userID)
			revisionState := "DRAFT"
			targetState := "READY"
			if test.action == "retry" {
				revisionState = "FAILED"
				targetState = "FAILED"
				if test.accountStatus == "REAUTH_REQUIRED" {
					targetState = "WAITING_FOR_REAUTH"
				}
			}
			mustScanID(t, pool, `INSERT INTO content_revisions(content_item_id,organization_id,revision,description,status,created_by) VALUES($1,$2,1,'preflight',$3,$4) RETURNING id`, &revisionID, itemID, organizationID, revisionState, userID)
			mustScanID(t, pool, `INSERT INTO content_publish_targets(content_revision_id,organization_id,company_id,creator_id,platform_account_id,platform,media_asset_id,status) VALUES($1,$2,$3,$4,$5,'YOUTUBE',$6,$7) RETURNING id`, &targetID, revisionID, organizationID, companyID, creatorID, accountID, mediaID, targetState)
			if test.assignment == "expired" || test.assignment == "reassigned" {
				if _, err = pool.Exec(t.Context(), `UPDATE creator_account_assignments SET valid_to=GREATEST(valid_from+interval '1 microsecond',now()) WHERE id=$1`, assignmentID); err != nil {
					t.Fatal(err)
				}
			}
			if test.assignment == "reassigned" {
				if _, err = pool.Exec(t.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2)`, otherCreatorID, accountID); err != nil {
					t.Fatal(err)
				}
			}

			body := test.body
			if test.action == "retry" {
				body = `{"targetIds":["` + targetID + `"]}`
			}
			request := httptest.NewRequest(http.MethodPost, "/content-items/"+itemID+"/"+test.action, strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "preflight-"+suffix)
			route := chi.NewRouteContext()
			route.URLParams.Add("id", itemID)
			principal := principal{ID: userID, OrganizationID: organizationID, Role: roleOwner, ActiveCompanyID: &companyID, Companies: map[string]companyAccess{companyID: testCompany(companyID, "CONTENT_PUBLISH", "CONTENT_VIEW")}}
			request = request.WithContext(context.WithValue(context.WithValue(request.Context(), chi.RouteCtxKey, route), principalKey, principal))
			server := &Server{pool: pool, now: time.Now, config: config.Config{ContentPublishingEnabled: true, ContentYouTubeEnabled: test.enabled}}
			response := httptest.NewRecorder()
			server.transitionContent(response, request, test.action)
			if response.Code != http.StatusConflict {
				t.Fatalf("case %d response=%d: %s", index, response.Code, response.Body.String())
			}
			var jobs int
			var snapshotPresent bool
			var storedRevisionState string
			if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM content_publish_jobs WHERE target_id=$1`, targetID).Scan(&jobs); err != nil {
				t.Fatal(err)
			}
			if err = pool.QueryRow(t.Context(), `SELECT sent_snapshot IS NOT NULL FROM content_publish_targets WHERE id=$1`, targetID).Scan(&snapshotPresent); err != nil {
				t.Fatal(err)
			}
			if err = pool.QueryRow(t.Context(), `SELECT status FROM content_revisions WHERE id=$1`, revisionID).Scan(&storedRevisionState); err != nil {
				t.Fatal(err)
			}
			if jobs != 0 || snapshotPresent || storedRevisionState != revisionState {
				t.Fatalf("preflight failure was not atomic: jobs=%d snapshot=%v revision=%s want=%s", jobs, snapshotPresent, storedRevisionState, revisionState)
			}
		})
	}
}
