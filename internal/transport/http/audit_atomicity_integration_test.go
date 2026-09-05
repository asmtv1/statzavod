package httpserver

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func forceAuditAction(server *Server, action string) {
	server.auditFailure = func(record auditRecord) error {
		if record.Action == action {
			return errors.New("forced audit outage")
		}
		return nil
	}
}

func TestTransactionalAuditFailureRollsBackBusinessMutationIntegration(t *testing.T) {
	t.Run("company", func(t *testing.T) {
		fixture := newLifecycleFixture(t, 1, false)
		forceAuditAction(fixture.server, "CREATE")
		name := "audit-company-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
		response := lifecycleRequest(fixture.server, http.MethodPost, "/api/v1/companies", `{"name":"`+name+`"}`, fixture.cookie)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertAuditRollbackCount(t, fixture, `SELECT count(*) FROM companies WHERE organization_id=$1 AND name=$2`, name)
	})

	t.Run("creator", func(t *testing.T) {
		fixture := newLifecycleFixture(t, 1, false)
		forceAuditAction(fixture.server, "CREATE")
		cookie := insertLifecycleSession(t, fixture.pool, fixture.ownerID, &fixture.companyID)
		response := lifecycleRequest(fixture.server, http.MethodPost, "/api/v1/creators", `{"firstName":"Audit","lastName":"Rollback"}`, cookie)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertAuditRollbackCount(t, fixture, `SELECT count(*) FROM creators WHERE organization_id=$1 AND display_name='Audit Rollback'`)
	})

	t.Run("team user", func(t *testing.T) {
		fixture := newLifecycleFixture(t, 1, false)
		forceAuditAction(fixture.server, "CREATE")
		email := "audit-rollback-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "") + "@test.local"
		t.Cleanup(func() { _, _ = fixture.pool.Exec(t.Context(), `DELETE FROM users WHERE email=$1`, email) })
		response := lifecycleRequest(fixture.server, http.MethodPost, "/api/v1/users", `{"email":"`+email+`","password":"audit-password-12","role":"OWNER","companyAssignments":[]}`, fixture.cookie)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var count int
		if err := fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM users WHERE email=$1`, email).Scan(&count); err != nil || count != 0 {
			t.Fatalf("team mutation survived audit failure count=%d err=%v", count, err)
		}
	})

	t.Run("oauth connect", func(t *testing.T) {
		fixture := newLifecycleFixture(t, 1, false)
		forceAuditAction(fixture.server, "CONNECT_YOUTUBE")
		externalID := "audit-oauth-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
		err := fixture.server.savePlatformConnection(t.Context(), fixture.organizationID, fixture.creatorID,
			oauthProvider{ID: "YOUTUBE"},
			oauthToken{AccessToken: "oauth-canary", RefreshToken: "refresh-canary", Scopes: []string{"scope"}},
			platformProfile{ExternalID: externalID, Username: externalID, DisplayName: "Audit OAuth", AccountType: "CHANNEL"})
		if err == nil {
			t.Fatal("OAuth save unexpectedly succeeded during audit outage")
		}
		assertAuditRollbackCount(t, fixture, `SELECT count(*) FROM platform_accounts WHERE organization_id=$1 AND external_id=$2`, externalID)
	})

	t.Run("platform purge", func(t *testing.T) {
		fixture := newLifecycleFixture(t, 1, true)
		forceAuditAction(fixture.server, "PURGE_YOUTUBE_DATA")
		accountID := fixture.accountIDs[0]
		response := lifecycleRequest(fixture.server, http.MethodDelete, "/api/v1/platform-accounts/"+accountID+"/data", "", fixture.cookie)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertAuditRollbackCount(t, fixture, `SELECT count(*) FROM platform_accounts WHERE organization_id=$1 AND id=$2`, accountID, 1)
	})

	t.Run("auth context", func(t *testing.T) {
		fixture := newLifecycleFixture(t, 1, false)
		forceAuditAction(fixture.server, "SET_AUTH_CONTEXT")
		response := lifecycleRequest(fixture.server, http.MethodPost, "/api/v1/auth/context", `{"companyId":"`+fixture.companyID+`"}`, fixture.cookie)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var count int
		if err := fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM sessions WHERE user_id=$1 AND active_company_id IS NOT NULL`, fixture.ownerID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("auth context survived audit failure count=%d err=%v", count, err)
		}
	})

	t.Run("creator contact", func(t *testing.T) {
		fixture := newLifecycleFixture(t, 1, false)
		forceAuditAction(fixture.server, "CREATE_CONTACT")
		cookie := insertLifecycleSession(t, fixture.pool, fixture.ownerID, &fixture.companyID)
		response := lifecycleRequest(fixture.server, http.MethodPost, "/api/v1/creators/"+fixture.creatorID+"/contacts", `{"kind":"EMAIL","value":"rollback@test.local","isPrimary":true}`, cookie)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertAuditRollbackCount(t, fixture, `SELECT count(*) FROM creator_contacts contact JOIN creators creator ON creator.id=contact.creator_id WHERE creator.organization_id=$1 AND contact.value='rollback@test.local'`)
	})

	t.Run("creator platform account", func(t *testing.T) {
		fixture := newLifecycleFixture(t, 1, false)
		forceAuditAction(fixture.server, "CREATE_PLATFORM_ACCOUNT")
		cookie := insertLifecycleSession(t, fixture.pool, fixture.ownerID, &fixture.companyID)
		externalID := "atomic-account-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
		response := lifecycleRequest(fixture.server, http.MethodPost, "/api/v1/creators/"+fixture.creatorID+"/accounts", `{"platform":"YOUTUBE","externalId":"`+externalID+`","username":"rollback"}`, cookie)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertAuditRollbackCount(t, fixture, `SELECT count(*) FROM platform_accounts WHERE organization_id=$1 AND external_id=$2`, externalID)
	})

	t.Run("content group", func(t *testing.T) {
		fixture := newLifecycleFixture(t, 1, false)
		forceAuditAction(fixture.server, "CREATE_CONTENT_GROUP")
		cookie := insertLifecycleSession(t, fixture.pool, fixture.ownerID, &fixture.companyID)
		response := lifecycleRequest(fixture.server, http.MethodPost, "/api/v1/content-groups", `{"creatorId":"`+fixture.creatorID+`","name":"Atomic rollback"}`, cookie)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertAuditRollbackCount(t, fixture, `SELECT count(*) FROM content_groups group_row JOIN creators creator ON creator.id=group_row.creator_id WHERE creator.organization_id=$1 AND group_row.name='Atomic rollback'`)
	})

	t.Run("team update", func(t *testing.T) {
		fixture := newLifecycleFixture(t, 1, false)
		forceAuditAction(fixture.server, "UPDATE_USER")
		response := lifecycleRequest(fixture.server, http.MethodPatch, "/api/v1/users/"+fixture.ownerID, `{"email":"atomic-team-update@test.local"}`, fixture.cookie)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var count int
		if err := fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM users WHERE id=$1 AND email='atomic-team-update@test.local'`, fixture.ownerID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("team update survived audit failure count=%d err=%v", count, err)
		}
	})

	t.Run("creator login account", func(t *testing.T) {
		fixture := newLifecycleFixture(t, 1, false)
		forceAuditAction(fixture.server, "LINK_CREATOR_LOGIN_ACCOUNT")
		cookie := insertLifecycleSession(t, fixture.pool, fixture.ownerID, &fixture.companyID)
		email := "atomic-creator-login-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "") + "@test.local"
		response := lifecycleRequest(fixture.server, http.MethodPut, "/api/v1/creators/"+fixture.creatorID+"/login-account", `{"email":"`+email+`","password":"atomic-password-12"}`, cookie)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var users, linked int
		if err := fixture.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM users WHERE email=$1),(SELECT count(*) FROM creators WHERE id=$2 AND login_user_id IS NOT NULL)`, email, fixture.creatorID).Scan(&users, &linked); err != nil || users != 0 || linked != 0 {
			t.Fatalf("creator login mutation survived users=%d linked=%d err=%v", users, linked, err)
		}
	})

	t.Run("invitation", func(t *testing.T) {
		fixture := newLifecycleFixture(t, 1, false)
		forceAuditAction(fixture.server, "CREATE_INVITATION")
		email := "atomic-invitation-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "") + "@test.local"
		response := lifecycleRequest(fixture.server, http.MethodPost, "/api/v1/users/invitations", `{"email":"`+email+`","role":"VIEWER"}`, fixture.cookie)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertAuditRollbackCount(t, fixture, `SELECT count(*) FROM user_invitations WHERE organization_id=$1 AND email=$2`, email)
	})

	t.Run("oauth authorize state", func(t *testing.T) {
		fixture := newLifecycleFixture(t, 1, false)
		fixture.server.config.YouTubeClientID = "client"
		fixture.server.config.YouTubeClientSecret = "secret"
		fixture.server.config.YouTubeRedirectURL = "https://callback.test"
		forceAuditAction(fixture.server, "START_YOUTUBE_OAUTH")
		cookie := insertLifecycleSession(t, fixture.pool, fixture.ownerID, &fixture.companyID)
		response := lifecycleRequest(fixture.server, http.MethodPost, "/api/v1/creators/"+fixture.creatorID+"/connections/youtube/authorize", "", cookie)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertAuditRollbackCount(t, fixture, `SELECT count(*) FROM oauth_states WHERE organization_id=$1 AND creator_id=$2`, fixture.creatorID)
	})

	t.Run("instagram selection", func(t *testing.T) {
		fixture := newLifecycleFixture(t, 1, false)
		externalID := "atomic-instagram-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
		selectionID, err := fixture.server.createInstagramAccountSelection(t.Context(), fixture.organizationID, fixture.creatorID, fixture.ownerID, []instagramFacebookCandidate{{
			Token:   oauthToken{AccessToken: "selection-canary", RefreshToken: "selection-refresh"},
			Profile: platformProfile{ExternalID: externalID, Username: "rollback", DisplayName: "Rollback", AccountType: "BUSINESS", Metadata: map[string]any{}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		forceAuditAction(fixture.server, "COMPLETE_INSTAGRAM_ACCOUNT_SELECTION")
		cookie := insertLifecycleSession(t, fixture.pool, fixture.ownerID, &fixture.companyID)
		response := lifecycleRequest(fixture.server, http.MethodPost, "/api/v1/creators/"+fixture.creatorID+"/connections/instagram-facebook/selections/"+selectionID, `{"accountIds":["`+externalID+`"]}`, cookie)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var accounts, consumed int
		if err = fixture.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM platform_accounts WHERE organization_id=$1 AND external_id=$2),(SELECT count(*) FROM oauth_account_selections WHERE id=$3 AND consumed_at IS NOT NULL)`, fixture.organizationID, externalID, selectionID).Scan(&accounts, &consumed); err != nil || accounts != 0 || consumed != 0 {
			t.Fatalf("selection survived audit failure accounts=%d consumed=%d err=%v", accounts, consumed, err)
		}
	})

	t.Run("sync now", func(t *testing.T) {
		fixture := newLifecycleFixture(t, 1, true)
		forceAuditAction(fixture.server, "REQUEST_PLATFORM_SYNC")
		accountID, targetID := fixture.accountIDs[0], fixture.syncTargetIDs[0]
		if _, err := fixture.pool.Exec(t.Context(), `UPDATE sync_targets SET next_sync_at=now()+interval '2 days' WHERE id=$1`, targetID); err != nil {
			t.Fatal(err)
		}
		cookie := insertLifecycleSession(t, fixture.pool, fixture.ownerID, &fixture.companyID)
		response := lifecycleRequest(fixture.server, http.MethodPost, "/api/v1/platform-accounts/"+accountID+"/sync", "", cookie)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var queued bool
		if err := fixture.pool.QueryRow(t.Context(), `SELECT next_sync_at<=now() FROM sync_targets WHERE id=$1`, targetID).Scan(&queued); err != nil || queued {
			t.Fatalf("sync request survived audit failure queued=%v err=%v", queued, err)
		}
	})
}

func TestOAuthRefreshAuditFailureRollbackAndTwoWorkerExactlyOnceIntegration(t *testing.T) {
	fixture := newLifecycleFixture(t, 1, true)
	connectionID := fixture.oauthConnectionIDs[0]
	var providerCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"refreshed-access","expires_in":3600,"scope":"scope-a"}`))
	}))
	defer provider.Close()
	fixture.server.config.YouTubeOAuthBase = provider.URL
	fixture.server.config.YouTubeClientID = "client"
	fixture.server.config.YouTubeClientSecret = "secret"
	if _, err := fixture.pool.Exec(t.Context(), `UPDATE oauth_connections SET expires_at=now()-interval '1 minute' WHERE id=$1`, connectionID); err != nil {
		t.Fatal(err)
	}
	var beforeCipher []byte
	if err := fixture.pool.QueryRow(t.Context(), `SELECT access_token_ciphertext FROM oauth_connections WHERE id=$1`, connectionID).Scan(&beforeCipher); err != nil {
		t.Fatal(err)
	}
	forceAuditAction(fixture.server, "SYSTEM_OAUTH_REFRESH")
	if processed, err := fixture.server.RunOAuthTokenRefresh(t.Context(), 1); err == nil || processed != 1 {
		t.Fatalf("forced audit failure processed=%d err=%v", processed, err)
	}
	var afterFailureCipher []byte
	var auditCount int
	if err := fixture.pool.QueryRow(t.Context(), `SELECT access_token_ciphertext,(SELECT count(*) FROM audit_logs WHERE organization_id=$2 AND action='SYSTEM_OAUTH_REFRESH') FROM oauth_connections WHERE id=$1`, connectionID, fixture.organizationID).Scan(&afterFailureCipher, &auditCount); err != nil {
		t.Fatal(err)
	}
	if string(afterFailureCipher) != string(beforeCipher) || auditCount != 0 {
		t.Fatalf("refresh survived audit failure tokenChanged=%v audits=%d", string(afterFailureCipher) != string(beforeCipher), auditCount)
	}

	fixture.server.auditFailure = nil
	var processed [2]int
	var workerErr [2]error
	var wait sync.WaitGroup
	for index := range processed {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			processed[index], workerErr[index] = fixture.server.RunOAuthTokenRefresh(t.Context(), 1)
		}(index)
	}
	wait.Wait()
	if workerErr[0] != nil || workerErr[1] != nil {
		t.Fatalf("worker errors=%v", workerErr)
	}
	var refreshedCipher, refreshedNonce []byte
	if err := fixture.pool.QueryRow(t.Context(), `SELECT access_token_ciphertext,access_token_nonce,(SELECT count(*) FROM audit_logs WHERE organization_id=$2 AND action='SYSTEM_OAUTH_REFRESH') FROM oauth_connections WHERE id=$1`, connectionID, fixture.organizationID).Scan(&refreshedCipher, &refreshedNonce, &auditCount); err != nil {
		t.Fatal(err)
	}
	plain, err := fixture.server.envelope.Decrypt(refreshedCipher, refreshedNonce)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "refreshed-access" || auditCount != 1 || providerCalls.Load() != 2 {
		// One provider call belongs to the rolled-back attempt; exactly one of the
		// two concurrent workers may perform the successful refresh.
		t.Fatalf("token=%q audits=%d providerCalls=%d processed=%v", plain, auditCount, providerCalls.Load(), processed)
	}
}

func TestSystemSyncClaimAuditFailurePreventsImportIntegration(t *testing.T) {
	fixture := newLifecycleFixture(t, 1, true)
	accountID, targetID := fixture.accountIDs[0], fixture.syncTargetIDs[0]
	if _, err := fixture.pool.Exec(t.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2)`, fixture.creatorID, accountID); err != nil {
		t.Fatal(err)
	}
	var nextSyncBefore time.Time
	if err := fixture.pool.QueryRow(t.Context(), `SELECT next_sync_at FROM sync_targets WHERE id=$1`, targetID).Scan(&nextSyncBefore); err != nil {
		t.Fatal(err)
	}
	forceAuditAction(fixture.server, auditSystemSyncStarted)
	processed, err := fixture.server.RunPlatformSync(t.Context(), 1)
	if err == nil || processed != 0 {
		t.Fatalf("sync claim audit failure processed=%d err=%v", processed, err)
	}
	var nextSyncAfter time.Time
	var runs, audits int
	if err = fixture.pool.QueryRow(t.Context(), `
		SELECT next_sync_at,
		       (SELECT count(*) FROM sync_runs WHERE target_id=$1),
		       (SELECT count(*) FROM audit_logs WHERE organization_id=$2 AND action=$3)
		FROM sync_targets WHERE id=$1
	`, targetID, fixture.organizationID, auditSystemSyncStarted).Scan(&nextSyncAfter, &runs, &audits); err != nil {
		t.Fatal(err)
	}
	if !nextSyncAfter.Equal(nextSyncBefore) || runs != 0 || audits != 0 {
		t.Fatalf("sync claim survived audit failure nextChanged=%v runs=%d audits=%d", !nextSyncAfter.Equal(nextSyncBefore), runs, audits)
	}
}

func TestSystemSyncFinalAuditFailureRetainsDurableStartRecordIntegration(t *testing.T) {
	fixture := newLifecycleFixture(t, 1, true)
	accountID := fixture.accountIDs[0]
	if _, err := fixture.pool.Exec(t.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2)`, fixture.creatorID, accountID); err != nil {
		t.Fatal(err)
	}
	job, claimed, err := fixture.server.claimPlatformSync(t.Context())
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	forceAuditAction(fixture.server, auditSystemSyncSuccess)
	if err = fixture.server.finishPlatformSync(t.Context(), job, syncResult{RecordsRead: 2, RecordsWritten: 1}, nil); err == nil {
		t.Fatal("sync finish unexpectedly survived final audit outage")
	}
	var started, succeeded int
	var outcome string
	if err = fixture.pool.QueryRow(t.Context(), `
		SELECT
		  (SELECT count(*) FROM audit_logs WHERE organization_id=$1 AND action=$2 AND entity_id=$4),
		  (SELECT count(*) FROM audit_logs WHERE organization_id=$1 AND action=$3 AND entity_id=$4),
		  outcome::text
		FROM sync_runs WHERE id=$5
	`, fixture.organizationID, auditSystemSyncStarted, auditSystemSyncSuccess, accountID, job.RunID).Scan(&started, &succeeded, &outcome); err != nil {
		t.Fatal(err)
	}
	if started != 1 || succeeded != 0 || outcome != "RUNNING" {
		t.Fatalf("started=%d succeeded=%d outcome=%s", started, succeeded, outcome)
	}
}

func TestOAuthReauthAuditFailureRollsBackStatusIntegration(t *testing.T) {
	fixture := newLifecycleFixture(t, 1, true)
	accountID, connectionID := fixture.accountIDs[0], fixture.oauthConnectionIDs[0]
	if _, err := fixture.pool.Exec(t.Context(), `UPDATE oauth_connections SET expires_at=now()-interval '1 minute' WHERE id=$1`, connectionID); err != nil {
		t.Fatal(err)
	}
	forceAuditAction(fixture.server, auditSystemOAuthReauthRequired)
	job := platformSyncJob{AccountID: accountID, OrganizationID: fixture.organizationID, CompanyID: fixture.companyID, Platform: "YOUTUBE"}
	refreshErr := &providerError{Platform: "YouTube", Kind: providerAuth, Message: "authorization rejected"}
	if err := fixture.server.markOAuthReauthRequired(t.Context(), job, refreshErr); err == nil {
		t.Fatal("reauth status mutation unexpectedly survived audit outage")
	}
	var oauthStatus, accountStatus string
	var audits int
	if err := fixture.pool.QueryRow(t.Context(), `
		SELECT oauth.status::text,account.status::text,
		       (SELECT count(*) FROM audit_logs WHERE organization_id=$3 AND action=$4)
		FROM oauth_connections oauth JOIN platform_accounts account ON account.id=oauth.platform_account_id
		WHERE oauth.id=$1 AND account.id=$2
	`, connectionID, accountID, fixture.organizationID, auditSystemOAuthReauthRequired).Scan(&oauthStatus, &accountStatus, &audits); err != nil {
		t.Fatal(err)
	}
	if oauthStatus != "ACTIVE" || accountStatus != "ACTIVE" || audits != 0 {
		t.Fatalf("reauth mutation survived oauth=%s account=%s audits=%d", oauthStatus, accountStatus, audits)
	}
}

func TestIdempotentMutationBranchesAuditExactlyOnceIntegration(t *testing.T) {
	cases := []struct {
		name, method, routeSuffix, body, action string
	}{
		{
			name: "creator profile", method: http.MethodPatch, routeSuffix: "/creators/{creator}", action: "UPDATE",
			body: `{"firstName":"Life","lastName":"Cycle","displayName":"Life Cycle","status":"ACTIVE"}`,
		},
		{
			name: "work status", method: http.MethodPatch, routeSuffix: "/creators/{creator}/work-status", action: "UPDATE_WORK_STATUS",
			body: `{"status":"OK","comment":""}`,
		},
		{
			name: "credentials", method: http.MethodPut, routeSuffix: "/creators/{creator}/credentials", action: "UPDATE_CREDENTIALS",
			body: `{"items":[]}`,
		},
		{
			name: "VK access", method: http.MethodPut, routeSuffix: "/creators/{creator}/vk-access", action: "UPDATE_VK_ACCESS",
			body: `{}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newLifecycleFixture(t, 1, false)
			cookie := insertLifecycleSession(t, fixture.pool, fixture.ownerID, &fixture.companyID)
			route := "/api/v1" + strings.ReplaceAll(testCase.routeSuffix, "{creator}", fixture.creatorID)
			var before int
			if err := fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_logs WHERE organization_id=$1 AND action=$2`, fixture.organizationID, testCase.action).Scan(&before); err != nil {
				t.Fatal(err)
			}
			response := lifecycleRequest(fixture.server, testCase.method, route, testCase.body, cookie)
			if response.Code != http.StatusNoContent {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var after, changedFalse int
			if err := fixture.pool.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE metadata->>'changed'='false') FROM audit_logs WHERE organization_id=$1 AND action=$2`, fixture.organizationID, testCase.action).Scan(&after, &changedFalse); err != nil {
				t.Fatal(err)
			}
			if after != before+1 || changedFalse < 1 {
				t.Fatalf("audit before/after=%d/%d changedFalse=%d", before, after, changedFalse)
			}
			forceAuditAction(fixture.server, testCase.action)
			response = lifecycleRequest(fixture.server, testCase.method, route, testCase.body, cookie)
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("forced outage status=%d body=%s", response.Code, response.Body.String())
			}
			var final int
			if err := fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_logs WHERE organization_id=$1 AND action=$2`, fixture.organizationID, testCase.action).Scan(&final); err != nil || final != after {
				t.Fatalf("audit count after outage=%d want=%d err=%v", final, after, err)
			}
		})
	}
}

func assertAuditRollbackCount(t *testing.T, fixture lifecycleFixture, query string, extra ...any) {
	t.Helper()
	want := 0
	if len(extra) > 1 {
		if value, ok := extra[len(extra)-1].(int); ok {
			want = value
			extra = extra[:len(extra)-1]
		}
	}
	args := append([]any{fixture.organizationID}, extra...)
	var count int
	if err := fixture.pool.QueryRow(t.Context(), query, args...).Scan(&count); err != nil || count != want {
		t.Fatalf("mutation count=%d want=%d err=%v", count, want, err)
	}
}

func TestOwnerAllModeResourceReadAuditHasResolvedScopeIntegration(t *testing.T) {
	fixture := newLifecycleFixture(t, 1, false)
	action := auditAction(http.MethodGet, "/api/v1/creators/{id}")
	var before int
	if err := fixture.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_logs WHERE organization_id=$1 AND action=$2`, fixture.organizationID, action).Scan(&before); err != nil {
		t.Fatal(err)
	}
	response := lifecycleRequest(fixture.server, http.MethodGet, "/api/v1/creators/"+fixture.creatorID, "", fixture.cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var companyID, entityType, entityID, endpoint, method string
	var status int
	var after int
	if err := fixture.pool.QueryRow(t.Context(), `
		SELECT company_id::text,entity_type,entity_id::text,endpoint,http_method,response_status,
		       (SELECT count(*) FROM audit_logs WHERE organization_id=$1 AND action=$2)
		FROM audit_logs WHERE organization_id=$1 AND action=$2
		ORDER BY created_at DESC,id DESC LIMIT 1
	`, fixture.organizationID, action).Scan(&companyID, &entityType, &entityID, &endpoint, &method, &status, &after); err != nil {
		t.Fatal(err)
	}
	if after != before+1 || companyID != fixture.companyID || entityType != "CREATOR" || entityID != fixture.creatorID || endpoint != "/api/v1/creators/{id}" || method != http.MethodGet || status != http.StatusOK {
		t.Fatalf("audit before/after=%d/%d company=%s entity=%s/%s endpoint=%s method=%s status=%d", before, after, companyID, entityType, entityID, endpoint, method, status)
	}
}

func TestTransactionalAuditIsWrittenExactlyOnceIntegration(t *testing.T) {
	fixture := newLifecycleFixture(t, 1, false)
	name := "audit-exactly-once-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	response := lifecycleRequest(fixture.server, http.MethodPost, "/api/v1/companies", `{"name":"`+name+`"}`, fixture.cookie)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var companyID string
	if err := fixture.pool.QueryRow(t.Context(), `SELECT id::text FROM companies WHERE organization_id=$1 AND name=$2`, fixture.organizationID, name).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := fixture.pool.QueryRow(t.Context(), `
		SELECT count(*) FROM audit_logs
		WHERE organization_id=$1 AND company_id=$2 AND entity_type='COMPANY' AND entity_id=$2
		  AND action='CREATE' AND endpoint='/api/v1/companies'
		  AND http_method='POST' AND response_status=201
	`, fixture.organizationID, companyID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("transactional audit count=%d want=1 err=%v", count, err)
	}
}
