package tests

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"user-service/handlers"
	"user-service/models"
)

func newTestGormStore(t *testing.T) *handlers.GormStore {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.HistoryEntry{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	return handlers.NewGormStore(db)
}

func TestGormStore_UserCRUD(t *testing.T) {
	store := newTestGormStore(t)

	user := &models.User{Email: "gorm@example.com", PasswordHash: "hash"}
	if err := store.CreateUser(user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if user.ID == 0 {
		t.Fatal("expected user ID to be set after create")
	}

	byEmail, err := store.GetUserByEmail("gorm@example.com")
	if err != nil {
		t.Fatalf("get by email: %v", err)
	}
	if byEmail.ID != user.ID {
		t.Fatalf("expected id %d, got %d", user.ID, byEmail.ID)
	}

	byID, err := store.GetUserByID(user.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if byID.Email != "gorm@example.com" {
		t.Fatalf("unexpected email: %s", byID.Email)
	}

	if _, err := store.GetUserByEmail("missing@example.com"); err != handlers.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := store.GetUserByID(9999); err != handlers.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGormStore_HistoryCRUD(t *testing.T) {
	store := newTestGormStore(t)

	user := &models.User{Email: "history@example.com", PasswordHash: "hash"}
	if err := store.CreateUser(user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	entry := &models.HistoryEntry{UserID: user.ID, SongA: "A", SongB: "B", GenreScore: 0.5, MoodScore: 0.6}
	if err := store.CreateHistory(entry); err != nil {
		t.Fatalf("create history: %v", err)
	}

	entries, err := store.GetHistoryByUserID(user.ID)
	if err != nil {
		t.Fatalf("get history: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
}
