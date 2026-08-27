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
	"time"

	"github.com/zhushilin/blog-project/internal/platform/database"
)

type ThemeCatalog struct {
	db      *database.DB
	manager *ThemeManager
}

func NewThemeCatalog(db *database.DB, manager *ThemeManager) *ThemeCatalog {
	return &ThemeCatalog{db: db, manager: manager}
}

type ThemeRecord struct {
	ID               int64
	Manifest         ThemeManifest
	Path             string
	Checksum         string
	ValidationStatus string
	ValidationReport string
	Active           bool
	InstalledAt      time.Time
}

func (c *ThemeCatalog) Install(ctx context.Context, source io.Reader, options ThemeInstallOptions) (ThemeRecord, error) {
	pkg, err := InstallTheme(ctx, source, options)
	if err != nil {
		return ThemeRecord{}, err
	}
	now := time.Now().UTC()
	checksum := hex.EncodeToString(pkg.Checksum[:])
	values, _ := json.Marshal(defaultThemeSettings(pkg.Manifest.SettingsSchema))
	tx, err := c.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		_ = os.RemoveAll(pkg.Path)
		return ThemeRecord{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO themes(theme_id,version,name,api_version,core_range,path,checksum,validation_status,validation_report,active,installed_at,updated_at) VALUES(?,?,?,?,?,?,?,'valid','',0,?,?)`, pkg.Manifest.ID, pkg.Manifest.Version, pkg.Manifest.Name, pkg.Manifest.ThemeAPI, pkg.Manifest.Core, pkg.Path, pkg.Checksum[:], now.UnixMilli(), now.UnixMilli())
	if err != nil {
		_ = os.RemoveAll(pkg.Path)
		return ThemeRecord{}, fmt.Errorf("save theme catalog: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		_ = os.RemoveAll(pkg.Path)
		return ThemeRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO theme_settings(theme_id,schema_version,values_json,updated_at) VALUES(?,1,?,?)", id, values, now.UnixMilli()); err != nil {
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
	return ThemeRecord{ID: id, Manifest: pkg.Manifest, Path: pkg.Path, Checksum: checksum, ValidationStatus: "valid", InstalledAt: now}, nil
}

func (c *ThemeCatalog) List(ctx context.Context) ([]ThemeRecord, error) {
	rows, err := c.db.Reader.QueryContext(ctx, `SELECT id,theme_id,name,version,api_version,core_range,path,hex(checksum),validation_status,validation_report,active,installed_at FROM themes ORDER BY theme_id,version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ThemeRecord
	for rows.Next() {
		var record ThemeRecord
		var id, api, active, installed int64
		var themeID, name, version, core string
		if err := rows.Scan(&id, &themeID, &name, &version, &api, &core, &record.Path, &record.Checksum, &record.ValidationStatus, &record.ValidationReport, &active, &installed); err != nil {
			return nil, err
		}
		record.ID, record.Active, record.InstalledAt = id, active == 1, time.UnixMilli(installed).UTC()
		record.Manifest = ThemeManifest{ID: themeID, Name: name, Version: version, ThemeAPI: int(api), Core: core}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (c *ThemeCatalog) Activate(ctx context.Context, themeID, version string) error {
	var record ThemeRecord
	var checksum []byte
	var active int
	var installed int64
	err := c.db.Reader.QueryRowContext(ctx, `SELECT id,theme_id,name,version,api_version,core_range,path,checksum,validation_status,validation_report,active,installed_at FROM themes WHERE theme_id=? AND version=?`, themeID, version).Scan(&record.ID, &record.Manifest.ID, &record.Manifest.Name, &record.Manifest.Version, &record.Manifest.ThemeAPI, &record.Manifest.Core, &record.Path, &checksum, &record.ValidationStatus, &record.ValidationReport, &active, &installed)
	if err == sql.ErrNoRows {
		return fmt.Errorf("theme %s@%s is not installed", themeID, version)
	}
	if err != nil {
		return err
	}
	record.Active = active == 1
	record.InstalledAt = time.UnixMilli(installed).UTC()
	if record.ValidationStatus != "valid" {
		return fmt.Errorf("theme %s@%s is not valid", themeID, version)
	}
	if _, err := NewThemeFromDirectory(record.Path, record.Manifest); err != nil {
		return fmt.Errorf("validate theme before activation: %w", err)
	}
	loaded, err := NewThemeFromDirectory(record.Path, record.Manifest)
	if err != nil {
		return err
	}
	if err := c.manager.Activate(loaded); err != nil {
		return err
	}
	now := time.Now().UTC()
	tx, err := c.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		_ = c.manager.Rollback()
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE themes SET active=0,updated_at=?", now.UnixMilli()); err != nil {
		_ = c.manager.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE themes SET active=1,updated_at=? WHERE id=?", now.UnixMilli(), record.ID); err != nil {
		_ = c.manager.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,result,context_json,created_at) VALUES('theme.activated','theme','succeeded',?,?)`, fmt.Sprintf(`{"theme_id":%q,"version":%q}`, themeID, version), now.UnixMilli()); err != nil {
		_ = c.manager.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		_ = c.manager.Rollback()
		return err
	}
	_ = checksum
	return nil
}

func (c *ThemeCatalog) Rollback(ctx context.Context) error {
	if err := c.manager.Rollback(); err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err := c.db.Writer.ExecContext(ctx, "UPDATE themes SET active=0,updated_at=?", now.UnixMilli())
	return err
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
	ctx := context.Background()
	records, err := c.List(ctx)
	if err != nil {
		return ThemeRecord{}, err
	}
	for _, record := range records {
		if record.Manifest.ID == themeID && record.Manifest.Version == version {
			return record, nil
		}
	}
	return ThemeRecord{}, fmt.Errorf("theme %s@%s not found", themeID, version)
}

func ThemePackageChecksum(data []byte) [32]byte { return sha256.Sum256(data) }

func (c *ThemeCatalog) Root() string { return filepath.Dir(c.manager.marker) }
