package httpserver

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func (s *Server) listOwnCreatorProfiles(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	items := make([]map[string]any, 0, len(p.CreatorProfiles))
	for _, profile := range p.CreatorProfiles {
		items = append(items, map[string]any{
			"id": profile.ID, "companyId": profile.CompanyID, "companyName": profile.CompanyName,
			"displayName": profile.DisplayName, "active": p.ActiveCreatorID != nil && *p.ActiveCreatorID == profile.ID,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i]["companyName"].(string) == items[j]["companyName"].(string) {
			return items[i]["displayName"].(string) < items[j]["displayName"].(string)
		}
		return items[i]["companyName"].(string) < items[j]["companyName"].(string)
	})
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getOwnCreatorProfile(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	profile, ok := activeCreatorContext(p)
	if !ok {
		problem(w, http.StatusForbidden, "creator context required", "select one of your creator profiles")
		return
	}
	var first, last, middle, display, status, telegram, companyName, workStatus, workComment string
	err := s.pool.QueryRow(r.Context(), `
		SELECT creator.first_name,creator.last_name,COALESCE(creator.middle_name,''),creator.display_name,
		       creator.status,creator.telegram_username,company.name,creator.work_status,creator.work_comment
		FROM creators creator
		JOIN companies company ON company.id=creator.company_id AND company.organization_id=creator.organization_id
		WHERE creator.id=$1 AND creator.organization_id=$2 AND creator.login_user_id=$3
		  AND creator.company_id=$4 AND creator.archived_at IS NULL AND company.archived_at IS NULL`,
		profile.ID, p.OrganizationID, p.ID, profile.CompanyID,
	).Scan(&first, &last, &middle, &display, &status, &telegram, &companyName, &workStatus, &workComment)
	if err == pgx.ErrNoRows {
		problem(w, http.StatusNotFound, "not found", "active creator profile does not exist")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "profile failed", "could not load creator profile")
		return
	}
	contactRows, err := s.pool.Query(r.Context(), `
		SELECT contact.id,contact.kind,contact.value,COALESCE(contact.label,''),contact.is_primary
		FROM creator_contacts contact
		JOIN creators creator ON creator.id=contact.creator_id
		WHERE creator.id=$1 AND creator.organization_id=$2 AND creator.login_user_id=$3 AND creator.company_id=$4
		ORDER BY contact.is_primary DESC,contact.created_at`, profile.ID, p.OrganizationID, p.ID, profile.CompanyID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "profile failed", "could not load creator contacts")
		return
	}
	contacts := make([]map[string]any, 0)
	for contactRows.Next() {
		var id, kind, value, label string
		var primary bool
		if err = contactRows.Scan(&id, &kind, &value, &label, &primary); err != nil {
			contactRows.Close()
			problem(w, http.StatusInternalServerError, "profile failed", "could not read creator contacts")
			return
		}
		contacts = append(contacts, map[string]any{"id": id, "kind": kind, "value": value, "label": label, "isPrimary": primary})
	}
	if err = contactRows.Err(); err != nil {
		contactRows.Close()
		problem(w, http.StatusInternalServerError, "profile failed", "could not finish reading creator contacts")
		return
	}
	contactRows.Close()
	writeJSON(w, http.StatusOK, map[string]any{
		"id": profile.ID, "companyId": profile.CompanyID, "companyName": companyName,
		"firstName": first, "lastName": last, "middleName": middle, "displayName": display,
		"status": status, "telegramUsername": telegram, "workStatus": workStatus, "workComment": workComment,
		"contacts": contacts,
	})
}

func (s *Server) listOwnSocials(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	profile, ok := activeCreatorContext(p)
	if !ok {
		problem(w, http.StatusForbidden, "creator context required", "select one of your creator profiles")
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT account.id,account.platform,account.username,account.display_name,account.status,
		       COALESCE(account.avatar_url,''),COALESCE(account.profile_url,''),account.last_synced_at,account.metadata
		FROM creator_account_assignments assignment
		JOIN creators creator ON creator.id=assignment.creator_id AND creator.organization_id=assignment.organization_id
		JOIN platform_accounts account ON account.id=assignment.platform_account_id AND account.organization_id=assignment.organization_id
		WHERE creator.id=$1 AND creator.organization_id=$2 AND creator.login_user_id=$3 AND creator.company_id=$4
		  AND account.company_id=creator.company_id AND assignment.valid_to IS NULL AND account.status<>'DISCONNECTED'
		ORDER BY account.platform,account.username`, profile.ID, p.OrganizationID, p.ID, profile.CompanyID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "socials failed", "could not load assigned socials")
		return
	}
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, platform, username, displayName, status, avatarURL, profileURL string
		var lastSyncedAt *time.Time
		var metadata []byte
		if err = rows.Scan(&id, &platform, &username, &displayName, &status, &avatarURL, &profileURL, &lastSyncedAt, &metadata); err != nil {
			rows.Close()
			problem(w, http.StatusInternalServerError, "socials failed", "could not read assigned social")
			return
		}
		metadataValues := map[string]any{}
		if len(metadata) > 0 {
			if err = json.Unmarshal(metadata, &metadataValues); err != nil {
				rows.Close()
				problem(w, http.StatusInternalServerError, "socials failed", "could not read social metadata")
				return
			}
		}
		items = append(items, map[string]any{
			"id": id, "platform": platform, "username": username, "displayName": displayName,
			"status": status, "avatarUrl": avatarURL, "profileUrl": profileURL, "lastSyncedAt": lastSyncedAt,
			"bioDescription": metadataValues["bioDescription"], "isVerified": metadataValues["isVerified"],
		})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		problem(w, http.StatusInternalServerError, "socials failed", "could not finish reading assigned socials")
		return
	}
	rows.Close()

	var vkID, communityURL, recipientURL, status string
	err = s.pool.QueryRow(r.Context(), `
		SELECT account.id,assignment.community_url,assignment.recipient_account_url,
		       CASE WHEN account.platform_account_id IS NULL THEN 'CREDENTIALS' ELSE COALESCE(platform.status::text,'REAUTH_REQUIRED') END
		FROM creator_vk_assignments assignment
		JOIN creators creator ON creator.id=assignment.creator_id AND creator.organization_id=assignment.organization_id
		JOIN company_vk_accounts account ON account.id=assignment.company_vk_account_id
		  AND account.organization_id=creator.organization_id AND account.company_id=creator.company_id
		LEFT JOIN platform_accounts platform ON platform.id=account.platform_account_id
		  AND platform.organization_id=account.organization_id AND platform.company_id=account.company_id
		WHERE creator.id=$1 AND creator.organization_id=$2 AND creator.login_user_id=$3 AND creator.company_id=$4`,
		profile.ID, p.OrganizationID, p.ID, profile.CompanyID,
	).Scan(&vkID, &communityURL, &recipientURL, &status)
	if err != nil && err != pgx.ErrNoRows {
		problem(w, http.StatusInternalServerError, "socials failed", "could not load assigned VK social")
		return
	}
	if err == nil {
		items = append(items, map[string]any{
			"id": "company-vk:" + vkID, "platform": "VK", "username": "", "displayName": "VK",
			"status": status, "profileUrl": communityURL, "communityUrl": communityURL, "recipientAccountUrl": recipientURL,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) listOwnCredentials(w http.ResponseWriter, r *http.Request) {
	if s.envelope == nil {
		problem(w, http.StatusServiceUnavailable, "encryption unavailable", "TOKEN_ENCRYPTION_KEY is not configured")
		return
	}
	p := r.Context().Value(principalKey).(principal)
	profile, ok := activeCreatorContext(p)
	if !ok {
		problem(w, http.StatusForbidden, "creator context required", "select one of your creator profiles")
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT credential.id,credential.section,credential.field_key,credential.is_secret,
		       credential.value_ciphertext,credential.value_nonce,credential.updated_at
		FROM creator_credentials credential
		JOIN creators creator ON creator.id=credential.creator_id
		JOIN companies company ON company.id=creator.company_id AND company.organization_id=creator.organization_id
		WHERE creator.id=$1 AND creator.organization_id=$2 AND creator.login_user_id=$3 AND creator.company_id=$4
		  AND creator.archived_at IS NULL AND company.archived_at IS NULL
		ORDER BY credential.section,credential.field_key`, profile.ID, p.OrganizationID, p.ID, profile.CompanyID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "credentials failed", "could not load credentials")
		return
	}
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, section, fieldKey string
		var secret bool
		var ciphertext, nonce []byte
		var updatedAt time.Time
		if err = rows.Scan(&id, &section, &fieldKey, &secret, &ciphertext, &nonce, &updatedAt); err != nil {
			rows.Close()
			problem(w, http.StatusInternalServerError, "credentials failed", "could not read credential")
			return
		}
		item := map[string]any{"id": id, "section": section, "fieldKey": fieldKey, "isSecret": secret, "hasValue": true, "updatedAt": updatedAt}
		if !secret {
			plain, decryptErr := s.envelope.Decrypt(ciphertext, nonce)
			if decryptErr != nil {
				rows.Close()
				problem(w, http.StatusInternalServerError, "credentials failed", "could not decrypt credential")
				return
			}
			item["value"] = string(plain)
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		problem(w, http.StatusInternalServerError, "credentials failed", "could not finish reading credentials")
		return
	}
	rows.Close()

	var vkID string
	var loginCiphertext, loginNonce, passwordCiphertext, passwordNonce, phoneCiphertext, phoneNonce []byte
	var updatedAt time.Time
	err = s.pool.QueryRow(r.Context(), `
		SELECT account.id,account.login_ciphertext,account.login_nonce,account.password_ciphertext,account.password_nonce,
		       account.phone_ciphertext,account.phone_nonce,account.updated_at
		FROM creator_vk_assignments assignment
		JOIN creators creator ON creator.id=assignment.creator_id AND creator.organization_id=assignment.organization_id
		JOIN company_vk_accounts account ON account.id=assignment.company_vk_account_id
		  AND account.organization_id=creator.organization_id AND account.company_id=creator.company_id
		WHERE creator.id=$1 AND creator.organization_id=$2 AND creator.login_user_id=$3 AND creator.company_id=$4`,
		profile.ID, p.OrganizationID, p.ID, profile.CompanyID,
	).Scan(&vkID, &loginCiphertext, &loginNonce, &passwordCiphertext, &passwordNonce, &phoneCiphertext, &phoneNonce, &updatedAt)
	if err != nil && err != pgx.ErrNoRows {
		problem(w, http.StatusInternalServerError, "credentials failed", "could not load assigned VK credentials")
		return
	}
	if err == nil {
		for _, value := range []struct {
			key        string
			secret     bool
			ciphertext []byte
			nonce      []byte
		}{
			{"login", false, loginCiphertext, loginNonce},
			{"password", true, passwordCiphertext, passwordNonce},
			{"phone", false, phoneCiphertext, phoneNonce},
		} {
			if len(value.ciphertext) == 0 {
				continue
			}
			item := map[string]any{"id": "company-vk:" + vkID + ":" + value.key, "section": "VK", "fieldKey": value.key, "isSecret": value.secret, "hasValue": true, "updatedAt": updatedAt}
			if !value.secret {
				plain, decryptErr := s.envelope.Decrypt(value.ciphertext, value.nonce)
				if decryptErr != nil {
					problem(w, http.StatusInternalServerError, "credentials failed", "could not decrypt assigned VK credential")
					return
				}
				item["value"] = string(plain)
			}
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) revealOwnCredential(w http.ResponseWriter, r *http.Request) {
	if s.envelope == nil {
		problem(w, http.StatusServiceUnavailable, "encryption unavailable", "TOKEN_ENCRYPTION_KEY is not configured")
		return
	}
	p := r.Context().Value(principalKey).(principal)
	profile, ok := activeCreatorContext(p)
	if !ok {
		problem(w, http.StatusForbidden, "creator context required", "select one of your creator profiles")
		return
	}
	credentialID := chi.URLParam(r, "credentialID")
	var ciphertext, nonce []byte
	auditCredentialID := credentialID
	if strings.HasPrefix(credentialID, "company-vk:") {
		parts := strings.Split(credentialID, ":")
		if len(parts) != 3 || parts[2] != "password" {
			problem(w, http.StatusNotFound, "not found", "credential does not exist")
			return
		}
		err := s.pool.QueryRow(r.Context(), `
			SELECT account.password_ciphertext,account.password_nonce
			FROM creator_vk_assignments assignment
			JOIN creators creator ON creator.id=assignment.creator_id AND creator.organization_id=assignment.organization_id
			JOIN company_vk_accounts account ON account.id=assignment.company_vk_account_id
			  AND account.organization_id=creator.organization_id AND account.company_id=creator.company_id
			JOIN companies company ON company.id=creator.company_id AND company.organization_id=creator.organization_id
			WHERE account.id=$1 AND creator.id=$2 AND creator.organization_id=$3 AND creator.login_user_id=$4
			  AND creator.company_id=$5 AND creator.archived_at IS NULL AND company.archived_at IS NULL
			  AND account.password_ciphertext IS NOT NULL`, parts[1], profile.ID, p.OrganizationID, p.ID, profile.CompanyID).Scan(&ciphertext, &nonce)
		if err == pgx.ErrNoRows {
			problem(w, http.StatusNotFound, "not found", "credential does not exist")
			return
		}
		if err != nil {
			problem(w, http.StatusInternalServerError, "reveal failed", "could not load credential")
			return
		}
	} else {
		err := s.pool.QueryRow(r.Context(), `
			SELECT credential.value_ciphertext,credential.value_nonce
			FROM creator_credentials credential
			JOIN creators creator ON creator.id=credential.creator_id
			JOIN companies company ON company.id=creator.company_id AND company.organization_id=creator.organization_id
			WHERE credential.id=$1 AND credential.is_secret=true AND creator.id=$2
			  AND creator.organization_id=$3 AND creator.login_user_id=$4 AND creator.company_id=$5
			  AND creator.archived_at IS NULL AND company.archived_at IS NULL`, credentialID, profile.ID, p.OrganizationID, p.ID, profile.CompanyID).Scan(&ciphertext, &nonce)
		if err == pgx.ErrNoRows {
			problem(w, http.StatusNotFound, "not found", "credential does not exist")
			return
		}
		if err != nil {
			problem(w, http.StatusInternalServerError, "reveal failed", "could not load credential")
			return
		}
	}
	plain, err := s.envelope.Decrypt(ciphertext, nonce)
	if err != nil {
		problem(w, http.StatusInternalServerError, "reveal failed", "could not decrypt credential")
		return
	}
	if err = s.writeAudit(r.Context(), s.pool, requestAuditRecord(r, p, &profile.CompanyID, "CREATOR_PORTAL_REVEAL", "CREATOR", &profile.ID, http.StatusOK, map[string]any{"credentialId": auditCredentialID})); err != nil {
		problem(w, http.StatusInternalServerError, "reveal failed", "could not write reveal audit record")
		return
	}
	markResponseAuditCommitted(w)
	writeJSON(w, http.StatusOK, map[string]string{"value": string(plain)})
}

func (s *Server) listOwnPublications(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	profile, ok := activeCreatorContext(p)
	if !ok {
		problem(w, http.StatusForbidden, "creator context required", "select one of your creator profiles")
		return
	}
	period, err := parseExportPeriod(r)
	if err != nil {
		problem(w, http.StatusBadRequest, "invalid period", err.Error())
		return
	}
	query := `
		SELECT publication.id,COALESCE(publication.title,''),publication.platform,publication.published_at,
		       COALESCE(publication.thumbnail_url,''),publication.external_id,COALESCE(publication.permalink,''),
		       CASE WHEN publication.content_publish_target_id IS NULL AND publication.status='ACTIVE' THEN 'SUCCEEDED' ELSE COALESCE(target.status,'') END,
		       COALESCE(metric.views,0),COALESCE(metric.likes,0),COALESCE(metric.comments,0),COALESCE(metric.shares,0)
		FROM publications publication
		LEFT JOIN content_publish_targets target ON target.id=publication.content_publish_target_id AND target.organization_id=publication.organization_id
		JOIN creators creator ON creator.id=publication.creator_id AND creator.organization_id=publication.organization_id
		JOIN companies company ON company.id=creator.company_id AND company.organization_id=creator.organization_id
		LEFT JOIN LATERAL (
			SELECT views,likes,comments,shares FROM publication_metric_snapshots
			WHERE publication_id=publication.id ORDER BY observed_at DESC LIMIT 1
		) metric ON true
		WHERE creator.id=$1 AND creator.organization_id=$2 AND creator.login_user_id=$3 AND creator.company_id=$4
		  AND creator.archived_at IS NULL AND company.archived_at IS NULL`
	args := []any{profile.ID, p.OrganizationID, p.ID, profile.CompanyID}
	query, args = addPeriodSQL(query, "publication", args, period)
	query += ` ORDER BY publication.published_at DESC,publication.id LIMIT 100`
	rows, err := s.pool.Query(r.Context(), query, args...)
	if err != nil {
		problem(w, http.StatusInternalServerError, "publications failed", "could not load publications")
		return
	}
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, title, platform, thumbnailURL, externalID, rawPermalink, targetStatus string
		var publishedAt time.Time
		var views, likes, comments, shares int64
		if err = rows.Scan(&id, &title, &platform, &publishedAt, &thumbnailURL, &externalID, &rawPermalink, &targetStatus, &views, &likes, &comments, &shares); err != nil {
			rows.Close()
			problem(w, http.StatusInternalServerError, "publications failed", "could not read publication")
			return
		}
		_, permalink := sanitizeSucceededPublication(platform, targetStatus, externalID, rawPermalink)
		items = append(items, map[string]any{
			"id": id, "title": title, "platform": platform, "publishedAt": publishedAt,
			"thumbnailUrl": thumbnailURL, "permalink": permalink,
			"views": views, "likes": likes, "comments": comments, "shares": shares,
		})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		problem(w, http.StatusInternalServerError, "publications failed", "could not finish reading publications")
		return
	}
	rows.Close()
	writeJSON(w, http.StatusOK, map[string]any{"creatorId": profile.ID, "period": periodResponse(period), "items": items})
}

func (s *Server) getOwnStats(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	profile, ok := activeCreatorContext(p)
	if !ok {
		problem(w, http.StatusForbidden, "creator context required", "select one of your creator profiles")
		return
	}
	period, err := parseExportPeriod(r)
	if err != nil {
		problem(w, http.StatusBadRequest, "invalid period", err.Error())
		return
	}
	query := `
		SELECT count(DISTINCT publication.id),COALESCE(sum(metric.views),0),COALESCE(sum(metric.likes),0),
		       COALESCE(sum(metric.comments),0),COALESCE(sum(metric.shares),0)
		FROM creators creator
		JOIN companies company ON company.id=creator.company_id AND company.organization_id=creator.organization_id
		LEFT JOIN publications publication ON publication.creator_id=creator.id AND publication.organization_id=creator.organization_id
		LEFT JOIN LATERAL (
			SELECT views,likes,comments,shares FROM publication_metric_snapshots
			WHERE publication_id=publication.id ORDER BY observed_at DESC LIMIT 1
		) metric ON true
		WHERE creator.id=$1 AND creator.organization_id=$2 AND creator.login_user_id=$3 AND creator.company_id=$4
		  AND creator.archived_at IS NULL AND company.archived_at IS NULL`
	args := []any{profile.ID, p.OrganizationID, p.ID, profile.CompanyID}
	query, args = addPeriodSQL(query, "publication", args, period)
	var publications, views, likes, comments, shares int64
	if err = s.pool.QueryRow(r.Context(), query, args...).Scan(&publications, &views, &likes, &comments, &shares); err == pgx.ErrNoRows {
		problem(w, http.StatusNotFound, "not found", "active creator profile does not exist")
		return
	} else if err != nil {
		problem(w, http.StatusInternalServerError, "stats failed", "could not load creator statistics")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"creatorId": profile.ID, "creatorName": profile.DisplayName, "period": periodResponse(period),
		"kpis": []map[string]any{
			{"key": "views", "label": localized(r, "Просмотры", "Views"), "value": views},
			{"key": "likes", "label": localized(r, "Реакции", "Reactions"), "value": likes},
			{"key": "comments", "label": localized(r, "Комментарии", "Comments"), "value": comments},
			{"key": "shares", "label": localized(r, "Репосты", "Shares"), "value": shares},
			{"key": "publications", "label": localized(r, "Публикации", "Publications"), "value": publications},
		},
	})
}

func (s *Server) exportOwnCreator(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	profile, ok := activeCreatorContext(p)
	if !ok {
		problem(w, http.StatusForbidden, "creator context required", "select one of your creator profiles")
		return
	}
	s.writeCreatorExport(w, r, p, []string{profile.ID}, "my-creator-report.xlsx")
}
