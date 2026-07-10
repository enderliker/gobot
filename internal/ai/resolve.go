package ai

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

func normalizeRoleReference(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "<@&") && strings.HasSuffix(s, ">") {
		s = strings.TrimPrefix(s, "<@&")
		s = strings.TrimSuffix(s, ">")
	}
	return strings.TrimSpace(s)
}

func normalizeChannelReference(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "<#") && strings.HasSuffix(s, ">") {
		s = strings.TrimPrefix(s, "<#")
		s = strings.TrimSuffix(s, ">")
	}
	return strings.TrimSpace(s)
}

// ResolveRole searches for a role by mention, ID, or name (case-insensitive) in the guild.
func ResolveRole(session *discordgo.Session, guildID, query string) (*discordgo.Role, error) {
	query = normalizeRoleReference(query)
	if query == "" {
		return nil, fmt.Errorf("role query is empty")
	}

	var roles []*discordgo.Role
	guild, err := session.State.Guild(guildID)
	if err == nil && guild != nil && len(guild.Roles) > 0 {
		roles = guild.Roles
	} else {
		roles, err = session.GuildRoles(guildID)
		if err != nil {
			return nil, fmt.Errorf("unable to retrieve roles: %w", err)
		}
	}

	// 1. Try exact ID match
	if isDiscordID(query) {
		for _, role := range roles {
			if role.ID == query {
				return role, nil
			}
		}
	}

	// 2. Try exact name match (case-insensitive)
	lowerQuery := strings.ToLower(query)
	for _, role := range roles {
		if strings.ToLower(role.Name) == lowerQuery {
			return role, nil
		}
	}

	// 3. Try partial name match
	for _, role := range roles {
		if strings.Contains(strings.ToLower(role.Name), lowerQuery) {
			return role, nil
		}
	}

	return nil, fmt.Errorf("no roles matched %q", query)
}

// ResolveChannel searches for a channel by mention, ID, or name (case-insensitive) in the guild.
func ResolveChannel(session *discordgo.Session, guildID, query string) (*discordgo.Channel, error) {
	query = normalizeChannelReference(query)
	if query == "" {
		return nil, fmt.Errorf("channel query is empty")
	}

	var channels []*discordgo.Channel
	guild, err := session.State.Guild(guildID)
	if err == nil && guild != nil && len(guild.Channels) > 0 {
		channels = guild.Channels
	} else {
		channels, err = session.GuildChannels(guildID)
		if err != nil {
			return nil, fmt.Errorf("unable to retrieve channels: %w", err)
		}
	}

	// 1. Try exact ID match
	if isDiscordID(query) {
		for _, ch := range channels {
			if ch.ID == query {
				return ch, nil
			}
		}
	}

	// 2. Try exact name match (case-insensitive)
	lowerQuery := strings.ToLower(query)
	for _, ch := range channels {
		if strings.ToLower(ch.Name) == lowerQuery {
			return ch, nil
		}
	}

	// 3. Try partial name match
	for _, ch := range channels {
		if strings.Contains(strings.ToLower(ch.Name), lowerQuery) {
			return ch, nil
		}
	}

	return nil, fmt.Errorf("no channels matched %q", query)
}
