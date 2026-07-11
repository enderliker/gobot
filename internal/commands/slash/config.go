package slash

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"unicode/utf8"

	"gobot/internal/database"
	"gobot/internal/embeds"
	"gobot/internal/registry"

	"github.com/bwmarrin/discordgo"
)

func init() {
	if err := registry.RegisterCommand(&registry.Command{
		Module: "Configuration",
		Data: &discordgo.ApplicationCommand{
			Name:        "config",
			Description: "View or change server-level bot settings (owner only)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "setting",
					Description: "The setting to change",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "multi_message — split long responses across multiple messages", Value: "multi_message"},
						{Name: "clearkey — delete the AI API key and settings for this server", Value: "clearkey"},
						{Name: "setprompt — configure the server's GuildSystem prompt", Value: "setprompt"},
						{Name: "getprompt — view or edit the server's GuildSystem prompt", Value: "getprompt"},
						{Name: "channel_context — number of recent messages passed as context (0 = default 5, 1–20)", Value: "channel_context"},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "value",
					Description: "New value for the setting (on/off for multi_message; 0–20 for channel_context)",
					Required:    false,
				},
			},
		},
		Execute: executeConfig,
	}); err != nil {
		panic(err)
	}
}

func executeConfig(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if _, denied := ownerOnlyAccessFailure(s, i, ""); denied {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{embeds.Error("Access Denied", "Only the server owner can change bot settings.")},
				Flags:  discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	if database.Default == nil {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{embeds.Error("Not Configured", "No database connection is active.")},
				Flags:  discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	opts := configOptionMap(i.ApplicationCommandData().Options)
	setting := strings.ToLower(configOptionString(opts, "setting"))
	value := strings.ToLower(configOptionString(opts, "value"))

	switch setting {
	case "multi_message":
		if value == "" {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Embeds: []*discordgo.MessageEmbed{embeds.Error("Missing Value", "The 'multi_message' setting requires a value ('on' or 'off').")},
					Flags:  discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		enabled := value == "on"
		if err := database.Default.SetGuildMultiMessage(i.GuildID, enabled); err != nil {
			log.Printf("[CONFIG] SetGuildMultiMessage %s: %v", i.GuildID, err)
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Embeds: []*discordgo.MessageEmbed{embeds.Error("Error", "Failed to save setting. Please try again.")},
					Flags:  discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		stateStr := "disabled"
		description := "Long AI responses will be **clipped** to fit in a single message."
		color := 0xED4245
		if enabled {
			stateStr = "enabled"
			description = "Long AI responses will be **split across multiple messages** instead of being clipped."
			color = 0x57F287
		}

		embed := &discordgo.MessageEmbed{
			Title:       fmt.Sprintf("✅ Setting Updated: multi_message %s", stateStr),
			Description: description,
			Color:       color,
		}
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{embed},
				Flags:  discordgo.MessageFlagsEphemeral,
			},
		})

	case "clearkey":
		if err := database.Default.DeleteGuildConfig(i.GuildID); err != nil {
			log.Printf("[CONFIG] DeleteGuildConfig %s: %v", i.GuildID, err)
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Embeds: []*discordgo.MessageEmbed{embeds.Error("Error", "Failed to delete server configuration. Please try again.")},
					Flags:  discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		embed := &discordgo.MessageEmbed{
			Title:       "✅ API Key Cleared",
			Description: "The AI API key and all settings for this server have been successfully deleted from the database.",
			Color:       0x57F287,
		}
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{embed},
				Flags:  discordgo.MessageFlagsEphemeral,
			},
		})

	case "setprompt":
		presentGuildPromptFlow(s, i, guildPromptSetupView, "")

	case "getprompt":
		prompt, err := database.Default.GetGuildSystemPrompt(i.GuildID)
		if err != nil {
			log.Printf("[PROMPT] read from config: %v", err)
			auditInteraction(i, "config_prompt_viewed", "error", map[string]any{
				"reason": "database_read_failed",
			})
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Embeds: []*discordgo.MessageEmbed{embeds.Error("Database Error", "Failed to load the guild system prompt. Please try again.")},
					Flags:  discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		auditInteraction(i, "config_prompt_viewed", "success", map[string]any{
			"has_prompt":    prompt != "",
			"prompt_length": utf8.RuneCountInString(prompt),
		})
		presentGuildPromptFlow(s, i, guildPromptSummaryView, prompt)

	case "channel_context":
		if value == "" {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Embeds: []*discordgo.MessageEmbed{embeds.Error("Missing Value", "The 'channel_context' setting requires a numeric value between 0 and 20.\n`0` resets to the default (5 messages).")},
					Flags:  discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}
		limit, parseErr := strconv.Atoi(value)
		if parseErr != nil || limit < 0 || limit > 20 {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Embeds: []*discordgo.MessageEmbed{embeds.Error("Invalid Value", "The 'channel_context' value must be a whole number between 0 and 20.")},
					Flags:  discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}
		if err := database.Default.SetGuildChannelContextLimit(i.GuildID, limit); err != nil {
			log.Printf("[CONFIG] SetGuildChannelContextLimit %s: %v", i.GuildID, err)
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Embeds: []*discordgo.MessageEmbed{embeds.Error("Error", "Failed to save setting. Please try again.")},
					Flags:  discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}
		var description string
		if limit == 0 {
			description = "Channel context has been **reset to the default** (5 recent messages)."
		} else {
			description = fmt.Sprintf("The last **%d** messages in the channel will be passed as context to the AI.", limit)
		}
		embed := &discordgo.MessageEmbed{
			Title:       fmt.Sprintf("✅ Setting Updated: channel_context = %d", limit),
			Description: description,
			Color:       0x57F287,
		}
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{embed},
				Flags:  discordgo.MessageFlagsEphemeral,
			},
		})

	default:
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{embeds.Error("Unknown Setting", fmt.Sprintf("Unknown setting %q.", setting))},
				Flags:  discordgo.MessageFlagsEphemeral,
			},
		})
	}
}

func configOptionMap(opts []*discordgo.ApplicationCommandInteractionDataOption) map[string]*discordgo.ApplicationCommandInteractionDataOption {
	m := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(opts))
	for _, o := range opts {
		m[o.Name] = o
	}
	return m
}

func configOptionString(opts map[string]*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	if o, ok := opts[name]; ok {
		return o.StringValue()
	}
	return ""
}
