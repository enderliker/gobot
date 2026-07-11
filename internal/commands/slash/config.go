package slash

import (
	"fmt"
	"log"
	"unicode/utf8"

	"gobot/internal/database"
	"gobot/internal/embeds"
	"gobot/internal/registry"

	"github.com/bwmarrin/discordgo"
)

func init() {
	minContextValue := 0.0
	maxContextValue := 20.0

	if err := registry.RegisterCommand(&registry.Command{
		Module: "Configuration",
		Data: &discordgo.ApplicationCommand{
			Name:        "config",
			Description: "View or change server-level bot settings (owner only)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "multi_message",
					Description: "Split long AI responses across multiple messages instead of clipping",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "value",
							Description: "Enable or disable multi-message splitting",
							Required:    true,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "on", Value: "on"},
								{Name: "off", Value: "off"},
							},
						},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "channel_context",
					Description: "Number of recent channel messages passed as context to the AI",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionInteger,
							Name:        "messages",
							Description: "Number of messages (0 resets to default of 5; max 20)",
							Required:    true,
							MinValue:    &minContextValue,
							MaxValue:    maxContextValue,
						},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "clearkey",
					Description: "Delete the AI API key and all settings for this server",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "setprompt",
					Description: "Configure the server's custom system prompt for the AI",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "getprompt",
					Description: "View or edit the server's current system prompt",
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

	// With subcommands, Options[0] is the chosen subcommand.
	opts := i.ApplicationCommandData().Options
	if len(opts) == 0 {
		return
	}
	sub := opts[0]
	subOpts := subCommandOptionMap(sub.Options)

	switch sub.Name {
	case "multi_message":
		value := subCommandOptionString(subOpts, "value")
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

	case "channel_context":
		limit := int(subCommandOptionInt(subOpts, "messages"))
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
	}
}

func subCommandOptionMap(opts []*discordgo.ApplicationCommandInteractionDataOption) map[string]*discordgo.ApplicationCommandInteractionDataOption {
	m := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(opts))
	for _, o := range opts {
		m[o.Name] = o
	}
	return m
}

func subCommandOptionString(opts map[string]*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	if o, ok := opts[name]; ok {
		return o.StringValue()
	}
	return ""
}

func subCommandOptionInt(opts map[string]*discordgo.ApplicationCommandInteractionDataOption, name string) int64 {
	if o, ok := opts[name]; ok {
		return o.IntValue()
	}
	return 0
}
