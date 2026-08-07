package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type instagramSelectionAccount struct {
	ID               string `json:"id"`
	Username         string `json:"username"`
	DisplayName      string `json:"displayName"`
	AvatarURL        string `json:"avatarUrl"`
	FacebookPageName string `json:"facebookPageName"`
	ConnectionState  string `json:"connectionState"`
	ConnectedCreator string `json:"connectedCreator,omitempty"`
	Selectable       bool   `json:"selectable"`
}

type instagramAssignment struct {
	CreatorID   string
	CreatorName string
}

type pgxQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (s *Server) createInstagramAccountSelection(ctx context.Context, organizationID, creatorID, initiatedBy string, candidates []instagramFacebookCandidate) (string, error) {
	if s.envelope == nil || len(candidates) == 0 {
		return "", errors.New("Instagram account selection is incomplete")
	}
	payload, err := json.Marshal(candidates)
	if err != nil {
		return "", err
	}
	ciphertext, nonce, err := s.envelope.Encrypt(payload)
	if err != nil {
		return "", err
	}
	_, _ = s.pool.Exec(ctx, `DELETE FROM oauth_account_selections WHERE expires_at<now() OR consumed_at<now()-interval '1 day'`)
	var selectionID string
	err = s.pool.QueryRow(ctx, `INSERT INTO oauth_account_selections(organization_id,creator_id,initiated_by,platform,payload_ciphertext,nonce,expires_at) VALUES($1,$2,$3,'INSTAGRAM',$4,$5,now()+interval '10 minutes') RETURNING id`, organizationID, creatorID, initiatedBy, ciphertext, nonce).Scan(&selectionID)
	return selectionID, err
}

func validSelectionID(value string) bool {
	var id pgtype.UUID
	return id.Scan(value) == nil && id.Valid
}

func (s *Server) loadInstagramSelection(ctx context.Context, organizationID, creatorID, initiatedBy, selectionID string, forUpdate bool, tx pgx.Tx) ([]instagramFacebookCandidate, time.Time, error) {
	if !validSelectionID(selectionID) || s.envelope == nil {
		return nil, time.Time{}, pgx.ErrNoRows
	}
	query := `SELECT payload_ciphertext,nonce,expires_at FROM oauth_account_selections WHERE id=$1 AND organization_id=$2 AND creator_id=$3 AND initiated_by=$4 AND platform='INSTAGRAM' AND consumed_at IS NULL AND expires_at>now()`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var ciphertext, nonce []byte
	var expiresAt time.Time
	var err error
	if tx != nil {
		err = tx.QueryRow(ctx, query, selectionID, organizationID, creatorID, initiatedBy).Scan(&ciphertext, &nonce, &expiresAt)
	} else {
		err = s.pool.QueryRow(ctx, query, selectionID, organizationID, creatorID, initiatedBy).Scan(&ciphertext, &nonce, &expiresAt)
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	payload, err := s.envelope.Decrypt(ciphertext, nonce)
	if err != nil {
		return nil, time.Time{}, err
	}
	var candidates []instagramFacebookCandidate
	if err = json.Unmarshal(payload, &candidates); err != nil || len(candidates) == 0 {
		return nil, time.Time{}, errors.New("Instagram account selection payload is invalid")
	}
	return candidates, expiresAt, nil
}

func instagramCandidateIDs(candidates []instagramFacebookCandidate) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.Profile.ExternalID)
	}
	return ids
}

func loadInstagramAssignments(ctx context.Context, queryer pgxQueryer, organizationID string, externalIDs []string, lock bool) (map[string]instagramAssignment, error) {
	query := `SELECT account.external_id,assignment.creator_id,COALESCE(creator.display_name,'')
		FROM platform_accounts account
		JOIN creator_account_assignments assignment ON assignment.platform_account_id=account.id AND assignment.valid_to IS NULL
		JOIN creators creator ON creator.id=assignment.creator_id
		WHERE account.organization_id=$1 AND account.platform='INSTAGRAM' AND account.external_id=ANY($2::text[]) AND account.status<>'DISCONNECTED'`
	if lock {
		query += ` FOR UPDATE OF account`
	}
	rows, err := queryer.Query(ctx, query, organizationID, externalIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assignments := make(map[string]instagramAssignment, len(externalIDs))
	for rows.Next() {
		var externalID string
		var assignment instagramAssignment
		if err = rows.Scan(&externalID, &assignment.CreatorID, &assignment.CreatorName); err != nil {
			return nil, err
		}
		assignments[externalID] = assignment
	}
	return assignments, rows.Err()
}

func selectionAccounts(candidates []instagramFacebookCandidate, assignments map[string]instagramAssignment, creatorID string) []instagramSelectionAccount {
	items := make([]instagramSelectionAccount, 0, len(candidates))
	for _, candidate := range candidates {
		state := "AVAILABLE"
		selectable := true
		connectedCreator := ""
		if assignment, ok := assignments[candidate.Profile.ExternalID]; ok {
			connectedCreator = assignment.CreatorName
			if assignment.CreatorID == creatorID {
				state = "CONNECTED_HERE"
			} else {
				state = "CONNECTED_ELSEWHERE"
				selectable = false
			}
		}
		pageName, _ := candidate.Profile.Metadata["facebookPageName"].(string)
		items = append(items, instagramSelectionAccount{
			ID: candidate.Profile.ExternalID, Username: candidate.Profile.Username, DisplayName: candidate.Profile.DisplayName,
			AvatarURL: candidate.Profile.AvatarURL, FacebookPageName: pageName, ConnectionState: state,
			ConnectedCreator: connectedCreator, Selectable: selectable,
		})
	}
	return items
}

func (s *Server) getInstagramAccountSelection(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	creatorID := chi.URLParam(r, "id")
	selectionID := chi.URLParam(r, "selectionID")
	candidates, expiresAt, err := s.loadInstagramSelection(r.Context(), p.OrganizationID, creatorID, p.ID, selectionID, false, nil)
	if err != nil {
		problem(w, http.StatusGone, "selection expired", localized(r, "Выбор аккаунтов устарел. Запустите подключение ещё раз.", "The account selection has expired. Start the connection again."))
		return
	}
	assignments, err := loadInstagramAssignments(r.Context(), s.pool, p.OrganizationID, instagramCandidateIDs(candidates), false)
	if err != nil {
		problem(w, http.StatusInternalServerError, "selection failed", localized(r, "Не удалось проверить найденные аккаунты.", "Could not check the discovered accounts."))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": selectionAccounts(candidates, assignments, creatorID), "expiresAt": expiresAt})
}

func (s *Server) completeInstagramAccountSelection(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	creatorID := chi.URLParam(r, "id")
	selectionID := chi.URLParam(r, "selectionID")
	var in struct {
		AccountIDs []string `json:"accountIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in.AccountIDs) == 0 || len(in.AccountIDs) > 50 {
		problem(w, http.StatusBadRequest, "invalid selection", localized(r, "Выберите хотя бы один аккаунт.", "Select at least one account."))
		return
	}
	selected := make(map[string]struct{}, len(in.AccountIDs))
	for _, accountID := range in.AccountIDs {
		if accountID == "" {
			problem(w, http.StatusBadRequest, "invalid selection", localized(r, "Выбор аккаунтов некорректен.", "The account selection is invalid."))
			return
		}
		selected[accountID] = struct{}{}
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "selection failed", localized(r, "Не удалось начать подключение аккаунтов.", "Could not start connecting the accounts."))
		return
	}
	defer tx.Rollback(r.Context())
	candidates, _, err := s.loadInstagramSelection(r.Context(), p.OrganizationID, creatorID, p.ID, selectionID, true, tx)
	if err != nil {
		problem(w, http.StatusGone, "selection expired", localized(r, "Выбор аккаунтов устарел. Запустите подключение ещё раз.", "The account selection has expired. Start the connection again."))
		return
	}
	available := make(map[string]instagramFacebookCandidate, len(candidates))
	for _, candidate := range candidates {
		available[candidate.Profile.ExternalID] = candidate
	}
	for accountID := range selected {
		if _, ok := available[accountID]; !ok {
			problem(w, http.StatusBadRequest, "invalid selection", localized(r, "В списке есть недоступный аккаунт.", "The selection contains an unavailable account."))
			return
		}
	}
	assignments, err := loadInstagramAssignments(r.Context(), tx, p.OrganizationID, in.AccountIDs, true)
	if err != nil {
		problem(w, http.StatusInternalServerError, "selection failed", localized(r, "Не удалось проверить выбранные аккаунты.", "Could not check the selected accounts."))
		return
	}
	for accountID, assignment := range assignments {
		if assignment.CreatorID != creatorID {
			problem(w, http.StatusConflict, "account already connected", localized(r, "Аккаунт @"+available[accountID].Profile.Username+" уже подключён к другому креатору.", "The account @"+available[accountID].Profile.Username+" is already connected to another creator."))
			return
		}
	}
	provider := s.oauthProviders()["instagram-facebook"]
	connected := 0
	for _, candidate := range candidates {
		if _, ok := selected[candidate.Profile.ExternalID]; !ok {
			continue
		}
		if err = s.savePlatformConnectionTx(r.Context(), tx, p.OrganizationID, creatorID, provider, candidate.Token, candidate.Profile); err != nil {
			problem(w, http.StatusInternalServerError, "connection failed", localized(r, "Не удалось сохранить выбранные аккаунты.", "Could not save the selected accounts."))
			return
		}
		connected++
	}
	if _, err = tx.Exec(r.Context(), `UPDATE oauth_account_selections SET consumed_at=now() WHERE id=$1`, selectionID); err != nil {
		problem(w, http.StatusInternalServerError, "selection failed", localized(r, "Не удалось завершить подключение аккаунтов.", "Could not finish connecting the accounts."))
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		problem(w, http.StatusInternalServerError, "connection failed", localized(r, "Не удалось сохранить выбранные аккаунты.", "Could not save the selected accounts."))
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"connected": connected})
}
