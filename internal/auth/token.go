package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Role string

const (
	RoleOwner      Role = "owner"
	RoleAccountant Role = "accountant"
	RoleEmployee   Role = "employee"
	RoleCustomer   Role = "customer"
)

func (r Role) Valid() bool {
	switch r {
	case RoleOwner, RoleAccountant, RoleEmployee, RoleCustomer:
		return true
	}
	return false
}

// Claims erişim belirtecinin taşıdığı kimlik bilgisidir.
type Claims struct {
	UserID     string `json:"uid"`
	BusinessID string `json:"bid"`
	Role       Role   `json:"role"`
	jwt.RegisteredClaims
}

type TokenIssuer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

func NewTokenIssuer(secret string, ttl time.Duration) *TokenIssuer {
	return &TokenIssuer{secret: []byte(secret), ttl: ttl, now: time.Now}
}

// WithClock testlerde zamanı kontrol etmek içindir.
func (t *TokenIssuer) WithClock(now func() time.Time) *TokenIssuer {
	c := *t
	c.now = now
	return &c
}

func (t *TokenIssuer) TTL() time.Duration { return t.ttl }

func (t *TokenIssuer) IssueAccess(userID, businessID string, role Role) (string, error) {
	n := t.now()
	c := Claims{
		UserID: userID, BusinessID: businessID, Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(n),
			ExpiresAt: jwt.NewNumericDate(n.Add(t.ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(t.secret)
}

var ErrInvalidToken = errors.New("geçersiz veya süresi dolmuş belirteç")

// ParseAccess imzayı, algoritmayı ve süreyi doğrular. Yalnızca HS256 kabul edilir.
func (t *TokenIssuer) ParseAccess(raw string) (*Claims, error) {
	var c Claims
	tok, err := jwt.ParseWithClaims(raw, &c, func(*jwt.Token) (any, error) { return t.secret, nil },
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithTimeFunc(t.now),
		jwt.WithExpirationRequired())
	if err != nil || !tok.Valid || !c.Role.Valid() || c.UserID == "" {
		return nil, ErrInvalidToken
	}
	return &c, nil
}

// NewRefreshToken rastgele bir yenileme belirteci ve veritabanında saklanacak SHA-256 özetini üretir.
// Düz belirteç veritabanına yazılmaz.
func NewRefreshToken() (plain string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", nil, err
	}
	plain = base64.RawURLEncoding.EncodeToString(b)
	return plain, HashRefreshToken(plain), nil
}

func HashRefreshToken(plain string) []byte {
	h := sha256.Sum256([]byte(plain))
	return h[:]
}
