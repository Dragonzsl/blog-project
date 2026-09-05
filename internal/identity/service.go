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
