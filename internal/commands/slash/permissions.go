package slash

import (
	"gobot/internal/discordperm"

	"github.com/bwmarrin/discordgo"
)

func canManageGuildConfig(session *discordgo.Session, guildID string, member *discordgo.Member) bool {
	return discordperm.HasGuildPermission(session, guildID, member, discordgo.PermissionManageServer)
}

func isGuildOwner(session *discordgo.Session, guildID string, member *discordgo.Member) bool {
	return discordperm.IsGuildOwner(session, guildID, member)
}

func ownerOnlyAccessFailure(s *discordgo.Session, i *discordgo.InteractionCreate, expectedUserID string) (string, bool) {
	if expectedUserID != "" && interactionUserID(i) != expectedUserID {
		return "wrong_actor", true
	}
	if !isGuildOwner(s, i.GuildID, i.Member) {
		return "not_guild_owner", true
	}
	return "", false
}

func ensureGuildConfigComponentAccess(s *discordgo.Session, i *discordgo.InteractionCreate, expectedUserID, actionType string) bool {
	if expectedUserID != "" && interactionUserID(i) != expectedUserID {
		auditInteraction(i, actionType, "denied", map[string]any{
			"reason":            "wrong_actor",
			"expected_actor_id": expectedUserID,
		})
		respondComponentError(s, i, "You cannot use someone else's configuration flow.")
		return false
	}

	if !canManageGuildConfig(s, i.GuildID, i.Member) {
		auditInteraction(i, actionType, "denied", map[string]any{
			"reason": "missing_manage_server",
		})
		respondComponentError(s, i, "You need the Manage Server permission to change this server's AI configuration.")
		return false
	}

	return true
}

func ensureSetKeyComponentAccess(s *discordgo.Session, i *discordgo.InteractionCreate, expectedUserID, actionType string) bool {
	if reason, denied := ownerOnlyAccessFailure(s, i, expectedUserID); denied {
		fields := map[string]any{
			"reason": reason,
		}
		if reason == "wrong_actor" {
			fields["expected_actor_id"] = expectedUserID
		}
		auditInteraction(i, actionType, "denied", fields)
		if reason == "wrong_actor" {
			respondComponentError(s, i, "You cannot use someone else's configuration flow.")
			return false
		}
		respondComponentError(s, i, "Only the server owner can configure the AI provider API key.")
		return false
	}

	return true
}

func ensurePromptComponentAccess(s *discordgo.Session, i *discordgo.InteractionCreate, expectedUserID, actionType string) bool {
	if reason, denied := ownerOnlyAccessFailure(s, i, expectedUserID); denied {
		fields := map[string]any{
			"reason": reason,
		}
		if reason == "wrong_actor" {
			fields["expected_actor_id"] = expectedUserID
		}
		auditInteraction(i, actionType, "denied", fields)
		if reason == "wrong_actor" {
			respondComponentError(s, i, "You cannot use someone else's configuration flow.")
			return false
		}
		respondComponentError(s, i, "Only the server owner can configure the guild system prompt.")
		return false
	}

	return true
}

func respondComponentError(s *discordgo.Session, i *discordgo.InteractionCreate, message string) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: message,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}
