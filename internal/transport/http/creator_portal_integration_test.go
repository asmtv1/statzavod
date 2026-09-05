package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/statzavod/statzavod/internal/config"
	"github.com/xuri/excelize/v2"
)

func TestLiveCreatorPortalIDORMutationExportAndManagerScope(t *testing.T) {
	pool := authzIntegrationPool(t)
	suffix := strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	var organizationID, ownerID, managerID, creatorUserID, otherCreatorUserID string
	var companyA, companyB, profileA, profileB, otherProfileA string
	var accountA, accountB, publicationA, publicationB string
	if err := pool.QueryRow(t.Context(), `INSERT INTO organizations(name,slug) VALUES($1,$2) RETURNING id`, "Portal scope", "portal-scope-"+suffix).Scan(&organizationID); err != nil {
		t.Fatal(err)
	}
	createUser := func(email, legacyRole, membershipRole string, target *string) {
		t.Helper()
		if err := pool.QueryRow(t.Context(), `INSERT INTO users(email,password_hash,role,status) VALUES($1,'portal-hash',$2,'ACTIVE') RETURNING id`, email, legacyRole).Scan(target); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role,membership_role) VALUES($1,$2,$3,$4)`, organizationID, *target, legacyRole, membershipRole); err != nil {
			t.Fatal(err)
		}
	}
	createUser("portal-owner-"+suffix+"@test.local", "ADMIN", roleOwner, &ownerID)
	createUser("portal-manager-"+suffix+"@test.local", "ANALYST", roleManager, &managerID)
	createUser("portal-creator-"+suffix+"@test.local", "VIEWER", roleCreator, &creatorUserID)
	createUser("portal-other-"+suffix+"@test.local", "VIEWER", roleCreator, &otherCreatorUserID)
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM users WHERE id=ANY($1::uuid[])`, []string{ownerID, managerID, creatorUserID, otherCreatorUserID}); err != nil {
			t.Errorf("cleanup portal users: %v", err)
		}
		if _, err := pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, organizationID); err != nil {
			t.Errorf("cleanup portal organization: %v", err)
		}
	})
	if err := pool.QueryRow(t.Context(), `INSERT INTO companies(organization_id,name) VALUES($1,'Portal A') RETURNING id`, organizationID).Scan(&companyA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO companies(organization_id,name) VALUES($1,'Portal B') RETURNING id`, organizationID).Scan(&companyB); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO creators(organization_id,company_id,login_user_id,first_name,last_name,display_name,created_by) VALUES($1,$2,$3,'Portal','A','Portal A',$4) RETURNING id`, organizationID, companyA, creatorUserID, ownerID).Scan(&profileA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO creators(organization_id,company_id,login_user_id,first_name,last_name,display_name,created_by) VALUES($1,$2,$3,'Portal','B','Portal B',$4) RETURNING id`, organizationID, companyB, creatorUserID, ownerID).Scan(&profileB); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO creators(organization_id,company_id,login_user_id,first_name,last_name,display_name,created_by) VALUES($1,$2,$3,'Other','A','Other A',$4) RETURNING id`, organizationID, companyA, otherCreatorUserID, ownerID).Scan(&otherProfileA); err != nil {
		t.Fatal(err)
	}
	var managerAssignmentID string
	if err := pool.QueryRow(t.Context(), `INSERT INTO manager_company_assignments(organization_id,manager_user_id,company_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, organizationID, managerID, companyA, ownerID).Scan(&managerAssignmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO manager_company_permissions(assignment_id,permission,granted_by) VALUES($1,'STATS_VIEW',$2),($1,'STATS_EXPORT',$2)`, managerAssignmentID, ownerID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status) VALUES($1,$2,'INSTAGRAM',$3,'portal-a','Portal A social','ACTIVE') RETURNING id`, organizationID, companyA, "portal-a-"+suffix).Scan(&accountA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status) VALUES($1,$2,'INSTAGRAM',$3,'portal-b','Portal B social','ACTIVE') RETURNING id`, organizationID, companyB, "portal-b-"+suffix).Scan(&accountB); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id,assigned_by) VALUES($1,$2,$3),($4,$5,$3)`, profileA, accountA, ownerID, profileB, accountB); err != nil {
		t.Fatal(err)
	}
	publishedAt := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	if err := pool.QueryRow(t.Context(), `INSERT INTO publications(organization_id,creator_id,platform_account_id,platform,external_id,publication_type,title,permalink,published_at) VALUES($1,$2,$3,'INSTAGRAM',$4,'POST','Only A','javascript:alert(''poison-session'')',$5) RETURNING id`, organizationID, profileA, accountA, "portal-pub-a-"+suffix, publishedAt).Scan(&publicationA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO publications(organization_id,creator_id,platform_account_id,platform,external_id,publication_type,title,permalink,published_at) VALUES($1,$2,$3,'INSTAGRAM',$4,'POST','Only B','https://evil.example/upload?X-Amz-Signature=secret',$5) RETURNING id`, organizationID, profileB, accountB, "portal-pub-b-"+suffix, publishedAt).Scan(&publicationB); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO publication_metric_snapshots(publication_id,views,likes,comments,shares) VALUES($1,111,11,1,2),($2,999,99,9,8)`, publicationA, publicationB); err != nil {
		t.Fatal(err)
	}

	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	server := New(pool, config.Config{CookieName: "portal_session", TokenEncryptionKey: key})
	if server.envelope == nil {
		t.Fatal("test encryption envelope was not created")
	}
	insertCredential := func(profileID, section, field, value string) string {
		t.Helper()
		ciphertext, nonce, err := server.envelope.Encrypt([]byte(value))
		if err != nil {
			t.Fatal(err)
		}
		var id string
		if err = pool.QueryRow(t.Context(), `INSERT INTO creator_credentials(creator_id,section,field_key,is_secret,value_ciphertext,value_nonce,updated_by) VALUES($1,$2,$3,true,$4,$5,$6) RETURNING id`, profileID, section, field, ciphertext, nonce, ownerID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	credentialA := insertCredential(profileA, "INSTAGRAM", "password", "secret-a")
	credentialB := insertCredential(profileB, "INSTAGRAM", "password", "secret-b")
	otherCredential := insertCredential(otherProfileA, "INSTAGRAM", "password", "secret-other")

	createSession := func(userID, label string, activeCompanyID, activeCreatorID any) *http.Cookie {
		t.Helper()
		token := "portal-session-" + suffix + "-" + label
		digest := sha256.Sum256([]byte(token))
		if _, err := pool.Exec(t.Context(), `INSERT INTO sessions(user_id,token_hash,expires_at,active_company_id,active_creator_id) VALUES($1,$2,now()+interval '1 hour',$3,$4)`, userID, digest[:], activeCompanyID, activeCreatorID); err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: "portal_session", Value: token}
	}
	creatorCookie := createSession(creatorUserID, "creator", companyA, profileA)
	managerCookie := createSession(managerID, "manager", companyA, nil)
	ownerCookie := createSession(ownerID, "owner", nil, nil)
	ownerSelectedCookie := createSession(ownerID, "owner-selected", companyA, nil)
	router := server.Router()
	serve := func(cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.AddCookie(cookie)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		request.Header.Set("Accept-Language", "en")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}

	profilesResponse := serve(creatorCookie, http.MethodGet, "/api/v1/creator-portal/profiles", "")
	if profilesResponse.Code != http.StatusOK || !strings.Contains(profilesResponse.Body.String(), profileA) || !strings.Contains(profilesResponse.Body.String(), profileB) {
		t.Fatalf("own profiles status=%d body=%s", profilesResponse.Code, profilesResponse.Body.String())
	}
	profileResponse := serve(creatorCookie, http.MethodGet, "/api/v1/creator-portal/profile?creatorId="+profileB, "")
	if profileResponse.Code != http.StatusOK || !strings.Contains(profileResponse.Body.String(), `"displayName":"Portal A"`) || strings.Contains(profileResponse.Body.String(), "Portal B") {
		t.Fatalf("active profile IDOR status=%d body=%s", profileResponse.Code, profileResponse.Body.String())
	}
	for _, reveal := range []struct {
		id, wantValue string
		wantStatus    int
	}{
		{credentialA, "secret-a", http.StatusOK},
		{credentialB, "", http.StatusNotFound},
		{otherCredential, "", http.StatusNotFound},
	} {
		response := serve(creatorCookie, http.MethodPost, "/api/v1/creator-portal/credentials/"+reveal.id+"/reveal", "")
		if response.Code != reveal.wantStatus || (reveal.wantValue != "" && !strings.Contains(response.Body.String(), reveal.wantValue)) {
			t.Fatalf("reveal %s status=%d body=%s", reveal.id, response.Code, response.Body.String())
		}
	}
	statsResponse := serve(creatorCookie, http.MethodGet, "/api/v1/creator-portal/stats?activityFrom=2026-08-01&activityTo=2026-08-10", "")
	if statsResponse.Code != http.StatusOK || portalKPIValue(t, statsResponse.Body.Bytes(), "views") != 111 {
		t.Fatalf("creator stats status=%d body=%s", statsResponse.Code, statsResponse.Body.String())
	}
	creatorPublications := serve(creatorCookie, http.MethodGet, "/api/v1/creator-portal/publications?activityFrom=2026-08-01&activityTo=2026-08-10", "")
	if creatorPublications.Code != http.StatusOK || !strings.Contains(creatorPublications.Body.String(), "Only A") || strings.Contains(creatorPublications.Body.String(), "javascript:") || strings.Contains(creatorPublications.Body.String(), "poison-session") {
		t.Fatalf("creator publications leaked unsafe permalink: status=%d body=%s", creatorPublications.Code, creatorPublications.Body.String())
	}
	for _, mutation := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/creators/" + profileA, ""},
		{http.MethodPost, "/api/v1/creators", `{"firstName":"Bad","lastName":"Mutation"}`},
		{http.MethodPut, "/api/v1/creators/" + profileA + "/credentials", `{"items":[]}`},
		{http.MethodPost, "/api/v1/creators/" + profileA + "/connections/youtube/authorize", ""},
		{http.MethodPost, "/api/v1/platform-accounts/" + accountA + "/sync", ""},
		{http.MethodPost, "/api/v1/content-groups", `{"creatorId":"` + profileA + `","name":"Bad"}`},
		{http.MethodGet, "/api/v1/exports?creatorIds=" + profileA, ""},
	} {
		response := serve(creatorCookie, mutation.method, mutation.path, mutation.body)
		if response.Code != http.StatusForbidden {
			t.Fatalf("creator management %s %s status=%d body=%s", mutation.method, mutation.path, response.Code, response.Body.String())
		}
	}
	exportResponse := serve(creatorCookie, http.MethodGet, "/api/v1/creator-portal/export?creatorIds="+profileB+"&activityFrom=2026-08-01&activityTo=2026-08-10&locale=en", "")
	assertPortalExport(t, exportResponse, "Portal A", "Portal B", "Only A")

	switchResponse := serve(creatorCookie, http.MethodPost, "/api/v1/auth/context", fmt.Sprintf(`{"companyId":%q,"creatorId":%q}`, companyB, profileB))
	if switchResponse.Code != http.StatusOK {
		t.Fatalf("context switch status=%d body=%s", switchResponse.Code, switchResponse.Body.String())
	}
	oldReveal := serve(creatorCookie, http.MethodPost, "/api/v1/creator-portal/credentials/"+credentialA+"/reveal", "")
	newReveal := serve(creatorCookie, http.MethodPost, "/api/v1/creator-portal/credentials/"+credentialB+"/reveal", "")
	if oldReveal.Code != http.StatusNotFound || newReveal.Code != http.StatusOK || !strings.Contains(newReveal.Body.String(), "secret-b") {
		t.Fatalf("switched reveal old=%d new=%d body=%s", oldReveal.Code, newReveal.Code, newReveal.Body.String())
	}
	switchedExport := serve(creatorCookie, http.MethodGet, "/api/v1/creator-portal/export?locale=en", "")
	assertPortalExport(t, switchedExport, "Portal B", "Portal A", "Only B")

	managerSummary := serve(managerCookie, http.MethodGet, "/api/v1/analytics/summary", "")
	if managerSummary.Code != http.StatusOK || portalKPIValue(t, managerSummary.Body.Bytes(), "views") != 111 || portalKPIValue(t, managerSummary.Body.Bytes(), "publications") != 1 {
		t.Fatalf("manager aggregate status=%d body=%s", managerSummary.Code, managerSummary.Body.String())
	}
	managerPublications := serve(managerCookie, http.MethodGet, "/api/v1/publications", "")
	if managerPublications.Code != http.StatusOK || !strings.Contains(managerPublications.Body.String(), "Only A") || strings.Contains(managerPublications.Body.String(), "javascript:") || strings.Contains(managerPublications.Body.String(), "poison-session") || strings.Contains(managerPublications.Body.String(), "X-Amz") {
		t.Fatalf("management publications leaked unsafe permalink: status=%d body=%s", managerPublications.Code, managerPublications.Body.String())
	}
	managerCreators := serve(managerCookie, http.MethodGet, "/api/v1/creators", "")
	if managerCreators.Code != http.StatusOK || !strings.Contains(managerCreators.Body.String(), "Portal A") || strings.Contains(managerCreators.Body.String(), "Portal B") {
		t.Fatalf("manager creator scope status=%d body=%s", managerCreators.Code, managerCreators.Body.String())
	}
	managerIntegrations := serve(managerCookie, http.MethodGet, "/api/v1/integrations", "")
	if managerIntegrations.Code != http.StatusOK || !strings.Contains(managerIntegrations.Body.String(), "Portal A") || strings.Contains(managerIntegrations.Body.String(), "Portal B") {
		t.Fatalf("manager integrations scope status=%d body=%s", managerIntegrations.Code, managerIntegrations.Body.String())
	}
	managerCompanies := serve(managerCookie, http.MethodGet, "/api/v1/companies", "")
	if managerCompanies.Code != http.StatusOK || !strings.Contains(managerCompanies.Body.String(), "Portal A") || strings.Contains(managerCompanies.Body.String(), "Portal B") {
		t.Fatalf("manager company list scope status=%d body=%s", managerCompanies.Code, managerCompanies.Body.String())
	}
	ownerSelectedCompanies := serve(ownerSelectedCookie, http.MethodGet, "/api/v1/companies", "")
	if ownerSelectedCompanies.Code != http.StatusOK || !strings.Contains(ownerSelectedCompanies.Body.String(), "Portal A") || strings.Contains(ownerSelectedCompanies.Body.String(), "Portal B") {
		t.Fatalf("owner selected company list scope status=%d body=%s", ownerSelectedCompanies.Code, ownerSelectedCompanies.Body.String())
	}
	ownerAllCompanies := serve(ownerCookie, http.MethodGet, "/api/v1/companies", "")
	if ownerAllCompanies.Code != http.StatusOK || !strings.Contains(ownerAllCompanies.Body.String(), "Portal A") || !strings.Contains(ownerAllCompanies.Body.String(), "Portal B") {
		t.Fatalf("owner all-company list status=%d body=%s", ownerAllCompanies.Code, ownerAllCompanies.Body.String())
	}
	ownerSummary := serve(ownerCookie, http.MethodGet, "/api/v1/analytics/summary", "")
	if ownerSummary.Code != http.StatusOK || portalKPIValue(t, ownerSummary.Body.Bytes(), "views") != 1110 || portalKPIValue(t, ownerSummary.Body.Bytes(), "publications") != 2 {
		t.Fatalf("owner all-company aggregate status=%d body=%s", ownerSummary.Code, ownerSummary.Body.String())
	}
}

func portalKPIValue(t *testing.T, body []byte, key string) int64 {
	t.Helper()
	var response struct {
		KPIs []struct {
			Key   string `json:"key"`
			Value int64  `json:"value"`
		} `json:"kpis"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode KPI response: %v body=%s", err, body)
	}
	for _, kpi := range response.KPIs {
		if kpi.Key == key {
			return kpi.Value
		}
	}
	t.Fatalf("KPI %q not found in %s", key, body)
	return 0
}

func assertPortalExport(t *testing.T, response *httptest.ResponseRecorder, wantCreator, forbiddenCreator, wantPublication string) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Fatalf("export content type=%q", response.Header().Get("Content-Type"))
	}
	workbook, err := excelize.OpenReader(bytes.NewReader(response.Body.Bytes()))
	if err != nil {
		t.Fatalf("reopen creator export: %v", err)
	}
	defer func() {
		if closeErr := workbook.Close(); closeErr != nil {
			t.Errorf("close export workbook: %v", closeErr)
		}
	}()
	sheets := workbook.GetSheetList()
	if len(sheets) != 2 || sheets[0] != "Summary" || sheets[1] != "Publications" {
		t.Fatalf("export sheets=%v", sheets)
	}
	creator, err := workbook.GetCellValue("Summary", "A5")
	if err != nil || creator != wantCreator {
		t.Fatalf("export creator=%q error=%v want=%q", creator, err, wantCreator)
	}
	publication, err := workbook.GetCellValue("Publications", "A2")
	if err != nil || publication != wantPublication {
		t.Fatalf("export publication=%q error=%v want=%q", publication, err, wantPublication)
	}
	for _, sheet := range sheets {
		rows, rowsErr := workbook.GetRows(sheet)
		if rowsErr != nil {
			t.Fatal(rowsErr)
		}
		for _, row := range rows {
			for _, cell := range row {
				if strings.Contains(cell, forbiddenCreator) {
					t.Fatalf("export leaked forbidden creator %q in %s", forbiddenCreator, sheet)
				}
			}
		}
	}
}
