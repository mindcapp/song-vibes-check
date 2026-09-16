package models

import "time"

type User struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Email        string    `gorm:"uniqueIndex;not null" json:"email"`
	PasswordHash string    `gorm:"not null" json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

type HistoryEntry struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	UserID     uint      `gorm:"index;not null" json:"user_id"`
	SongA      string    `gorm:"not null" json:"song_a"`
	SongB      string    `gorm:"not null" json:"song_b"`
	GenreScore float64   `json:"genre_score"`
	MoodScore  float64   `json:"mood_score"`
	CreatedAt  time.Time `json:"created_at"`
}
