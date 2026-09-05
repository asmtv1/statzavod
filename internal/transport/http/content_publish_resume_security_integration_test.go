package httpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/statzavod/statzavod/internal/config"
)

func TestProviderResumeCapabilitiesAreEncryptedAndOpaqueIntegration(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable PostgreSQL database migrated through 00026")
	}
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := fmt.Sprintf("resume-secret-%d", time.Now().UnixNano())
	var org, company, user, creator, account, item, revision, target, job string
	mustScanID(t, pool, `INSERT INTO organizations(name,slug) VALUES('Resume secret',$1) RETURNING id`, &org, suffix)
	mustScanID(t, pool, `INSERT INTO companies(organization_id,name) VALUES($1,'Resume secret') RETURNING id`, &company, org)
	mustScanID(t, pool, `INSERT INTO users(email,password_hash,role,status) VALUES($1,'test','ADMIN','ACTIVE') RETURNING id`, &user, suffix+"@example.com")
	if _, err = pool.Exec(t.Context(), `INSERT INTO organization_memberships(organization_id,user_id,membership_role) VALUES($1,$2,'OWNER')`, org, user); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, pool, `INSERT INTO creators(organization_id,company_id,created_by,first_name,last_name,display_name) VALUES($1,$2,$3,'Resume','Secret','Resume Secret') RETURNING id`, &creator, org, company, user)
	mustScanID(t, pool, `INSERT INTO platform_accounts(organization_id,company_id,platform,external_id,username,display_name,status) VALUES($1,$2,'YOUTUBE',$3,$3,'Resume account','ACTIVE') RETURNING id`, &account, org, company, suffix)
	if _, err = pool.Exec(t.Context(), `INSERT INTO creator_account_assignments(creator_id,platform_account_id) VALUES($1,$2)`, creator, account); err != nil {
		t.Fatal(err)
	}
	mustScanID(t, pool, `INSERT INTO content_items(organization_id,company_id,creator_id,created_by) VALUES($1,$2,$3,$4) RETURNING id`, &item, org, company, creator, user)
	mustScanID(t, pool, `INSERT INTO content_revisions(content_item_id,organization_id,revision,created_by) VALUES($1,$2,1,$3) RETURNING id`, &revision, item, org, user)
	mustScanID(t, pool, `INSERT INTO content_publish_targets(content_revision_id,organization_id,company_id,creator_id,platform_account_id,platform) VALUES($1,$2,$3,$4,$5,'YOUTUBE') RETURNING id`, &target, revision, org, company, creator, account)
	mustScanID(t, pool, `INSERT INTO content_publish_jobs(target_id,content_revision_id,organization_id,company_id,run_at) VALUES($1,$2,$3,$4,now()) RETURNING id`, &job, target, revision, org, company)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM content_publish_attempts WHERE organization_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM content_publish_jobs WHERE organization_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM content_publish_targets WHERE organization_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM content_revisions WHERE organization_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM content_items WHERE organization_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM creator_account_assignments WHERE creator_id=$1`, creator)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_accounts WHERE organization_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM creators WHERE organization_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM organization_memberships WHERE organization_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM companies WHERE organization_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, user)
	})

	key := base64.StdEncoding.EncodeToString(bytesOf(32, 29))
	server := New(pool, config.Config{TokenEncryptionKey: key})
	request := PublishRequest{TargetID: target, OrganizationID: org}
	capabilityURL := "https://upload.youtube.com/resumable/session?upload_id=highly-secret-capability"
	payload, _ := json.Marshal(map[string]string{"sessionUrl": capabilityURL})
	op := "yt:session:" + makeToken()
	if err = server.persistProviderResume(t.Context(), request, op, "YOUTUBE", payload); err != nil {
		t.Fatal(err)
	}

	var storedOperation, ciphertextText, nonceText string
	if err = pool.QueryRow(t.Context(), `SELECT provider_operation_id,encode(provider_resume_ciphertext,'base64'),encode(provider_resume_nonce,'base64') FROM content_publish_targets WHERE id=$1`, target).Scan(&storedOperation, &ciphertextText, &nonceText); err != nil {
		t.Fatal(err)
	}
	reversible := base64.RawURLEncoding.EncodeToString([]byte(capabilityURL))
	for _, stored := range []string{storedOperation, ciphertextText, nonceText} {
		if strings.Contains(stored, capabilityURL) || strings.Contains(stored, reversible) || strings.Contains(stored, "upload_id") {
			t.Fatalf("resume capability is reversibly stored: %q", stored)
		}
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO content_publish_attempts(job_id,target_id,organization_id,attempt_number,status,provider_operation_id) VALUES($1,$2,$3,1,'RUNNING',$4)`, job, target, org, storedOperation); err != nil {
		t.Fatal(err)
	}
	var attemptOperation string
	if err = pool.QueryRow(t.Context(), `SELECT provider_operation_id FROM content_publish_attempts WHERE job_id=$1`, job).Scan(&attemptOperation); err != nil {
		t.Fatal(err)
	}
	if attemptOperation != storedOperation || strings.Contains(attemptOperation, "upload") {
		t.Fatalf("attempt checkpoint is not opaque: %q", attemptOperation)
	}
	loaded, err := server.loadProviderResume(t.Context(), PublishRequest{TargetID: target, OrganizationID: org, ProviderOperationID: op}, "YOUTUBE")
	if err != nil || string(loaded) != string(payload) {
		t.Fatalf("crash recovery payload=%s err=%v", loaded, err)
	}
	if _, err = server.loadProviderResume(t.Context(), PublishRequest{TargetID: target, OrganizationID: org, ProviderOperationID: op}, "VK"); err == nil {
		t.Fatal("cross-platform resume payload binding was accepted")
	}

	if _, err = pool.Exec(t.Context(), `UPDATE content_publish_targets SET provider_operation_id=NULL,provider_resume_ciphertext=NULL,provider_resume_nonce=NULL,provider_resume_key_version=NULL WHERE id=$1`, target); err != nil {
		t.Fatal(err)
	}
	vkCapabilityURL := "https://cs123.vkuser.net/upload?key=vk-secret-capability"
	vk := newVKPublishAdapter(server)
	vkOperation, err := vk.defaultVKPersistResume(t.Context(), request, vkUploadResume{Owner: -42, VideoID: 7, UploadURL: vkCapabilityURL})
	if err != nil || !strings.HasPrefix(vkOperation, "vk:upload:") || strings.Contains(vkOperation, vkCapabilityURL) || strings.Contains(vkOperation, base64.RawURLEncoding.EncodeToString([]byte(vkCapabilityURL))) {
		t.Fatalf("VK checkpoint operation=%q err=%v", vkOperation, err)
	}
	vkResume, err := vk.defaultVKLoadResume(t.Context(), PublishRequest{TargetID: target, OrganizationID: org, ProviderOperationID: vkOperation})
	if err != nil || vkResume.UploadURL != vkCapabilityURL || vkResume.Owner != -42 || vkResume.VideoID != 7 {
		t.Fatalf("VK crash recovery resume=%#v err=%v", vkResume, err)
	}
}
