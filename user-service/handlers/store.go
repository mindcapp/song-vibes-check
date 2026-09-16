package handlers

import (
	"errors"

	"gorm.io/gorm"

	"user-service/models"
)

var ErrNotFound = errors.New("record not found")

// UserStore abstracts persistence so handlers can be unit tested with a mock.
type UserStore interface {
	CreateUser(u *models.User) error
	GetUserByEmail(email string) (*models.User, error)
	GetUserByID(id uint) (*models.User, error)
	CreateHistory(h *models.HistoryEntry) error
	GetHistoryByUserID(userID uint) ([]models.HistoryEntry, error)
}

// GormStore is the production UserStore backed by GORM/PostgreSQL.
type GormStore struct {
	DB *gorm.DB
}

func NewGormStore(db *gorm.DB) *GormStore {
	return &GormStore{DB: db}
}

func (s *GormStore) CreateUser(u *models.User) error {
	return s.DB.Create(u).Error
}

func (s *GormStore) GetUserByEmail(email string) (*models.User, error) {
	var user models.User
	if err := s.DB.Where("email = ?", email).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &user, nil
}

func (s *GormStore) GetUserByID(id uint) (*models.User, error) {
	var user models.User
	if err := s.DB.First(&user, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &user, nil
}

func (s *GormStore) CreateHistory(h *models.HistoryEntry) error {
	return s.DB.Create(h).Error
}

func (s *GormStore) GetHistoryByUserID(userID uint) ([]models.HistoryEntry, error) {
	var entries []models.HistoryEntry
	if err := s.DB.Where("user_id = ?", userID).Order("created_at desc").Find(&entries).Error; err != nil {
		return nil, err
	}
	return entries, nil
}
