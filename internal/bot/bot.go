package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"gobot/internal/audit"
	_ "gobot/internal/commands/prefix"
	_ "gobot/internal/commands/slash"
	"gobot/internal/database"
	"gobot/internal/lifecycle"
	_ "gobot/internal/modules"
	"gobot/internal/registry"

	"github.com/bwmarrin/discordgo"
)

type Bot struct {
	Session      *discordgo.Session
	shutdownOnce sync.Once
	shutdownErr  error
}

func New(token string) (*Bot, error) {
	if token == "" {
		return nil, fmt.Errorf("DISCORD_TOKEN is not set")
	}

	lifecycle.Init()

	if err := database.Init(); err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}

	s, err := discordgo.New("Bot " + token)
	if err != nil {
		_ = database.Default.Close()
		return nil, err
	}

	s.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent
	s.AddHandler(handleInteraction)
	s.AddHandler(handleMessageCreate)
	s.AddHandler(func(s *discordgo.Session, _ *discordgo.GuildCreate) {
		updateGuildStatus(s)
	})
	s.AddHandler(func(s *discordgo.Session, _ *discordgo.GuildDelete) {
		updateGuildStatus(s)
	})

	// Wrap the HTTP client with our transport so that non-JSON 429 responses
	// (e.g. Cloudflare "error 1015" plain-text rate limits) are intercepted,
	// slept through, and converted to valid JSON before discordgo sees them.
	// Without this, discordgo v0.29.0 returns immediately on unmarshal failure
	// instead of sleeping and retrying (restapi.go:283-284).
	s.Client = &http.Client{
		Transport: newRateLimitTransport(nil),
	}

	return &Bot{Session: s}, nil
}

func (b *Bot) Start() error {
	log.Println("[BOOT] Starting Discord bot")
	log.Println("[BOOT] Opening gateway connection")

	if err := b.Session.Open(); err != nil {
		log.Printf("[ERROR] Gateway connection failed: %v", err)
		return err
	}

	for _, module := range registry.Default.Modules() {
		log.Printf("[MODULE] LOADED %s", module.Name)
	}

	if shouldSyncCommands() {
		if err := syncGlobalCommands(b.Session); err != nil {
			return err
		}
	} else {
		log.Printf("[SLASH] Command sync skipped (DISCORD_SYNC_COMMANDS=false)")
	}

	log.Printf("[READY] Logged in as %s (%s)", b.Session.State.User.Username, b.Session.State.User.ID)
	log.Printf("[READY] Gateway latency: %s", formatHeartbeatLatency(b.Session.HeartbeatLatency()))
	log.Printf("[READY] Loaded modules: %d", len(registry.Default.Modules()))
	log.Printf("[READY] Loaded slash commands: %d", len(registry.Default.Commands()))
	log.Printf("[READY] Loaded prefix commands: %d", len(registry.Default.PrefixCommands()))
	log.Printf("[READY] Prefix: %s", os.Getenv("PREFIX"))

	updateGuildStatus(b.Session)

	return nil
}

func updateGuildStatus(s *discordgo.Session) {
	guilds := len(s.State.Guilds)
	if err := s.UpdateCustomStatus(fmt.Sprintf("%d/100 servers", guilds)); err != nil {
		log.Printf("[STATUS] failed to update status: %v", err)
	}
}

func (b *Bot) Close() error {
	return b.Shutdown(context.Background())
}

func (b *Bot) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	b.shutdownOnce.Do(func() {
		activeTasks := lifecycle.ActiveTasks()
		log.Printf("[SHUTDOWN] Starting graceful shutdown (active tasks: %d)", activeTasks)
		lifecycle.Cancel()

		var errs []error
		if b.Session != nil {
			if err := b.Session.Close(); err != nil {
				errs = append(errs, fmt.Errorf("discord session: %w", err))
			} else {
				log.Println("[SHUTDOWN] Discord session closed")
			}
		}

		done := make(chan struct{})
		go func() {
			lifecycle.Wait()
			close(done)
		}()

		log.Printf("[SHUTDOWN] Waiting for background tasks to drain (%d active)", activeTasks)

		select {
		case <-done:
			log.Printf("[SHUTDOWN] Background tasks drained (remaining: %d)", lifecycle.ActiveTasks())
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf("background tasks: %w", ctx.Err()))
			log.Printf("[SHUTDOWN] Background tasks did not finish before timeout: %v (remaining: %d)", ctx.Err(), lifecycle.ActiveTasks())
		}

		if database.Default != nil {
			if err := database.Default.Close(); err != nil {
				errs = append(errs, fmt.Errorf("database: %w", err))
			} else {
				log.Println("[SHUTDOWN] Database closed")
			}
		}

		if err := audit.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("audit logger: %w", err))
		} else {
			log.Println("[SHUTDOWN] Audit logger closed")
		}

		if len(errs) > 0 {
			b.shutdownErr = errors.Join(errs...)
		}
	})

	return b.shutdownErr
}

func syncGlobalCommands(s *discordgo.Session) error {
	desired := registry.Default.Commands()
	existing, err := s.ApplicationCommands(s.State.User.ID, "")
	if err != nil {
		log.Printf("[SLASH] FAILED to fetch existing commands: %v", err)
		return err
	}

	existingByName := make(map[string]*discordgo.ApplicationCommand, len(existing))
	for _, cmd := range existing {
		existingByName[cmd.Name] = cmd
	}

	desiredNames := make(map[string]struct{}, len(desired))
	for _, command := range desired {
		desiredNames[command.Data.Name] = struct{}{}
		log.Printf("[SLASH] Syncing %s (%s)", command.Data.Name, command.Module)

		current, ok := existingByName[command.Data.Name]
		switch {
		case !ok:
			if _, err := s.ApplicationCommandCreate(s.State.User.ID, "", command.Data); err != nil {
				log.Printf("[SLASH] FAILED create %s: %v", command.Data.Name, err)
				return err
			}
			log.Printf("[SLASH] CREATED %s", command.Data.Name)
		case applicationCommandEqual(current, command.Data):
			log.Printf("[SLASH] UNCHANGED %s", command.Data.Name)
		default:
			if _, err := s.ApplicationCommandEdit(s.State.User.ID, "", current.ID, command.Data); err != nil {
				log.Printf("[SLASH] FAILED update %s: %v", command.Data.Name, err)
				return err
			}
			log.Printf("[SLASH] UPDATED %s", command.Data.Name)
		}
	}

	for _, command := range existing {
		if _, ok := desiredNames[command.Name]; ok {
			continue
		}
		if err := s.ApplicationCommandDelete(s.State.User.ID, "", command.ID); err != nil {
			log.Printf("[SLASH] FAILED delete stale %s: %v", command.Name, err)
			return err
		}
		log.Printf("[SLASH] DELETED stale %s", command.Name)
	}

	return nil
}

func applicationCommandEqual(a, b *discordgo.ApplicationCommand) bool {
	return applicationCommandSignature(a) == applicationCommandSignature(b)
}

func applicationCommandSignature(cmd *discordgo.ApplicationCommand) string {
	if cmd == nil {
		return ""
	}

	defaultPerms := ""
	if cmd.DefaultMemberPermissions != nil {
		defaultPerms = fmt.Sprintf("%d", *cmd.DefaultMemberPermissions)
	}

	normalized := normalizedCommand{
		Type:                     normalizeCommandType(cmd.Type),
		Name:                     cmd.Name,
		Description:              cmd.Description,
		DefaultMemberPermissions: defaultPerms,
		Options:                  normalizeCommandOptions(cmd.Options),
	}

	body, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	return string(body)
}

func shouldSyncCommands() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv("DISCORD_SYNC_COMMANDS")))
	switch value {
	case "", "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		log.Printf("[SLASH] invalid DISCORD_SYNC_COMMANDS=%q, defaulting to enabled", value)
		return true
	}
}

func normalizeCommandType(t discordgo.ApplicationCommandType) discordgo.ApplicationCommandType {
	if t == 0 {
		return discordgo.ChatApplicationCommand
	}
	return t
}

func formatHeartbeatLatency(latency time.Duration) string {
	if latency < 0 {
		return "unavailable"
	}
	return fmt.Sprintf("%dms", latency.Milliseconds())
}

type normalizedCommand struct {
	Type                     discordgo.ApplicationCommandType `json:"type"`
	Name                     string                           `json:"name"`
	Description              string                           `json:"description"`
	DefaultMemberPermissions string                           `json:"default_member_permissions,omitempty"`
	Options                  []normalizedCommandOption        `json:"options,omitempty"`
}

type normalizedCommandOption struct {
	Type         discordgo.ApplicationCommandOptionType `json:"type"`
	Name         string                                 `json:"name"`
	Description  string                                 `json:"description"`
	Required     bool                                   `json:"required,omitempty"`
	Autocomplete bool                                   `json:"autocomplete,omitempty"`
	MinLength    *int                                   `json:"min_length,omitempty"`
	MaxLength    int                                    `json:"max_length,omitempty"`
	MinValue     *float64                               `json:"min_value,omitempty"`
	MaxValue     float64                                `json:"max_value,omitempty"`
	ChannelTypes []discordgo.ChannelType                `json:"channel_types,omitempty"`
	Choices      []normalizedCommandChoice              `json:"choices,omitempty"`
	Options      []normalizedCommandOption              `json:"options,omitempty"`
}

type normalizedCommandChoice struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

func normalizeCommandOptions(options []*discordgo.ApplicationCommandOption) []normalizedCommandOption {
	if len(options) == 0 {
		return nil
	}

	out := make([]normalizedCommandOption, 0, len(options))
	for _, option := range options {
		if option == nil {
			continue
		}

		channelTypes := append([]discordgo.ChannelType(nil), option.ChannelTypes...)
		sort.Slice(channelTypes, func(i, j int) bool { return channelTypes[i] < channelTypes[j] })

		choices := make([]normalizedCommandChoice, 0, len(option.Choices))
		for _, choice := range option.Choices {
			choices = append(choices, normalizedCommandChoice{
				Name:  choice.Name,
				Value: choice.Value,
			})
		}
		sort.Slice(choices, func(i, j int) bool { return choices[i].Name < choices[j].Name })

		out = append(out, normalizedCommandOption{
			Type:         option.Type,
			Name:         option.Name,
			Description:  option.Description,
			Required:     option.Required,
			Autocomplete: option.Autocomplete,
			MinLength:    option.MinLength,
			MaxLength:    option.MaxLength,
			MinValue:     option.MinValue,
			MaxValue:     option.MaxValue,
			ChannelTypes: channelTypes,
			Choices:      choices,
			Options:      normalizeCommandOptions(option.Options),
		})
	}

	return out
}
