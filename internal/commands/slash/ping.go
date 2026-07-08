package slash

import (
	"log"
	"time"

	"gobot/internal/embeds"
	"gobot/internal/registry"

	"github.com/bwmarrin/discordgo"
)

func init() {
	if err := registry.RegisterCommand(&registry.Command{
		Module: "Utility",
		Data: &discordgo.ApplicationCommand{
			Name:        "ping",
			Description: "Show gateway and API latency",
		},
		Execute: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			gatewayLatency := s.HeartbeatLatency().Milliseconds()
			messageLatency := measureDiscordAPILatency(s)

			err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Embeds: []*discordgo.MessageEmbed{embeds.Ping(gatewayLatency, messageLatency)},
				},
			})

			if err != nil {
				log.Printf("failed to respond to interaction: %v", err)
			}
		},
	}); err != nil {
		panic(err)
	}
}

func measureDiscordAPILatency(s *discordgo.Session) int64 {
	start := time.Now()
	if _, err := s.User("@me"); err != nil {
		return -1
	}
	return time.Since(start).Milliseconds()
}
