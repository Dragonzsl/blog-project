package identity

import (
	"context"
	"database/sql"
	"encoding/json"
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

func (r *Repository) Timezone(ctx context.Context) (string, error) {
	var timezone string
	if err := r.database.Reader.QueryRowContext(ctx, "SELECT timezone FROM sites WHERE id = 1").Scan(&timezone); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "Asia/Shanghai", nil
		}
		return "", fmt.Errorf("read site timezone: %w", err)
	}
	return timezone, nil
}

func (r *Repository) SiteSettings(ctx context.Context) (SiteSettings, error) {
	var settings SiteSettings
	var imageID []byte
	var socialJSON string
	err := r.database.Reader.QueryRowContext(ctx, `
		SELECT name,primary_language,timezone,base_url,description,
		       default_seo_title,default_seo_description,social_links_json,
		       default_social_image_public_id,feed_summary_mode
		FROM sites WHERE id=1`).Scan(
		&settings.Name, &settings.PrimaryLanguage, &settings.Timezone,
		&settings.BaseURL, &settings.Description, &settings.DefaultSEOTitle,
		&settings.DefaultSEODescription, &socialJSON, &imageID, &settings.FeedSummaryMode)
	if errors.Is(err, sql.ErrNoRows) {
		return SiteSettings{Name: "个人博客", PrimaryLanguage: "zh-CN", Timezone: "Asia/Shanghai", FeedSummaryMode: "excerpt", SocialLinks: []string{}}, nil
	}
	if err != nil {
		return SiteSettings{}, fmt.Errorf("read site settings: %w", err)
	}
	if socialJSON == "" {
		socialJSON = "[]"
	}
	if settings.FeedSummaryMode == "" {
		settings.FeedSummaryMode = "excerpt"
	}
	if err := json.Unmarshal([]byte(socialJSON), &settings.SocialLinks); err != nil {
		return SiteSettings{}, fmt.Errorf("decode site social links: %w", err)
	}
	settings.SocialLinks = append([]string(nil), settings.SocialLinks...)
	settings.DefaultSocialImageID = append([]byte(nil), imageID...)
	return settings, nil
}

func (r *Repository) UpdateSiteSettings(ctx context.Context, settings SiteSettings, now time.Time) error {
	socialJSON, err := json.Marshal(settings.SocialLinks)
	if err != nil {
		return fmt.Errorf("encode site social links: %w", err)
	}
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE sites SET name=?,primary_language=?,timezone=?,base_url=?,description=?,default_seo_title=?,default_seo_description=?,feed_summary_mode=?,social_links_json=?,default_social_image_public_id=?,updated_at=? WHERE id=1`,
		settings.Name, settings.PrimaryLanguage, settings.Timezone, settings.BaseURL, settings.Description,
		settings.DefaultSEOTitle, settings.DefaultSEODescription, settings.FeedSummaryMode, string(socialJSON), nullableBytes(settings.DefaultSocialImageID), millis(now)); err != nil {
		return fmt.Errorf("update site settings: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE system_state SET render_epoch=render_epoch+1,updated_at=? WHERE id=1", millis(now)); err != nil {
		return fmt.Errorf("invalidate site settings: %w", err)
	}
	if err := insertAuditContext(ctx, tx, "identity.site_settings.updated", "site", `{"fields":["public_metadata"]}`, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ChangePassword(ctx context.Context, ownerID, authVersion int64, passwordHash string, now time.Time) error {
	return r.rotateOwnerSecurity(ctx, ownerID, authVersion, passwordHash, nil, nil, "identity.password.changed", now)
}

func (r *Repository) RotateRecoveryCodes(ctx context.Context, ownerID, authVersion int64, recoveryHashes [][]byte, now time.Time) error {
	return r.rotateOwnerSecurity(ctx, ownerID, authVersion, "", nil, recoveryHashes, "identity.recovery_codes.rotated", now)
}

func (r *Repository) CompleteTOTPRotation(ctx context.Context, ownerID, authVersion int64, totpCipher []byte, recoveryHashes [][]byte, now time.Time) error {
	return r.rotateOwnerSecurity(ctx, ownerID, authVersion, "", totpCipher, recoveryHashes, "identity.totp.rotated", now)
}

// CompleteTOTPRotationChallenge claims the one-time challenge and rotates the
// owner security state in one transaction. A crash after either operation is
// therefore rolled back instead of leaving a replayable challenge or a
// partially rotated identity.
func (r *Repository) CompleteTOTPRotationChallenge(ctx context.Context, ownerID, authVersion int64, purpose string, tokenHash, totpCipher []byte, recoveryHashes [][]byte, now time.Time) error {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "DELETE FROM owner_security_challenges WHERE owner_id=? AND purpose=? AND token_hash=? AND expires_at>?", ownerID, purpose, tokenHash, millis(now))
	if err != nil {
		return err
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if claimed != 1 {
		return ErrInvalidChallenge
	}
	if err := r.rotateOwnerSecurityTx(ctx, tx, ownerID, authVersion, "", totpCipher, recoveryHashes, "identity.totp.rotated", now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) RevokeAllSessions(ctx context.Context, ownerID, authVersion int64, now time.Time) error {
	return r.rotateOwnerSecurity(ctx, ownerID, authVersion, "", nil, nil, "identity.sessions.revoked", now)
}

func (r *Repository) rotateOwnerSecurity(ctx context.Context, ownerID, authVersion int64, passwordHash string, totpCipher []byte, recoveryHashes [][]byte, action string, now time.Time) error {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := r.rotateOwnerSecurityTx(ctx, tx, ownerID, authVersion, passwordHash, totpCipher, recoveryHashes, action, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) rotateOwnerSecurityTx(ctx context.Context, tx *sql.Tx, ownerID, authVersion int64, passwordHash string, totpCipher []byte, recoveryHashes [][]byte, action string, now time.Time) error {
	query := "UPDATE owners SET auth_version=auth_version+1,updated_at=?"
	args := []any{millis(now)}
	if passwordHash != "" {
		query += ",password_hash=?"
		args = append(args, passwordHash)
	}
	if len(totpCipher) > 0 {
		query += ",totp_secret_cipher=?"
		args = append(args, totpCipher)
	}
	query += " WHERE id=? AND auth_version=?"
	args = append(args, ownerID, authVersion)
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	updated, _ := result.RowsAffected()
	if updated != 1 {
		return ErrInvalidSecurity
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE owner_id=?", ownerID); err != nil {
		return err
	}
	if recoveryHashes != nil {
		if _, err := tx.ExecContext(ctx, "DELETE FROM owner_recovery_codes WHERE owner_id=?", ownerID); err != nil {
			return err
		}
		for _, hash := range recoveryHashes {
			if _, err := tx.ExecContext(ctx, "INSERT INTO owner_recovery_codes(owner_id,code_hash,created_at) VALUES(?,?,?)", ownerID, hash, millis(now)); err != nil {
				return err
			}
		}
	}
	if err := insertAuditContext(ctx, tx, action, "owner", "{}", now); err != nil {
		return err
	}
	return nil
}

func (r *Repository) SaveSecurityChallenge(ctx context.Context, ownerID int64, purpose string, tokenHash, candidateCipher []byte, expiresAt, now time.Time) error {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM owner_security_challenges WHERE owner_id=? AND purpose=?", ownerID, purpose); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO owner_security_challenges(owner_id,purpose,token_hash,candidate_totp_secret_cipher,expires_at,created_at) VALUES(?,?,?,?,?,?)`, ownerID, purpose, tokenHash, candidateCipher, millis(expiresAt), millis(now)); err != nil {
		return err
	}
	return tx.Commit()
}

type securityChallenge struct {
	OwnerID         int64
	Purpose         string
	TokenHash       []byte
	CandidateCipher []byte
	ExpiresAt       time.Time
}

func (r *Repository) SecurityChallenge(ctx context.Context, ownerID int64, purpose string, tokenHash []byte, now time.Time) (securityChallenge, error) {
	var challenge securityChallenge
	var expiresAt int64
	err := r.database.Reader.QueryRowContext(ctx, `SELECT owner_id,purpose,token_hash,candidate_totp_secret_cipher,expires_at FROM owner_security_challenges WHERE owner_id=? AND purpose=? AND token_hash=? AND expires_at>?`, ownerID, purpose, tokenHash, millis(now)).Scan(&challenge.OwnerID, &challenge.Purpose, &challenge.TokenHash, &challenge.CandidateCipher, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return securityChallenge{}, ErrInvalidChallenge
	}
	if err != nil {
		return securityChallenge{}, err
	}
	challenge.ExpiresAt = fromMillis(expiresAt)
	return challenge, nil
}

func (r *Repository) ConsumeSecurityChallenge(ctx context.Context, ownerID int64, purpose string, tokenHash []byte, now time.Time) error {
	result, err := r.database.Writer.ExecContext(ctx, "DELETE FROM owner_security_challenges WHERE owner_id=? AND purpose=? AND token_hash=? AND expires_at>?", ownerID, purpose, tokenHash, millis(now))
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return ErrInvalidChallenge
	}
	return nil
}

func (r *Repository) Sessions(ctx context.Context, ownerID int64) ([]SecuritySession, error) {
	rows, err := r.database.Reader.QueryContext(ctx, "SELECT id,created_at,last_seen_at,expires_at FROM sessions WHERE owner_id=? ORDER BY last_seen_at DESC,id DESC LIMIT 50", ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SecuritySession
	for rows.Next() {
		var item SecuritySession
		var created, lastSeen, expires int64
		if err := rows.Scan(&item.ID, &created, &lastSeen, &expires); err != nil {
			return nil, err
		}
		item.CreatedAt, item.LastSeenAt, item.ExpiresAt = fromMillis(created), fromMillis(lastSeen), fromMillis(expires)
		result = append(result, item)
	}
	return result, rows.Err()
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

func insertAuditContext(ctx context.Context, tx *sql.Tx, action, objectKind, contextJSON string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO audit_entries (action, object_kind, result, context_json, created_at)
		VALUES (?, ?, 'succeeded', ?, ?)
	`, action, objectKind, contextJSON, millis(now))
	if err != nil {
		return fmt.Errorf("save audit entry: %w", err)
	}
	return nil
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
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
