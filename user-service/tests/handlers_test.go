package tests

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"user-service/handlers"
	"user-service/models"
	"user-service/utils"
)

func init() {
	os.Setenv("SECRET_KEY", "test-secret-key")
}

func newTestHandlers(store *mockStore) *handlers.Handlers {
	return handlers.New(store, nil)
}

func doRequest(h http.HandlerFunc, method, target string, body any) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, target, reader)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestRegister_Success(t *testing.T) {
	store := newMockStore()
	h := newTestHandlers(store)

	rec := doRequest(h.Register, http.MethodPost, "/register", map[string]string{
		"email":    "alice@example.com",
		"password": "supersecret",
	})

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("expected non-empty token")
	}

	stored, ok := store.usersByEmail["alice@example.com"]
	if !ok {
		t.Fatal("expected user to be stored")
	}
	if stored.PasswordHash == "supersecret" {
		t.Fatal("password must be hashed, not stored in plaintext")
	}
}

func TestRegister_DuplicateEmail(t *testing.T) {
	store := newMockStore()
	store.usersByEmail["alice@example.com"] = &models.User{ID: 1, Email: "alice@example.com"}
	h := newTestHandlers(store)

	rec := doRequest(h.Register, http.MethodPost, "/register", map[string]string{
		"email":    "alice@example.com",
		"password": "supersecret",
	})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestRegister_MissingFields(t *testing.T) {
	store := newMockStore()
	h := newTestHandlers(store)

	rec := doRequest(h.Register, http.MethodPost, "/register", map[string]string{
		"email": "alice@example.com",
	})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestRegister_InvalidBody(t *testing.T) {
	store := newMockStore()
	h := newTestHandlers(store)

	req := httptest.NewRequest(http.MethodPost, "/register", bytes.NewReader([]byte("{invalid")))
	rec := httptest.NewRecorder()
	h.Register(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestRegister_DBError(t *testing.T) {
	store := newMockStore()
	store.createUserErr = errors.New("boom")
	h := newTestHandlers(store)

	rec := doRequest(h.Register, http.MethodPost, "/register", map[string]string{
		"email":    "alice@example.com",
		"password": "supersecret",
	})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestLogin_Success(t *testing.T) {
	store := newMockStore()
	hash, _ := bcrypt.GenerateFromPassword([]byte("supersecret"), bcrypt.DefaultCost)
	store.usersByEmail["alice@example.com"] = &models.User{ID: 1, Email: "alice@example.com", PasswordHash: string(hash)}
	h := newTestHandlers(store)

	rec := doRequest(h.Login, http.MethodPost, "/login", map[string]string{
		"email":    "alice@example.com",
		"password": "supersecret",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	store := newMockStore()
	hash, _ := bcrypt.GenerateFromPassword([]byte("supersecret"), bcrypt.DefaultCost)
	store.usersByEmail["alice@example.com"] = &models.User{ID: 1, Email: "alice@example.com", PasswordHash: string(hash)}
	h := newTestHandlers(store)

	rec := doRequest(h.Login, http.MethodPost, "/login", map[string]string{
		"email":    "alice@example.com",
		"password": "wrongpassword",
	})

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestLogin_UserNotFound(t *testing.T) {
	store := newMockStore()
	h := newTestHandlers(store)

	rec := doRequest(h.Login, http.MethodPost, "/login", map[string]string{
		"email":    "ghost@example.com",
		"password": "whatever",
	})

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestLogin_InvalidBody(t *testing.T) {
	store := newMockStore()
	h := newTestHandlers(store)

	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader([]byte("{invalid")))
	rec := httptest.NewRecorder()
	h.Login(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestGetMe_Success(t *testing.T) {
	store := newMockStore()
	store.usersByID[1] = &models.User{ID: 1, Email: "alice@example.com"}
	h := newTestHandlers(store)

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req = handlers.WithUserID(req, 1)
	rec := httptest.NewRecorder()
	h.GetMe(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		ID    uint   `json:"id"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Email != "alice@example.com" {
		t.Fatalf("unexpected email: %s", resp.Email)
	}
}

func TestGetMe_NotFound(t *testing.T) {
	store := newMockStore()
	h := newTestHandlers(store)

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req = handlers.WithUserID(req, 99)
	rec := httptest.NewRecorder()
	h.GetMe(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestGetMe_Unauthorized(t *testing.T) {
	store := newMockStore()
	h := newTestHandlers(store)

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	rec := httptest.NewRecorder()
	h.GetMe(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAddHistory_Success(t *testing.T) {
	store := newMockStore()
	h := newTestHandlers(store)

	body, _ := json.Marshal(map[string]any{
		"song_a":      "Song A",
		"song_b":      "Song B",
		"genre_score": 0.8,
		"mood_score":  0.5,
	})
	req := httptest.NewRequest(http.MethodPost, "/history", bytes.NewReader(body))
	req = handlers.WithUserID(req, 1)
	rec := httptest.NewRecorder()
	h.AddHistory(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.history[1]) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(store.history[1]))
	}
}

func TestAddHistory_MissingFields(t *testing.T) {
	store := newMockStore()
	h := newTestHandlers(store)

	body, _ := json.Marshal(map[string]any{"song_a": "Song A"})
	req := httptest.NewRequest(http.MethodPost, "/history", bytes.NewReader(body))
	req = handlers.WithUserID(req, 1)
	rec := httptest.NewRecorder()
	h.AddHistory(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAddHistory_Unauthorized(t *testing.T) {
	store := newMockStore()
	h := newTestHandlers(store)

	req := httptest.NewRequest(http.MethodPost, "/history", bytes.NewReader([]byte("{}")))
	rec := httptest.NewRecorder()
	h.AddHistory(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestGetHistory_Success(t *testing.T) {
	store := newMockStore()
	store.history[1] = []models.HistoryEntry{
		{ID: 1, UserID: 1, SongA: "A", SongB: "B", GenreScore: 0.5, MoodScore: 0.5},
	}
	h := newTestHandlers(store)

	req := httptest.NewRequest(http.MethodGet, "/history", nil)
	req = handlers.WithUserID(req, 1)
	rec := httptest.NewRecorder()
	h.GetHistory(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var entries []models.HistoryEntry
	if err := json.NewDecoder(rec.Body).Decode(&entries); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
}

func TestGetHistory_Unauthorized(t *testing.T) {
	store := newMockStore()
	h := newTestHandlers(store)

	req := httptest.NewRequest(http.MethodGet, "/history", nil)
	rec := httptest.NewRecorder()
	h.GetHistory(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestGetHistory_DBError(t *testing.T) {
	store := newMockStore()
	store.getHistoryErr = errors.New("boom")
	h := newTestHandlers(store)

	req := httptest.NewRequest(http.MethodGet, "/history", nil)
	req = handlers.WithUserID(req, 1)
	rec := httptest.NewRecorder()
	h.GetHistory(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestAddHistory_DBError(t *testing.T) {
	store := newMockStore()
	store.createHistErr = errors.New("boom")
	h := newTestHandlers(store)

	body, _ := json.Marshal(map[string]any{"song_a": "A", "song_b": "B"})
	req := httptest.NewRequest(http.MethodPost, "/history", bytes.NewReader(body))
	req = handlers.WithUserID(req, 1)
	rec := httptest.NewRecorder()
	h.AddHistory(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestLogin_DBError(t *testing.T) {
	store := newMockStore()
	store.getByEmailErr = errors.New("boom")
	h := newTestHandlers(store)

	rec := doRequest(h.Login, http.MethodPost, "/login", map[string]string{
		"email":    "alice@example.com",
		"password": "whatever",
	})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestGetMe_DBError(t *testing.T) {
	store := newMockStore()
	store.getByIDErr = errors.New("boom")
	h := newTestHandlers(store)

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req = handlers.WithUserID(req, 1)
	rec := httptest.NewRecorder()
	h.GetMe(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestRegister_LookupError(t *testing.T) {
	store := newMockStore()
	store.getByEmailErr = errors.New("boom")
	h := newTestHandlers(store)

	rec := doRequest(h.Register, http.MethodPost, "/register", map[string]string{
		"email":    "alice@example.com",
		"password": "supersecret",
	})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestAuthMiddleware(t *testing.T) {
	os.Setenv("SECRET_KEY", "test-secret-key")

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("missing header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/me", nil)
		rec := httptest.NewRecorder()
		handlers.AuthMiddleware(next).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("invalid token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/me", nil)
		req.Header.Set("Authorization", "Bearer not-a-token")
		rec := httptest.NewRecorder()
		handlers.AuthMiddleware(next).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("valid token", func(t *testing.T) {
		token, err := utils.GenerateToken(1, "alice@example.com")
		if err != nil {
			t.Fatalf("generate token: %v", err)
		}
		req := httptest.NewRequest(http.MethodGet, "/me", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		handlers.AuthMiddleware(next).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})
}

func TestRateLimit(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	limited := handlers.RateLimit(1, 2, next)

	req := httptest.NewRequest(http.MethodPost, "/register", nil)
	req.RemoteAddr = "1.2.3.4:5555"

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		limited.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	limited.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
}
