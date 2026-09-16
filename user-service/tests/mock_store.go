package tests

import (
	"user-service/handlers"
	"user-service/models"
)

// mockStore is an in-memory handlers.UserStore used to unit test handlers
// without a real database.
type mockStore struct {
	usersByEmail map[string]*models.User
	usersByID    map[uint]*models.User
	history      map[uint][]models.HistoryEntry
	nextUserID   uint
	nextHistID   uint

	createUserErr error
	getByEmailErr error
	getByIDErr    error
	createHistErr error
	getHistoryErr error
}

func newMockStore() *mockStore {
	return &mockStore{
		usersByEmail: make(map[string]*models.User),
		usersByID:    make(map[uint]*models.User),
		history:      make(map[uint][]models.HistoryEntry),
	}
}

func (m *mockStore) CreateUser(u *models.User) error {
	if m.createUserErr != nil {
		return m.createUserErr
	}
	m.nextUserID++
	u.ID = m.nextUserID
	m.usersByEmail[u.Email] = u
	m.usersByID[u.ID] = u
	return nil
}

func (m *mockStore) GetUserByEmail(email string) (*models.User, error) {
	if m.getByEmailErr != nil {
		return nil, m.getByEmailErr
	}
	u, ok := m.usersByEmail[email]
	if !ok {
		return nil, handlers.ErrNotFound
	}
	return u, nil
}

func (m *mockStore) GetUserByID(id uint) (*models.User, error) {
	if m.getByIDErr != nil {
		return nil, m.getByIDErr
	}
	u, ok := m.usersByID[id]
	if !ok {
		return nil, handlers.ErrNotFound
	}
	return u, nil
}

func (m *mockStore) CreateHistory(h *models.HistoryEntry) error {
	if m.createHistErr != nil {
		return m.createHistErr
	}
	m.nextHistID++
	h.ID = m.nextHistID
	m.history[h.UserID] = append(m.history[h.UserID], *h)
	return nil
}

func (m *mockStore) GetHistoryByUserID(userID uint) ([]models.HistoryEntry, error) {
	if m.getHistoryErr != nil {
		return nil, m.getHistoryErr
	}
	return m.history[userID], nil
}
