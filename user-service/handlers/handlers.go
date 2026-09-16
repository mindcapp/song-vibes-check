package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"golang.org/x/crypto/bcrypt"

	"user-service/models"
	"user-service/utils"
)

type Handlers struct {
	Store  UserStore
	Logger *slog.Logger
}

func New(store UserStore, logger *slog.Logger) *Handlers {
	return &Handlers{Store: store, Logger: logger}
}

type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type addHistoryRequest struct {
	SongA      string  `json:"song_a"`
	SongB      string  `json:"song_b"`
	GenreScore float64 `json:"genre_score"`
	MoodScore  float64 `json:"mood_score"`
}

type tokenResponse struct {
	Token string `json:"token"`
}

type meResponse struct {
	ID        uint   `json:"id"`
	Email     string `json:"email"`
	CreatedAt string `json:"created_at"`
}

func (h *Handlers) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password are required")
		return
	}

	if _, err := h.Store.GetUserByEmail(req.Email); err == nil {
		writeError(w, http.StatusBadRequest, "email already exists")
		return
	} else if !errors.Is(err, ErrNotFound) {
		h.logError("lookup user by email", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		h.logError("hash password", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	user := &models.User{Email: req.Email, PasswordHash: string(hash)}
	if err := h.Store.CreateUser(user); err != nil {
		h.logError("create user", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	token, err := utils.GenerateToken(user.ID, user.Email)
	if err != nil {
		h.logError("generate token", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusCreated, tokenResponse{Token: token})
}

func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	user, err := h.Store.GetUserByEmail(req.Email)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		h.logError("lookup user by email", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	token, err := utils.GenerateToken(user.ID, user.Email)
	if err != nil {
		h.logError("generate token", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, tokenResponse{Token: token})
}

func (h *Handlers) GetMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	user, err := h.Store.GetUserByID(userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		h.logError("lookup user by id", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, meResponse{
		ID:        user.ID,
		Email:     user.Email,
		CreatedAt: user.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

func (h *Handlers) AddHistory(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req addHistoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SongA == "" || req.SongB == "" {
		writeError(w, http.StatusBadRequest, "song_a and song_b are required")
		return
	}

	entry := &models.HistoryEntry{
		UserID:     userID,
		SongA:      req.SongA,
		SongB:      req.SongB,
		GenreScore: req.GenreScore,
		MoodScore:  req.MoodScore,
	}
	if err := h.Store.CreateHistory(entry); err != nil {
		h.logError("create history entry", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusCreated, entry)
}

func (h *Handlers) GetHistory(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	entries, err := h.Store.GetHistoryByUserID(userID)
	if err != nil {
		h.logError("list history", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, entries)
}

func (h *Handlers) logError(msg string, err error) {
	if h.Logger != nil {
		h.Logger.Error(msg, "error", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
