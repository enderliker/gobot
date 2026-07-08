package discordperm

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestHasGuildPermissionAllowsOwner(t *testing.T) {
	session, guildID := seedPermissionSession(t, "owner-1", []*discordgo.Role{
		{
			ID:          "role-member",
			Permissions: discordgo.PermissionViewChannel,
		},
	})

	member := &discordgo.Member{
		User:  &discordgo.User{ID: "owner-1"},
		Roles: []string{"role-member"},
	}

	if !HasGuildPermission(session, guildID, member, discordgo.PermissionManageServer) {
		t.Fatal("expected owner to be allowed")
	}
}

func TestIsGuildOwnerAllowsOnlyOwner(t *testing.T) {
	session, guildID := seedPermissionSession(t, "owner-1", []*discordgo.Role{
		{
			ID:          "role-admin",
			Permissions: discordgo.PermissionAdministrator,
		},
	})

	owner := &discordgo.Member{
		User:  &discordgo.User{ID: "owner-1"},
		Roles: []string{"role-admin"},
	}
	admin := &discordgo.Member{
		User:  &discordgo.User{ID: "user-1"},
		Roles: []string{"role-admin"},
	}

	if !IsGuildOwner(session, guildID, owner) {
		t.Fatal("expected owner to be allowed")
	}
	if IsGuildOwner(session, guildID, admin) {
		t.Fatal("expected non-owner administrator to be rejected")
	}
}

func TestHasGuildPermissionAllowsAdministrator(t *testing.T) {
	session, guildID := seedPermissionSession(t, "owner-1", []*discordgo.Role{
		{
			ID:          "role-admin",
			Permissions: discordgo.PermissionAdministrator,
		},
	})

	member := &discordgo.Member{
		User:  &discordgo.User{ID: "user-1"},
		Roles: []string{"role-admin"},
	}

	if !HasGuildPermission(session, guildID, member, discordgo.PermissionManageServer) {
		t.Fatal("expected administrator to be allowed")
	}
}

func TestHasGuildPermissionAllowsRequestedBit(t *testing.T) {
	session, guildID := seedPermissionSession(t, "owner-1", []*discordgo.Role{
		{
			ID:          "role-manager",
			Permissions: discordgo.PermissionManageServer,
		},
	})

	member := &discordgo.Member{
		User:  &discordgo.User{ID: "user-2"},
		Roles: []string{"role-manager"},
	}

	if !HasGuildPermission(session, guildID, member, discordgo.PermissionManageServer) {
		t.Fatal("expected Manage Server member to be allowed")
	}
}

func TestHasGuildPermissionRejectsMissingBit(t *testing.T) {
	session, guildID := seedPermissionSession(t, "owner-1", []*discordgo.Role{
		{
			ID:          "role-member",
			Permissions: discordgo.PermissionViewChannel,
		},
	})

	member := &discordgo.Member{
		User:  &discordgo.User{ID: "user-3"},
		Roles: []string{"role-member"},
	}

	if HasGuildPermission(session, guildID, member, discordgo.PermissionManageServer) {
		t.Fatal("expected member without Manage Server to be rejected")
	}
}

func seedPermissionSession(t *testing.T, ownerID string, roles []*discordgo.Role) (*discordgo.Session, string) {
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
