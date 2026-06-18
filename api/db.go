package main

import (
	"database/sql"
	"fmt"
	"os"
)

// openEmbeddedDB writes the embedded SQLite bytes to a temp file and opens it.
func openEmbeddedDB() (*sql.DB, error) {
	if len(sqliteData) == 0 {
		return nil, fmt.Errorf("vpic.sqlite is empty — run 'make convert' then 'make build'")
	}

	f, err := os.CreateTemp("", "vpic-*.sqlite")
	if err != nil {
		return nil, fmt.Errorf("temp file: %w", err)
	}
	if _, err := f.Write(sqliteData); err != nil {
		f.Close()
		return nil, fmt.Errorf("write db: %w", err)
	}
	path := f.Name()
	f.Close()

	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}

	// Validate schema — catches the case where make build ran before make convert.
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('patterns','wmi')`).Scan(&count)
	if err != nil || count < 2 {
		return nil, fmt.Errorf("vpic.sqlite is missing required tables (got %d/2) — run 'make convert' then 'make build'", count)
	}

	return db, nil
}
