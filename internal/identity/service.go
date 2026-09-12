package identity

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{3,32}$`)

const dummyPasswordHash = "$argon2id$v=19$m=65536,t=3,p=1$Zz57grWS/CdfUaUZd6zkWw$4Gcj7QdbSwG2t03bhCwZqsFgAzMXKTzFO+Rp5CJYTg8"

type Service struct {
	repository      *Repository
	hasher          passwordHasher
	protector       *protector
	sessionLifetime time.Duration
	dummyHash       string
	limiter         *attemptLimiter
	authSlot        chan struct{}
	siteNameMu      sync.RWMutex
	siteName        string
	siteNameLoaded  bool
	now             func() time.Time
}

func NewService(repository *Repository, secret []byte, sessionLifetime time.Duration) (*Service, error) {
	protector, err := newProtector(secret)
	if err != nil {
		return nil, err
	}
	hasher := passwordHasher{}
	return &Service{
		repository:      repository,
		hasher:          hasher,
		protector:       protector,
		sessionLifetime: sessionLifetime,
		dummyHash:       dummyPasswordHash,
		limiter:         newAttemptLimiter(),
		authSlot:        make(chan struct{}, 1),
		now:             func() time.Time { return time.Now().UTC() },
	}, nil
}

func (s *Service) Initialized(ctx context.Context) (bool, error) {
	return s.repository.Initialized(ctx)
}

func (s *Service) SiteName(ctx context.Context) (string, error) {
	s.siteNameMu.RLock()
	if s.siteNameLoaded {
		name := s.siteName
		s.siteNameMu.RUnlock()
		return name, nil
	}
	s.siteNameMu.RUnlock()

	s.siteNameMu.Lock()
	defer s.siteNameMu.Unlock()
	if s.siteNameLoaded {
		return s.siteName, nil
	}
	name, err := s.repository.SiteName(ctx)
	if err != nil {
		return "", err
	}
	s.siteName = name
	s.siteNameLoaded = true
	return name, nil
}

func (s *Service) Timezone(ctx context.Context) (string, error) {
	return s.repository.Timezone(ctx)
}

func (s *Service) SiteSettings(ctx context.Context) (SiteSettings, error) {
	return s.repository.SiteSettings(ctx)
}

func (s *Service) UpdateSiteSettings(ctx context.Context, settings SiteSettings) error {
	normalized, err := normalizeSiteSettings(settings)
	if err != nil {
		return err
	}
	if err := s.repository.UpdateSiteSettings(ctx, normalized, s.now()); err != nil {
		return err
	}
	s.siteNameMu.Lock()
	s.siteName = normalized.Name
	s.siteNameLoaded = true
	s.siteNameMu.Unlock()
	return nil
}

func normalizeSiteSettings(settings SiteSettings) (SiteSettings, error) {
	settings.Name = strings.TrimSpace(settings.Name)
	if !utf8.ValidString(settings.Name) || utf8.RuneCountInString(settings.Name) < 1 || utf8.RuneCountInString(settings.Name) > 100 {
		return SiteSettings{}, errors.New("站点名称需要 1–100 个字符")
	}
	settings.PrimaryLanguage = strings.TrimSpace(settings.PrimaryLanguage)
	if settings.PrimaryLanguage == "" || len(settings.PrimaryLanguage) > 32 || strings.ContainsAny(settings.PrimaryLanguage, "\r\n") {
		return SiteSettings{}, errors.New("主要语言无效")
	}
	settings.Timezone = strings.TrimSpace(settings.Timezone)
	if _, err := time.LoadLocation(settings.Timezone); err != nil {
		return SiteSettings{}, errors.New("时区无效")
	}
	settings.BaseURL = strings.TrimRight(strings.TrimSpace(settings.BaseURL), "/")
	if settings.BaseURL != "" {
		parsed, err := url.Parse(settings.BaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return SiteSettings{}, errors.New("公开基础 URL 必须是没有凭据、查询参数和片段的 HTTP(S) 地址")
		}
	}
	settings.Description = strings.TrimSpace(settings.Description)
	settings.DefaultSEOTitle = strings.TrimSpace(settings.DefaultSEOTitle)
	settings.DefaultSEODescription = strings.TrimSpace(settings.DefaultSEODescription)
	settings.FeedSummaryMode = strings.TrimSpace(settings.FeedSummaryMode)
	if settings.FeedSummaryMode == "" {
		settings.FeedSummaryMode = "excerpt"
	}
	if settings.FeedSummaryMode != "excerpt" && settings.FeedSummaryMode != "full" {
		return SiteSettings{}, errors.New("Feed 摘要策略无效")
	}
	if !utf8.ValidString(settings.Description) || utf8.RuneCountInString(settings.Description) > 1000 || !utf8.ValidString(settings.DefaultSEOTitle) || utf8.RuneCountInString(settings.DefaultSEOTitle) > 200 || !utf8.ValidString(settings.DefaultSEODescription) || utf8.RuneCountInString(settings.DefaultSEODescription) > 500 {
		return SiteSettings{}, errors.New("站点描述或 SEO 字段超过长度限制")
	}
	if len(settings.SocialLinks) > 10 {
		return SiteSettings{}, errors.New("社交链接最多 10 个")
	}
	links := make([]string, 0, len(settings.SocialLinks))
	for _, value := range settings.SocialLinks {
		value = strings.TrimSpace(value)
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return SiteSettings{}, errors.New("社交链接必须是安全的 HTTP(S) 地址")
		}
		links = append(links, value)
	}
	settings.SocialLinks = links
	if len(settings.DefaultSocialImageID) != 0 && len(settings.DefaultSocialImageID) != 16 {
		return SiteSettings{}, errors.New("默认社交图片公共 ID 无效")
	}
	settings.DefaultSocialImageID = append([]byte(nil), settings.DefaultSocialImageID...)
	return settings, nil
}

func validateSiteSettings(settings SiteSettings) error {
	_, err := normalizeSiteSettings(settings)
	return err
}

func (s *Service) verifyCurrentCredentials(ctx context.Context, session Session, password, secondFactor string) (Owner, error) {
	owner, err := s.repository.OwnerByUsername(ctx, usernameKey(session.Username))
	if err != nil || owner.ID != session.OwnerID || owner.AuthVersion != session.AuthVersion {
		return Owner{}, ErrInvalidSecurity
	}
	valid, err := s.hasher.Verify(owner.PasswordHash, password)
	if err != nil || !valid {
		return Owner{}, ErrInvalidSecurity
	}
	secret, err := s.protector.Decrypt(owner.TOTPSecretCipher)
	if err != nil || !validateTOTP(secondFactor, secret, s.now()) {
		return Owner{}, ErrInvalidSecurity
	}
	return owner, nil
}

func (s *Service) ChangePassword(ctx context.Context, session Session, currentPassword, currentTOTP, newPassword string) error {
	owner, err := s.verifyCurrentCredentials(ctx, session, currentPassword, currentTOTP)
	if err != nil {
		return err
	}
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}
	hash, err := s.hashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.repository.ChangePassword(ctx, owner.ID, owner.AuthVersion, hash, s.now())
}

func (s *Service) StartTOTPRotation(ctx context.Context, session Session, currentPassword, currentTOTP string) (TOTPChallenge, error) {
	owner, err := s.verifyCurrentCredentials(ctx, session, currentPassword, currentTOTP)
	if err != nil {
		return TOTPChallenge{}, err
	}
	issuer, err := s.SiteName(ctx)
	if err != nil {
		return TOTPChallenge{}, err
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: owner.Username, Period: 30, SecretSize: 20, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		return TOTPChallenge{}, err
	}
	ciphertext, err := s.protector.Encrypt(key.Secret())
	if err != nil {
		return TOTPChallenge{}, err
	}
	token, err := randomToken(32)
	if err != nil {
		return TOTPChallenge{}, err
	}
	now := s.now()
	challenge := TOTPChallenge{Token: token, Secret: key.Secret(), TOTPURI: key.URL(), ExpiresAt: now.Add(10 * time.Minute)}
	if err := s.repository.SaveSecurityChallenge(ctx, owner.ID, "totp_rotation", tokenHash(token), ciphertext, challenge.ExpiresAt, now); err != nil {
		return TOTPChallenge{}, err
	}
	return challenge, nil
}

func (s *Service) CompleteTOTPRotation(ctx context.Context, session Session, token, code string) ([]string, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrInvalidChallenge
	}
	owner, err := s.repository.OwnerByUsername(ctx, usernameKey(session.Username))
	if err != nil || owner.ID != session.OwnerID || owner.AuthVersion != session.AuthVersion {
		return nil, ErrInvalidSecurity
	}
	challenge, err := s.repository.SecurityChallenge(ctx, owner.ID, "totp_rotation", tokenHash(token), s.now())
	if err != nil {
		return nil, err
	}
	secret, err := s.protector.Decrypt(challenge.CandidateCipher)
	if err != nil || !validateTOTP(code, secret, s.now()) {
		return nil, ErrInvalidCredentials
	}
	codes, hashes, err := generateRecoveryCodes(s.protector)
	if err != nil {
		return nil, err
	}
	if err := s.repository.CompleteTOTPRotationChallenge(ctx, owner.ID, owner.AuthVersion, "totp_rotation", tokenHash(token), challenge.CandidateCipher, hashes, s.now()); err != nil {
		return nil, err
	}
	return codes, nil
}

func (s *Service) RegenerateRecoveryCodes(ctx context.Context, session Session, currentPassword, currentTOTP string) ([]string, error) {
	owner, err := s.verifyCurrentCredentials(ctx, session, currentPassword, currentTOTP)
	if err != nil {
		return nil, err
	}
	codes, hashes, err := generateRecoveryCodes(s.protector)
	if err != nil {
		return nil, err
	}
	if err := s.repository.RotateRecoveryCodes(ctx, owner.ID, owner.AuthVersion, hashes, s.now()); err != nil {
		return nil, err
	}
	return codes, nil
}

func (s *Service) RevokeAllSessions(ctx context.Context, session Session, currentPassword, currentTOTP string) error {
	owner, err := s.verifyCurrentCredentials(ctx, session, currentPassword, currentTOTP)
	if err != nil {
		return err
	}
	return s.repository.RevokeAllSessions(ctx, owner.ID, owner.AuthVersion, s.now())
}

func (s *Service) Sessions(ctx context.Context, session Session) ([]SecuritySession, error) {
	return s.repository.Sessions(ctx, session.OwnerID)
}

func (s *Service) StartSetup(ctx context.Context, siteName, username, password string) (SetupStartResult, error) {
	initialized, err := s.repository.Initialized(ctx)
	if err != nil {
		return SetupStartResult{}, err
	}
	if initialized {
		return SetupStartResult{}, ErrAlreadyInitialized
	}
	siteName = strings.TrimSpace(siteName)
	username = strings.TrimSpace(username)
	if !utf8.ValidString(siteName) || utf8.RuneCountInString(siteName) < 1 || utf8.RuneCountInString(siteName) > 100 {
		return SetupStartResult{}, errors.New("站点名称需要 1–100 个字符")
	}
	if !usernamePattern.MatchString(username) {
		return SetupStartResult{}, errors.New("用户名需要 3–32 位，仅使用字母、数字、点、横线或下划线")
	}
	passwordHash, err := s.hashPassword(password)
	if err != nil {
		return SetupStartResult{}, err
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      siteName,
		AccountName: username,
		Period:      30,
		SecretSize:  20,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return SetupStartResult{}, fmt.Errorf("generate TOTP key: %w", err)
	}
	ciphertext, err := s.protector.Encrypt(key.Secret())
	if err != nil {
		return SetupStartResult{}, err
	}
	token, err := randomToken(32)
	if err != nil {
		return SetupStartResult{}, err
	}
	now := s.now()
	challenge := setupChallenge{
		TokenHash:        tokenHash(token),
		SiteName:         siteName,
		Username:         username,
		UsernameKey:      usernameKey(username),
		PasswordHash:     passwordHash,
		TOTPSecretCipher: ciphertext,
		ExpiresAt:        now.Add(15 * time.Minute),
	}
	if err := s.repository.SaveSetupChallenge(ctx, challenge, now); err != nil {
		return SetupStartResult{}, err
	}
	return SetupStartResult{Token: token, SiteName: siteName, TOTPSecret: key.Secret(), TOTPURI: key.URL()}, nil
}

func (s *Service) SetupBinding(ctx context.Context, token string) (SetupStartResult, error) {
	challenge, err := s.repository.SetupChallenge(ctx, tokenHash(token), s.now())
	if err != nil {
		return SetupStartResult{}, err
	}
	secret, err := s.protector.Decrypt(challenge.TOTPSecretCipher)
	if err != nil {
		return SetupStartResult{}, err
	}
	return SetupStartResult{
		Token:      token,
		SiteName:   challenge.SiteName,
		TOTPSecret: secret,
		TOTPURI:    buildTOTPURI(challenge.SiteName, challenge.Username, secret),
	}, nil
}

func (s *Service) CompleteSetup(ctx context.Context, token, code string) (SetupCompleteResult, error) {
	now := s.now()
	challenge, err := s.repository.SetupChallenge(ctx, tokenHash(token), now)
	if err != nil {
		return SetupCompleteResult{}, err
	}
	secret, err := s.protector.Decrypt(challenge.TOTPSecretCipher)
	if err != nil {
		return SetupCompleteResult{}, err
	}
	if !validateTOTP(code, secret, now) {
		return SetupCompleteResult{}, ErrInvalidCredentials
	}
	codes, hashes, err := generateRecoveryCodes(s.protector)
	if err != nil {
		return SetupCompleteResult{}, err
	}
	session, err := s.newSession(1, challenge.Username, 1, now)
	if err != nil {
		return SetupCompleteResult{}, err
	}
	if err := s.repository.CompleteSetup(ctx, challenge, hashes, session, now); err != nil {
		return SetupCompleteResult{}, err
	}
	s.siteNameMu.Lock()
	s.siteName = challenge.SiteName
	s.siteNameLoaded = true
	s.siteNameMu.Unlock()
	return SetupCompleteResult{Session: session, RecoveryCodes: codes}, nil
}

func (s *Service) Login(ctx context.Context, limiterKey, username, password, secondFactor string) (Session, error) {
	now := s.now()
	limiterKey = limiterKey + "\x00" + usernameKey(username)
	if !s.limiter.Allowed(limiterKey, now) {
		return Session{}, ErrRateLimited
	}
	select {
	case s.authSlot <- struct{}{}:
		defer func() {
			<-s.authSlot
			debug.FreeOSMemory()
		}()
	default:
		return Session{}, ErrRateLimited
	}

	owner, err := s.repository.OwnerByUsername(ctx, usernameKey(username))
	encodedHash := s.dummyHash
	if err == nil {
		encodedHash = owner.PasswordHash
	} else if !errors.Is(err, ErrInvalidCredentials) {
		return Session{}, err
	}
	passwordValid, verifyErr := s.hasher.Verify(encodedHash, password)
	if verifyErr != nil || err != nil || !passwordValid {
		s.limiter.Failed(limiterKey, now)
		return Session{}, ErrInvalidCredentials
	}
	secret, err := s.protector.Decrypt(owner.TOTPSecretCipher)
	if err != nil {
		return Session{}, err
	}
	var recoveryHash []byte
	if !validateTOTP(secondFactor, secret, now) {
		normalized := normalizeRecoveryCode(secondFactor)
		if len(normalized) != 16 {
			s.limiter.Failed(limiterKey, now)
			return Session{}, ErrInvalidCredentials
		}
		recoveryHash = s.protector.MAC("recovery-code", normalized)
	}
	session, err := s.newSession(owner.ID, owner.Username, owner.AuthVersion, now)
	if err != nil {
		return Session{}, err
	}
	if err := s.repository.CreateSession(ctx, session, recoveryHash, now); err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			s.limiter.Failed(limiterKey, now)
		}
		return Session{}, err
	}
	s.limiter.Succeeded(limiterKey)
	return session, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrInvalidCredentials
	}
	now := s.now()
	session, err := s.repository.SessionByToken(ctx, tokenHash(token), now)
	if err != nil {
		return Session{}, err
	}
	session.Token = token
	if now.Sub(session.LastSeenAt) >= 5*time.Minute {
		if err := s.repository.TouchSession(ctx, session.TokenHash, now); err != nil {
			return Session{}, err
		}
		session.LastSeenAt = now
	}
	return session, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.repository.DeleteSession(ctx, tokenHash(token), s.now())
}

func (s *Service) Recover(ctx context.Context, username, password string) (RecoveryResult, error) {
	owner, err := s.repository.OwnerByUsername(ctx, usernameKey(username))
	if err != nil {
		return RecoveryResult{}, err
	}
	passwordHash, err := s.hashPassword(password)
	if err != nil {
		return RecoveryResult{}, err
	}
	siteName, err := s.repository.SiteName(ctx)
	if err != nil {
		return RecoveryResult{}, err
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      siteName,
		AccountName: owner.Username,
		Period:      30,
		SecretSize:  20,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("generate replacement TOTP key: %w", err)
	}
	ciphertext, err := s.protector.Encrypt(key.Secret())
	if err != nil {
		return RecoveryResult{}, err
	}
	codes, hashes, err := generateRecoveryCodes(s.protector)
	if err != nil {
		return RecoveryResult{}, err
	}
	if err := s.repository.RecoverOwner(ctx, owner, passwordHash, ciphertext, hashes, s.now()); err != nil {
		return RecoveryResult{}, err
	}
	return RecoveryResult{TOTPSecret: key.Secret(), TOTPURI: key.URL(), RecoveryCodes: codes}, nil
}

func (s *Service) NewFormNonce() (string, error) {
	random, err := randomToken(24)
	if err != nil {
		return "", err
	}
	signature := base64.RawURLEncoding.EncodeToString(s.protector.MAC("anonymous-form", random))
	return random + "." + signature, nil
}

func (s *Service) VerifyFormNonce(cookieValue, formValue string) bool {
	if !constantStringEqual(cookieValue, formValue) {
		return false
	}
	parts := strings.Split(cookieValue, ".")
	if len(parts) != 2 {
		return false
	}
	want := base64.RawURLEncoding.EncodeToString(s.protector.MAC("anonymous-form", parts[0]))
	return constantStringEqual(parts[1], want)
}

func (s *Service) SessionCSRF(sessionToken string) string {
	return base64.RawURLEncoding.EncodeToString(s.protector.MAC("session-csrf", sessionToken))
}

func (s *Service) VerifySessionCSRF(sessionToken, formValue string) bool {
	return constantStringEqual(s.SessionCSRF(sessionToken), formValue)
}

func (s *Service) SetupCSRF(setupToken string) string {
	return base64.RawURLEncoding.EncodeToString(s.protector.MAC("setup-csrf", setupToken))
}

func (s *Service) VerifySetupCSRF(setupToken, formValue string) bool {
	return constantStringEqual(s.SetupCSRF(setupToken), formValue)
}

func (s *Service) hashPassword(password string) (string, error) {
	select {
	case s.authSlot <- struct{}{}:
		defer func() {
			<-s.authSlot
			debug.FreeOSMemory()
		}()
	default:
		return "", ErrRateLimited
	}
	return s.hasher.Hash(password)
}

func ValidatePassword(password string) error {
	return validatePassword(password)
}

func (s *Service) newSession(ownerID int64, username string, authVersion int64, now time.Time) (Session, error) {
	token, err := randomToken(32)
	if err != nil {
		return Session{}, err
	}
	return Session{
		OwnerID:     ownerID,
		Username:    username,
		Token:       token,
		TokenHash:   tokenHash(token),
		AuthVersion: authVersion,
		ExpiresAt:   now.Add(s.sessionLifetime),
		LastSeenAt:  now,
	}, nil
}

func validateTOTP(code, secret string, now time.Time) bool {
	valid, err := totp.ValidateCustom(normalizeTOTP(code), secret, now, totp.ValidateOpts{
		Period:    30,
		Skew:      1,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	return err == nil && valid
}

func buildTOTPURI(issuer, account, secret string) string {
	query := url.Values{}
	query.Set("secret", secret)
	query.Set("issuer", issuer)
	query.Set("period", "30")
	query.Set("digits", "6")
	query.Set("algorithm", "SHA1")
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + query.Encode()
}
