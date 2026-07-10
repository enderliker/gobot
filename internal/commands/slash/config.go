package slash

import (
	"fmt"
	"log"
	"strings"

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
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "value",
					Description: "New value for the setting",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "on", Value: "on"},
						{Name: "off", Value: "off"},
					},
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
				Embeds: []*discordgo.MessageEmbed{embeds.Error("Not Configured", "No API key has been set for this server. Use /setkey first.")},
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
