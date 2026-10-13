package db

import (
	"fmt"
	"os"
	"strings"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"user-service/models"
)

// Init opens a PostgreSQL connection using DATABASE_URL, falling back to
// individual DB_* environment variables, and runs auto-migrations.
func Init() (*gorm.DB, error) {
	dsn := DSNFromEnv()

	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	if err := database.AutoMigrate(&models.User{}, &models.HistoryEntry{}); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return database, nil
}

// DSNFromEnv builds the PostgreSQL connection string from the environment.
// DATABASE_URL, when set, is used as is and takes precedence. Otherwise the
// DSN is assembled from DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME and
// DB_SSLMODE; every value is quoted, so a password may contain spaces, quotes
// or any other character without escaping.
func DSNFromEnv() string {
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return url
	}

	return strings.Join([]string{
		"host=" + quoteDSNValue(getenvDefault("DB_HOST", "localhost")),
		"port=" + quoteDSNValue(getenvDefault("DB_PORT", "5432")),
		"user=" + quoteDSNValue(getenvDefault("DB_USER", "postgres")),
		"password=" + quoteDSNValue(os.Getenv("DB_PASSWORD")),
		"dbname=" + quoteDSNValue(getenvDefault("DB_NAME", "user_service")),
		"sslmode=" + quoteDSNValue(getenvDefault("DB_SSLMODE", "disable")),
	}, " ")
}

// quoteDSNValue quotes a value for a keyword/value connection string as libpq
// defines it: wrapped in single quotes, with backslashes and single quotes
// escaped by a backslash.
func quoteDSNValue(value string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value) + "'"
}

func getenvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
