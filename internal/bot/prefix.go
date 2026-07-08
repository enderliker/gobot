package bot

import (
	"gobot/internal/registry"
	"os"
	"strings"

	"github.com/bwmarrin/discordgo"
)

func handleMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.Bot {
		return
	}

	prefix := os.Getenv("PREFIX")
	if prefix == "" || !strings.HasPrefix(m.Content, prefix) {
		return
	}

	parts := strings.Fields(strings.TrimPrefix(m.Content, prefix))
	if len(parts) == 0 {
		return
	}

	for _, command := range registry.Default.PrefixCommands() {
		if command.Name == parts[0] {
			command.Execute(s, m)
			return
		}
	}
}
