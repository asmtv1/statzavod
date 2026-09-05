package httpserver

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/statzavod/statzavod/internal/config"
)

// This test uses the same disposable PostgreSQL 18 database as the publishing
// integration suite. The generation check is a database CAS, so a mock cannot
// establish the ordering guarantee that matters here.
func TestOAuthReconnectGenerationPreventsOlderCallbackOverwriteIntegration(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable PostgreSQL database migrated through 00026")
	}
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	var generationColumn bool
	if err = pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name='oauth_connections' AND column_name='authorization_generation')`).Scan(&generationColumn); err != nil || !generationColumn {
		t.Fatalf("TEST_DATABASE_URL must include OAuth reconnect generation migration: ready=%v err=%v", generationColumn, err)
	}

	suffix := strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	var organizationID, userID, companyID, creatorID, accountID string
	if err = pool.QueryRow(t.Context(), `INSERT INTO organizations(name,slug) VALUES('OAuth generation',$1) RETURNING id::text`, "oauth-generation-"+suffix).Scan(&organizationID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// resumeVerifiedOAuthReconnect creates legacy sync_targets, whose
		// target_id deliberately has no FK. Delete those first so repeated runs
		// on the same database cannot trip the partial ACTIVE uniqueness index.
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM sync_runs run USING sync_targets target WHERE run.target_id=target.id AND target.organization_id=$1`, organizationID)
		_, _ = pool.Exec(ctx, `DELETE FROM sync_targets WHERE organization_id=$1`, organizationID)
		_, _ = pool.Exec(ctx, `DELETE FROM content_publish_attempts WHERE organization_id=$1`, organizationID)
		_, _ = pool.Exec(ctx, `DELETE FROM content_publish_jobs WHERE organization_id=$1`, organizationID)
		_, _ = pool.Exec(ctx, `DELETE FROM content_publish_targets WHERE organization_id=$1`, organizationID)
		_, _ = pool.Exec(ctx, `DELETE FROM content_approval_requests WHERE organization_id=$1`, organizationID)
		_, _ = pool.Exec(ctx, `DELETE FROM content_revisions WHERE organization_id=$1`, organizationID)
		_, _ = pool.Exec(ctx, `DELETE FROM content_items WHERE organization_id=$1`, organizationID)
		_, _ = pool.Exec(ctx, `DELETE FROM oauth_connections WHERE organization_id=$1`, organizationID)
		_, _ = pool.Exec(ctx, `DELETE FROM creator_account_assignments assignment USING creators creator WHERE assignment.creator_id=creator.id AND creator.organization_id=$1`, organizationID)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_accounts WHERE organization_id=$1`, organizationID)
		_, _ = pool.Exec(ctx, `DELETE FROM creators WHERE organization_id=$1`, organizationID)
		_, _ = pool.Exec(ctx, `DELETE FROM companies WHERE organization_id=$1`, organizationID)
		_, _ = pool.Exec(ctx, `DELETE FROM audit_logs WHERE organization_id=$1`, organizationID)
		_, _ = pool.Exec(ctx, `DELETE FROM organization_memberships WHERE organization_id=$1`, organizationID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id=$1`, organizationID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	passwordHash, err := hashPassword("oauth-generation-password")
	if err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `INSERT INTO users(email,password_hash,role,status) VALUES($1,$2,'ADMIN','ACTIVE') RETURNING id::text`, "oauth-generation-"+suffix+"@test.local", passwordHash).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'ADMIN')`, organizationID, userID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `INSERT INTO companies(organization_id,name) VALUES($1,'OAuth generation') RETURNING id::text`, organizationID).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `INSERT INTO creators(organization_id,company_id,first_name,last_name,display_name,created_by) VALUES($1,$2,'OAuth','Generation','OAuth Generation',$3) RETURNING id::text`, organizationID, companyID, userID).Scan(&creatorID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status) VALUES($1,$2,'YOUTUBE',$3,'generation','Generation','REAUTH_REQUIRED') RETURNING id::text`, organizationID, companyID, "youtube-generation-"+suffix).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2)`, creatorID, accountID); err != nil {
		t.Fatal(err)
	}

	key := base64.StdEncoding.EncodeToString(bytesOf(32, 11))
	server := New(pool, config.Config{TokenEncryptionKey: key})
	initialCipher, initialNonce, err := server.envelope.Encrypt([]byte("initial-token"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO oauth_connections(organization_id,platform_account_id,access_token_ciphertext,nonce,access_token_nonce,status) VALUES($1,$2,$3,$4,$4,'REAUTH_REQUIRED')`, organizationID, accountID, initialCipher, initialNonce); err != nil {
		t.Fatal(err)
	}
	var itemID, revisionID, targetID, jobID string
	if err = pool.QueryRow(t.Context(), `INSERT INTO content_items(organization_id,company_id,creator_id,created_by) VALUES($1,$2,$3,$4) RETURNING id::text`, organizationID, companyID, creatorID, userID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `INSERT INTO content_revisions(content_item_id,organization_id,revision,description,status,created_by) VALUES($1,$2,1,'Reconnect generation','PUBLISHING',$3) RETURNING id::text`, itemID, organizationID, userID).Scan(&revisionID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `INSERT INTO content_publish_targets(content_revision_id,organization_id,company_id,creator_id,platform_account_id,platform,status) VALUES($1,$2,$3,$4,$5,'YOUTUBE','WAITING_FOR_REAUTH') RETURNING id::text`, revisionID, organizationID, companyID, creatorID, accountID).Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `INSERT INTO content_publish_jobs(target_id,content_revision_id,organization_id,company_id,run_at,status,reauth_deadline_at) VALUES($1,$2,$3,$4,now()+interval '1 hour','RETRY_SCHEDULED',now()+interval '1 hour') RETURNING id::text`, targetID, revisionID, organizationID, companyID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}

	begin := func() oauthReconnectAuthorization {
		tx, beginErr := pool.Begin(t.Context())
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		defer tx.Rollback(t.Context())
		reconnect, beginErr := beginCreatorOAuthReconnectAuthorization(t.Context(), tx, organizationID, creatorID, "YOUTUBE")
		if beginErr != nil || reconnect == nil {
			t.Fatalf("begin reconnect authorization: reconnect=%v err=%v", reconnect, beginErr)
		}
		if beginErr = tx.Commit(t.Context()); beginErr != nil {
			t.Fatal(beginErr)
		}
		return *reconnect
	}
	older, newer := begin(), begin()
	if newer.Generation != older.Generation+1 {
		t.Fatalf("generations older=%d newer=%d", older.Generation, newer.Generation)
	}

	provider := oauthProvider{ID: "YOUTUBE"}
	profile := func(reconnect oauthReconnectAuthorization) platformProfile {
		value := platformProfile{ExternalID: "youtube-generation-" + suffix, Username: "generation", DisplayName: "Generation", AccountType: "CHANNEL", Metadata: map[string]any{}}
		setOAuthReconnectAuthorization(&value, &reconnect)
		return value
	}
	newerDone := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	var newerErr, olderErr error
	go func() {
		defer wait.Done()
		newerErr = server.savePlatformConnection(t.Context(), organizationID, creatorID, provider, oauthToken{AccessToken: "newer-token", Scopes: []string{"youtube.upload"}}, profile(newer))
		close(newerDone)
	}()
	go func() {
		defer wait.Done()
		<-newerDone // both callbacks exist concurrently; the older finishes last.
		olderErr = server.savePlatformConnection(t.Context(), organizationID, creatorID, provider, oauthToken{AccessToken: "older-token", Scopes: []string{"youtube.upload"}}, profile(older))
	}()
	wait.Wait()
	if newerErr != nil {
		t.Fatalf("newer callback failed: %v", newerErr)
	}
	if !errors.Is(olderErr, errOAuthAuthorizationSuperseded) {
		t.Fatalf("older callback error=%v, want superseded", olderErr)
	}

	var cipher, nonce []byte
	var generation int64
	if err = pool.QueryRow(t.Context(), `SELECT access_token_ciphertext,access_token_nonce,authorization_generation FROM oauth_connections WHERE organization_id=$1 AND platform_account_id=$2`, organizationID, accountID).Scan(&cipher, &nonce, &generation); err != nil {
		t.Fatal(err)
	}
	plain, err := server.envelope.Decrypt(cipher, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "newer-token" || generation != newer.Generation {
		t.Fatalf("persisted token=%q generation=%d, want newer generation=%d", string(plain), generation, newer.Generation)
	}
	var targetStatus, jobStatus string
	if err = pool.QueryRow(t.Context(), `SELECT target.status,job.status FROM content_publish_targets target JOIN content_publish_jobs job ON job.target_id=target.id WHERE target.id=$1 AND job.id=$2`, targetID, jobID).Scan(&targetStatus, &jobStatus); err != nil {
		t.Fatal(err)
	}
	if targetStatus != "READY" || jobStatus != "READY" {
		t.Fatalf("current reconnect did not resume its waiting target: target=%s job=%s", targetStatus, jobStatus)
	}
}
