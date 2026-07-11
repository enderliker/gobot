package ai

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// DefaultChannelContextLimit is the default number of messages retrieved for channel context.
const DefaultChannelContextLimit = 5

// FetchRecentChannelContext retrieves the recent message history from a channel,
// formatted chronologically (oldest first). Each message is truncated to ~300 characters,
// and messages without text but containing embeds/attachments are marked as "[adjunto sin texto]".
func FetchRecentChannelContext(session *discordgo.Session, channelID string, excludeMessageID string, limit int) (string, error) {
	if limit <= 0 {
		return "", nil
	}

	beforeID := excludeMessageID
	messages, err := session.ChannelMessages(channelID, limit, beforeID, "", "")
	if err != nil {
		return "", err
	}

	// Discord returns messages from newest to oldest. Reverse to chronological order (oldest first).
	n := len(messages)
	for i := 0; i < n/2; i++ {
		messages[i], messages[n-1-i] = messages[n-1-i], messages[i]
	}

	var builder strings.Builder
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if excludeMessageID != "" && msg.ID == excludeMessageID {
			continue
		}

		authorName := "Unknown"
		if msg.Author != nil {
			authorName = msg.Author.Username
		}

		content := strings.TrimSpace(msg.Content)
		if content == "" {
			if len(msg.Attachments) > 0 || len(msg.Embeds) > 0 {
				content = "[adjunto sin texto]"
			} else {
				// Empty message with no text, attachments, or embeds (e.g. system messages)
				continue
			}
		}

		runes := []rune(content)
		if len(runes) > 300 {
			content = string(runes[:300]) + "..."
		}

		builder.WriteString(fmt.Sprintf("%s: %s\n", authorName, content))
	}

	return builder.String(), nil
}
