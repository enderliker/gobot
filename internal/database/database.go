package database

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

const MaxGuildSystemPromptChars = 4000

type GuildConfig struct {
	GuildID      string
	APIKey       string
	Provider     string
	Model        string
	MultiMessage bool
}

type Database struct {
	db        *sql.DB
	driver    string
	encryptor *apiKeyEncryptor
}

var Default *Database

const encryptedAPIKeyPrefix = "enc:v1:"

type apiKeyEncryptor struct {
	aead cipher.AEAD
}

func Init() error {
	driver := os.Getenv("DB_DRIVER")
	if driver == "" {
		driver = "sqlite"
	}

	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		if driver == "sqlite" {
			dsn = "gobot.db"
		} else {
			return fmt.Errorf("DB_DSN is required for driver %q", driver)
		}
	}

	var (
		db  *sql.DB
		err error
	)

	if driver == "mysql" {
		db, err = openMySQL(dsn)
	} else {
		db, err = sql.Open(driver, dsn)
	}
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}

	if err := db.Ping(); err != nil {
		return fmt.Errorf("ping: %w", err)
	}

	encryptor, err := newAPIKeyEncryptorFromEnv()
	if err != nil {
		return fmt.Errorf("api key encryption: %w", err)
	}

	Default = &Database{db: db, driver: driver, encryptor: encryptor}
	if err := Default.migrate(); err != nil {
		return err
	}
	return Default.migratePlaintextAPIKeys()
}

func (d *Database) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

// RawDB exposes the underlying SQL database handle for standalone metrics/web queries.
func (d *Database) RawDB() *sql.DB {
	if d == nil {
		return nil
	}
	return d.db
}

func (d *Database) migrate() error {
	var q string
	var alterModel string
	var alterMultiMessage string
	var promptTable string
	var warningsTable string
	if d.driver == "mysql" {
		q = `CREATE TABLE IF NOT EXISTS guild_config (
			guild_id      VARCHAR(20)  PRIMARY KEY,
			api_key       TEXT         NOT NULL,
			provider      VARCHAR(64)  NOT NULL,
			model         VARCHAR(128) NOT NULL DEFAULT '',
			multi_message TINYINT(1)   NOT NULL DEFAULT 0,
			updated_at    TIMESTAMP    DEFAULT CURRENT_TIMESTAMP
		)`
		alterModel = `ALTER TABLE guild_config ADD COLUMN model VARCHAR(128) NOT NULL DEFAULT ''`
		alterMultiMessage = `ALTER TABLE guild_config ADD COLUMN multi_message TINYINT(1) NOT NULL DEFAULT 0`
		promptTable = `CREATE TABLE IF NOT EXISTS guild_prompt_config (
			guild_id             VARCHAR(20)  PRIMARY KEY,
			guild_system_prompt  TEXT         NOT NULL,
			updated_at           TIMESTAMP    DEFAULT CURRENT_TIMESTAMP
		)`
		warningsTable = `CREATE TABLE IF NOT EXISTS member_warnings (
			id         INT AUTO_INCREMENT PRIMARY KEY,
			guild_id   VARCHAR(20) NOT NULL,
			user_id    VARCHAR(20) NOT NULL,
			reason     TEXT NOT NULL,
			actor_id   VARCHAR(20) NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`
	} else if d.driver == "postgres" {
		q = `CREATE TABLE IF NOT EXISTS guild_config (
			guild_id      TEXT PRIMARY KEY,
			api_key       TEXT NOT NULL,
			provider      TEXT NOT NULL,
			model         TEXT NOT NULL DEFAULT '',
			multi_message BOOLEAN NOT NULL DEFAULT FALSE,
			updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`
		alterModel = `ALTER TABLE guild_config ADD COLUMN model TEXT NOT NULL DEFAULT ''`
		alterMultiMessage = `ALTER TABLE guild_config ADD COLUMN multi_message BOOLEAN NOT NULL DEFAULT FALSE`
		promptTable = `CREATE TABLE IF NOT EXISTS guild_prompt_config (
			guild_id            TEXT PRIMARY KEY,
			guild_system_prompt TEXT NOT NULL,
			updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`
		warningsTable = `CREATE TABLE IF NOT EXISTS member_warnings (
			id         SERIAL PRIMARY KEY,
			guild_id   TEXT NOT NULL,
			user_id    TEXT NOT NULL,
			reason     TEXT NOT NULL,
			actor_id   TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`
	} else {
		q = `CREATE TABLE IF NOT EXISTS guild_config (
			guild_id      TEXT PRIMARY KEY,
			api_key       TEXT NOT NULL,
			provider      TEXT NOT NULL,
			model         TEXT NOT NULL DEFAULT '',
			multi_message INTEGER NOT NULL DEFAULT 0,
			updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`
		alterModel = `ALTER TABLE guild_config ADD COLUMN model TEXT NOT NULL DEFAULT ''`
		alterMultiMessage = `ALTER TABLE guild_config ADD COLUMN multi_message INTEGER NOT NULL DEFAULT 0`
		promptTable = `CREATE TABLE IF NOT EXISTS guild_prompt_config (
			guild_id            TEXT PRIMARY KEY,
			guild_system_prompt TEXT NOT NULL,
			updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`
		warningsTable = `CREATE TABLE IF NOT EXISTS member_warnings (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			guild_id   TEXT NOT NULL,
			user_id    TEXT NOT NULL,
			reason     TEXT NOT NULL,
			actor_id   TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`
	}
	if _, err := d.db.Exec(q); err != nil {
		return err
	}
	_, _ = d.db.Exec(alterModel)
	_, _ = d.db.Exec(alterMultiMessage)
	if _, err := d.db.Exec(promptTable); err != nil {
		return err
	}
	if _, err := d.db.Exec(warningsTable); err != nil {
		return err
	}
	return nil
}

func openMySQL(dsn string) (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DSN: %w", err)
	}

	decoded, err := url.PathUnescape(cfg.Passwd)
	if err == nil {
		cfg.Passwd = decoded
	}

	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("connector: %w", err)
	}

	return sql.OpenDB(connector), nil
}

func (d *Database) format(query string) string {
	if d.driver != "postgres" {
		return query
	}
	n := 0
	var b strings.Builder
	for _, c := range query {
		if c == '?' {
			n++
			b.WriteString(fmt.Sprintf("$%d", n))
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func (d *Database) GetGuildConfig(guildID string) (*GuildConfig, error) {
	q := d.format("SELECT guild_id, api_key, provider, model, multi_message FROM guild_config WHERE guild_id = ?")
	row := d.db.QueryRow(q, guildID)

	cfg := &GuildConfig{}
	var multiMessage int
	if err := row.Scan(&cfg.GuildID, &cfg.APIKey, &cfg.Provider, &cfg.Model, &multiMessage); err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	cfg.MultiMessage = multiMessage != 0

	apiKey, err := d.decryptAPIKey(cfg.APIKey)
	if err != nil {
		return nil, err
	}
	cfg.APIKey = apiKey

	return cfg, nil
}

func (d *Database) SetGuildConfig(guildID, apiKey, provider, model string) error {
	encryptedAPIKey, err := d.encryptAPIKey(apiKey)
	if err != nil {
		return err
	}

	var q string
	switch d.driver {
	case "mysql":
		q = `INSERT INTO guild_config (guild_id, api_key, provider, model, updated_at)
			 VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
			 ON DUPLICATE KEY UPDATE
				 api_key = VALUES(api_key),
				 provider = VALUES(provider),
				 model = VALUES(model),
				 updated_at = CURRENT_TIMESTAMP`
	default:
		q = d.format(`INSERT INTO guild_config (guild_id, api_key, provider, model, updated_at)
			 VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
			 ON CONFLICT(guild_id) DO UPDATE SET
				 api_key = excluded.api_key,
				 provider = excluded.provider,
				 model = excluded.model,
				 updated_at = CURRENT_TIMESTAMP`)
	}
	_, err = d.db.Exec(q, guildID, encryptedAPIKey, provider, model)
	return err
}

func (d *Database) DeleteGuildConfig(guildID string) error {
	q := d.format("DELETE FROM guild_config WHERE guild_id = ?")
	_, err := d.db.Exec(q, guildID)
	return err
}


func (d *Database) SetGuildMultiMessage(guildID string, enabled bool) error {
	val := 0
	if enabled {
		val = 1
	}
	var q string
	switch d.driver {
	case "mysql":
		q = `INSERT INTO guild_config (guild_id, api_key, provider, model, multi_message, updated_at)
			 VALUES (?, '', '', '', ?, CURRENT_TIMESTAMP)
			 ON DUPLICATE KEY UPDATE multi_message = VALUES(multi_message), updated_at = CURRENT_TIMESTAMP`
	default:
		q = d.format(`INSERT INTO guild_config (guild_id, api_key, provider, model, multi_message, updated_at)
			 VALUES (?, '', '', '', ?, CURRENT_TIMESTAMP)
			 ON CONFLICT(guild_id) DO UPDATE SET
				 multi_message = excluded.multi_message,
				 updated_at = CURRENT_TIMESTAMP`)
	}
	_, err := d.db.Exec(q, guildID, val)
	return err
}

func (d *Database) GetGuildSystemPrompt(guildID string) (string, error) {
	q := d.format("SELECT guild_system_prompt FROM guild_prompt_config WHERE guild_id = ?")

	var prompt string
	if err := d.db.QueryRow(q, guildID).Scan(&prompt); err == sql.ErrNoRows {
		return "", nil
	} else if err != nil {
		return "", err
	}

	return prompt, nil
}

func (d *Database) SetGuildSystemPrompt(guildID, prompt string) error {
	if err := validateGuildSystemPromptSize(prompt); err != nil {
		return err
	}

	var q string
	switch d.driver {
	case "mysql":
		q = `INSERT INTO guild_prompt_config (guild_id, guild_system_prompt, updated_at)
			 VALUES (?, ?, CURRENT_TIMESTAMP)
			 ON DUPLICATE KEY UPDATE
				 guild_system_prompt = VALUES(guild_system_prompt),
				 updated_at = CURRENT_TIMESTAMP`
	default:
		q = d.format(`INSERT INTO guild_prompt_config (guild_id, guild_system_prompt, updated_at)
			 VALUES (?, ?, CURRENT_TIMESTAMP)
			 ON CONFLICT(guild_id) DO UPDATE SET
				 guild_system_prompt = excluded.guild_system_prompt,
				 updated_at = CURRENT_TIMESTAMP`)
	}

	_, err := d.db.Exec(q, guildID, prompt)
	return err
}

func (d *Database) ClearGuildSystemPrompt(guildID string) error {
	q := d.format("DELETE FROM guild_prompt_config WHERE guild_id = ?")
	_, err := d.db.Exec(q, guildID)
	return err
}

func newAPIKeyEncryptorFromEnv() (*apiKeyEncryptor, error) {
	rawKey := strings.TrimSpace(os.Getenv("API_KEY_ENCRYPTION_KEY"))
	if rawKey == "" {
		return nil, fmt.Errorf("API_KEY_ENCRYPTION_KEY is required")
	}

	return newAPIKeyEncryptor(rawKey)
}

func newAPIKeyEncryptor(rawKey string) (*apiKeyEncryptor, error) {
	key, err := parseAPIKeyEncryptionKey(rawKey)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("cipher init: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm init: %w", err)
	}

	return &apiKeyEncryptor{aead: aead}, nil
}

func parseAPIKeyEncryptionKey(raw string) ([]byte, error) {
	if len(raw) == 32 {
		return []byte(raw), nil
	}

	decode := func(encoding *base64.Encoding) ([]byte, error) {
		key, err := encoding.DecodeString(raw)
		if err != nil {
			return nil, err
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("decoded key must be 32 bytes, got %d", len(key))
		}
		return key, nil
	}

	if key, err := decode(base64.StdEncoding); err == nil {
		return key, nil
	}
	if key, err := decode(base64.RawStdEncoding); err == nil {
		return key, nil
	}

	return nil, fmt.Errorf("API_KEY_ENCRYPTION_KEY must be 32 raw bytes or base64 for 32 bytes")
}

func validateGuildSystemPromptSize(prompt string) error {
	if utf8.RuneCountInString(prompt) <= MaxGuildSystemPromptChars {
		return nil
	}

	return fmt.Errorf("guild system prompt exceeds %d characters", MaxGuildSystemPromptChars)
}

func (d *Database) encryptAPIKey(apiKey string) (string, error) {
	if d.encryptor == nil {
		return "", fmt.Errorf("api key encryptor is not configured")
	}
	return d.encryptor.encrypt(apiKey)
}

func (d *Database) decryptAPIKey(apiKey string) (string, error) {
	if d.encryptor == nil {
		return "", fmt.Errorf("api key encryptor is not configured")
	}
	return d.encryptor.decrypt(apiKey)
}

func (e *apiKeyEncryptor) encrypt(plaintext string) (string, error) {
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}

	ciphertext := e.aead.Seal(nil, nonce, []byte(plaintext), nil)
	payload := append(nonce, ciphertext...)
	return encryptedAPIKeyPrefix + base64.RawStdEncoding.EncodeToString(payload), nil
}

func (e *apiKeyEncryptor) decrypt(value string) (string, error) {
	if !strings.HasPrefix(value, encryptedAPIKeyPrefix) {
		return value, nil
	}

	payload, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, encryptedAPIKeyPrefix))
	if err != nil {
		return "", fmt.Errorf("decode encrypted api key: %w", err)
	}

	nonceSize := e.aead.NonceSize()
	if len(payload) < nonceSize {
		return "", fmt.Errorf("encrypted api key payload is too short")
	}

	nonce := payload[:nonceSize]
	ciphertext := payload[nonceSize:]
	plaintext, err := e.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt api key: %w", err)
	}

	return string(plaintext), nil
}

func (d *Database) migratePlaintextAPIKeys() error {
	if d.encryptor == nil {
		return fmt.Errorf("api key encryptor is not configured")
	}

	rows, err := d.db.Query(d.format("SELECT guild_id, api_key FROM guild_config"))
	if err != nil {
		return err
	}
	defer rows.Close()

	type entry struct {
		guildID string
		apiKey  string
	}

	var updates []entry
	for rows.Next() {
		var item entry
		if err := rows.Scan(&item.guildID, &item.apiKey); err != nil {
			return err
		}
		if strings.HasPrefix(item.apiKey, encryptedAPIKeyPrefix) {
			continue
		}
		updates = append(updates, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}

	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	q := d.format("UPDATE guild_config SET api_key = ?, updated_at = CURRENT_TIMESTAMP WHERE guild_id = ?")
	for _, item := range updates {
		encrypted, err := d.encryptAPIKey(item.apiKey)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(q, encrypted, item.guildID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

type Warning struct {
	ID        int
	GuildID   string
	UserID    string
	Reason    string
	ActorID   string
	CreatedAt string
}

func (d *Database) AddWarning(guildID, userID, actorID, reason string) error {
	if d == nil || d.db == nil {
		return fmt.Errorf("database not initialized")
	}

	q := d.format("INSERT INTO member_warnings (guild_id, user_id, actor_id, reason) VALUES (?, ?, ?, ?)")
	_, err := d.db.Exec(q, guildID, userID, actorID, reason)
	return err
}

func (d *Database) ClearWarnings(guildID, userID string) error {
	if d == nil || d.db == nil {
		return fmt.Errorf("database not initialized")
	}

	q := d.format("DELETE FROM member_warnings WHERE guild_id = ? AND user_id = ?")
	_, err := d.db.Exec(q, guildID, userID)
	return err
}

func (d *Database) GetWarnings(guildID, userID string) ([]Warning, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	q := d.format("SELECT id, guild_id, user_id, actor_id, reason, created_at FROM member_warnings WHERE guild_id = ? AND user_id = ? ORDER BY created_at DESC")
	rows, err := d.db.Query(q, guildID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var warnings []Warning
	for rows.Next() {
		var w Warning
		if err := rows.Scan(&w.ID, &w.GuildID, &w.UserID, &w.ActorID, &w.Reason, &w.CreatedAt); err != nil {
			return nil, err
		}
		warnings = append(warnings, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return warnings, nil
}
