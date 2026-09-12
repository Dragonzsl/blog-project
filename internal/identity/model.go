package identity

import (
	"errors"
	"strings"
	"time"
)

var (
	ErrAlreadyInitialized = errors.New("site is already initialized")
	ErrNotInitialized     = errors.New("site is not initialized")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidSetup       = errors.New("invalid or expired setup challenge")
	ErrRateLimited        = errors.New("too many authentication attempts")
	ErrInvalidSecurity    = errors.New("security reauthentication failed")
	ErrInvalidChallenge   = errors.New("security challenge is invalid or expired")
)

type Owner struct {
	ID               int64
	Username         string
	UsernameKey      string
	PasswordHash     string
	TOTPSecretCipher []byte
	AuthVersion      int64
}

type setupChallenge struct {
	TokenHash        []byte
	SiteName         string
	Username         string
	UsernameKey      string
	PasswordHash     string
	TOTPSecretCipher []byte
	ExpiresAt        time.Time
}

type Session struct {
	OwnerID     int64
	Username    string
	Token       string
	TokenHash   []byte
	AuthVersion int64
	ExpiresAt   time.Time
	LastSeenAt  time.Time
}

type SetupStartResult struct {
	Token      string
	SiteName   string
	TOTPSecret string
	TOTPURI    string
}

type SetupCompleteResult struct {
	Session       Session
	RecoveryCodes []string
}

type RecoveryResult struct {
	TOTPSecret    string
	TOTPURI       string
	RecoveryCodes []string
}

// SiteSettings contains only bounded, public-facing site metadata. Secrets,
// plugin credentials, and delivery configuration deliberately do not belong
// here.
type SiteSettings struct {
	Name                  string
	PrimaryLanguage       string
	Timezone              string
	BaseURL               string
	Description           string
	DefaultSEOTitle       string
	DefaultSEODescription string
	SocialLinks           []string
	DefaultSocialImageID  []byte
}

type TOTPChallenge struct {
	Token     string
	Secret    string
	TOTPURI   string
	ExpiresAt time.Time
}

type SecuritySession struct {
	ID         int64
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

func usernameKey(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}
