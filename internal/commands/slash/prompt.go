package slash

import (
	"context"
	"errors"
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"gobot/internal/ai"
	"gobot/internal/database"
	"gobot/internal/embeds"
	"gobot/internal/lifecycle"
	"gobot/internal/registry"

	"github.com/bwmarrin/discordgo"
)

const (
	guildPromptFlowTimeout     = 10 * time.Minute
	guildPromptFlowGrace       = 2 * time.Minute
	guildPromptAuditExcerptLen = 1024
	guildPromptInputCustomID   = "guild_system_prompt_input"
)

type guildPromptViewMode int

const (
	guildPromptSetupView guildPromptViewMode = iota
	guildPromptSummaryView
)

func init() {
	if err := registry.RegisterCommand(&registry.Command{
		Module: "AI",
		Data: &discordgo.ApplicationCommand{
			Name:                     "setprompt",
			Description:              "Configure the server's GuildSystem prompt (server owner only)",
			DefaultMemberPermissions: int64Ptr(discordgo.PermissionManageServer),
		},
		Execute: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			if i.GuildID == "" {
				respond(s, i, embeds.Error("Server Only", "This command can only be used in a server."))
				return
			}
			if !isGuildOwner(s, i.GuildID, i.Member) {
				auditInteraction(i, "config_prompt_set", "denied", map[string]any{
					"reason": "not_guild_owner",
				})
				respond(s, i, embeds.Error("Owner Only", "Only the server owner can configure the guild system prompt."))
				return
			}
			presentGuildPromptFlow(s, i, guildPromptSetupView, "")
		},
	}); err != nil {
		panic(err)
	}

	if err := registry.RegisterCommand(&registry.Command{
		Module: "AI",
		Data: &discordgo.ApplicationCommand{
			Name:                     "getprompt",
			Description:              "View or edit the server's GuildSystem prompt (server owner only)",
			DefaultMemberPermissions: int64Ptr(discordgo.PermissionManageServer),
		},
		Execute: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			if i.GuildID == "" {
				respond(s, i, embeds.Error("Server Only", "This command can only be used in a server."))
				return
			}
			if !isGuildOwner(s, i.GuildID, i.Member) {
				auditInteraction(i, "config_prompt_viewed", "denied", map[string]any{
					"reason": "not_guild_owner",
				})
				respond(s, i, embeds.Error("Owner Only", "Only the server owner can view the guild system prompt."))
				return
			}

			prompt, err := database.Default.GetGuildSystemPrompt(i.GuildID)
			if err != nil {
				log.Printf("[PROMPT] read: %v", err)
				auditInteraction(i, "config_prompt_viewed", "error", map[string]any{
					"reason": "database_read_failed",
				})
				respond(s, i, embeds.Error("Database Error", "Failed to load the guild system prompt. Please try again."))
				return
			}

			auditInteraction(i, "config_prompt_viewed", "success", map[string]any{
				"has_prompt":    prompt != "",
				"prompt_length": utf8.RuneCountInString(prompt),
			})
			presentGuildPromptFlow(s, i, guildPromptSummaryView, prompt)
		},
	}); err != nil {
		panic(err)
	}
}

func presentGuildPromptFlow(s *discordgo.Session, i *discordgo.InteractionCreate, mode guildPromptViewMode, initialPrompt string) {
	flowID := strconv.FormatInt(time.Now().UnixNano(), 10)
	actorID := interactionUserID(i)

	editID := "guild_prompt_edit_" + flowID
	clearID := "guild_prompt_clear_" + flowID
	confirmClearID := "guild_prompt_clear_confirm_" + flowID
	cancelClearID := "guild_prompt_clear_cancel_" + flowID
	modalID := "guild_prompt_modal_" + flowID

	doneCh := make(chan struct{})
	var once sync.Once
	var expired atomic.Bool

	var removeHandler func()
	cleanup := func() {
		once.Do(func() {
			close(doneCh)
			if removeHandler != nil {
				removeHandler()
			}
		})
	}

	removeHandler = s.AddHandler(func(s *discordgo.Session, ic *discordgo.InteractionCreate) {
		switch ic.Type {
		case discordgo.InteractionMessageComponent:
			data := ic.MessageComponentData()
			switch data.CustomID {
			case editID:
				if !ensurePromptComponentAccess(s, ic, actorID, "config_prompt_set") {
					return
				}
				if expired.Load() {
					respondExpiredPromptComponent(s, ic)
					cleanup()
					return
				}

				currentPrompt, err := database.Default.GetGuildSystemPrompt(i.GuildID)
				if err != nil {
					log.Printf("[PROMPT] read for edit: %v", err)
					auditInteraction(ic, "config_prompt_set", "error", map[string]any{
						"reason": "database_read_failed",
					})
					respondPromptComponentError(s, ic, "Database Error", "Failed to load the current guild system prompt.")
					return
				}

				_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseModal,
					Data: &discordgo.InteractionResponseData{
						CustomID: modalID,
						Title:    "Edit Guild System Prompt",
						Components: []discordgo.MessageComponent{
							discordgo.ActionsRow{
								Components: []discordgo.MessageComponent{
									discordgo.TextInput{
										CustomID:    guildPromptInputCustomID,
										Label:       "Guild System Prompt",
										Style:       discordgo.TextInputParagraph,
										Placeholder: "Lower-priority server-specific instructions for /ask",
										Value:       currentPrompt,
										Required:    true,
										MinLength:   1,
										MaxLength:   ai.MaxGuildSystemPromptChars,
									},
								},
							},
						},
					},
				})
			case clearID:
				if !ensurePromptComponentAccess(s, ic, actorID, "config_prompt_cleared") {
					return
				}
				if expired.Load() {
					respondExpiredPromptComponent(s, ic)
					cleanup()
					return
				}
				_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseUpdateMessage,
					Data: &discordgo.InteractionResponseData{
						Embeds: []*discordgo.MessageEmbed{guildPromptClearConfirmationEmbed()},
						Components: []discordgo.MessageComponent{
							discordgo.ActionsRow{
								Components: []discordgo.MessageComponent{
									discordgo.Button{
										Label:    "Confirm Clear",
										Style:    discordgo.DangerButton,
										CustomID: confirmClearID,
									},
									discordgo.Button{
										Label:    "Cancel",
										Style:    discordgo.SecondaryButton,
										CustomID: cancelClearID,
									},
								},
							},
						},
					},
				})
			case confirmClearID:
				if !ensurePromptComponentAccess(s, ic, actorID, "config_prompt_cleared") {
					return
				}
				if expired.Load() {
					respondExpiredPromptComponent(s, ic)
					cleanup()
					return
				}

				currentPrompt, err := database.Default.GetGuildSystemPrompt(i.GuildID)
				if err != nil {
					log.Printf("[PROMPT] read before clear: %v", err)
					auditInteraction(ic, "config_prompt_cleared", "error", map[string]any{
						"reason": "database_read_failed",
					})
					_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseUpdateMessage,
						Data: &discordgo.InteractionResponseData{
							Embeds:     []*discordgo.MessageEmbed{embeds.Error("Database Error", "Failed to load the guild system prompt before clearing it.")},
							Components: guildPromptSummaryComponents(editID, clearID),
						},
					})
					return
				}

				if err := database.Default.ClearGuildSystemPrompt(i.GuildID); err != nil {
					log.Printf("[PROMPT] clear: %v", err)
					auditInteraction(ic, "config_prompt_cleared", "error", map[string]any{
						"reason": "database_write_failed",
					})
					_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseUpdateMessage,
						Data: &discordgo.InteractionResponseData{
							Embeds:     []*discordgo.MessageEmbed{embeds.Error("Database Error", "Failed to clear the guild system prompt. Please try again.")},
							Components: guildPromptSummaryComponents(editID, clearID),
						},
					})
					return
				}

				auditInteraction(ic, "config_prompt_cleared", "success", mergeAuditFields(
					map[string]any{
						"had_prompt":    currentPrompt != "",
						"prompt_length": utf8.RuneCountInString(currentPrompt),
					},
					auditPromptFields(currentPrompt),
				))
				_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseUpdateMessage,
					Data: &discordgo.InteractionResponseData{
						Embeds:     []*discordgo.MessageEmbed{guildPromptSummaryEmbed("")},
						Components: guildPromptSummaryComponents(editID, clearID),
					},
				})
			case cancelClearID:
				if !ensurePromptComponentAccess(s, ic, actorID, "config_prompt_cleared") {
					return
				}
				if expired.Load() {
					respondExpiredPromptComponent(s, ic)
					cleanup()
					return
				}

				currentPrompt, err := database.Default.GetGuildSystemPrompt(i.GuildID)
				if err != nil {
					log.Printf("[PROMPT] read after cancel: %v", err)
					auditInteraction(ic, "config_prompt_cleared", "error", map[string]any{
						"reason": "database_read_failed",
					})
					_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseUpdateMessage,
						Data: &discordgo.InteractionResponseData{
							Embeds:     []*discordgo.MessageEmbed{embeds.Error("Database Error", "Failed to reload the guild system prompt.")},
							Components: guildPromptSummaryComponents(editID, clearID),
						},
					})
					return
				}

				_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseUpdateMessage,
					Data: &discordgo.InteractionResponseData{
						Embeds:     []*discordgo.MessageEmbed{guildPromptSummaryEmbed(currentPrompt)},
						Components: guildPromptSummaryComponents(editID, clearID),
					},
				})
			}
		case discordgo.InteractionModalSubmit:
			data := ic.ModalSubmitData()
			if data.CustomID != modalID {
				return
			}
			if !ensurePromptComponentAccess(s, ic, actorID, "config_prompt_set") {
				return
			}
			if expired.Load() {
				_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Embeds: []*discordgo.MessageEmbed{
							embeds.Error("Configuration Expired", "This prompt configuration session expired. Run `/setprompt` or `/getprompt` again."),
						},
						Flags: discordgo.MessageFlagsEphemeral,
					},
				})
				cleanup()
				return
			}

			savedPrompt, err := saveGuildSystemPrompt(database.Default, i.GuildID, modalTextInputValue(data, guildPromptInputCustomID))
			if err != nil {
				var validationErr *ai.GuildSystemPromptValidationError
				if errors.As(err, &validationErr) {
					auditInteraction(ic, "config_prompt_set", "error", map[string]any{
						"reason":        "content_rejected",
						"validation":    validationErr.Code,
						"prompt_length": utf8.RuneCountInString(ai.NormalizeGuildSystemPrompt(modalTextInputValue(data, guildPromptInputCustomID))),
					})
					_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Embeds: []*discordgo.MessageEmbed{
								embeds.Error("Prompt Rejected", validationErr.Message),
							},
							Flags: discordgo.MessageFlagsEphemeral,
						},
					})
					return
				}

				log.Printf("[PROMPT] save: %v", err)
				auditInteraction(ic, "config_prompt_set", "error", map[string]any{
					"reason": "database_write_failed",
				})
				_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Embeds: []*discordgo.MessageEmbed{
							embeds.Error("Database Error", "Failed to save the guild system prompt. Please try again."),
						},
						Flags: discordgo.MessageFlagsEphemeral,
					},
				})
				return
			}

			auditInteraction(ic, "config_prompt_set", "success", mergeAuditFields(
				map[string]any{
					"prompt_length": utf8.RuneCountInString(savedPrompt),
				},
				auditPromptFields(savedPrompt),
			))
			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Embeds: []*discordgo.MessageEmbed{
						{
							Title:       "✅ Guild System Prompt Saved",
							Description: "The guild system prompt was updated and `/ask` will use it immediately.",
							Color:       embeds.SuccessColor,
						},
					},
					Flags: discordgo.MessageFlagsEphemeral,
				},
			})
		}
	})

	initialEmbed := guildPromptSetupEmbed()
	initialComponents := guildPromptSetupComponents(editID)
	if mode == guildPromptSummaryView {
		initialEmbed = guildPromptSummaryEmbed(initialPrompt)
		initialComponents = guildPromptSummaryComponents(editID, clearID)
	}

	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds:     []*discordgo.MessageEmbed{initialEmbed},
			Components: initialComponents,
			Flags:      discordgo.MessageFlagsEphemeral,
		},
	}); err != nil {
		cleanup()
		log.Printf("[PROMPT] respond: %v", err)
		return
	}

	lifecycle.Go(func(ctx context.Context) {
		select {
		case <-doneCh:
			return
		case <-ctx.Done():
			cleanup()
			return
		case <-time.After(guildPromptFlowTimeout):
			expired.Store(true)
		}

		select {
		case <-doneCh:
			return
		case <-ctx.Done():
			cleanup()
		case <-time.After(guildPromptFlowGrace):
			cleanup()
		}
	})
}

func saveGuildSystemPrompt(d *database.Database, guildID, prompt string) (string, error) {
	prompt = ai.NormalizeGuildSystemPrompt(prompt)
	if err := ai.ValidateGuildSystemPrompt(prompt); err != nil {
		return "", err
	}
	if err := d.SetGuildSystemPrompt(guildID, prompt); err != nil {
		return "", err
	}
	return prompt, nil
}

func modalTextInputValue(data discordgo.ModalSubmitInteractionData, customID string) string {
	for _, component := range data.Components {
		row, ok := component.(*discordgo.ActionsRow)
		if !ok {
			continue
		}
		for _, child := range row.Components {
			input, ok := child.(*discordgo.TextInput)
			if ok && input.CustomID == customID {
				return input.Value
			}
		}
	}
	return ""
}

func guildPromptSetupEmbed() *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       "Guild System Prompt",
		Description: "Click **Edit System Prompt** to configure the lower-priority GuildSystem prompt used by `/ask` for this server.",
		Color:       embeds.AccentColor,
	}
}

func guildPromptSummaryEmbed(prompt string) *discordgo.MessageEmbed {
	description := "No prompt configured."
	if prompt != "" {
		description = prompt
	}
	return &discordgo.MessageEmbed{
		Title:       "Guild System Prompt",
		Description: description,
		Color:       embeds.AccentColor,
	}
}

func guildPromptClearConfirmationEmbed() *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       "Clear Guild System Prompt?",
		Description: "This will remove the persisted GuildSystem prompt for this server. Confirm to continue.",
		Color:       embeds.ErrorColor,
	}
}

func guildPromptSetupComponents(editID string) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Edit System Prompt",
					Style:    discordgo.PrimaryButton,
					CustomID: editID,
				},
			},
		},
	}
}

func guildPromptSummaryComponents(editID, clearID string) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Edit",
					Style:    discordgo.PrimaryButton,
					CustomID: editID,
				},
				discordgo.Button{
					Label:    "Clear",
					Style:    discordgo.DangerButton,
					CustomID: clearID,
				},
			},
		},
	}
}

func respondExpiredPromptComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{
				embeds.Error("Configuration Expired", "This prompt configuration session expired. Run `/setprompt` or `/getprompt` again."),
			},
			Components: []discordgo.MessageComponent{},
		},
	})
}

func respondPromptComponentError(s *discordgo.Session, i *discordgo.InteractionCreate, title, message string) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{
				embeds.Error(title, message),
			},
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})
}

func auditPromptFields(prompt string) map[string]any {
	if prompt == "" {
		return nil
	}
	return map[string]any{
		"prompt_excerpt": truncatePromptForAudit(prompt, guildPromptAuditExcerptLen),
	}
}

func truncatePromptForAudit(prompt string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if utf8.RuneCountInString(prompt) <= maxLen {
		return prompt
	}

	runes := []rune(prompt)
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}
