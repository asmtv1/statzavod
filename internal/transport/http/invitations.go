package httpserver

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"time"
)

func validRole(role string) bool { return role == "ADMIN" || role == "ANALYST" || role == "VIEWER" }

func (s *Server) createInvitation(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey).(principal)
	var in struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Email) == "" {
		problem(w, 400, "invalid invitation", "email is required")
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if in.Role == "" {
		in.Role = "VIEWER"
	}
	if !validRole(in.Role) {
		problem(w, 400, "invalid invitation", "invalid role")
		return
	}
	token := makeToken()
	digest := sha256.Sum256([]byte(token))
	_, err := s.pool.Exec(r.Context(), `INSERT INTO user_invitations(organization_id,email,role,token_hash,expires_at,created_by) VALUES($1,$2,$3,$4,now()+interval '24 hours',$5)`, p.OrganizationID, in.Email, in.Role, digest[:], p.ID)
	if err != nil {
		problem(w, 500, "invitation failed", "could not create invitation")
		return
	}
	acceptanceURL := strings.TrimRight(s.config.PublicBaseURL, "/") + "/accept-invitation?token=" + token
	delivery := "manual"
	if s.config.SMTPURL != "" {
		if err := sendInvitationEmail(s.config.SMTPURL, in.Email, acceptanceURL); err != nil {
			problem(w, 502, "invitation delivery failed", err.Error())
			return
		}
		delivery = "email"
	}
	writeJSON(w, 201, map[string]any{"email": in.Email, "role": in.Role, "delivery": delivery, "expiresAt": time.Now().Add(24 * time.Hour), "acceptanceUrl": acceptanceURL})
}

func sendInvitationEmail(rawURL, recipient, acceptanceURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid SMTP_URL: %w", err)
	}
	if u.Scheme != "smtp" && u.Scheme != "smtps" {
		return fmt.Errorf("SMTP_URL must use smtp or smtps")
	}
	host := u.Hostname()
	port := u.Port()
	if host == "" {
		return fmt.Errorf("SMTP_URL host is required")
	}
	if port == "" {
		if u.Scheme == "smtps" {
			port = "465"
		} else {
			port = "587"
		}
	}
	from := u.Query().Get("from")
	if from == "" {
		from = u.User.Username()
	}
	if from == "" {
		return fmt.Errorf("SMTP_URL requires a from address")
	}
	address := host + ":" + port
	var client *smtp.Client
	if u.Scheme == "smtps" {
		conn, err := tls.Dial("tcp", address, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err != nil {
			return err
		}
		client, err = smtp.NewClient(conn, host)
		if err != nil {
			return err
		}
	} else {
		client, err = smtp.Dial(address)
		if err != nil {
			return err
		}
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err = client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return err
			}
		}
	}
	defer client.Quit()
	if username := u.User.Username(); username != "" {
		password, _ := u.User.Password()
		if err = client.Auth(smtp.PlainAuth("", username, password, host)); err != nil {
			return err
		}
	}
	if err = client.Mail(from); err != nil {
		return err
	}
	if err = client.Rcpt(recipient); err != nil {
		return err
	}
	data, err := client.Data()
	if err != nil {
		return err
	}
	defer data.Close()
	_, err = fmt.Fprintf(data, "To: %s\r\nFrom: Statzavod <%s>\r\nSubject: =?UTF-8?B?0J/RgNC40LPQu9Cw0YjQtdC90LjQtSDQsiBTdGF0emF2b2Q=?=\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nВы приглашены в Statzavod. Активируйте доступ в течение 24 часов:\r\n%s\r\n", recipient, from, acceptanceURL)
	return err
}

func (s *Server) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Token == "" || len(in.Password) < 12 {
		problem(w, 400, "invalid invitation", "token and a password of at least 12 characters are required")
		return
	}
	digest := sha256.Sum256([]byte(in.Token))
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		problem(w, 500, "invitation failed", "could not start invitation")
		return
	}
	defer tx.Rollback(r.Context())
	var orgID, email, role string
	err = tx.QueryRow(r.Context(), `UPDATE user_invitations SET accepted_at=now() WHERE token_hash=$1 AND accepted_at IS NULL AND expires_at>now() RETURNING organization_id,email,role`, digest[:]).Scan(&orgID, &email, &role)
	if err != nil {
		problem(w, 400, "invalid invitation", "invitation is invalid or expired")
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		problem(w, 500, "invitation failed", "could not secure password")
		return
	}
	var userID string
	err = tx.QueryRow(r.Context(), `INSERT INTO users(email,password_hash,role,status) VALUES($1,$2,$3,'ACTIVE') ON CONFLICT(email) DO UPDATE SET password_hash=excluded.password_hash,role=excluded.role,status='ACTIVE',updated_at=now() RETURNING id`, email, hash, role).Scan(&userID)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,$3) ON CONFLICT(organization_id,user_id) DO UPDATE SET role=excluded.role`, orgID, userID, role)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		problem(w, 500, "invitation failed", "could not activate user")
		return
	}
	writeJSON(w, 201, map[string]string{"email": email})
}
