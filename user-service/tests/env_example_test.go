package tests

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"user-service/utils"
)

// TestEnvExampleSecretIsRejected guards the example file: whoever copies
// .env.example to .env without editing it must get a service that refuses to
// start, not one that signs tokens with a key published in the repository.
func TestEnvExampleSecretIsRejected(t *testing.T) {
	f, err := os.Open("../.env.example")
	if err != nil {
		t.Fatalf("open .env.example: %v", err)
	}
	defer f.Close()

	var key string
	var found bool
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if value, ok := strings.CutPrefix(strings.TrimSpace(scanner.Text()), "SECRET_KEY="); ok {
			key, found = value, true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	if !found {
		t.Fatal(".env.example has no SECRET_KEY line")
	}

	if err := utils.ValidateSecretKey(key); err == nil {
		t.Errorf("SECRET_KEY=%q from .env.example passes validation; the example must never be usable as is", key)
	}
}
