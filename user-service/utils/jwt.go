package utils

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var ErrInvalidToken = errors.New("invalid or expired token")

const defaultTokenTTL = 24 * time.Hour

const (
	// MinSecretKeyLength is the shortest SECRET_KEY the service accepts, in
	// bytes. HS256 is only as strong as its key; 32 bytes matches the 256-bit
	// output of the hash.
	MinSecretKeyLength = 32
	// placeholderSecretKey is the value shipped in .env.example. It is public,
	// so tokens signed with it can be forged by anyone.
	placeholderSecretKey = "change-me-to-a-long-random-string"
)

// ValidateSecretKey reports why key must not be used to sign tokens, or nil
// if it is acceptable. It is meant to be called once at startup so that a
// misconfigured service refuses to run instead of issuing forgeable tokens.
func ValidateSecretKey(key string) error {
	switch {
	case key == "":
		return errors.New("SECRET_KEY is not set")
	case key == placeholderSecretKey:
		return errors.New("SECRET_KEY is still the placeholder from .env.example")
	case len(key) < MinSecretKeyLength:
		return fmt.Errorf("SECRET_KEY is %d characters long, need at least %d", len(key), MinSecretKeyLength)
	default:
		return nil
	}
}

// Claims are the custom JWT claims embedded in every issued token.
type Claims struct {
	UserID uint   `json:"user_id"`
	Email  string `json:"email"`
	jwt.RegisteredClaims
}

func secretKey() []byte {
	return []byte(os.Getenv("SECRET_KEY"))
}

// GenerateToken issues a signed JWT for the given user.
func GenerateToken(userID uint, email string) (string, error) {
	claims := Claims{
		UserID: userID,
		Email:  email,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(defaultTokenTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(secretKey())
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signed, nil
}

// ParseToken validates a JWT and returns its claims.
func ParseToken(tokenString string) (*Claims, error) {
	claims := &Claims{}

	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secretKey(), nil
	})
	if err != nil || !token.Valid {
		return nil, ErrInvalidToken
	}

	return claims, nil
}
