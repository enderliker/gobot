package ai

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestParseToolArgumentsJSONValidBan(t *testing.T) {
	call, err := ParseToolArgumentsJSON("BAN", `{"user":"12345678901234567","reason":" spam "}`)
	if err != nil {
		t.Fatalf("expected valid tool call, got error: %v", err)
	}

	if call.Tool != "ban" {
		t.Fatalf("expected normalized tool ban, got %q", call.Tool)
	}
	if call.Reason != "spam" {
		t.Fatalf("expected trimmed reason, got %q", call.Reason)
	}
	if got, want := call.Confirmation, "Ban <@12345678901234567> for `spam`?"; got != want {
		t.Fatalf("expected canonical confirmation %q, got %q", want, got)
	}
}

func TestParseToolArgumentsRejectsInvalidTimeout(t *testing.T) {
	if _, err := ParseToolArguments("timeout", map[string]any{
		"user":    "12345678901234567",
		"minutes": 0,
		"reason":  "spam",
	}); err == nil {
		t.Fatal("expected invalid timeout duration to be rejected")
	}
}

func TestParseToolArgumentsRejectsOverlongReason(t *testing.T) {
	reason := strings.Repeat("a", maxAuditReasonLen+1)
	if _, err := ParseToolArguments("kick", map[string]any{
		"user":   "12345678901234567",
		"reason": reason,
	}); err == nil {
		t.Fatal("expected overlong reason to be rejected")
	}
}

func TestParseToolArgumentsJSONValidUnban(t *testing.T) {
	call, err := ParseToolArgumentsJSON("UNBAN", `{"user":"12345678901234567","reason":"appealed"}`)
	if err != nil {
		t.Fatalf("expected valid tool call, got error: %v", err)
	}

	if call.Tool != "unban" {
		t.Fatalf("expected normalized tool unban, got %q", call.Tool)
	}
	if got, want := call.Confirmation, "Unban <@12345678901234567> for `appealed`?"; got != want {
		t.Fatalf("expected canonical confirmation %q, got %q", want, got)
	}
}

func TestParseToolArgumentsJSONValidUntimeout(t *testing.T) {
	call, err := ParseToolArgumentsJSON("UNTIMEOUT", `{"user":"12345678901234567","reason":"behavior improved"}`)
	if err != nil {
		t.Fatalf("expected valid tool call, got error: %v", err)
	}

	if call.Tool != "untimeout" {
		t.Fatalf("expected normalized tool untimeout, got %q", call.Tool)
	}
	if got, want := call.Confirmation, "Remove timeout/mute from <@12345678901234567> for `behavior improved`?"; got != want {
		t.Fatalf("expected canonical confirmation %q, got %q", want, got)
	}
}

func TestModerationToolsForMemberIncludesAllToolsForGuildOwner(t *testing.T) {
	session, guildID := seedPermissionTestSession(t, "owner-1", []*discordgo.Role{
		{
			ID:          "role-member",
			Permissions: discordgo.PermissionViewChannel,
		},
	})

	member := &discordgo.Member{
		User:  &discordgo.User{ID: "owner-1"},
		Roles: []string{"role-member"},
	}

	tools := ModerationToolsForMember(session, guildID, member)
	if got, want := toolNames(tools), []string{"ban", "unban", "kick", "timeout", "untimeout", "purge", "member_info"}; !equalStrings(got, want) {
		t.Fatalf("expected owner to receive all moderation tools, got %v", got)
	}
}

func TestModerationToolsForMemberIncludesAllToolsForAdministrator(t *testing.T) {
	session, guildID := seedPermissionTestSession(t, "owner-1", []*discordgo.Role{
		{
			ID:          "role-admin",
			Permissions: discordgo.PermissionAdministrator,
		},
	})

	member := &discordgo.Member{
		User:  &discordgo.User{ID: "user-1"},
		Roles: []string{"role-admin"},
	}

	tools := ModerationToolsForMember(session, guildID, member)
	if got, want := toolNames(tools), []string{"ban", "unban", "kick", "timeout", "untimeout", "purge", "member_info"}; !equalStrings(got, want) {
		t.Fatalf("expected admin to receive all moderation tools, got %v", got)
	}
}

func TestUserFacingToolExecutionErrorKeepsBusinessReason(t *testing.T) {
	err := errors.New(toolErrRoleHierarchyPreventsAction)

	if got := UserFacingToolExecutionError(err); got != toolErrRoleHierarchyPreventsAction {
		t.Fatalf("expected business error to pass through, got %q", got)
	}
}

func TestUserFacingToolExecutionErrorSanitizesDiscordRESTError(t *testing.T) {
	err := &discordgo.RESTError{
		Response:     &http.Response{Status: "404 Not Found"},
		ResponseBody: []byte(`{"message":"Unknown Member","code":10007,"debug":"internal/path"}`),
		Message: &discordgo.APIErrorMessage{
			Code:    discordgo.ErrCodeUnknownMember,
			Message: "Unknown Member",
		},
	}

	if got := UserFacingToolExecutionError(err); got != toolErrTargetMemberNotFound {
		t.Fatalf("expected unknown member to map to %q, got %q", toolErrTargetMemberNotFound, got)
	}
}

func seedPermissionTestSession(t *testing.T, ownerID string, roles []*discordgo.Role) (*discordgo.Session, string) {
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

func toolNames(tools []ToolDefinition) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
