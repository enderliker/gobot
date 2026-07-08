package slash

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gobot/internal/ai"
	"gobot/internal/database"
	"gobot/internal/embeds"
	"gobot/internal/lifecycle"
	"gobot/internal/registry"

	"github.com/bwmarrin/discordgo"
)

func init() {
	if err := registry.RegisterCommand(&registry.Command{
		Module: "AI",
		Data: &discordgo.ApplicationCommand{
			Name:                     "setkey",
			Description:              "Set your server's AI API key (stored encrypted; server owner only)",
			DefaultMemberPermissions: int64Ptr(discordgo.PermissionManageServer),
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "provider",
					Description: "The AI provider this key belongs to",
					Required:    true,
					Choices:     setKeyProviderChoices(),
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "key",
					Description: "Your AI provider API key (stored encrypted)",
					Required:    true,
				},
			},
		},
		Execute: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			if i.GuildID == "" {
				respond(s, i, embeds.Error("Server Only", "This command can only be used in a server."))
				return
			}
			if !isGuildOwner(s, i.GuildID, i.Member) {
				auditInteraction(i, "config_setkey_requested", "denied", map[string]any{
					"reason": "not_guild_owner",
				})
				respond(s, i, embeds.Error("Owner Only", "Only the server owner can configure the AI provider API key."))
				return
			}

			providerName, apiKey, err := parseSetKeyOptions(i.ApplicationCommandData().Options)
			if err != nil {
				auditInteraction(i, "config_setkey_requested", "error", map[string]any{
					"reason": "invalid_request",
				})
				respond(s, i, embeds.Error("Invalid Request", "The provider and API key are required. Please run `/setkey` again."))
				return
			}

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			provider, err := ai.DefaultManager.ValidateProvider(ctx, providerName, apiKey)
			if err != nil {
				if errors.Is(err, ai.ErrUnknownProvider) {
					auditInteraction(i, "config_setkey_requested", "error", map[string]any{
						"provider": providerName,
						"reason":   "unknown_provider",
					})
					respond(s, i, embeds.Error(
						"Unsupported Provider",
						"Selected provider is not supported by this bot.",
					))
					return
				}

				auditInteraction(i, "config_setkey_requested", "error", map[string]any{
					"provider": providerName,
					"reason":   "provider_validation_failed",
					"error":    ai.UserFacingError(err),
				})
				respond(s, i, embeds.Error(
					"API Key Validation Error",
					fmt.Sprintf("Failed to validate the API key with **%s**.\n\n%s", providerName, ai.UserFacingError(err)),
				))
				return
			}

			modelsCtx, modelsCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer modelsCancel()
			models, err := provider.ListModels(modelsCtx, apiKey)
			if err != nil {
				log.Printf("[SETKEY] list models error for %s: %v", provider.Name(), err)
				auditInteraction(i, "config_setkey_requested", "error", map[string]any{
					"provider": provider.Name(),
					"reason":   "list_models_failed",
					"error":    ai.UserFacingError(err),
				})
				respond(s, i, embeds.Error(
					"Model List Error",
					fmt.Sprintf("Failed to retrieve models from **%s**.\n\n%s", provider.Name(), ai.UserFacingError(err)),
				))
				return
			}

			if len(models) == 0 {
				auditInteraction(i, "config_setkey_requested", "error", map[string]any{
					"provider": provider.Name(),
					"reason":   "no_models_found",
				})
				respond(s, i, embeds.Error(
					"No Models Found",
					fmt.Sprintf("Successfully validated key with **%s**, but no compatible models were returned by the API.", provider.Name()),
				))
				return
			}

			auditInteraction(i, "config_setkey_requested", "success", map[string]any{
				"provider": provider.Name(),
			})

			// Always use a select-menu dropdown. When there are more than 25
			// models we show them in pages of 25 using Prev/Next buttons.
			// This avoids the emoji-reaction flow that caused Discord rate-limits.
			showModelPage(s, i, apiKey, provider, models, 0)
		},
	}); err != nil {
		panic(err)
	}
}

const (
	pageSize           = 25
	customIDPagePrefix = "setkey_page_"
	customIDModelSel   = "setkey_model_select_"
	selectionTimeout   = 60 * time.Second
	selectionGrace     = 5 * time.Minute
)

// showModelPage sends (or edits) an interaction response showing a paginated
// list of models as a Discord select-menu. page is 0-indexed.
func showModelPage(s *discordgo.Session, i *discordgo.InteractionCreate, apiKey string, provider ai.Provider, models []string, page int) {
	flowID := strconv.FormatInt(time.Now().UnixNano(), 10)

	slice, totalPages, err := modelPage(models, page)
	if err != nil {
		respond(s, i, embeds.Error("Model List Error", "The model list could not be paginated safely. Please run `/setkey` again."))
		return
	}

	var options []discordgo.SelectMenuOption
	for _, m := range slice {
		options = append(options, discordgo.SelectMenuOption{
			Label: m,
			Value: m,
		})
	}

	pageInfo := ""
	if totalPages > 1 {
		pageInfo = fmt.Sprintf(" (page %d/%d)", page+1, totalPages)
	}

	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					CustomID:    modelSelectCustomID(flowID),
					Placeholder: "Choose a model...",
					Options:     options,
				},
			},
		},
	}

	// Add Prev/Next navigation row when there are multiple pages.
	if totalPages > 1 {
		var navButtons []discordgo.MessageComponent
		if page > 0 {
			navButtons = append(navButtons, discordgo.Button{
				Label:    "◀ Previous",
				Style:    discordgo.SecondaryButton,
				CustomID: pageButtonCustomID(flowID, page-1),
			})
		}
		if page < totalPages-1 {
			navButtons = append(navButtons, discordgo.Button{
				Label:    "Next ▶",
				Style:    discordgo.SecondaryButton,
				CustomID: pageButtonCustomID(flowID, page+1),
			})
		}
		if len(navButtons) > 0 {
			components = append(components, discordgo.ActionsRow{Components: navButtons})
		}
	}

	embed := &discordgo.MessageEmbed{
		Title:       "Select AI Model" + pageInfo,
		Description: fmt.Sprintf("Validated key with **%s**.%s\nSelect the model you wish to use for this server:", provider.Name(), pageInfo),
		Color:       embeds.AccentColor,
	}

	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds:     []*discordgo.MessageEmbed{embed},
			Components: components,
			Flags:      discordgo.MessageFlagsEphemeral,
		},
	})
	if err != nil {
		log.Printf("[SETKEY] dropdown response: %v", err)
		return
	}

	// origUserID is captured for ownership checks inside handlers below.
	origUserID := interactionUserID(i)

	doneCh := make(chan struct{})
	var once sync.Once
	var expired atomic.Bool

	var removeSel func()
	var removePage func()

	cleanup := func() {
		once.Do(func() {
			close(doneCh)
			removeSel()
			removePage()
		})
	}

	// Handler for model selection.
	removeSel = s.AddHandler(func(s *discordgo.Session, ic *discordgo.InteractionCreate) {
		if ic.Type != discordgo.InteractionMessageComponent {
			return
		}
		data := ic.MessageComponentData()
		if data.CustomID != modelSelectCustomID(flowID) {
			return
		}
		if !ensureSetKeyComponentAccess(s, ic, origUserID, "config_model_selected") {
			return
		}
		if expired.Load() {
			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseUpdateMessage,
				Data: &discordgo.InteractionResponseData{
					Embeds:     []*discordgo.MessageEmbed{embeds.Error("Configuration Timeout", fmt.Sprintf("You didn't select a model in time (%ds). Please run `/setkey` again.", int(selectionTimeout.Seconds())))},
					Components: []discordgo.MessageComponent{},
				},
			})
			cleanup()
			return
		}
		if len(data.Values) == 0 {
			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "No model was selected.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		selectedModel := data.Values[0]
		if !containsModel(models, selectedModel) {
			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "That model is not part of this selection session.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		if err := database.Default.SetGuildConfig(i.GuildID, apiKey, provider.Name(), selectedModel); err != nil {
			log.Printf("[SETKEY] db: %v", err)
			auditInteraction(ic, "config_model_selected", "error", map[string]any{
				"provider": provider.Name(),
				"model":    selectedModel,
				"reason":   "database_write_failed",
			})
			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseUpdateMessage,
				Data: &discordgo.InteractionResponseData{
					Embeds:     []*discordgo.MessageEmbed{embeds.Error("Database Error", "Failed to save configuration. Please try again.")},
					Components: []discordgo.MessageComponent{},
				},
			})
		} else {
			auditInteraction(ic, "config_model_selected", "success", map[string]any{
				"provider": provider.Name(),
				"model":    selectedModel,
			})
			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseUpdateMessage,
				Data: &discordgo.InteractionResponseData{
					Embeds:     []*discordgo.MessageEmbed{embeds.KeySet(provider.Name(), selectedModel)},
					Components: []discordgo.MessageComponent{},
				},
			})
		}
		cleanup()
	})

	// Handler for page navigation buttons.
	removePage = s.AddHandler(func(s *discordgo.Session, ic *discordgo.InteractionCreate) {
		if ic.Type != discordgo.InteractionMessageComponent {
			return
		}
		data := ic.MessageComponentData()
		targetPage, ok := parsePageButtonCustomID(flowID, data.CustomID)
		if !ok {
			return
		}
		if !ensureSetKeyComponentAccess(s, ic, origUserID, "config_model_page") {
			return
		}
		if expired.Load() {
			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseUpdateMessage,
				Data: &discordgo.InteractionResponseData{
					Embeds:     []*discordgo.MessageEmbed{embeds.Error("Configuration Timeout", fmt.Sprintf("You didn't select a model in time (%ds). Please run `/setkey` again.", int(selectionTimeout.Seconds())))},
					Components: []discordgo.MessageComponent{},
				},
			})
			cleanup()
			return
		}

		slice, _, err := modelPage(models, targetPage)
		if err != nil {
			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "That page is no longer available.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		var opts []discordgo.SelectMenuOption
		for _, m := range slice {
			opts = append(opts, discordgo.SelectMenuOption{Label: m, Value: m})
		}

		pageInfo := fmt.Sprintf(" (page %d/%d)", targetPage+1, totalPages)
		newComponents := []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.SelectMenu{
						CustomID:    modelSelectCustomID(flowID),
						Placeholder: "Choose a model...",
						Options:     opts,
					},
				},
			},
		}
		var navButtons []discordgo.MessageComponent
		if targetPage > 0 {
			navButtons = append(navButtons, discordgo.Button{
				Label:    "◀ Previous",
				Style:    discordgo.SecondaryButton,
				CustomID: pageButtonCustomID(flowID, targetPage-1),
			})
		}
		if targetPage < totalPages-1 {
			navButtons = append(navButtons, discordgo.Button{
				Label:    "Next ▶",
				Style:    discordgo.SecondaryButton,
				CustomID: pageButtonCustomID(flowID, targetPage+1),
			})
		}
		if len(navButtons) > 0 {
			newComponents = append(newComponents, discordgo.ActionsRow{Components: navButtons})
		}

		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{{
					Title:       "Select AI Model" + pageInfo,
					Description: fmt.Sprintf("Validated key with **%s**.%s\nSelect the model you wish to use for this server:", provider.Name(), pageInfo),
					Color:       embeds.AccentColor,
				}},
				Components: newComponents,
			},
		})
	})

	// IMPORTANT: the timeout goroutine runs separately so it never blocks the
	// discordgo event dispatcher goroutine.
	lifecycle.Go(func(ctx context.Context) {
		select {
		case <-doneCh:
			// Model was selected — clean up handlers.
		case <-ctx.Done():
			cleanup()
			return
		case <-time.After(selectionTimeout):
			expired.Store(true)
			if !lifecycle.IsShuttingDown() {
				_, _ = s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
					Embeds: []*discordgo.MessageEmbed{
						embeds.Error("Configuration Timeout", fmt.Sprintf("You didn't select a model in time (%ds). Please run `/setkey` again.", int(selectionTimeout.Seconds()))),
					},
					Flags: discordgo.MessageFlagsEphemeral,
				})
			}
			select {
			case <-doneCh:
				return
			case <-ctx.Done():
				cleanup()
				return
			case <-time.After(selectionGrace):
				cleanup()
				return
			}
		}
		cleanup()
	})
}

// interactionUserID returns the Discord user ID from an interaction regardless
// of whether it was triggered in a guild (Member) or DM (User).
func interactionUserID(i *discordgo.InteractionCreate) string {
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User.ID
	}
	if i.User != nil {
		return i.User.ID
	}
	return ""
}

func respond(s *discordgo.Session, i *discordgo.InteractionCreate, embed *discordgo.MessageEmbed) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
			Flags:  discordgo.MessageFlagsEphemeral,
		},
	}); err != nil {
		log.Printf("[SETKEY] respond: %v", err)
	}
}

func int64Ptr(v int64) *int64 { return &v }

func setKeyProviderChoices() []*discordgo.ApplicationCommandOptionChoice {
	names := ai.DefaultManager.Names()
	choices := make([]*discordgo.ApplicationCommandOptionChoice, 0, len(names))
	for _, name := range names {
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
			Name:  name,
			Value: name,
		})
	}
	return choices
}

func parseSetKeyOptions(options []*discordgo.ApplicationCommandInteractionDataOption) (providerName, apiKey string, err error) {
	for _, option := range options {
		switch option.Name {
		case "provider":
			providerName = option.StringValue()
		case "key":
			apiKey = option.StringValue()
		}
	}

	if providerName == "" || apiKey == "" {
		return "", "", fmt.Errorf("missing required setkey options")
	}
	return providerName, apiKey, nil
}

func modelSelectCustomID(flowID string) string {
	return customIDModelSel + flowID
}

func pageButtonCustomID(flowID string, page int) string {
	return fmt.Sprintf("%s%s_%d", customIDPagePrefix, flowID, page)
}

func parsePageButtonCustomID(flowID, customID string) (int, bool) {
	prefix := customIDPagePrefix + flowID + "_"
	if !strings.HasPrefix(customID, prefix) {
		return 0, false
	}

	page, err := strconv.Atoi(strings.TrimPrefix(customID, prefix))
	if err != nil {
		return 0, false
	}
	return page, true
}

func modelPage(models []string, page int) ([]string, int, error) {
	if len(models) == 0 {
		return nil, 0, fmt.Errorf("empty model list")
	}

	totalPages := (len(models) + pageSize - 1) / pageSize
	if page < 0 || page >= totalPages {
		return nil, totalPages, fmt.Errorf("page out of range")
	}

	start := page * pageSize
	end := start + pageSize
	if end > len(models) {
		end = len(models)
	}

	return models[start:end], totalPages, nil
}

func containsModel(models []string, selected string) bool {
	for _, model := range models {
		if model == selected {
			return true
		}
	}
	return false
}
