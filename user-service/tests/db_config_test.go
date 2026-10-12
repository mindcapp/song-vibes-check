package tests

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"user-service/db"
)

var dbEnvVars = []string{"DATABASE_URL", "DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME", "DB_SSLMODE"}

// setDBEnv clears every database variable and then sets the given ones, so a
// test is not affected by the environment it happens to run in.
func setDBEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, name := range dbEnvVars {
		t.Setenv(name, "")
	}
	for name, value := range env {
		t.Setenv(name, value)
	}
}

// TestDSNFromEnv checks both ways of configuring the database. The DSN is
// parsed with pgx, the driver the service uses, so the test proves the
// connection parameters arrive intact rather than comparing strings.
func TestDSNFromEnv(t *testing.T) {
	tests := []struct {
		name         string
		env          map[string]string
		wantHost     string
		wantPort     uint16
		wantUser     string
		wantPassword string
		wantDatabase string
	}{
		{
			name:     "DATABASE_URL",
			env:      map[string]string{"DATABASE_URL": "postgres://alice:s3cret@db.internal:6543/users?sslmode=disable"},
			wantHost: "db.internal", wantPort: 6543, wantUser: "alice", wantPassword: "s3cret", wantDatabase: "users",
		},
		{
			name: "DB_* variables",
			env: map[string]string{
				"DB_HOST": "user-service-db", "DB_PORT": "5432", "DB_USER": "user_service",
				"DB_PASSWORD": "user_service", "DB_NAME": "user_service", "DB_SSLMODE": "disable",
			},
			wantHost: "user-service-db", wantPort: 5432, wantUser: "user_service", wantPassword: "user_service", wantDatabase: "user_service",
		},
		{
			name: "DATABASE_URL wins over DB_*",
			env: map[string]string{
				"DATABASE_URL": "postgres://alice:s3cret@db.internal:6543/users?sslmode=disable",
				"DB_HOST":      "ignored-host", "DB_USER": "ignored-user", "DB_PASSWORD": "ignored", "DB_NAME": "ignored",
			},
			wantHost: "db.internal", wantPort: 6543, wantUser: "alice", wantPassword: "s3cret", wantDatabase: "users",
		},
		{
			name:     "DB_* defaults",
			env:      map[string]string{"DB_PASSWORD": "pw"},
			wantHost: "localhost", wantPort: 5432, wantUser: "postgres", wantPassword: "pw", wantDatabase: "user_service",
		},
		{
			name:     "password with characters special to a DSN",
			env:      map[string]string{"DB_HOST": "db", "DB_USER": "u", "DB_PASSWORD": `p@ss word='q' \ / ?&#%`, "DB_NAME": "d"},
			wantHost: "db", wantPort: 5432, wantUser: "u", wantPassword: `p@ss word='q' \ / ?&#%`, wantDatabase: "d",
		},
		{
			name:     "password that looks like another parameter",
			env:      map[string]string{"DB_HOST": "db", "DB_USER": "u", "DB_PASSWORD": "x host=evil.example", "DB_NAME": "d"},
			wantHost: "db", wantPort: 5432, wantUser: "u", wantPassword: "x host=evil.example", wantDatabase: "d",
		},
		{
			name:     "empty password",
			env:      map[string]string{"DB_HOST": "db", "DB_USER": "u", "DB_NAME": "d"},
			wantHost: "db", wantPort: 5432, wantUser: "u", wantPassword: "", wantDatabase: "d",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setDBEnv(t, tt.env)

			cfg, err := pgx.ParseConfig(db.DSNFromEnv())
			if err != nil {
				t.Fatalf("DSN %q does not parse: %v", db.DSNFromEnv(), err)
			}

			if cfg.Host != tt.wantHost || cfg.Port != tt.wantPort {
				t.Errorf("host:port = %s:%d, want %s:%d", cfg.Host, cfg.Port, tt.wantHost, tt.wantPort)
			}
			if cfg.User != tt.wantUser {
				t.Errorf("user = %q, want %q", cfg.User, tt.wantUser)
			}
			if cfg.Password != tt.wantPassword {
				t.Errorf("password = %q, want %q", cfg.Password, tt.wantPassword)
			}
			if cfg.Database != tt.wantDatabase {
				t.Errorf("database = %q, want %q", cfg.Database, tt.wantDatabase)
			}
			if cfg.TLSConfig != nil {
				t.Error("TLS is enabled, want sslmode=disable to be honoured")
			}
		})
	}
}
