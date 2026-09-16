package db

import (
	"fmt"
	"os"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"user-service/models"
)

// Init opens a PostgreSQL connection using DATABASE_URL, falling back to
// individual DB_* environment variables, and runs auto-migrations.
func Init() (*gorm.DB, error) {
	dsn := dsnFromEnv()

	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	if err := database.AutoMigrate(&models.User{}, &models.HistoryEntry{}); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return database, nil
}

func dsnFromEnv() string {
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return url
	}

	host := getenvDefault("DB_HOST", "localhost")
	port := getenvDefault("DB_PORT", "5432")
	user := getenvDefault("DB_USER", "postgres")
	password := os.Getenv("DB_PASSWORD")
	name := getenvDefault("DB_NAME", "user_service")
	sslmode := getenvDefault("DB_SSLMODE", "disable")

	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		host, port, user, password, name, sslmode,
	)
}

func getenvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
