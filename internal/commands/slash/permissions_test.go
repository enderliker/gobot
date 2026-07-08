package slash

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestCanManageGuildConfigWithManageServer(t *testing.T) {
	session, guildID := seedConfigPermissionSession(t, "owner-1", []*discordgo.Role{
		{
			ID:          "role-manager",
			Permissions: discordgo.PermissionManageServer,
		},
	})

	member := &discordgo.Member{
		User:  &discordgo.User{ID: "user-1"},
		Roles: []string{"role-manager"},
	}

	if !canManageGuildConfig(session, guildID, member) {
		t.Fatal("expected Manage Server member to be allowed")
	}
}

func TestCanManageGuildConfigAllowsAdministrator(t *testing.T) {
	session, guildID := seedConfigPermissionSession(t, "owner-1", []*discordgo.Role{
		{
			ID:          "role-admin",
			Permissions: discordgo.PermissionAdministrator,
		},
	})

	member := &discordgo.Member{
		User:  &discordgo.User{ID: "user-1"},
		Roles: []string{"role-admin"},
	}

	if !canManageGuildConfig(session, guildID, member) {
		t.Fatal("expected administrator to be allowed")
	}
}

func TestCanManageGuildConfigAllowsOwner(t *testing.T) {
	session, guildID := seedConfigPermissionSession(t, "owner-1", []*discordgo.Role{
		{
			ID:          "role-member",
			Permissions: discordgo.PermissionViewChannel,
		},
	})

	member := &discordgo.Member{
		User:  &discordgo.User{ID: "owner-1"},
		Roles: []string{"role-member"},
	}

	if !canManageGuildConfig(session, guildID, member) {
		t.Fatal("expected owner to be allowed")
	}
}

func TestCanManageGuildConfigRejectsMemberWithoutPermission(t *testing.T) {
	session, guildID := seedConfigPermissionSession(t, "owner-1", []*discordgo.Role{
		{
			ID:          "role-member",
			Permissions: discordgo.PermissionViewChannel,
		},
	})

	member := &discordgo.Member{
		User:  &discordgo.User{ID: "user-2"},
		Roles: []string{"role-member"},
	}

	if canManageGuildConfig(session, guildID, member) {
		t.Fatal("expected member without Manage Server to be rejected")
	}
}

func seedConfigPermissionSession(t *testing.T, ownerID string, roles []*discordgo.Role) (*discordgo.Session, string) {
	t.Helper()

	session := &discordgo.Session{
		State: discordgo.NewState(),
	}

	guildID := "guild-1"
	guild := &discordgo.Guild{
		ID:      guildID,
		OwnerID: ownerID,
		Roles:   roles,
	}
	if err := session.State.GuildAdd(guild); err != nil {
		t.Fatalf("failed to seed guild state: %v", err)
	}

	return session, guildID
}
