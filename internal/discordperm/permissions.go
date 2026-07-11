package discordperm

import "github.com/bwmarrin/discordgo"

// IsGuildOwner returns true only when the member is the guild owner resolved
// from the current guild state/API. It does not fall back to Administrator or
// any permission bit.
func IsGuildOwner(session *discordgo.Session, guildID string, member *discordgo.Member) bool {
	if session == nil || guildID == "" || member == nil || member.User == nil {
		return false
	}

	guild, err := session.State.Guild(guildID)
	if err != nil || guild == nil {
		guild, err = session.Guild(guildID)
		if err != nil || guild == nil {
			return false
		}
	}

	return guild.OwnerID == member.User.ID
}

// HasGuildPermission evaluates guild-level permissions using the current member
// roles, treating guild owners and administrators as implicitly allowed.
func HasGuildPermission(session *discordgo.Session, guildID string, member *discordgo.Member, permission int64) bool {
	if session == nil || guildID == "" || member == nil || member.User == nil {
		return false
	}

	if IsGuildOwner(session, guildID, member) {
		return true
	}

	guild, err := session.State.Guild(guildID)
	if err != nil || guild == nil {
		guild, err = session.Guild(guildID)
		if err != nil || guild == nil {
			return false
		}
	}

	var perms int64
	for _, role := range guild.Roles {
		if role.ID == guildID {
			perms |= role.Permissions
			break
		}
	}

	for _, roleID := range member.Roles {
		for _, role := range guild.Roles {
			if role.ID == roleID {
				perms |= role.Permissions
				break
			}
		}
	}

	if perms&discordgo.PermissionAdministrator != 0 {
		return true
	}

	return perms&permission != 0
}
