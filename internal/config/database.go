package config

import "os"

// DatabaseURLEnv is the optional PostgreSQL connection string used by database integration tests.
const DatabaseURLEnv = "SOFTWARE_FACTORY_DATABASE_URL"

// DatabaseURL reads the optional PostgreSQL connection string for database integration tests.
func DatabaseURL() string {
	return os.Getenv(DatabaseURLEnv)
}
