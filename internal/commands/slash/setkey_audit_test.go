package slash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gobot/internal/ai"
	"gobot/internal/audit"
	"gobot/internal/lifecycle"
	"gobot/internal/registry"

	"github.com/bwmarrin/discordgo"
)

func TestSetKeyAuditNeverLogsAPIKeyCanary(t *testing.T) {
	const canary = "sk-test-CANARY-VALUE-12345"

	cases := []struct {
		name           string
		provider       ai.Provider
		interaction    *discordgo.InteractionCreate
		wantActionType string
		wantOutcome    string
		wantReason     string
	}{
		{
			name: "success",
			provider: &setKeyAuditProvider{
				name:   "AuditTest",
				models: []string{"model-1"},
			},
			interaction:    newSetKeyInteractionForUser("owner-1", []string{"role-member"}, canary, "AuditTest"),
			wantActionType: "config_setkey_requested",
			wantOutcome:    "success",
		},
		{
			name: "denied non-owner manage server",
			provider: &setKeyAuditProvider{
				name:   "AuditTest",
				models: []string{"model-1"},
			},
			interaction:    newSetKeyInteractionForUser("user-1", []string{"role-manager"}, canary, "AuditTest"),
			wantActionType: "config_setkey_requested",
			wantOutcome:    "denied",
			wantReason:     "not_guild_owner",
		},
		{
			name: "validate error",
			provider: &setKeyAuditProvider{
				name:        "AuditTest",
				validateErr: errors.New("provider rejected key"),
			},
			interaction:    newSetKeyInteractionForUser("owner-1", []string{"role-member"}, canary, "AuditTest"),
			wantActionType: "config_setkey_requested",
			wantOutcome:    "error",
		},
		{
			name: "list models error",
			provider: &setKeyAuditProvider{
				name:          "AuditTest",
				listModelsErr: errors.New("list models failed"),
			},
			interaction:    newSetKeyInteractionForUser("owner-1", []string{"role-member"}, canary, "AuditTest"),
			wantActionType: "config_setkey_requested",
			wantOutcome:    "error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle.Init()
			defer lifecycle.Cancel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			restoreEndpoints := stubDiscordEndpoints(t, server.URL+"/")
			defer restoreEndpoints()

			session, err := discordgo.New("Bot test-token")
			if err != nil {
				t.Fatalf("new session: %v", err)
			}
			session.Client = server.Client()
			session.SyncEvents = true

			seedManageServerGuild(t, session)

			var auditBuf bytes.Buffer
			logger := audit.New(&auditBuf, nil)
			restoreAudit := audit.SetDefaultForTest(logger)
			defer restoreAudit()

			restoreManager := stubAIManager(t, tc.provider)
			defer restoreManager()

			cmd := registeredSlashCommand(t, "setkey")
			cmd.Execute(session, tc.interaction)

			if err := logger.Close(context.Background()); err != nil {
				t.Fatalf("close audit logger: %v", err)
			}

			raw := auditBuf.String()
			if raw == "" {
				t.Fatal("expected audit output")
			}
			if strings.Contains(raw, canary) {
				t.Fatalf("audit output leaked canary API key: %q", raw)
			}

			lines := strings.Split(strings.TrimSpace(raw), "\n")
			var found bool
			for _, line := range lines {
				var payload map[string]any
				if err := json.Unmarshal([]byte(line), &payload); err != nil {
					t.Fatalf("unmarshal audit line: %v", err)
				}
				if payload["action_type"] == tc.wantActionType && payload["outcome"] == tc.wantOutcome {
					if tc.wantReason != "" && payload["reason"] != tc.wantReason {
						t.Fatalf("expected reason %q, got payload %v", tc.wantReason, payload)
					}
					found = true
				}
				for key, value := range payload {
					if strings.Contains(stringifyAuditValue(value), canary) {
						t.Fatalf("field %q leaked canary in payload %v", key, payload)
					}
				}
			}
			if !found {
				t.Fatalf("expected audit event action=%s outcome=%s, got %q", tc.wantActionType, tc.wantOutcome, raw)
			}
		})
	}
}

type setKeyAuditProvider struct {
	name          string
	validateErr   error
	models        []string
	listModelsErr error
}

func (p *setKeyAuditProvider) Name() string { return p.name }

func (p *setKeyAuditProvider) Validate(ctx context.Context, apiKey string) error {
	return p.validateErr
}

func (p *setKeyAuditProvider) ListModels(ctx context.Context, apiKey string) ([]string, error) {
	if p.listModelsErr != nil {
		return nil, p.listModelsErr
	}
	return append([]string(nil), p.models...), nil
}

func (p *setKeyAuditProvider) Ask(ctx context.Context, apiKey, model string, prompt ai.PromptEnvelope, tools []ai.ToolDefinition) (*ai.AskResult, error) {
	return nil, nil
}

func registeredSlashCommand(t *testing.T, name string) *registry.Command {
	t.Helper()

	for _, cmd := range registry.Default.Commands() {
		if cmd.Data != nil && cmd.Data.Name == name {
			return cmd
		}
	}

	t.Fatalf("command %q not found", name)
	return nil
}

func newSetKeyInteraction(apiKey, provider string) *discordgo.InteractionCreate {
	return newSetKeyInteractionForUser("user-1", []string{"role-manager"}, apiKey, provider)
}

func newSetKeyInteractionForUser(userID string, roles []string, apiKey, provider string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			ID:        "interaction-1",
			AppID:     "app-1",
			Type:      discordgo.InteractionApplicationCommand,
			GuildID:   "guild-1",
			ChannelID: "channel-1",
			Token:     "token-1",
			Member: &discordgo.Member{
				User:  &discordgo.User{ID: userID},
				Roles: append([]string(nil), roles...),
			},
			Data: discordgo.ApplicationCommandInteractionData{
				Name: "setkey",
				Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{
						Name:  "provider",
						Type:  discordgo.ApplicationCommandOptionString,
						Value: provider,
					},
					{
						Name:  "key",
						Type:  discordgo.ApplicationCommandOptionString,
						Value: apiKey,
					},
				},
			},
		},
	}
}

func seedManageServerGuild(t *testing.T, session *discordgo.Session) {
	t.Helper()

	guild := &discordgo.Guild{
		ID:      "guild-1",
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
		},
	}
	if err := session.State.GuildAdd(guild); err != nil {
		t.Fatalf("seed guild state: %v", err)
	}
}

func stubAIManager(t *testing.T, provider ai.Provider) func() {
	t.Helper()

	prev := ai.DefaultManager
	ai.DefaultManager = ai.NewManager(provider)
	return func() {
		ai.DefaultManager = prev
	}
}

func stubDiscordEndpoints(t *testing.T, base string) func() {
	t.Helper()

	prevDiscord := discordgo.EndpointDiscord
	prevAPI := discordgo.EndpointAPI
	prevGuilds := discordgo.EndpointGuilds
	prevChannels := discordgo.EndpointChannels
	prevUsers := discordgo.EndpointUsers
	prevWebhooks := discordgo.EndpointWebhooks

	discordgo.EndpointDiscord = base
	discordgo.EndpointAPI = base + "api/v9/"
	discordgo.EndpointGuilds = discordgo.EndpointAPI + "guilds/"
	discordgo.EndpointChannels = discordgo.EndpointAPI + "channels/"
	discordgo.EndpointUsers = discordgo.EndpointAPI + "users/"
	discordgo.EndpointWebhooks = discordgo.EndpointAPI + "webhooks/"

	return func() {
		discordgo.EndpointDiscord = prevDiscord
		discordgo.EndpointAPI = prevAPI
		discordgo.EndpointGuilds = prevGuilds
		discordgo.EndpointChannels = prevChannels
		discordgo.EndpointUsers = prevUsers
		discordgo.EndpointWebhooks = prevWebhooks
	}
}

func stringifyAuditValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		body, _ := json.Marshal(v)
		return string(body)
	}
}
