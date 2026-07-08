package bot

import (
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestApplicationCommandEqual(t *testing.T) {
	perms := int64(discordgo.PermissionManageServer)

	a := &discordgo.ApplicationCommand{
		Name:                     "setkey",
		Description:              "Set key",
		DefaultMemberPermissions: &perms,
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "key",
				Description: "API key",
				Required:    true,
			},
		},
	}
	b := &discordgo.ApplicationCommand{
		Type:                     discordgo.ChatApplicationCommand,
		Name:                     "setkey",
		Description:              "Set key",
		DefaultMemberPermissions: &perms,
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "key",
				Description: "API key",
				Required:    true,
			},
		},
	}

	if !applicationCommandEqual(a, b) {
		t.Fatal("expected commands to be equal")
	}

	b.Description = "Updated"
	if applicationCommandEqual(a, b) {
		t.Fatal("expected commands with different descriptions to differ")
	}
}

func TestShouldSyncCommands(t *testing.T) {
	t.Setenv("DISCORD_SYNC_COMMANDS", "false")
	if shouldSyncCommands() {
		t.Fatal("expected command sync to be disabled")
	}

	t.Setenv("DISCORD_SYNC_COMMANDS", "true")
	if !shouldSyncCommands() {
		t.Fatal("expected command sync to be enabled")
	}
}

func TestFormatHeartbeatLatency(t *testing.T) {
	if got := formatHeartbeatLatency(-1 * time.Millisecond); got != "unavailable" {
		t.Fatalf("expected unavailable for negative latency, got %q", got)
	}
	if got := formatHeartbeatLatency(123 * time.Millisecond); got != "123ms" {
		t.Fatalf("expected formatted latency, got %q", got)
	}
}
