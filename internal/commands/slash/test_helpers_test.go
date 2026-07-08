package slash

import (
	"path/filepath"
	"testing"

	"gobot/internal/database"

	"github.com/bwmarrin/discordgo"
)

func newSlashTestDatabase(t *testing.T) *database.Database {
	t.Helper()

	prev := database.Default

	t.Setenv("API_KEY_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("DB_DRIVER", "sqlite")
	t.Setenv("DB_DSN", filepath.Join(t.TempDir(), "test.db"))

	if err := database.Init(); err != nil {
		t.Fatalf("database init: %v", err)
	}
	t.Cleanup(func() {
		if database.Default != nil {
			_ = database.Default.Close()
		}
		database.Default = prev
	})

	return database.Default
}

func seedOwnerOnlySession(t *testing.T) (*discordgo.Session, string) {
	t.Helper()

	session := &discordgo.Session{
		State: discordgo.NewState(),
	}

	guildID := "guild-1"
	guild := &discordgo.Guild{
		ID:      guildID,
		OwnerID: "owner-1",
		Roles: []*discordgo.Role{
			{
				ID:          "role-member",
				Permissions: discordgo.PermissionViewChannel,
			},
			{
				ID:          "role-manager",
				Permissions: discordgo.PermissionManageServer,
			},
			{
				ID:          "role-admin",
				Permissions: discordgo.PermissionAdministrator,
			},
		},
	}
	if err := session.State.GuildAdd(guild); err != nil {
		t.Fatalf("seed guild state: %v", err)
	}

	return session, guildID
}
