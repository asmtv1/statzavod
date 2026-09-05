package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

var errTransactionalPublishPreflight = errors.New("publishing target is not ready")

// publishAdapter is intentionally small: every platform must preflight,
// start/poll a provider operation and return a redacted outcome. Concrete API
// adapters may evolve independently without changing queue semantics.
type publishAdapter interface {
	Preflight(platformAccountID string, scopes []string) platformPreflight
}
type platformPreflight struct {
	Compatible    bool     `json:"compatible"`
	Warnings      []string `json:"warnings"`
	Required      []string `json:"requiredFields"`
	Privacy       []string `json:"allowedPrivacy"`
	Reauth        bool     `json:"requiresReauth"`
	MissingScopes []string `json:"missingScopes,omitempty"`
	Reconnect     string   `json:"reconnectAction,omitempty"`
}
type scopePreflightAdapter struct {
	platform       string
	requiredScopes []string
	requiredFields []string
	privacy        []string
}

func (a scopePreflightAdapter) Preflight(_ string, scopes []string) platformPreflight {
	have := map[string]bool{}
	for _, scope := range scopes {
		have[strings.TrimSpace(scope)] = true
	}
	missing := make([]string, 0)
	for _, scope := range a.requiredScopes {
		if !have[scope] {
			missing = append(missing, scope)
		}
	}
	result := platformPreflight{Compatible: len(missing) == 0, Required: a.requiredFields, Privacy: a.privacy}
	if len(missing) != 0 {
		result.Reauth = true
		result.MissingScopes = missing
		result.Reconnect = "RECONNECT_PLATFORM_ACCOUNT"
		result.Warnings = []string{"Publishing permission is missing; reconnect this account."}
	}
	return result
}

// publishingReadiness combines the scope contract with the local connection
// lifecycle. A revoked or absent connection is never publish-ready, even if an
// old account row still happens to retain a previous scopes array.
func publishingReadiness(platform, platformAccountID, accountStatus, connectionStatus string, scopes []string) platformPreflight {
	result := contentAdapter(platform).Preflight(platformAccountID, scopes)
	if accountStatus == "ACTIVE" && connectionStatus == "ACTIVE" {
		return result
	}
	result.Compatible = false
	result.Reauth = true
	result.Reconnect = "RECONNECT_PLATFORM_ACCOUNT"
	if connectionStatus == "REVOKED" || connectionStatus == "DISCONNECTED" || accountStatus == "DISCONNECTED" {
		result.Warnings = append(result.Warnings, "The connection was revoked and cannot be selected for publishing.")
	} else {
		result.Warnings = append(result.Warnings, "The connected account is not ready for publishing.")
	}
	return result
}
func instagramPublishingReadiness(accountType, connectionMode, accountStatus, connectionStatus string, scopes []string) platformPreflight {
	requiredScope := "instagram_business_content_publish" // Instagram Login
	if strings.EqualFold(connectionMode, "FACEBOOK") {
		requiredScope = "instagram_content_publish" // Facebook Login for Business
	}
	result := scopePreflightAdapter{platform: "INSTAGRAM", requiredScopes: []string{requiredScope}, requiredFields: []string{"caption"}}.Preflight("", scopes)
	if !instagramProfessional(accountType) {
		result.Compatible = false
		result.Reauth = false
		result.Reconnect = ""
		result.Warnings = append(result.Warnings, "Instagram publishing requires a Professional Business or Creator account.")
	}
	if accountStatus != "ACTIVE" || connectionStatus != "ACTIVE" {
		result.Compatible, result.Reauth, result.Reconnect = false, true, "RECONNECT_PLATFORM_ACCOUNT"
		result.Warnings = append(result.Warnings, "The connected account is not ready for publishing.")
	}
	return result
}
func contentAdapter(platform string) publishAdapter {
	switch platform {
	case "TIKTOK":
		return scopePreflightAdapter{platform: platform, requiredScopes: []string{"video.publish"}, requiredFields: []string{"privacy", "musicUsageConfirmation", "commercialDisclosure"}}
	case "INSTAGRAM":
		return scopePreflightAdapter{platform: platform, requiredScopes: []string{"instagram_content_publish"}, requiredFields: []string{"caption"}}
	case "YOUTUBE":
		return scopePreflightAdapter{platform: platform, requiredScopes: []string{"https://www.googleapis.com/auth/youtube.upload"}, requiredFields: []string{"title", "description", "categoryId", "privacyStatus", "madeForKids", "notifySubscribers"}, privacy: []string{"private", "unlisted", "public"}}
	case "VK":
		return scopePreflightAdapter{platform: platform, requiredScopes: []string{"video"}, requiredFields: []string{"owner", "title", "description"}, privacy: []string{"public", "private"}}
	default:
		return scopePreflightAdapter{platform: platform, requiredScopes: []string{"__unsupported__"}}
	}
}

// transactionalPublishPreflight is the final authorization/readiness fence
// before a queue row is created. It deliberately re-reads mutable OAuth,
// account and assignment state inside the command transaction and holds locks
// on every row that can revoke publishability until the job insert commits.
// The revision is already locked by transitionContent; this function locks the
// selected target rows as well.
func (s *Server) transactionalPublishPreflight(ctx context.Context, tx pgx.Tx, revisionID, organizationID string, selectedTargetIDs []string, eligibleStatuses []string) error {
	query := `
		SELECT target.id::text,target.creator_id::text,target.company_id::text,
		       target.platform_account_id::text,target.platform::text
		FROM content_publish_targets target
		WHERE target.content_revision_id=$1 AND target.organization_id=$2
		  AND target.status=ANY($3::text[])`
	args := []any{revisionID, organizationID, eligibleStatuses}
	if len(selectedTargetIDs) > 0 {
		query += ` AND target.id=ANY($4::uuid[])`
		args = append(args, selectedTargetIDs)
	}
	query += ` ORDER BY target.id FOR UPDATE OF target`

	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	type target struct{ id, creatorID, companyID, accountID, platform string }
	targets := make([]target, 0)
	for rows.Next() {
		var item target
		if err = rows.Scan(&item.id, &item.creatorID, &item.companyID, &item.accountID, &item.platform); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(targets) == 0 || (len(selectedTargetIDs) > 0 && len(targets) != len(selectedTargetIDs)) {
		return errTransactionalPublishPreflight
	}

	for _, target := range targets {
		if !s.contentPlatformPublishingEnabled(target.platform) {
			return errTransactionalPublishPreflight
		}
		var accountStatus, connectionStatus, accountType, connectionMode string
		var scopes []string
		err = tx.QueryRow(ctx, `
			SELECT account.status::text,connection.status,
			       COALESCE(connection.scopes,'{}'::text[]),
			       COALESCE(account.account_type,''),
			       COALESCE(account.metadata->>'connectionMode','')
			FROM platform_accounts account
			JOIN oauth_connections connection
			  ON connection.platform_account_id=account.id
			 AND connection.organization_id=account.organization_id
			WHERE account.id=$1 AND account.organization_id=$2
			FOR UPDATE OF account,connection`, target.accountID, organizationID).
			Scan(&accountStatus, &connectionStatus, &scopes, &accountType, &connectionMode)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errTransactionalPublishPreflight
			}
			return err
		}
		readiness := publishingReadiness(target.platform, target.accountID, accountStatus, connectionStatus, scopes)
		if target.platform == "INSTAGRAM" {
			readiness = instagramPublishingReadiness(accountType, connectionMode, accountStatus, connectionStatus, scopes)
		}
		if !readiness.Compatible {
			return errTransactionalPublishPreflight
		}

		// Normal creator accounts require a currently effective assignment.
		// Company-owned VK accounts retain their intentional company binding.
		var bindingID string
		err = tx.QueryRow(ctx, `
			SELECT assignment.id::text
			FROM creator_account_assignments assignment
			WHERE assignment.creator_id=$1
			  AND assignment.platform_account_id=$2
			  AND assignment.organization_id=$3
			  AND assignment.valid_from<=now()
			  AND (assignment.valid_to IS NULL OR assignment.valid_to>now())
			FOR UPDATE OF assignment`, target.creatorID, target.accountID, organizationID).Scan(&bindingID)
		if errors.Is(err, pgx.ErrNoRows) && target.platform == "VK" {
			err = tx.QueryRow(ctx, `
				SELECT vk.id::text
				FROM company_vk_accounts vk
				WHERE vk.platform_account_id=$1 AND vk.organization_id=$2 AND vk.company_id=$3
				FOR UPDATE OF vk`, target.accountID, organizationID, target.companyID).Scan(&bindingID)
		}
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errTransactionalPublishPreflight
			}
			return err
		}
	}
	return nil
}

func (s *Server) contentPlatformPublishingEnabled(platform string) bool {
	if !s.config.ContentPublishingEnabled {
		return false
	}
	switch platform {
	case "TIKTOK":
		return s.config.ContentTikTokEnabled
	case "INSTAGRAM":
		return s.config.ContentInstagramEnabled
	case "YOUTUBE":
		return s.config.ContentYouTubeEnabled
	case "VK":
		return s.config.ContentVKEnabled
	default:
		return false
	}
}

func (s *Server) enabledContentPublishingPlatforms() []string {
	platforms := make([]string, 0, 4)
	for _, platform := range publishingMetricPlatforms {
		if s.contentPlatformPublishingEnabled(platform) {
			platforms = append(platforms, platform)
		}
	}
	return platforms
}

func (s *Server) preflightContentItem(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	id := contentItemID(r)
	_, companyID, revision, code := s.assertContentItem(r.Context(), p, id, "CONTENT_VIEW")
	if code != 0 {
		problem(w, code, "not found", "content item is not available")
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT target.id::text,target.platform::text,target.platform_account_id::text,account.status::text,COALESCE(connection.status,''),COALESCE(connection.scopes,'{}'::text[]),COALESCE(account.account_type,''),COALESCE(account.metadata->>'connectionMode','') FROM content_publish_targets target JOIN platform_accounts account ON account.id=target.platform_account_id AND account.organization_id=target.organization_id LEFT JOIN oauth_connections connection ON connection.platform_account_id=account.id AND connection.organization_id=account.organization_id WHERE target.content_revision_id=(SELECT id FROM content_revisions WHERE content_item_id=$1 AND organization_id=$2 AND revision=$3)`, id, p.OrganizationID, revision)
	if err != nil {
		problem(w, 500, "preflight failed", "could not load publishing targets")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var targetID, platform, account, accountStatus, connectionStatus, accountType, connectionMode string
		var scopes []string
		if err = rows.Scan(&targetID, &platform, &account, &accountStatus, &connectionStatus, &scopes, &accountType, &connectionMode); err != nil {
			problem(w, 500, "preflight failed", "could not read publishing target")
			return
		}
		result := publishingReadiness(platform, account, accountStatus, connectionStatus, scopes)
		if platform == "INSTAGRAM" {
			result = instagramPublishingReadiness(accountType, connectionMode, accountStatus, connectionStatus, scopes)
		}
		if platform == "TIKTOK" && result.Compatible {
			creator, creatorErr := s.tiktokCreatorPreflight(r.Context(), p.OrganizationID, companyID, account)
			if creatorErr != nil {
				result.Compatible = false
				result.Warnings = append(result.Warnings, "TikTok creator information is unavailable; refresh before publishing.")
			} else {
				result.Privacy = creator.PrivacyLevelOptions
				result.Required = append(result.Required, "explicitConsent", "commentsEnabled", "duetEnabled", "stitchEnabled")
				if creator.CommentDisabled {
					result.Required = append(result.Required, "commentsDisabledByTikTok")
				}
				if creator.DuetDisabled {
					result.Required = append(result.Required, "duetDisabledByTikTok")
				}
				if creator.StitchDisabled {
					result.Required = append(result.Required, "stitchDisabledByTikTok")
				}
			}
		}
		items = append(items, map[string]any{"targetId": targetID, "platform": platform, "platformLabel": map[string]string{"VK": "VK Video"}[platform], "preflight": result})
	}
	if err = rows.Err(); err != nil {
		problem(w, 500, "preflight failed", "could not finish preflight")
		return
	}
	if err := s.commitAuditOnly(r.Context(), w, requestAuditRecord(r, p, &companyID, "PREFLIGHT_CONTENT_ITEM", "CONTENT_ITEM", &id, http.StatusOK, map[string]any{"revision": revision, "targetCount": len(items)})); err != nil {
		problem(w, http.StatusInternalServerError, "audit failed", "could not record content preflight")
		return
	}
	writeJSON(w, 200, map[string]any{"contentItemId": id, "revision": revision, "targets": items})
}

// composerPreflight exposes only publishability metadata for selected existing
// accounts. It is deliberately available before a draft exists so the browser
// never invents TikTok privacy or interaction switches.
func (s *Server) composerPreflight(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	var in struct {
		CreatorID  string   `json:"creatorId"`
		AccountIDs []string `json:"accountIds"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil || len(in.AccountIDs) == 0 || len(in.AccountIDs) > 20 {
		problem(w, http.StatusBadRequest, "invalid preflight", "accountIds is required")
		return
	}
	creatorID, _, code := contentRouteCreator(r, p, in.CreatorID)
	if code != 0 {
		problem(w, code, "forbidden", "creator scope is required")
		return
	}
	if p.Role != roleCreator && s.authorizeCreatorAction(r.Context(), p, creatorID, "CONTENT_VIEW") != 0 {
		problem(w, http.StatusNotFound, "not found", "creator is not available")
		return
	}
	items := make([]map[string]any, 0, len(in.AccountIDs))
	for _, accountID := range in.AccountIDs {
		var platform, accountStatus, connectionStatus, accountType, connectionMode, companyID string
		var scopes []string
		err := s.pool.QueryRow(r.Context(), `
			SELECT a.platform::text,a.status::text,COALESCE(o.status,''),COALESCE(o.scopes,'{}'::text[]),COALESCE(a.account_type,''),COALESCE(a.metadata->>'connectionMode',''),a.company_id::text
			FROM platform_accounts a
			JOIN creator_account_assignments assignment
			  ON assignment.platform_account_id=a.id AND assignment.organization_id=a.organization_id
			 AND assignment.creator_id=$2 AND assignment.valid_to IS NULL
			JOIN creators creator ON creator.id=assignment.creator_id AND creator.organization_id=assignment.organization_id AND creator.company_id=a.company_id
			LEFT JOIN oauth_connections o ON o.platform_account_id=a.id AND o.organization_id=a.organization_id
			WHERE a.id=$1 AND a.organization_id=$3`, accountID, creatorID, p.OrganizationID).Scan(&platform, &accountStatus, &connectionStatus, &scopes, &accountType, &connectionMode, &companyID)
		if err != nil {
			problem(w, http.StatusNotFound, "not found", "account is not available")
			return
		}
		result := publishingReadiness(platform, accountID, accountStatus, connectionStatus, scopes)
		if platform == "INSTAGRAM" {
			result = instagramPublishingReadiness(accountType, connectionMode, accountStatus, connectionStatus, scopes)
		}
		entry := map[string]any{"accountId": accountID, "platform": platform, "preflight": result}
		if platform == "TIKTOK" {
			capability := map[string]any{"privacy": []string{}, "commentsDisabled": true, "duetDisabled": true, "stitchDisabled": true, "warnings": result.Warnings, "requiredFields": result.Required, "reconnectAction": result.Reconnect, "compatible": result.Compatible}
			if result.Compatible {
				creator, creatorErr := s.tiktokCreatorPreflight(r.Context(), p.OrganizationID, companyID, accountID)
				if creatorErr != nil {
					result.Compatible = false
					result.Warnings = append(result.Warnings, "TikTok creator information is unavailable; refresh before publishing.")
					capability["warnings"] = result.Warnings
					capability["compatible"] = false
				} else {
					capability["privacy"] = creator.PrivacyLevelOptions
					capability["commentsDisabled"] = creator.CommentDisabled
					capability["duetDisabled"] = creator.DuetDisabled
					capability["stitchDisabled"] = creator.StitchDisabled
				}
			}
			entry["preflight"] = result
			entry["preflight"] = map[string]any{"compatible": result.Compatible, "warnings": result.Warnings, "requiredFields": result.Required, "allowedPrivacy": result.Privacy, "requiresReauth": result.Reauth, "reconnectAction": result.Reconnect, "tiktok": capability}
		}
		items = append(items, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": items})
}
