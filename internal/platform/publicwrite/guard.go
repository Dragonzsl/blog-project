// Package publicwrite contains bounded replay and duplicate-submission
// controls for unauthenticated write endpoints.
package publicwrite

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/database"
)

var (
	ErrIdempotencyConflict = errors.New("idempotency key was reused with a different request")
	ErrIdempotencyPending  = errors.New("idempotent request is already being processed")
	ErrDuplicateRequest    = errors.New("duplicate public request")
)

const (
	DefaultIdempotencyTTL = 24 * time.Hour
	DefaultFingerprintTTL = 10 * time.Minute
	maxScopeLength        = 80
	maxKeyLength          = 160
	maxResponseLength     = 8192
	cleanupBatchSize      = 200
)

type Guard struct {
	db             *database.DB
	secret         []byte
	idempotencyTTL time.Duration
	fingerprintTTL time.Duration
	now            func() time.Time
}

type Options struct {
	IdempotencyTTL time.Duration
	FingerprintTTL time.Duration
	Now            func() time.Time
}

func NewGuard(db *database.DB, secret []byte, configured ...Options) *Guard {
	options := Options{IdempotencyTTL: DefaultIdempotencyTTL, FingerprintTTL: DefaultFingerprintTTL, Now: func() time.Time { return time.Now().UTC() }}
	if len(configured) > 0 {
		provided := configured[0]
		if provided.IdempotencyTTL > 0 {
			options.IdempotencyTTL = provided.IdempotencyTTL
		}
		if provided.FingerprintTTL > 0 {
			options.FingerprintTTL = provided.FingerprintTTL
		}
		if provided.Now != nil {
			options.Now = provided.Now
		}
	}
	if options.IdempotencyTTL > 7*24*time.Hour {
		options.IdempotencyTTL = 7 * 24 * time.Hour
	}
	if options.FingerprintTTL > 24*time.Hour {
		options.FingerprintTTL = 24 * time.Hour
	}
	if len(secret) == 0 {
		// A process-local random fallback keeps tests and explicitly constructed
		// handlers keyed without ever falling back to a guessable digest. The
		// application passes its persistent auth secret in production.
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			fallback := sha256.Sum256([]byte("blog-public-write-fallback"))
			secret = fallback[:]
		}
	}
	return &Guard{db: db, secret: append([]byte(nil), secret...), idempotencyTTL: options.IdempotencyTTL, fingerprintTTL: options.FingerprintTTL, now: options.Now}
}

type Claim struct {
	New            bool
	Completed      bool
	InProgress     bool
	ResponseStatus int
	ResultPublicID []byte
	ResponseBody   []byte
}

// ClaimTx reserves an explicit Idempotency-Key inside the caller's business
// transaction. That makes the key and the business result commit or roll back
// together, while a crashed processing row expires and can be reclaimed.
func (g *Guard) ClaimTx(ctx context.Context, tx *sql.Tx, scope, key, requestFingerprint string) (Claim, error) {
	if err := validate(scope, key); err != nil {
		return Claim{}, err
	}
	if tx == nil {
		return Claim{}, errors.New("idempotency transaction is required")
	}
	now := g.now().UTC()
	keyHash := g.digest(scope, key)
	requestHash := g.digest(scope+"\x00request", requestFingerprint)
	var storedRequest []byte
	var status string
	var responseStatus int
	var resultID, responseBody []byte
	var expiresAt int64
	err := tx.QueryRowContext(ctx, `SELECT request_hash,status,response_status,result_public_id,response_body,expires_at FROM request_idempotencies WHERE scope=? AND key_hash=?`, scope, keyHash).Scan(&storedRequest, &status, &responseStatus, &resultID, &responseBody, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `INSERT INTO request_idempotencies(scope,key_hash,request_hash,status,response_status,result_public_id,response_body,created_at,expires_at,updated_at) VALUES(?,?,?,'processing',0,NULL,x'',?,?,?)`, scope, keyHash, requestHash, now.UnixMilli(), now.Add(g.idempotencyTTL).UnixMilli(), now.UnixMilli())
		if err != nil {
			return Claim{}, err
		}
		return Claim{New: true}, nil
	}
	if err != nil {
		return Claim{}, err
	}
	if expiresAt <= now.UnixMilli() {
		_, err := tx.ExecContext(ctx, `UPDATE request_idempotencies SET request_hash=?,status='processing',response_status=0,result_public_id=NULL,response_body=x'',created_at=?,expires_at=?,updated_at=? WHERE scope=? AND key_hash=?`, requestHash, now.UnixMilli(), now.Add(g.idempotencyTTL).UnixMilli(), now.UnixMilli(), scope, keyHash)
		if err != nil {
			return Claim{}, err
		}
		return Claim{New: true}, nil
	}
	if !hmac.Equal(storedRequest, requestHash) {
		return Claim{}, ErrIdempotencyConflict
	}
	if status == "succeeded" {
		return Claim{Completed: true, ResponseStatus: responseStatus, ResultPublicID: append([]byte(nil), resultID...), ResponseBody: append([]byte(nil), responseBody...)}, nil
	}
	return Claim{InProgress: true}, ErrIdempotencyPending
}

func (g *Guard) CompleteTx(ctx context.Context, tx *sql.Tx, scope, key string, responseStatus int, resultPublicID []byte, responseBody []byte) error {
	if err := validate(scope, key); err != nil {
		return err
	}
	if responseStatus < 100 || responseStatus > 599 || len(responseBody) > maxResponseLength {
		return errors.New("idempotency response is invalid")
	}
	result, err := tx.ExecContext(ctx, `UPDATE request_idempotencies SET status='succeeded',response_status=?,result_public_id=?,response_body=?,updated_at=? WHERE scope=? AND key_hash=? AND status='processing'`, responseStatus, nullableBytes(resultPublicID), responseBody, g.now().UTC().UnixMilli(), scope, g.digest(scope, key))
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return errors.New("idempotency claim is not active")
	}
	return nil
}

func (g *Guard) AbandonTx(ctx context.Context, tx *sql.Tx, scope, key string) error {
	if err := validate(scope, key); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM request_idempotencies WHERE scope=? AND key_hash=? AND status='processing'`, scope, g.digest(scope, key))
	return err
}

type FingerprintClaim struct {
	New bool
}

func (g *Guard) ClaimFingerprintTx(ctx context.Context, tx *sql.Tx, scope, fingerprint string, resultPublicID []byte) (FingerprintClaim, error) {
	if strings.TrimSpace(scope) == "" || len(scope) > maxScopeLength || fingerprint == "" {
		return FingerprintClaim{}, errors.New("request fingerprint is invalid")
	}
	now := g.now().UTC()
	hash := g.digest(scope+"\x00fingerprint", fingerprint)
	var expiresAt int64
	err := tx.QueryRowContext(ctx, `SELECT expires_at FROM public_write_fingerprints WHERE scope=? AND fingerprint_hash=?`, scope, hash).Scan(&expiresAt)
	if err == nil && expiresAt > now.UnixMilli() {
		return FingerprintClaim{}, ErrDuplicateRequest
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return FingerprintClaim{}, err
	}
	if expiresAt > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM public_write_fingerprints WHERE scope=? AND fingerprint_hash=?`, scope, hash); err != nil {
			return FingerprintClaim{}, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO public_write_fingerprints(scope,fingerprint_hash,result_public_id,created_at,expires_at) VALUES(?,?,?,?,?)`, scope, hash, nullableBytes(resultPublicID), now.UnixMilli(), now.Add(g.fingerprintTTL).UnixMilli())
	if err != nil {
		return FingerprintClaim{}, err
	}
	return FingerprintClaim{New: true}, nil
}

func (g *Guard) Digest(scope, value string) []byte { return g.digest(scope, value) }

func (g *Guard) NewToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return token, g.digest("newsletter-token", token), nil
}

func (g *Guard) Cleanup(ctx context.Context) (int64, error) {
	if g == nil || g.db == nil {
		return 0, nil
	}
	now := g.now().UTC().UnixMilli()
	tx, err := g.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var removed int64
	for _, table := range []string{"request_idempotencies", "public_write_fingerprints", "newsletter_tokens"} {
		result, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE expires_at<=? LIMIT "+fmt.Sprint(cleanupBatchSize), now)
		if err != nil {
			return removed, err
		}
		count, _ := result.RowsAffected()
		removed += count
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return removed, nil
}

func (g *Guard) digest(scope, value string) []byte {
	mac := hmac.New(sha256.New, g.secret)
	_, _ = mac.Write([]byte(scope))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

func validate(scope, key string) error {
	if strings.TrimSpace(scope) == "" || len(scope) > maxScopeLength || strings.ContainsAny(scope, "\r\n") {
		return errors.New("idempotency scope is invalid")
	}
	if strings.TrimSpace(key) == "" || len(key) > maxKeyLength || strings.ContainsAny(key, "\r\n") {
		return errors.New("idempotency key is invalid")
	}
	return nil
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
