package slash

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gobot/internal/database"
	"gobot/internal/lifecycle"

	"github.com/bwmarrin/discordgo"
)

func newConfigTestSession(t *testing.T, ownerID string) (*discordgo.Session, string) {
	t.Helper()
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	guildID := "guild-1"
	guild := &discordgo.Guild{
		ID:      guildID,
		OwnerID: ownerID,
		Roles: []*discordgo.Role{
			{
				ID:          "role-member",
				Permissions: discordgo.PermissionViewChannel,
			},
		},
	}
	if err := session.State.GuildAdd(guild); err != nil {
		t.Fatalf("seed guild state: %v", err)
	}
	return session, guildID
}

// newConfigSubCommandInteraction builds an interaction for a /config <subcommand> call.
func newConfigSubCommandInteraction(userID, guildID, subCommand string, subOpts []*discordgo.ApplicationCommandInteractionDataOption) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type:    discordgo.InteractionApplicationCommand,
			GuildID: guildID,
			Member: &discordgo.Member{
				User:  &discordgo.User{ID: userID},
				Roles: []string{"role-member"},
			},
			Data: discordgo.ApplicationCommandInteractionData{
				Name: "config",
				Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{
						Name:    subCommand,
						Type:    discordgo.ApplicationCommandOptionSubCommand,
						Options: subOpts,
					},
				},
			},
		},
	}
}

func TestConfigExecuteDeniedForNonOwner(t *testing.T) {
	d := newSlashTestDatabase(t)
	if err := d.SetGuildConfig("guild-1", "secret-key", "Capture", "model-1"); err != nil {
		t.Fatalf("set config: %v", err)
	}

	session, guildID := newConfigTestSession(t, "owner-1")

	// user-2 is NOT the owner
	interaction := newConfigSubCommandInteraction("user-2", guildID, "multi_message", []*discordgo.ApplicationCommandInteractionDataOption{
		{Name: "value", Value: "on", Type: discordgo.ApplicationCommandOptionString},
	})

	recorder := &discordRequestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(t, r)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	session.Client = server.Client()
	restoreEndpoints := stubDiscordEndpoints(t, server.URL+"/")
	defer restoreEndpoints()

	cmd := registeredSlashCommand(t, "config")
	cmd.Execute(session, interaction)

	recorder.waitForCount(t, 1)

	requests := recorder.snapshot()
	callback := findDiscordRequest(t, requests, http.MethodPost, "/callback")

	var response discordgo.InteractionResponse
	if err := json.Unmarshal(callback.Body, &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if response.Type != discordgo.InteractionResponseChannelMessageWithSource {
		t.Fatalf("expected message response, got %d", response.Type)
	}
	if (response.Data.Flags & discordgo.MessageFlagsEphemeral) == 0 {
		t.Fatal("expected ephemeral response")
	}
	if len(response.Data.Embeds) != 1 || !strings.Contains(response.Data.Embeds[0].Title, "Access Denied") {
		t.Fatalf("expected Access Denied embed, got %#v", response.Data.Embeds)
	}

	cfg, err := database.Default.GetGuildConfig(guildID)
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if cfg != nil && cfg.MultiMessage {
		t.Fatal("expected multi_message to remain false")
	}
}

func TestConfigExecuteAllowedForOwner(t *testing.T) {
	d := newSlashTestDatabase(t)
	if err := d.SetGuildConfig("guild-1", "secret-key", "Capture", "model-1"); err != nil {
		t.Fatalf("set config: %v", err)
	}

	session, guildID := newConfigTestSession(t, "owner-1")

	// Enable multi_message
	interaction := newConfigSubCommandInteraction("owner-1", guildID, "multi_message", []*discordgo.ApplicationCommandInteractionDataOption{
		{Name: "value", Value: "on", Type: discordgo.ApplicationCommandOptionString},
	})

	recorder := &discordRequestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(t, r)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	session.Client = server.Client()
	restoreEndpoints := stubDiscordEndpoints(t, server.URL+"/")
	defer restoreEndpoints()

	lifecycle.Init()
	defer func() {
		lifecycle.Cancel()
		lifecycle.Wait()
	}()

	cmd := registeredSlashCommand(t, "config")
	cmd.Execute(session, interaction)

	recorder.waitForCount(t, 1)
	lifecycle.Wait()

	requests := recorder.snapshot()
	callback := findDiscordRequest(t, requests, http.MethodPost, "/callback")

	var response discordgo.InteractionResponse
	if err := json.Unmarshal(callback.Body, &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if response.Type != discordgo.InteractionResponseChannelMessageWithSource {
		t.Fatalf("expected message response, got %d", response.Type)
	}
	if (response.Data.Flags & discordgo.MessageFlagsEphemeral) == 0 {
		t.Fatal("expected ephemeral response")
	}
	if len(response.Data.Embeds) != 1 || !strings.Contains(response.Data.Embeds[0].Title, "Setting Updated") {
		t.Fatalf("expected Setting Updated embed, got %#v", response.Data.Embeds)
	}

	cfg, err := database.Default.GetGuildConfig(guildID)
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected guild config to exist")
	}
	if !cfg.MultiMessage {
		t.Fatal("expected multi_message to be true in DB")
	}

	// Toggle off
	interaction.Interaction.Data = discordgo.ApplicationCommandInteractionData{
		Name: "config",
		Options: []*discordgo.ApplicationCommandInteractionDataOption{
			{
				Name: "multi_message",
				Type: discordgo.ApplicationCommandOptionSubCommand,
				Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{Name: "value", Value: "off", Type: discordgo.ApplicationCommandOptionString},
				},
			},
		},
	}

	recorder.clear()
	cmd.Execute(session, interaction)
	recorder.waitForCount(t, 1)

	cfg, err = database.Default.GetGuildConfig(guildID)
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if cfg.MultiMessage {
		t.Fatal("expected multi_message to be false in DB after toggling off")
	}
}

// Add a clear method to discordRequestRecorder for testing toggling
func (r *discordRequestRecorder) clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = nil
}

func TestConfigExecuteClearKey(t *testing.T) {
	d := newSlashTestDatabase(t)
	if err := d.SetGuildConfig("guild-1", "secret-key", "Capture", "model-1"); err != nil {
		t.Fatalf("set config: %v", err)
	}

	session, guildID := newConfigTestSession(t, "owner-1")

	interaction := newConfigSubCommandInteraction("owner-1", guildID, "clearkey", nil)

	recorder := &discordRequestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(t, r)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	session.Client = server.Client()
	restoreEndpoints := stubDiscordEndpoints(t, server.URL+"/")
	defer restoreEndpoints()

	cmd := registeredSlashCommand(t, "config")
	cmd.Execute(session, interaction)

	recorder.waitForCount(t, 1)

	requests := recorder.snapshot()
	callback := findDiscordRequest(t, requests, http.MethodPost, "/callback")

	var response discordgo.InteractionResponse
	if err := json.Unmarshal(callback.Body, &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if response.Type != discordgo.InteractionResponseChannelMessageWithSource {
		t.Fatalf("expected message response, got %d", response.Type)
	}
	if len(response.Data.Embeds) != 1 || !strings.Contains(response.Data.Embeds[0].Title, "API Key Cleared") {
		t.Fatalf("expected API Key Cleared embed, got %#v", response.Data.Embeds)
	}

	cfg, err := database.Default.GetGuildConfig(guildID)
	if err != nil {
		t.Fatalf("get config after clear: %v", err)
	}
	if cfg != nil {
		t.Fatal("expected guild config to be deleted from DB")
	}
}

func TestConfigExecuteChannelContext(t *testing.T) {
	d := newSlashTestDatabase(t)
	if err := d.SetGuildConfig("guild-1", "secret-key", "Capture", "model-1"); err != nil {
		t.Fatalf("set config: %v", err)
	}

	session, guildID := newConfigTestSession(t, "owner-1")

	interaction := newConfigSubCommandInteraction("owner-1", guildID, "channel_context", []*discordgo.ApplicationCommandInteractionDataOption{
		{Name: "messages", Value: float64(10), Type: discordgo.ApplicationCommandOptionInteger},
	})

	recorder := &discordRequestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(t, r)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	session.Client = server.Client()
	restoreEndpoints := stubDiscordEndpoints(t, server.URL+"/")
	defer restoreEndpoints()

	cmd := registeredSlashCommand(t, "config")
	cmd.Execute(session, interaction)

	recorder.waitForCount(t, 1)

	requests := recorder.snapshot()
	callback := findDiscordRequest(t, requests, http.MethodPost, "/callback")

	var response discordgo.InteractionResponse
	if err := json.Unmarshal(callback.Body, &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if response.Type != discordgo.InteractionResponseChannelMessageWithSource {
		t.Fatalf("expected message response, got %d", response.Type)
	}
	if len(response.Data.Embeds) != 1 || !strings.Contains(response.Data.Embeds[0].Title, "channel_context = 10") {
		t.Fatalf("expected channel_context embed, got %#v", response.Data.Embeds)
	}

	cfg, err := database.Default.GetGuildConfig(guildID)
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected guild config to exist")
	}
	if cfg.ChannelContextLimit != 10 {
		t.Fatalf("expected ChannelContextLimit 10, got %d", cfg.ChannelContextLimit)
	}
}
