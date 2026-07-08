package bot

import (
	"gobot/internal/registry"

	"github.com/bwmarrin/discordgo"
)

func handleInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// MessageComponent and ModalSubmit interactions are handled by their own
	// dedicated handlers registered via s.AddHandler inside each command.
	// Calling ApplicationCommandData() on those types panics, so we guard here.
	if i.Type != discordgo.InteractionApplicationCommand {
		return
	}

	name := i.ApplicationCommandData().Name

	for _, command := range registry.Default.Commands() {
		if command.Data.Name == name {
			command.Execute(s, i)
			return
		}
	}
}
