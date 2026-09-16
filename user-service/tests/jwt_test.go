package tests

import (
	"os"
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
