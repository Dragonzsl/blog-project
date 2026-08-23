package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/database"
)

type Repository struct {
	database *database.DB
}

func NewRepository(database *database.DB) *Repository {
	return &Repository{database: database}
}

func (r *Repository) Initialized(ctx context.Context) (bool, error) {
	var initialized bool
	if err := r.database.Reader.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM owners WHERE id = 1)").Scan(&initialized); err != nil {
		return false, fmt.Errorf("check owner initialization: %w", err)
	}
	return initialized, nil
}

func (r *Repository) SiteName(ctx context.Context) (string, error) {
	var name string
	if err := r.database.Reader.QueryRowContext(ctx, "SELECT name FROM sites WHERE id = 1").Scan(&name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "个人博客", nil
		}
		return "", fmt.Errorf("read site name: %w", err)
	}
	return name, nil
}

func (r *Repository) SaveSetupChallenge(ctx context.Context, challenge setupChallenge, now time.Time) error {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin setup transaction: %w", err)
	}
	defer tx.Rollback()

	var initialized bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM owners WHERE id = 1)").Scan(&initialized); err != nil {
		return fmt.Errorf("check setup state: %w", err)
	}
	if initialized {
		return ErrAlreadyInitialized
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM owner_setup_challenges"); err != nil {
		return fmt.Errorf("replace setup challenge: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO owner_setup_challenges (
			id, token_hash, site_name, username, username_key, password_hash,
			totp_secret_cipher, expires_at, created_at
		) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?)
	`, challenge.TokenHash, challenge.SiteName, challenge.Username, challenge.UsernameKey,
		challenge.PasswordHash, challenge.TOTPSecretCipher, millis(challenge.ExpiresAt), millis(now))
	if err != nil {
		return fmt.Errorf("save setup challenge: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit setup challenge: %w", err)
	}
	return nil
}

func (r *Repository) SetupChallenge(ctx context.Context, hash []byte, now time.Time) (setupChallenge, error) {
	var challenge setupChallenge
	var expiresAt int64
	err := r.database.Reader.QueryRowContext(ctx, `
		SELECT token_hash, site_name, username, username_key, password_hash,
		       totp_secret_cipher, expires_at
		FROM owner_setup_challenges
		WHERE id = 1 AND token_hash = ? AND expires_at > ?
	`, hash, millis(now)).Scan(
		&challenge.TokenHash,
		&challenge.SiteName,
		&challenge.Username,
		&challenge.UsernameKey,
		&challenge.PasswordHash,
		&challenge.TOTPSecretCipher,
		&expiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return setupChallenge{}, ErrInvalidSetup
	}
	if err != nil {
		return setupChallenge{}, fmt.Errorf("read setup challenge: %w", err)
	}
	challenge.ExpiresAt = fromMillis(expiresAt)
	return challenge, nil
}

func (r *Repository) CompleteSetup(
	ctx context.Context,
	challenge setupChallenge,
	recoveryHashes [][]byte,
	session Session,
	now time.Time,
) error {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin setup completion: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		DELETE FROM owner_setup_challenges
		WHERE id = 1 AND token_hash = ? AND expires_at > ?
	`, challenge.TokenHash, millis(now))
	if err != nil {
		return fmt.Errorf("claim setup challenge: %w", err)
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check setup challenge claim: %w", err)
	}
	if claimed != 1 {
		return ErrInvalidSetup
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sites (id, name, created_at, updated_at)
		VALUES (1, ?, ?, ?)
	`, challenge.SiteName, millis(now), millis(now)); err != nil {
		return mapInitializationError(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO owners (
			id, username, username_key, password_hash, totp_secret_cipher,
			auth_version, created_at, updated_at
		) VALUES (1, ?, ?, ?, ?, 1, ?, ?)
	`, challenge.Username, challenge.UsernameKey, challenge.PasswordHash,
		challenge.TOTPSecretCipher, millis(now), millis(now)); err != nil {
		return mapInitializationError(err)
	}
	for _, hash := range recoveryHashes {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO owner_recovery_codes (owner_id, code_hash, created_at)
			VALUES (1, ?, ?)
		`, hash, millis(now)); err != nil {
			return fmt.Errorf("save recovery code: %w", err)
		}
	}
	if err := insertSession(ctx, tx, session, now); err != nil {
		return err
	}
	if err := insertAudit(ctx, tx, "identity.setup.completed", "owner", "succeeded", now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit setup completion: %w", err)
	}
	return nil
}

func (r *Repository) OwnerByUsername(ctx context.Context, key string) (Owner, error) {
	var owner Owner
	err := r.database.Reader.QueryRowContext(ctx, `
		SELECT id, username, username_key, password_hash, totp_secret_cipher, auth_version
		FROM owners WHERE username_key = ?
	`, key).Scan(
		&owner.ID,
		&owner.Username,
		&owner.UsernameKey,
		&owner.PasswordHash,
		&owner.TOTPSecretCipher,
		&owner.AuthVersion,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Owner{}, ErrInvalidCredentials
	}
	if err != nil {
		return Owner{}, fmt.Errorf("read owner: %w", err)
	}
	return owner, nil
}

func (r *Repository) CreateSession(ctx context.Context, session Session, recoveryHash []byte, now time.Time) error {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin login transaction: %w", err)
	}
	defer tx.Rollback()

	factor := "totp"
	if len(recoveryHash) > 0 {
		result, err := tx.ExecContext(ctx, `
			UPDATE owner_recovery_codes
			SET used_at = ?
			WHERE owner_id = ? AND code_hash = ? AND used_at IS NULL
		`, millis(now), session.OwnerID, recoveryHash)
		if err != nil {
			return fmt.Errorf("consume recovery code: %w", err)
		}
		used, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("check recovery code: %w", err)
		}
		if used != 1 {
			return ErrInvalidCredentials
		}
		factor = "recovery"
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ?", millis(now)); err != nil {
		return fmt.Errorf("remove expired sessions: %w", err)
	}
	if err := insertSession(ctx, tx, session, now); err != nil {
		return err
	}
	if err := insertAudit(ctx, tx, "identity.login."+factor, "owner", "succeeded", now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit login: %w", err)
	}
	return nil
}

func (r *Repository) SessionByToken(ctx context.Context, hash []byte, now time.Time) (Session, error) {
	var session Session
	var expiresAt, lastSeenAt int64
	err := r.database.Reader.QueryRowContext(ctx, `
		SELECT s.owner_id, o.username, s.auth_version, s.expires_at, s.last_seen_at
		FROM sessions s
		JOIN owners o ON o.id = s.owner_id AND o.auth_version = s.auth_version
		WHERE s.token_hash = ? AND s.expires_at > ?
	`, hash, millis(now)).Scan(
		&session.OwnerID,
		&session.Username,
		&session.AuthVersion,
		&expiresAt,
		&lastSeenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, fmt.Errorf("read session: %w", err)
	}
	session.TokenHash = hash
	session.ExpiresAt = fromMillis(expiresAt)
	session.LastSeenAt = fromMillis(lastSeenAt)
	return session, nil
}

func (r *Repository) TouchSession(ctx context.Context, hash []byte, now time.Time) error {
	if _, err := r.database.Writer.ExecContext(ctx, `
		UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?
	`, millis(now), hash); err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

func (r *Repository) DeleteSession(ctx context.Context, hash []byte, now time.Time) error {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin logout: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", hash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	if err := insertAudit(ctx, tx, "identity.logout", "owner", "succeeded", now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit logout: %w", err)
	}
	return nil
}

func (r *Repository) RecoverOwner(
	ctx context.Context,
	owner Owner,
	passwordHash string,
	totpCipher []byte,
	recoveryHashes [][]byte,
	now time.Time,
) error {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin owner recovery: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE owners
		SET password_hash = ?, totp_secret_cipher = ?, auth_version = auth_version + 1, updated_at = ?
		WHERE id = ? AND username_key = ?
	`, passwordHash, totpCipher, millis(now), owner.ID, owner.UsernameKey)
	if err != nil {
		return fmt.Errorf("recover owner: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check owner recovery: %w", err)
	}
	if updated != 1 {
		return ErrInvalidCredentials
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE owner_id = ?", owner.ID); err != nil {
		return fmt.Errorf("invalidate owner sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM owner_recovery_codes WHERE owner_id = ?", owner.ID); err != nil {
		return fmt.Errorf("replace recovery codes: %w", err)
	}
	for _, hash := range recoveryHashes {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO owner_recovery_codes (owner_id, code_hash, created_at)
			VALUES (?, ?, ?)
		`, owner.ID, hash, millis(now)); err != nil {
			return fmt.Errorf("save replacement recovery code: %w", err)
		}
	}
	if err := insertAudit(ctx, tx, "identity.cli.recovered", "owner", "succeeded", now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit owner recovery: %w", err)
	}
	return nil
}

func insertSession(ctx context.Context, tx *sql.Tx, session Session, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (
			owner_id, token_hash, auth_version, expires_at, last_seen_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?)
	`, session.OwnerID, session.TokenHash, session.AuthVersion,
		millis(session.ExpiresAt), millis(now), millis(now))
	if err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	return nil
}

func insertAudit(ctx context.Context, tx *sql.Tx, action, objectKind, result string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO audit_entries (action, object_kind, result, context_json, created_at)
		VALUES (?, ?, ?, '{}', ?)
	`, action, objectKind, result, millis(now))
	if err != nil {
		return fmt.Errorf("save audit entry: %w", err)
	}
	return nil
}

func mapInitializationError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("initialize owner: %w", err)
}

func millis(value time.Time) int64 {
	return value.UTC().UnixMilli()
}

func fromMillis(value int64) time.Time {
	return time.UnixMilli(value).UTC()
}
