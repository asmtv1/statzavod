package httpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

var errContentCommandBodyTooLarge = errors.New("content command request body is too large")

const (
	contentCommandBodyLimit = int64(1 << 20)
	mediaCreateBodyLimit    = int64(64 << 10)
)

// beginContentCommandIdempotency claims a key in the same transaction as the
// mutation.  The unique row is deliberately inserted before the transition:
// a concurrent duplicate waits for commit, then replays the stored response.
// Hashing canonical JSON makes insignificant whitespace/key ordering harmless.
func beginContentCommandIdempotency(w http.ResponseWriter, r *http.Request, tx pgx.Tx, p principal) (func(int, any) error, bool) {
	key, ok := mustIdempotencyKey(w, r)
	if !ok {
		return nil, false
	}
	limit := contentCommandBodyLimitFor(r)
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	raw, err := ioReadAndRestoreBounded(r, limit)
	if err != nil {
		if errors.Is(err, errContentCommandBodyTooLarge) || isMaxBytesError(err) {
			problem(w, http.StatusRequestEntityTooLarge, "request too large", "request body exceeds the endpoint limit")
			return nil, false
		}
		problem(w, 400, "invalid request", "could not read request body")
		return nil, false
	}
	canonical := raw
	if len(bytes.TrimSpace(raw)) > 0 {
		var value any
		if json.Unmarshal(raw, &value) != nil {
			problem(w, 400, "invalid request", "expected JSON request body")
			return nil, false
		}
		canonical, _ = json.Marshal(value)
	}
	sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"), canonical...))
	return claimContentCommandIdempotency(w, r, tx, p, key, sum[:])
}

func claimContentCommandIdempotency(w http.ResponseWriter, r *http.Request, tx pgx.Tx, p principal, key string, requestHash []byte) (func(int, any) error, bool) {
	var existing []byte
	var status int
	err := tx.QueryRow(r.Context(), `INSERT INTO content_command_idempotency(organization_id,actor_id,idempotency_key,request_hash,response_status,response_body) VALUES($1,$2,$3,$4,0,''::bytea) ON CONFLICT (organization_id,actor_id,idempotency_key) DO NOTHING RETURNING response_body`, p.OrganizationID, p.ID, key, requestHash).Scan(&existing)
	if err == nil { // newly claimed; RETURNING body is the placeholder
		return func(status int, response any) error {
			body, e := encodeContentCommandResponse(response)
			if e != nil {
				return e
			}
			_, e = tx.Exec(r.Context(), `UPDATE content_command_idempotency SET response_status=$4,response_body=$5 WHERE organization_id=$1 AND actor_id=$2 AND idempotency_key=$3`, p.OrganizationID, p.ID, key, status, body)
			return e
		}, true
	}
	if err != pgx.ErrNoRows {
		problem(w, 500, "request failed", "could not claim idempotency key")
		return nil, false
	}
	var hash []byte
	if err = tx.QueryRow(r.Context(), `SELECT request_hash,response_status,response_body FROM content_command_idempotency WHERE organization_id=$1 AND actor_id=$2 AND idempotency_key=$3`, p.OrganizationID, p.ID, key).Scan(&hash, &status, &existing); err != nil {
		problem(w, 500, "request failed", "could not read idempotency state")
		return nil, false
	}
	if !bytes.Equal(hash, requestHash) {
		problem(w, 409, "idempotency conflict", "Idempotency-Key was already used with another request")
		return nil, false
	}
	// The original transaction already committed its business mutation and its
	// audit row before this response became replayable.  Tell the fail-closed
	// audit middleware that this successful replay is backed by that committed
	// audit, just as the primary response is.  This marker is response-local;
	// it does not create a second audit record.
	markResponseAuditCommitted(w)
	writeContentCommandBytes(w, status, existing)
	return nil, false
}

func encodeContentCommandResponse(response any) ([]byte, error) {
	if response == nil {
		return []byte{}, nil
	}
	return json.Marshal(response)
}

func writeContentCommandBytes(w http.ResponseWriter, status int, body []byte) {
	if len(body) > 0 {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	if len(body) > 0 {
		_, _ = w.Write(body)
	}
}

func writeContentCommandResponse(w http.ResponseWriter, status int, response any) {
	body, err := encodeContentCommandResponse(response)
	if err != nil {
		problem(w, http.StatusInternalServerError, "request failed", "could not encode response")
		return
	}
	writeContentCommandBytes(w, status, body)
}

func ioReadAndRestore(r *http.Request) ([]byte, error) {
	return ioReadAndRestoreBounded(r, contentCommandBodyLimit)
}

func ioReadAndRestoreBounded(r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	var b bytes.Buffer
	n, err := io.Copy(&b, io.LimitReader(r.Body, limit+1))
	r.Body.Close()
	if n > limit {
		r.Body = http.NoBody
		return nil, errContentCommandBodyTooLarge
	}
	r.Body = io.NopCloser(bytes.NewReader(b.Bytes()))
	return b.Bytes(), err
}

func contentCommandBodyLimitFor(r *http.Request) int64 {
	path := r.URL.Path
	if strings.Contains(path, "/media/uploads") && !strings.HasSuffix(path, "/complete") {
		return mediaCreateBodyLimit
	}
	return contentCommandBodyLimit
}

func isMaxBytesError(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}
