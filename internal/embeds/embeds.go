package embeds

import (
	"fmt"

	"github.com/bwmarrin/discordgo"
)

const (
	AccentColor  = 0x5865F2
	SuccessColor = 0x57F287
	ErrorColor   = 0xED4245
)

func Ping(gatewayLatency, messageLatency int64) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       "Pong!",
		Description: fmt.Sprintf("Gateway: `%s`\nMessage/API: `%s`", formatLatency(gatewayLatency), formatLatency(messageLatency)),
		Color:       AccentColor,
	}
}

func formatLatency(latency int64) string {
	if latency < 0 {
		return "unavailable"
	}
	return fmt.Sprintf("%dms", latency)
}

func Error(title, message string) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       "❌ " + title,
		Description: message,
		Color:       ErrorColor,
	}
}

func KeySet(provider, model string) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       "✅ Provider & Model Configured",
		Description: fmt.Sprintf("Successfully configured **%s** as your AI provider and **%s** as the active model.\nYour API key is stored encrypted at rest.\nYou can now use `/ask` to chat.", provider, model),
		Color:       SuccessColor,
	}
}

func AIResponse(provider, model, question, answer string) *discordgo.MessageEmbed {
	const maxAnswer = 4000
	if len(answer) > maxAnswer {
		answer = answer[:maxAnswer] + "\n\n*Response truncated...*"
	}

	return &discordgo.MessageEmbed{
		Title:       "🤖 AI Response",
		Description: answer,
		Color:       AccentColor,
	}
}

func AIConfirmation(message string) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       "AI Action Confirmation",
		Description: message,
		Color:       AccentColor,
	}
}
