package database

import (
	"strings"
	"testing"
)

func TestBaselineHasNoLegacyTokenTable(t *testing.T) {
	data, err := migrationFiles.ReadFile("migrations/000001_baseline.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "api_tokens") {
		t.Fatal("baseline contains old token table")
	}
}
