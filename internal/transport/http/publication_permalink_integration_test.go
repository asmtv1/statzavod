package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/statzavod/statzavod/internal/config"
)

func TestLegacyPublicationPermalinksRequireActiveOrSucceededTarget(t *testing.T) {
	pool := authzIntegrationPool(t)
	suffix := strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	var organizationID, companyID, ownerID, creatorUserID, creatorID string
	mustScanID(t, pool, `INSERT INTO organizations(name,slug) VALUES('Permalink integration',$1) RETURNING id`, &organizationID, "permalink-"+suffix)
	mustScanID(t, pool, `INSERT INTO companies(organization_id,name) VALUES($1,'Permalink company') RETURNING id`, &companyID, organizationID)
	mustScanID(t, pool, `INSERT INTO users(email,password_hash,role,status) VALUES($1,'x','ADMIN','ACTIVE') RETURNING id`, &ownerID, "permalink-owner-"+suffix+"@test")
	mustScanID(t, pool, `INSERT INTO users(email,password_hash,role,status) VALUES($1,'x','VIEWER','ACTIVE') RETURNING id`, &creatorUserID, "permalink-creator-"+suffix+"@test")
	if _, err := pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role,membership_role) VALUES($1,$2,'ADMIN','OWNER'),($1,$3,'VIEWER','CREATOR')`, organizationID, ownerID, creatorUserID); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, pool, `INSERT INTO creators(organization_id,company_id,login_user_id,created_by,first_name,last_name,display_name) VALUES($1,$2,$3,$4,'Legacy','Creator','Legacy Creator') RETURNING id`, &creatorID, organizationID, companyID, creatorUserID, ownerID)

	accounts := map[string]string{}
	for _, platform := range []string{"YOUTUBE", "INSTAGRAM", "VK", "TIKTOK"} {
		var accountID string
		mustScanID(t, pool, `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status) VALUES($1,$2,$3,$4,$4,$5,'ACTIVE') RETURNING id`, &accountID, organizationID, companyID, platform, strings.ToLower(platform)+"-"+suffix, platform)
		if _, err := pool.Exec(t.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id,assigned_by) VALUES($1,$2,$3)`, creatorID, accountID, ownerID); err != nil {
			t.Fatal(err)
		}
		accounts[platform] = accountID
	}

	type publicationSeed struct {
		title, platform, externalID, permalink, status string
	}
	seeds := []publicationSeed{
		{"Legacy YouTube", "YOUTUBE", "LgcyYT_0001", "https://www.youtube.com/shorts/LgcyYT_0001", "ACTIVE"},
		{"Legacy Instagram", "INSTAGRAM", "910000000000001", "https://www.instagram.com/reel/Legacy_safe-1/", "ACTIVE"},
		{"Legacy VK", "VK", "-91001_92001", "https://vk.ru/video-91001_92001", "ACTIVE"},
		{"Legacy TikTok", "TIKTOK", "7910000000000000001", "https://www.tiktok.com/@legacy.safe/video/7910000000000000001", "ACTIVE"},
		{"Poison javascript", "INSTAGRAM", "910000000000002", "javascript:alert('upload')", "ACTIVE"},
		{"Poison signed", "TIKTOK", "7910000000000000002", "https://www.tiktok.com/@legacy.safe/video/7910000000000000002?X-Amz-Signature=secret", "ACTIVE"},
		{"Poison upload", "INSTAGRAM", "910000000000003", "https://www.instagram.com/upload/session", "ACTIVE"},
		{"Poison base64", "TIKTOK", "7910000000000000003", "c2Vzc2lvbi11cGxvYWQtdXJs", "ACTIVE"},
		{"Inactive legacy", "INSTAGRAM", "910000000000004", "https://www.instagram.com/reel/Inactive_safe-1/", "ARCHIVED"},
	}
	for _, seed := range seeds {
		if _, err := pool.Exec(t.Context(), `INSERT INTO publications(organization_id,creator_id,platform_account_id,platform,external_id,publication_type,title,permalink,published_at,status) VALUES($1,$2,$3,$4,$5,'VIDEO',$6,$7,now(),$8)`, organizationID, creatorID, accounts[seed.platform], seed.platform, seed.externalID, seed.title, seed.permalink, seed.status); err != nil {
			t.Fatal(err)
		}
	}

	var itemID, revisionID, targetID string
	mustScanID(t, pool, `INSERT INTO content_items(organization_id,company_id,creator_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, &itemID, organizationID, companyID, creatorID, ownerID)
	mustScanID(t, pool, `INSERT INTO content_revisions(content_item_id,organization_id,revision,status,created_by) VALUES($1,$2,1,'FAILED',$3) RETURNING id`, &revisionID, itemID, organizationID, ownerID)
	mustScanID(t, pool, `INSERT INTO content_publish_targets(content_revision_id,organization_id,company_id,creator_id,platform_account_id,platform,status) VALUES($1,$2,$3,$4,$5,'INSTAGRAM','FAILED') RETURNING id`, &targetID, revisionID, organizationID, companyID, creatorID, accounts["INSTAGRAM"])
	if _, err := pool.Exec(t.Context(), `INSERT INTO publications(organization_id,creator_id,platform_account_id,platform,external_id,publication_type,title,permalink,published_at,status,content_item_id,content_publish_target_id) VALUES($1,$2,$3,'INSTAGRAM','910000000000005','VIDEO','Linked failed','https://www.instagram.com/reel/Linked_safe-1/',now(),'ACTIVE',$4,$5)`, organizationID, creatorID, accounts["INSTAGRAM"], itemID, targetID); err != nil {
		t.Fatal(err)
	}

	server := New(pool, config.Config{})
	manager := principal{ID: ownerID, OrganizationID: organizationID, Role: roleOwner, Companies: map[string]companyAccess{companyID: testCompany(companyID)}}
	activeCompany, activeCreator := companyID, creatorID
	creator := principal{ID: creatorUserID, OrganizationID: organizationID, Role: roleCreator, ActiveCompanyID: &activeCompany, ActiveCreatorID: &activeCreator, CreatorProfiles: map[string]creatorProfileAccess{creatorID: {ID: creatorID, CompanyID: companyID}}}

	serve := func(handler http.HandlerFunc, p principal) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request = request.WithContext(context.WithValue(request.Context(), principalKey, p))
		response := httptest.NewRecorder()
		handler(response, request)
		return response
	}
	assertResponse := func(label string, response *httptest.ResponseRecorder) {
		t.Helper()
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", label, response.Code, response.Body.String())
		}
		var payload struct {
			Items []struct {
				Title     string  `json:"title"`
				Permalink *string `json:"permalink"`
			} `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		got := make(map[string]string, len(payload.Items))
		for _, item := range payload.Items {
			if item.Permalink != nil {
				got[item.Title] = *item.Permalink
			} else {
				got[item.Title] = ""
			}
		}
		want := map[string]string{
			"Legacy YouTube":   "https://www.youtube.com/shorts/LgcyYT_0001",
			"Legacy Instagram": "https://www.instagram.com/reel/Legacy_safe-1/",
			"Legacy VK":        "https://vk.ru/video-91001_92001",
			"Legacy TikTok":    "https://www.tiktok.com/@legacy.safe/video/7910000000000000001",
		}
		for title, permalink := range want {
			if got[title] != permalink {
				t.Errorf("%s %s permalink=%q, want %q", label, title, got[title], permalink)
			}
		}
		for _, title := range []string{"Poison javascript", "Poison signed", "Poison upload", "Poison base64", "Inactive legacy", "Linked failed"} {
			if _, present := got[title]; !present {
				t.Errorf("%s lost analytics row %q", label, title)
			} else if got[title] != "" {
				t.Errorf("%s exposed %q permalink %q", label, title, got[title])
			}
		}
		for _, poison := range []string{"javascript:", "X-Amz", "/upload/", "c2Vzc2lvbi"} {
			if strings.Contains(response.Body.String(), poison) {
				t.Errorf("%s response leaked %q: %s", label, poison, response.Body.String())
			}
		}
	}

	assertResponse("management", serve(server.listPublications, manager))
	assertResponse("creator", serve(server.listOwnPublications, creator))
}
