package database

import (
	"database/sql"
	"strings"
	"testing"

	"gobot/internal/ai"
)

func newTestDatabase(t *testing.T) *Database {
	t.Helper()

	encryptor, err := newAPIKeyEncryptor("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	d := &Database{
		db:        db,
		driver:    "sqlite",
		encryptor: encryptor,
	}
	if err := d.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return d
}

func TestParseAPIKeyEncryptionKey(t *testing.T) {
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{name: "raw", input: "0123456789abcdef0123456789abcdef", valid: true},
		{name: "base64", input: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=", valid: true},
		{name: "raw base64", input: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY", valid: true},
		{name: "short", input: "short", valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := parseAPIKeyEncryptionKey(tt.input)
			if tt.valid && err != nil {
				t.Fatalf("expected valid key, got error: %v", err)
			}
			if !tt.valid && err == nil {
				t.Fatal("expected invalid key to fail")
			}
			if tt.valid && len(key) != 32 {
				t.Fatalf("expected 32-byte key, got %d", len(key))
			}
		})
	}
}

func TestAPIKeyEncryptorRoundTrip(t *testing.T) {
	encryptor, err := newAPIKeyEncryptor("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	encrypted, err := encryptor.encrypt("secret-api-key")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !strings.HasPrefix(encrypted, encryptedAPIKeyPrefix) {
		t.Fatalf("expected encrypted prefix, got %q", encrypted)
	}

	decrypted, err := encryptor.decrypt(encrypted)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if decrypted != "secret-api-key" {
		t.Fatalf("expected original plaintext, got %q", decrypted)
	}
}

func TestSetGuildConfigEncryptsAtRest(t *testing.T) {
	d := newTestDatabase(t)

	if err := d.SetGuildConfig("guild-1", "secret-api-key", "OpenAI", "gpt-4o"); err != nil {
		t.Fatalf("set guild config: %v", err)
	}

	var stored string
	row := d.db.QueryRow("SELECT api_key FROM guild_config WHERE guild_id = ?", "guild-1")
	if err := row.Scan(&stored); err != nil {
		t.Fatalf("scan raw api key: %v", err)
	}
	if stored == "secret-api-key" {
		t.Fatal("expected api key to be encrypted at rest")
	}
	if !strings.HasPrefix(stored, encryptedAPIKeyPrefix) {
		t.Fatalf("expected encrypted prefix, got %q", stored)
	}

	cfg, err := d.GetGuildConfig("guild-1")
	if err != nil {
		t.Fatalf("get guild config: %v", err)
	}
	if cfg.APIKey != "secret-api-key" {
		t.Fatalf("expected decrypted api key, got %q", cfg.APIKey)
	}
}

func TestMigratePlaintextAPIKeys(t *testing.T) {
	d := newTestDatabase(t)

	if _, err := d.db.Exec(
		"INSERT INTO guild_config (guild_id, api_key, provider, model, updated_at) VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)",
		"guild-2",
		"plaintext-key",
		"Gemini",
		"gemini-2.5-flash",
	); err != nil {
		t.Fatalf("insert plaintext row: %v", err)
	}

	if err := d.migratePlaintextAPIKeys(); err != nil {
		t.Fatalf("migrate plaintext api keys: %v", err)
	}

	var stored string
	row := d.db.QueryRow("SELECT api_key FROM guild_config WHERE guild_id = ?", "guild-2")
	if err := row.Scan(&stored); err != nil {
		t.Fatalf("scan migrated api key: %v", err)
	}
	if !strings.HasPrefix(stored, encryptedAPIKeyPrefix) {
		t.Fatalf("expected migrated api key to be encrypted, got %q", stored)
	}

	cfg, err := d.GetGuildConfig("guild-2")
	if err != nil {
		t.Fatalf("get migrated guild config: %v", err)
	}
	if cfg.APIKey != "plaintext-key" {
		t.Fatalf("expected decrypted plaintext key, got %q", cfg.APIKey)
	}
}

func TestGuildSystemPromptRoundTrip(t *testing.T) {
	d := newTestDatabase(t)

	const prompt = "Be concise.\nReference server policies when relevant."
	if err := d.SetGuildSystemPrompt("guild-1", prompt); err != nil {
		t.Fatalf("set guild system prompt: %v", err)
	}

	got, err := d.GetGuildSystemPrompt("guild-1")
	if err != nil {
		t.Fatalf("get guild system prompt: %v", err)
	}
	if got != prompt {
		t.Fatalf("expected prompt %q, got %q", prompt, got)
	}
}

func TestClearGuildSystemPromptRemovesRow(t *testing.T) {
	d := newTestDatabase(t)

	if err := d.SetGuildSystemPrompt("guild-1", "Use internal terminology."); err != nil {
		t.Fatalf("set guild system prompt: %v", err)
	}
	if err := d.ClearGuildSystemPrompt("guild-1"); err != nil {
		t.Fatalf("clear guild system prompt: %v", err)
	}

	got, err := d.GetGuildSystemPrompt("guild-1")
	if err != nil {
		t.Fatalf("get guild system prompt after clear: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty prompt after clear, got %q", got)
	}
}

func TestSetGuildSystemPromptRejectsOversizedPrompt(t *testing.T) {
	d := newTestDatabase(t)

	oversized := strings.Repeat("x", ai.MaxGuildSystemPromptChars+1)
	if err := d.SetGuildSystemPrompt("guild-1", oversized); err == nil {
		t.Fatal("expected oversized prompt to fail")
	}

	got, err := d.GetGuildSystemPrompt("guild-1")
	if err != nil {
		t.Fatalf("get guild system prompt after rejection: %v", err)
	}
	if got != "" {
		t.Fatalf("expected rejected prompt not to be persisted, got %q", got)
	}
}
