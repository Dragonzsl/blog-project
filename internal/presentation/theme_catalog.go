package presentation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/database"
)

const themeSecretPlaceholder = "••••••"

type ThemeCatalog struct {
	operationMu   sync.Mutex
	db            *database.DB
	manager       *ThemeManager
	mediaResolver ThemeMediaResolver
}

// ThemeMediaResolver turns a validated media public ID into the immutable,
// presentation-only view allowed in theme settings.
type ThemeMediaResolver func(context.Context, string) (MediaData, error)

func NewThemeCatalog(db *database.DB, manager *ThemeManager) *ThemeCatalog {
	return &ThemeCatalog{db: db, manager: manager}
}

func (c *ThemeCatalog) SetMediaResolver(resolver ThemeMediaResolver) {
	c.mediaResolver = resolver
}

type ThemeRecord struct {
	ID                int64
	Manifest          ThemeManifest
	Path              string
	Checksum          string
	DirectoryChecksum string
	ValidationStatus  string
	ValidationReport  string
	Active            bool
	InstalledAt       time.Time
}

func (c *ThemeCatalog) Install(ctx context.Context, source io.Reader, options ThemeInstallOptions) (ThemeRecord, error) {
	pkg, err := InstallTheme(ctx, source, options)
	if err != nil {
		return ThemeRecord{}, err
	}
	now := time.Now().UTC()
	checksum := hex.EncodeToString(pkg.Checksum[:])
	directoryChecksum := hex.EncodeToString(pkg.DirectoryChecksum[:])
	settingsVersion := normalizedSettingsVersion(pkg.Manifest.SettingsVersion)
	values, err := json.Marshal(defaultThemeSettings(pkg.Manifest.SettingsSchema))
	if err != nil {
		_ = os.RemoveAll(pkg.Path)
		return ThemeRecord{}, err
	}
	tx, err := c.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		_ = os.RemoveAll(pkg.Path)
		return ThemeRecord{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO themes(theme_id,version,name,api_version,core_range,path,checksum,directory_checksum,validation_status,validation_report,active,installed_at,updated_at) VALUES(?,?,?,?,?,?,?,?,'valid','',0,?,?)`, pkg.Manifest.ID, pkg.Manifest.Version, pkg.Manifest.Name, pkg.Manifest.ThemeAPI, pkg.Manifest.Core, pkg.Path, pkg.Checksum[:], pkg.DirectoryChecksum[:], now.UnixMilli(), now.UnixMilli())
	if err != nil {
		_ = os.RemoveAll(pkg.Path)
		return ThemeRecord{}, fmt.Errorf("save theme catalog: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		_ = os.RemoveAll(pkg.Path)
		return ThemeRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO theme_settings(theme_id,schema_version,values_json,updated_at) VALUES(?,?,?,?)", id, settingsVersion, string(values), now.UnixMilli()); err != nil {
		_ = os.RemoveAll(pkg.Path)
		return ThemeRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,result,context_json,created_at) VALUES('theme.installed','theme','succeeded',?,?)`, fmt.Sprintf(`{"theme_id":%q,"version":%q}`, pkg.Manifest.ID, pkg.Manifest.Version), now.UnixMilli()); err != nil {
		_ = os.RemoveAll(pkg.Path)
		return ThemeRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		_ = os.RemoveAll(pkg.Path)
		return ThemeRecord{}, err
	}
	return ThemeRecord{ID: id, Manifest: pkg.Manifest, Path: pkg.Path, Checksum: checksum, DirectoryChecksum: directoryChecksum, ValidationStatus: "valid", InstalledAt: now}, nil
}

func (c *ThemeCatalog) List(ctx context.Context) ([]ThemeRecord, error) {
	return c.list(ctx, false)
}

func (c *ThemeCatalog) ListRemoved(ctx context.Context) ([]ThemeRecord, error) {
	return c.list(ctx, true)
}

func (c *ThemeCatalog) list(ctx context.Context, removed bool) ([]ThemeRecord, error) {
	rows, err := c.db.Reader.QueryContext(ctx, `SELECT id,theme_id,name,version,api_version,core_range,path,hex(checksum),hex(directory_checksum),validation_status,validation_report,active,installed_at FROM themes WHERE (removed_at IS NOT NULL)=? ORDER BY theme_id,version DESC`, removed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ThemeRecord
	for rows.Next() {
		record, err := scanThemeRecord(rows)
		if err != nil {
			return nil, err
		}
		if manifest, err := readThemeManifest(record.Path); err == nil && manifest.ID == record.Manifest.ID && manifest.Version == record.Manifest.Version {
			record.Manifest = manifest
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

type themeRecordScanner interface{ Scan(...any) error }

func scanThemeRecord(row themeRecordScanner) (ThemeRecord, error) {
	var record ThemeRecord
	var active, installed int64
	var api int
	if err := row.Scan(&record.ID, &record.Manifest.ID, &record.Manifest.Name, &record.Manifest.Version, &api, &record.Manifest.Core, &record.Path, &record.Checksum, &record.DirectoryChecksum, &record.ValidationStatus, &record.ValidationReport, &active, &installed); err != nil {
		return ThemeRecord{}, err
	}
	record.Manifest.ThemeAPI = api
	record.Active = active == 1
	record.InstalledAt = time.UnixMilli(installed).UTC()
	return record, nil
}

func readThemeManifest(root string) (ThemeManifest, error) {
	contents, err := os.ReadFile(filepath.Join(root, "theme.json"))
	if err != nil {
		return ThemeManifest{}, err
	}
	var manifest ThemeManifest
	decoder := json.NewDecoder(bytesReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return ThemeManifest{}, err
	}
	return manifest, nil
}

// bytesReader keeps manifest loading local to this package without exposing
// mutable package buffers to callers.
func bytesReader(contents []byte) io.Reader { return &themeBytesReader{contents: contents} }

type themeBytesReader struct {
	contents []byte
	position int
}

func (r *themeBytesReader) Read(p []byte) (int, error) {
	if r.position >= len(r.contents) {
		return 0, io.EOF
	}
	n := copy(p, r.contents[r.position:])
	r.position += n
	return n, nil
}

func (c *ThemeCatalog) record(ctx context.Context, themeID, version string) (ThemeRecord, error) {
	row := c.db.Reader.QueryRowContext(ctx, `SELECT id,theme_id,name,version,api_version,core_range,path,hex(checksum),hex(directory_checksum),validation_status,validation_report,active,installed_at FROM themes WHERE theme_id=? AND version=? AND removed_at IS NULL`, themeID, version)
	record, err := scanThemeRecord(row)
	if err == sql.ErrNoRows {
		return ThemeRecord{}, fmt.Errorf("theme %s@%s is not installed", themeID, version)
	}
	if err != nil {
		return ThemeRecord{}, err
	}
	if manifest, err := readThemeManifest(record.Path); err == nil && manifest.ID == themeID && manifest.Version == version {
		record.Manifest = manifest
	}
	return record, nil
}

func (c *ThemeCatalog) settings(ctx context.Context, record ThemeRecord) (map[string]any, error) {
	var schemaVersion int
	var valuesJSON string
	err := c.db.Reader.QueryRowContext(ctx, "SELECT schema_version,values_json FROM theme_settings WHERE theme_id=?", record.ID).Scan(&schemaVersion, &valuesJSON)
	if err == sql.ErrNoRows {
		return validateThemeSettings(record.Manifest.SettingsSchema, defaultThemeSettings(record.Manifest.SettingsSchema))
	}
	if err != nil {
		return nil, err
	}
	if schemaVersion != normalizedSettingsVersion(record.Manifest.SettingsVersion) {
		return nil, fmt.Errorf("theme %s@%s settings require migration from schema %d to %d", record.Manifest.ID, record.Manifest.Version, schemaVersion, normalizedSettingsVersion(record.Manifest.SettingsVersion))
	}
	values := make(map[string]any)
	if err := json.Unmarshal([]byte(valuesJSON), &values); err != nil {
		return nil, fmt.Errorf("decode theme settings: %w", err)
	}
	return validateThemeSettings(record.Manifest.SettingsSchema, values)
}

func (c *ThemeCatalog) Settings(ctx context.Context, themeID, version string) (ThemeRecord, map[string]any, error) {
	record, err := c.record(ctx, themeID, version)
	if err != nil {
		return ThemeRecord{}, nil, err
	}
	values, err := c.settings(ctx, record)
	return record, c.maskSettings(record.Manifest.SettingsSchema, values), err
}

func (c *ThemeCatalog) maskSettings(schema map[string]SettingDefinition, values map[string]any) map[string]any {
	masked := cloneSettings(values)
	for key, definition := range schema {
		if definition.Secret {
			if _, ok := masked[key]; ok {
				masked[key] = themeSecretPlaceholder
			}
		}
	}
	return masked
}

func (c *ThemeCatalog) SaveSettings(ctx context.Context, themeID, version string, incoming map[string]any) error {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	record, err := c.record(ctx, themeID, version)
	if err != nil {
		return err
	}
	existing, err := c.settings(ctx, record)
	if err != nil {
		return err
	}
	for key, value := range incoming {
		if definition, ok := record.Manifest.SettingsSchema[key]; ok && definition.Secret {
			// The admin form deliberately sends an empty input for secrets so
			// the existing value is never echoed back to the browser. Treat both
			// the mask and an empty value as "leave unchanged".
			if value == themeSecretPlaceholder || value == "" {
				continue
			}
		}
		existing[key] = value
	}
	validated, err := validateThemeSettings(record.Manifest.SettingsSchema, existing)
	if err != nil {
		return err
	}
	valuesJSON, err := json.Marshal(validated)
	if err != nil {
		return err
	}
	if record.Active {
		if _, err := c.runtimeSettings(ctx, record.Manifest, validated); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	tx, err := c.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO theme_settings(theme_id,schema_version,values_json,updated_at) VALUES(?,?,?,?) ON CONFLICT(theme_id) DO UPDATE SET schema_version=excluded.schema_version,values_json=excluded.values_json,updated_at=excluded.updated_at`, record.ID, normalizedSettingsVersion(record.Manifest.SettingsVersion), string(valuesJSON), now.UnixMilli()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE system_state SET render_epoch=render_epoch+1,updated_at=? WHERE id=1", now.UnixMilli()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,result,context_json,created_at) VALUES('theme.settings_saved','theme','succeeded',?,?)`, fmt.Sprintf(`{"theme_id":%q,"version":%q}`, themeID, version), now.UnixMilli()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if record.Active {
		loaded, err := c.loadValidatedTheme(ctx, record)
		if err != nil {
			return err
		}
		if err := c.manager.ActivateRuntime(loaded); err != nil {
			return err
		}
		if err := c.manager.PersistMarker(loaded); err != nil {
			return err
		}
	}
	return nil
}

func (c *ThemeCatalog) loadValidatedTheme(ctx context.Context, record ThemeRecord) (*Theme, error) {
	if record.ValidationStatus != "valid" {
		return nil, fmt.Errorf("theme %s@%s is not valid", record.Manifest.ID, record.Manifest.Version)
	}
	loaded, err := NewThemeFromDirectory(record.Path, record.Manifest)
	if err != nil {
		return nil, fmt.Errorf("validate theme before activation: %w", err)
	}
	if err := validateThemeFixtures(ctx, record.Path, record.Manifest); err != nil {
		return nil, err
	}
	directoryChecksum, err := ThemeDirectoryChecksum(record.Path)
	if err != nil {
		return nil, err
	}
	actual := hex.EncodeToString(directoryChecksum[:])
	if record.DirectoryChecksum != "" && !stringsEqualFold(record.DirectoryChecksum, actual) {
		return nil, fmt.Errorf("theme %s@%s directory checksum mismatch", record.Manifest.ID, record.Manifest.Version)
	}
	values, err := c.settings(ctx, record)
	if err != nil {
		return nil, err
	}
	runtime, err := c.runtimeSettings(ctx, record.Manifest, values)
	if err != nil {
		return nil, err
	}
	return loaded.WithSettings(runtime), nil
}

func (c *ThemeCatalog) runtimeSettings(ctx context.Context, manifest ThemeManifest, values map[string]any) (map[string]any, error) {
	runtime := runtimeThemeSettings(manifest.SettingsSchema, values)
	if c.mediaResolver == nil {
		return runtime, nil
	}
	for key, definition := range manifest.SettingsSchema {
		if definition.Type != "media" {
			continue
		}
		value, ok := values[key].(string)
		if !ok || value == "" {
			continue
		}
		view, err := c.mediaResolver(ctx, value)
		if err != nil {
			return nil, fmt.Errorf("resolve theme media setting %q: %w", key, err)
		}
		runtime[key] = view
	}
	return runtime, nil
}

func stringsEqualFold(left, right string) bool {
	return len(left) == len(right) && equalFoldBytes(left, right)
}

func equalFoldBytes(left, right string) bool {
	for index := range left {
		l, r := left[index], right[index]
		if l == r {
			continue
		}
		if l >= 'A' && l <= 'F' {
			l += 'a' - 'A'
		}
		if r >= 'A' && r <= 'F' {
			r += 'a' - 'A'
		}
		if l != r {
			return false
		}
	}
	return true
}

func (c *ThemeCatalog) Activate(ctx context.Context, themeID, version string) error {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	record, err := c.record(ctx, themeID, version)
	if err != nil {
		return err
	}
	loaded, err := c.loadValidatedTheme(ctx, record)
	if err != nil {
		return err
	}
	return c.commitActivation(ctx, record, loaded, "activate")
}

func (c *ThemeCatalog) commitActivation(ctx context.Context, record ThemeRecord, loaded *Theme, operation string) error {
	directoryChecksum, err := ThemeDirectoryChecksum(record.Path)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	tx, err := c.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM themes WHERE active=1").Scan(&previous); err != nil && err != sql.ErrNoRows {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO theme_activation_history(previous_theme_id,active_theme_id,operation,status,started_at) VALUES(?,?,?,?,?)`, nullableThemeID(previous), record.ID, operation, "pending", now.UnixMilli())
	if err != nil {
		return err
	}
	historyID, err := result.LastInsertId()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE themes SET active=0,updated_at=?", now.UnixMilli()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE themes SET active=1,directory_checksum=COALESCE(directory_checksum,?),updated_at=? WHERE id=?", directoryChecksum[:], now.UnixMilli(), record.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE system_state SET render_epoch=render_epoch+1,updated_at=? WHERE id=1", now.UnixMilli()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE theme_activation_history SET status='succeeded',completed_at=? WHERE id=?", now.UnixMilli(), historyID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,result,context_json,created_at) VALUES(?,?, 'succeeded',?,?)`, "theme."+operation, "theme", fmt.Sprintf(`{"theme_id":%q,"version":%q}`, record.Manifest.ID, record.Manifest.Version), now.UnixMilli()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	previousTheme := c.manager.Current()
	if err := c.manager.ActivateRuntime(loaded); err != nil {
		_ = c.markActivationFailure(ctx, historyID)
		_ = c.restoreActiveRecord(ctx, previous)
		c.manager.setRuntime(previousTheme)
		return err
	}
	if err := c.manager.PersistMarker(loaded); err != nil {
		// The database is authoritative. Runtime remains on the validated theme;
		// startup reconciliation will repair a marker write failure.
		return err
	}
	return nil
}

func (c *ThemeCatalog) markActivationFailure(ctx context.Context, historyID int64) error {
	_, err := c.db.Writer.ExecContext(ctx, "UPDATE theme_activation_history SET status='failed',error_summary='runtime activation failed',completed_at=? WHERE id=?", time.Now().UTC().UnixMilli(), historyID)
	return err
}

func nullableThemeID(value sql.NullInt64) any {
	if !value.Valid || value.Int64 < 1 {
		return nil
	}
	return value.Int64
}

func (c *ThemeCatalog) restoreActiveRecord(ctx context.Context, previous sql.NullInt64) error {
	tx, err := c.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().UnixMilli()
	if _, err := tx.ExecContext(ctx, "UPDATE themes SET active=0,updated_at=?", now); err != nil {
		return err
	}
	if previous.Valid {
		if _, err := tx.ExecContext(ctx, "UPDATE themes SET active=1,updated_at=? WHERE id=?", now, previous.Int64); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (c *ThemeCatalog) Rollback(ctx context.Context) error {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	var previous sql.NullInt64
	err := c.db.Reader.QueryRowContext(ctx, `SELECT CASE WHEN EXISTS(SELECT 1 FROM themes previous WHERE previous.id=history.previous_theme_id AND previous.removed_at IS NULL) THEN history.previous_theme_id ELSE NULL END
		FROM theme_activation_history history
		JOIN themes active_theme ON active_theme.active=1 AND active_theme.id=history.active_theme_id
		WHERE history.operation='activate' AND history.status='succeeded'
		ORDER BY history.id DESC LIMIT 1`).Scan(&previous)
	if err == sql.ErrNoRows || (err == nil && !previous.Valid) {
		now := time.Now().UTC()
		tx, err := c.db.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var current sql.NullInt64
		if scanErr := tx.QueryRowContext(ctx, "SELECT id FROM themes WHERE active=1").Scan(&current); scanErr != nil && scanErr != sql.ErrNoRows {
			return scanErr
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO theme_activation_history(previous_theme_id,active_theme_id,operation,status,error_summary,started_at,completed_at) VALUES(?,?, 'rollback','succeeded','',?,?)`, nullableThemeID(current), nil, now.UnixMilli(), now.UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE themes SET active=0,updated_at=?", now.UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE system_state SET render_epoch=render_epoch+1,updated_at=? WHERE id=1", now.UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,result,context_json,created_at) VALUES('theme.rollback','theme','succeeded','{"fallback":true}',?)`, now.UnixMilli()); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		if err := c.manager.Rollback(); err != nil {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	var record ThemeRecord
	if err := c.db.Reader.QueryRowContext(ctx, `SELECT id,theme_id,name,version,api_version,core_range,path,hex(checksum),hex(directory_checksum),validation_status,validation_report,active,installed_at FROM themes WHERE id=?`, previous.Int64).Scan(&record.ID, &record.Manifest.ID, &record.Manifest.Name, &record.Manifest.Version, &record.Manifest.ThemeAPI, &record.Manifest.Core, &record.Path, &record.Checksum, &record.DirectoryChecksum, &record.ValidationStatus, &record.ValidationReport, new(int64), new(int64)); err != nil {
		return err
	}
	if manifest, err := readThemeManifest(record.Path); err == nil {
		record.Manifest = manifest
	}
	loaded, err := c.loadValidatedTheme(ctx, record)
	if err != nil {
		return err
	}
	return c.commitActivation(ctx, record, loaded, "rollback")
}

// Reconcile makes the database active row authoritative after startup. A
// stale/corrupt marker therefore cannot select a different theme silently.
func (c *ThemeCatalog) Reconcile(ctx context.Context) error {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	var record ThemeRecord
	var active, installed int64
	err := c.db.Reader.QueryRowContext(ctx, `SELECT id,theme_id,name,version,api_version,core_range,path,hex(checksum),hex(directory_checksum),validation_status,validation_report,active,installed_at FROM themes WHERE active=1`).Scan(&record.ID, &record.Manifest.ID, &record.Manifest.Name, &record.Manifest.Version, &record.Manifest.ThemeAPI, &record.Manifest.Core, &record.Path, &record.Checksum, &record.DirectoryChecksum, &record.ValidationStatus, &record.ValidationReport, &active, &installed)
	if err == sql.ErrNoRows {
		return c.manager.Rollback()
	}
	if err != nil {
		return err
	}
	record.Active = active == 1
	record.InstalledAt = time.UnixMilli(installed).UTC()
	if manifest, err := readThemeManifest(record.Path); err == nil {
		record.Manifest = manifest
	}
	loaded, err := c.loadValidatedTheme(ctx, record)
	if err != nil {
		_ = c.manager.Rollback()
		return err
	}
	if err := c.manager.ActivateRuntime(loaded); err != nil {
		_ = c.manager.Rollback()
		return err
	}
	return c.manager.PersistMarker(loaded)
}

func defaultThemeSettings(schema map[string]SettingDefinition) map[string]any {
	values := make(map[string]any, len(schema))
	keys := make([]string, 0, len(schema))
	for key := range schema {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		values[key] = schema[key].Default
	}
	return values
}

func (c *ThemeCatalog) Resolve(themeID, version string) (ThemeRecord, error) {
	return c.record(context.Background(), themeID, version)
}

// Theme loads a validated, settings-bound theme for a no-store preview. It
// deliberately shares the activation validation path so preview cannot render
// a package that activation would reject.
func (c *ThemeCatalog) Theme(ctx context.Context, themeID, version string) (*Theme, error) {
	record, err := c.record(ctx, themeID, version)
	if err != nil {
		return nil, err
	}
	return c.loadValidatedTheme(ctx, record)
}

func ThemePackageChecksum(data []byte) [32]byte { return sha256.Sum256(data) }

func (c *ThemeCatalog) Root() string { return filepath.Dir(c.manager.marker) }

// Remove retains the package and settings, but never removes the active theme.
func (c *ThemeCatalog) Remove(ctx context.Context, themeID, version string) error {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	return c.changeRemoval(ctx, themeID, version, true)
}

// Restore revalidates retained files before making a theme available again.
// Restoring does not activate it.
func (c *ThemeCatalog) Restore(ctx context.Context, themeID, version string) error {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	record, err := scanThemeRecord(c.db.Reader.QueryRowContext(ctx, `SELECT id,theme_id,name,version,api_version,core_range,path,hex(checksum),hex(directory_checksum),validation_status,validation_report,active,installed_at FROM themes WHERE theme_id=? AND version=?`, themeID, version))
	if err != nil {
		return fmt.Errorf("主题不存在。")
	}
	manifest, err := readThemeManifest(record.Path)
	if err != nil {
		return err
	}
	record.Manifest = manifest
	if _, err := c.loadValidatedTheme(ctx, record); err != nil {
		return err
	}
	return c.changeRemoval(ctx, themeID, version, false)
}

func (c *ThemeCatalog) changeRemoval(ctx context.Context, themeID, version string, removed bool) error {
	tx, err := c.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	var active, wasRemoved bool
	if err := tx.QueryRowContext(ctx, "SELECT id,active,removed_at IS NOT NULL FROM themes WHERE theme_id=? AND version=?", themeID, version).Scan(&id, &active, &wasRemoved); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("主题不存在，内嵌默认主题不能删除。")
		}
		return err
	}
	if removed && active {
		return fmt.Errorf("当前正在使用的主题不能删除，请先切换到其他主题。")
	}
	if removed == wasRemoved {
		return nil
	}
	now := time.Now().UTC().UnixMilli()
	var removalTime any
	action := "theme.restored"
	if removed {
		removalTime = now
		action = "theme.removed"
	}
	if _, err := tx.ExecContext(ctx, "UPDATE themes SET removed_at=?,updated_at=? WHERE id=?", removalTime, now, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,result,context_json,created_at) VALUES(?, 'theme','succeeded',?,?)`, action, fmt.Sprintf(`{"theme_id":%q,"version":%q}`, themeID, version), now); err != nil {
		return err
	}
	return tx.Commit()
}
