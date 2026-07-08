package prefix

import (
	"time"

	"gobot/internal/embeds"
	"gobot/internal/registry"

	"github.com/bwmarrin/discordgo"
)

func init() {
	if err := registry.RegisterPrefixCommand(&registry.PrefixCommand{
		Module: "Utility",
		Name:   "ping",
		Execute: func(s *discordgo.Session, m *discordgo.MessageCreate) {
			gatewayLatency := s.HeartbeatLatency().Milliseconds()
			messageLatency := measureDiscordAPILatency(s)
			_, _ = s.ChannelMessageSendEmbed(m.ChannelID, embeds.Ping(gatewayLatency, messageLatency))
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
