package analytics

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/database"
)

type Daily struct {
	Day            string
	Path           string
	Views          int64
	UniqueVisitors int64
}

type Service struct {
	db            *database.DB
	secret        []byte
	retentionDays int
	now           func() time.Time
}

func NewService(db *database.DB, secret []byte, retentionDays int) *Service {
	if retentionDays < 1 {
		retentionDays = 365
	}
	return &Service{db: db, secret: append([]byte(nil), secret...), retentionDays: retentionDays, now: func() time.Time { return time.Now().UTC() }}
}

// Record stores only a keyed digest of the visitor identity. The raw address
// and user-agent never enter SQLite, and path cardinality is bounded to keep a
// public request from becoming an unbounded analytics workload.
func (s *Service) Record(ctx context.Context, path, visitor string) error {
	path = strings.TrimSpace(path)
	if path == "" || len(path) > 512 || s.db == nil {
		return nil
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	now := s.now().UTC()
	day := now.Format("2006-01-02")
	// Keep uniqueness scoped to the same day and path.  The digest remains
	// keyed and opaque, while a visitor viewing two pages is counted once in
	// each page's aggregate rather than being charged to whichever page came
	// first.
	hash := s.visitorHash(path, visitor)
	tx, err := s.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO analytics_daily(day,path,views,unique_visitors,updated_at) VALUES(?,?,1,0,?) ON CONFLICT(day,path) DO UPDATE SET views=views+1,updated_at=excluded.updated_at`, day, path, now.UnixMilli()); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO analytics_visitor_days(day,visitor_hash) VALUES(?,?)`, day, hash)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE analytics_daily SET unique_visitors=unique_visitors+1,updated_at=? WHERE day=? AND path=?`, now.UnixMilli(), day, path); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) visitorHash(path, visitor string) []byte {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(path))
	_, _ = mac.Write([]byte{'\x00'})
	_, _ = mac.Write([]byte(strings.TrimSpace(visitor)))
	return mac.Sum(nil)
}

func VisitorIdentity(r *http.Request) string {
	remote := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	return remote + "\x00" + strings.TrimSpace(r.UserAgent())
}

func (s *Service) Summary(ctx context.Context, days int) ([]Daily, error) {
	if days < 1 {
		days = 30
	}
	if days > 3660 {
		days = 3660
	}
	cutoff := s.now().UTC().AddDate(0, 0, -days+1).Format("2006-01-02")
	rows, err := s.db.Reader.QueryContext(ctx, `SELECT day,path,views,unique_visitors FROM analytics_daily WHERE day>=? ORDER BY day DESC,path`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Daily
	for rows.Next() {
		var item Daily
		if err := rows.Scan(&item.Day, &item.Path, &item.Views, &item.UniqueVisitors); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) Purge(ctx context.Context) (int64, error) {
	cutoff := s.now().UTC().AddDate(0, 0, -s.retentionDays).Format("2006-01-02")
	tx, err := s.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM analytics_visitor_days WHERE day<?", cutoff); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM analytics_daily WHERE day<?", cutoff)
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	if count > 0 {
		_, _ = tx.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,result,context_json,created_at) VALUES('analytics.purged','analytics','succeeded',?,?)`, fmt.Sprintf(`{"rows":%d}`, count), s.now().UTC().UnixMilli())
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

var ErrDisabled = errors.New("analytics disabled")

func (s *Service) Enabled() bool { return s != nil && s.db != nil }
