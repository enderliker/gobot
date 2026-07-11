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

func TestConfigExecuteDeniedForNonOwner(t *testing.T) {
	d := newSlashTestDatabase(t)
	// seedOwnerOnlySession / newConfigTestSession returns guild-1
	if err := d.SetGuildConfig("guild-1", "secret-key", "Capture", "model-1"); err != nil {
		t.Fatalf("set config: %v", err)
	}

	session, guildID := newConfigTestSession(t, "owner-1")

	// user-2 is not the owner (owner-1 is the owner)
	interaction := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type:    discordgo.InteractionApplicationCommand,
			GuildID: guildID,
			Member: &discordgo.Member{
				User:  &discordgo.User{ID: "user-2"},
				Roles: []string{"role-member"},
			},
			Data: discordgo.ApplicationCommandInteractionData{
				Name: "config",
				Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{
						Name:  "setting",
						Value: "multi_message",
						Type:  discordgo.ApplicationCommandOptionString,
					},
					{
						Name:  "value",
						Value: "on",
						Type:  discordgo.ApplicationCommandOptionString,
					},
				},
			},
		},
	}

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

	// Verify DB didn't change
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

	// owner-1 is the owner
	interaction := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type:    discordgo.InteractionApplicationCommand,
			GuildID: guildID,
			Member: &discordgo.Member{
				User:  &discordgo.User{ID: "owner-1"},
				Roles: []string{"role-member"},
			},
			Data: discordgo.ApplicationCommandInteractionData{
				Name: "config",
				Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{
						Name:  "setting",
						Value: "multi_message",
						Type:  discordgo.ApplicationCommandOptionString,
					},
					{
						Name:  "value",
						Value: "on",
						Type:  discordgo.ApplicationCommandOptionString,
					},
				},
			},
		},
	}

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

	// Verify DB changed
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

	// Now toggle off
	interaction.Interaction.Data = discordgo.ApplicationCommandInteractionData{
		Name: "config",
		Options: []*discordgo.ApplicationCommandInteractionDataOption{
			{
				Name:  "setting",
				Value: "multi_message",
				Type:  discordgo.ApplicationCommandOptionString,
			},
			{
				Name:  "value",
				Value: "off",
				Type:  discordgo.ApplicationCommandOptionString,
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

	// owner-1 runs /config setting:clearkey
	interaction := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type:    discordgo.InteractionApplicationCommand,
			GuildID: guildID,
			Member: &discordgo.Member{
				User:  &discordgo.User{ID: "owner-1"},
				Roles: []string{"role-member"},
			},
			Data: discordgo.ApplicationCommandInteractionData{
				Name: "config",
				Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{
						Name:  "setting",
						Value: "clearkey",
						Type:  discordgo.ApplicationCommandOptionString,
					},
				},
			},
		},
	}

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

	// Verify DB is cleared
	cfg, err := database.Default.GetGuildConfig(guildID)
	if err != nil {
		t.Fatalf("get config after clear: %v", err)
	}
	if cfg != nil {
		t.Fatal("expected guild config to be deleted from DB")
	}
}

