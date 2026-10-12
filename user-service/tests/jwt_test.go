package tests

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"user-service/utils"
)

func TestGenerateAndParseToken(t *testing.T) {
	os.Setenv("SECRET_KEY", "test-secret-key")

	token, err := utils.GenerateToken(42, "alice@example.com")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	claims, err := utils.ParseToken(token)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	if claims.UserID != 42 {
		t.Fatalf("expected user id 42, got %d", claims.UserID)
	}
	if claims.Email != "alice@example.com" {
		t.Fatalf("expected email alice@example.com, got %s", claims.Email)
	}
}

func TestParseToken_Invalid(t *testing.T) {
	os.Setenv("SECRET_KEY", "test-secret-key")

	if _, err := utils.ParseToken("not-a-valid-token"); err == nil {
		t.Fatal("expected error for malformed token")
	}
}

func TestParseToken_WrongSecret(t *testing.T) {
	os.Setenv("SECRET_KEY", "secret-a")
	token, err := utils.GenerateToken(1, "bob@example.com")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	os.Setenv("SECRET_KEY", "secret-b")
	if _, err := utils.ParseToken(token); err == nil {
		t.Fatal("expected error when secret key differs")
	}

	os.Setenv("SECRET_KEY", "test-secret-key")
}

func TestParseToken_Expired(t *testing.T) {
	os.Setenv("SECRET_KEY", "test-secret-key")

	claims := utils.Claims{
		UserID: 1,
		Email:  "expired@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte("test-secret-key"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	if _, err := utils.ParseToken(signed); err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestValidateSecretKey(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{name: "empty", key: "", wantErr: true},
		{name: "placeholder from .env.example", key: "change-me-to-a-long-random-string", wantErr: true},
		{name: "too short", key: "short-key", wantErr: true},
		{name: "31 characters", key: strings.Repeat("a", 31), wantErr: true},
		{name: "32 characters", key: strings.Repeat("a", 32), wantErr: false},
		{name: "openssl rand -hex 32", key: "9f2c1e7ab4d3586e0c1a7f5b2d4e6f8091a3b5c7d9e1f20416283a4c5e6f7081", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := utils.ValidateSecretKey(tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSecretKey(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}
