package slash

import (
	"fmt"
	"strconv"
	"strings"

	"gobot/internal/ai"
	"gobot/internal/database"
	"gobot/internal/embeds"

	"github.com/bwmarrin/discordgo"
)

func handleReadTool(s *discordgo.Session, i *discordgo.InteractionCreate, call *ai.ToolCall) bool {
	switch call.Tool {
	case "warnings":
		return handleWarningsTool(s, i, call)
	case "role_info":
		return handleRoleInfoTool(s, i, call)
	case "server_info":
		return handleServerInfoTool(s, i)
	case "channel_info":
		return handleChannelInfoTool(s, i, call)
	case "role_list":
		return handleRoleListTool(s, i)
	case "audit_log":
		return handleAuditLogTool(s, i, call)
	case "voice_status":
		return handleVoiceStatusTool(s, i)
	}
	return false
}

func handleWarningsTool(s *discordgo.Session, i *discordgo.InteractionCreate, call *ai.ToolCall) bool {
	candidates, err := ai.ResolveMembers(s, i.GuildID, call.User)
	if err != nil || len(candidates) == 0 {
		embed := embeds.Error("Member Not Found", fmt.Sprintf("No members matched %q", call.User))
		_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
		return true
	}

	target := candidates[0].Member
	wList, err := database.Default.GetWarnings(i.GuildID, target.User.ID)
	if err != nil {
		embed := embeds.Error("Database Error", fmt.Sprintf("Failed to fetch warnings: %v", err))
		_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
		return true
	}

	var desc strings.Builder
	if len(wList) == 0 {
		desc.WriteString("This member has no active warnings.")
	} else {
		desc.WriteString(fmt.Sprintf("Total warnings: **%d**\n\n", len(wList)))
		for idx, w := range wList {
			desc.WriteString(fmt.Sprintf("**%d. Warned by <@%s>** on %s\nReason: `%s`\n\n", idx+1, w.ActorID, w.CreatedAt, w.Reason))
		}
	}

	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("Warnings for %s", target.User.Username),
		Description: desc.String(),
		Color:       0xFEE75C, // Yellow
		Thumbnail: &discordgo.MessageEmbedThumbnail{
			URL: target.User.AvatarURL("256"),
		},
	}

	_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
	return true
}

func handleRoleInfoTool(s *discordgo.Session, i *discordgo.InteractionCreate, call *ai.ToolCall) bool {
	role, err := ai.ResolveRole(s, i.GuildID, call.RoleID)
	if err != nil {
		embed := embeds.Error("Role Not Found", fmt.Sprintf("No roles matched %q", call.RoleID))
		_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
		return true
	}

	colorHex := fmt.Sprintf("#%06X", role.Color)
	if role.Color == 0 {
		colorHex = "Default/None"
	}

	embed := &discordgo.MessageEmbed{
		Title:       "Role Information",
		Description: fmt.Sprintf("Details for role %s", role.Mention()),
		Color:       role.Color,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Name", Value: role.Name, Inline: true},
			{Name: "ID", Value: role.ID, Inline: true},
			{Name: "Position", Value: strconv.Itoa(role.Position), Inline: true},
			{Name: "Color Hex", Value: colorHex, Inline: true},
			{Name: "Hoist (Separated)", Value: strconv.FormatBool(role.Hoist), Inline: true},
			{Name: "Mentionable", Value: strconv.FormatBool(role.Mentionable), Inline: true},
			{Name: "Permissions Bitmask", Value: strconv.FormatInt(role.Permissions, 10), Inline: false},
		},
	}

	_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
	return true
}

func handleServerInfoTool(s *discordgo.Session, i *discordgo.InteractionCreate) bool {
	guild, err := s.State.Guild(i.GuildID)
	if err != nil || guild == nil {
		guild, err = s.Guild(i.GuildID)
		if err != nil {
			embed := embeds.Error("Error", "Failed to retrieve server information.")
			_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
			return true
		}
	}

	createdAtTime, err := discordgo.SnowflakeTimestamp(guild.ID)
	createdAt := "Unknown"
	if err == nil {
		createdAt = fmt.Sprintf("<t:%d:F> (<t:%d:R>)", createdAtTime.Unix(), createdAtTime.Unix())
	}

	embed := &discordgo.MessageEmbed{
		Title: guild.Name,
		Color: 0x5865F2,
		Thumbnail: &discordgo.MessageEmbedThumbnail{
			URL: guild.IconURL("256"),
		},
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Server ID", Value: guild.ID, Inline: true},
			{Name: "Owner", Value: fmt.Sprintf("<@%s>", guild.OwnerID), Inline: true},
			{Name: "Members", Value: strconv.Itoa(guild.MemberCount), Inline: true},
			{Name: "Roles Count", Value: strconv.Itoa(len(guild.Roles)), Inline: true},
			{Name: "Channels Count", Value: strconv.Itoa(len(guild.Channels)), Inline: true},
			{Name: "Boost Tier", Value: fmt.Sprintf("Tier %d (%d boosts)", guild.PremiumTier, guild.PremiumSubscriptionCount), Inline: true},
			{Name: "Created At", Value: createdAt, Inline: false},
		},
	}

	_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
	return true
}

func handleChannelInfoTool(s *discordgo.Session, i *discordgo.InteractionCreate, call *ai.ToolCall) bool {
	chRef := call.TargetChannel
	if chRef == "" {
		chRef = i.ChannelID
	}

	ch, err := ai.ResolveChannel(s, i.GuildID, chRef)
	if err != nil {
		embed := embeds.Error("Channel Not Found", fmt.Sprintf("No channels matched %q", chRef))
		_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
		return true
	}

	chTypes := map[discordgo.ChannelType]string{
		discordgo.ChannelTypeGuildText:          "Text Channel",
		discordgo.ChannelTypeGuildVoice:         "Voice Channel",
		discordgo.ChannelTypeGuildCategory:      "Category",
		discordgo.ChannelTypeGuildNews:          "Announcement Channel",
		discordgo.ChannelTypeGuildStore:         "Store Channel",
		discordgo.ChannelTypeGuildNewsThread:    "News Thread",
		discordgo.ChannelTypeGuildPublicThread:  "Public Thread",
		discordgo.ChannelTypeGuildPrivateThread: "Private Thread",
		discordgo.ChannelTypeGuildStageVoice:    "Stage Voice Channel",
		discordgo.ChannelTypeGuildForum:         "Forum Channel",
	}

	chTypeStr, ok := chTypes[ch.Type]
	if !ok {
		chTypeStr = "Unknown Type"
	}

	topic := ch.Topic
	if topic == "" {
		topic = "No topic set."
	}

	categoryName := "None / Root"
	if ch.ParentID != "" {
		if cat, err := s.Channel(ch.ParentID); err == nil && cat != nil {
			categoryName = fmt.Sprintf("%s (%s)", cat.Name, cat.ID)
		} else {
			categoryName = ch.ParentID
		}
	}

	embed := &discordgo.MessageEmbed{
		Title:       "Channel Information",
		Description: fmt.Sprintf("Details for channel %s", ch.Mention()),
		Color:       0x5865F2,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Name", Value: ch.Name, Inline: true},
			{Name: "ID", Value: ch.ID, Inline: true},
			{Name: "Type", Value: chTypeStr, Inline: true},
			{Name: "Category", Value: categoryName, Inline: true},
			{Name: "Slowmode (Seconds)", Value: strconv.Itoa(ch.RateLimitPerUser), Inline: true},
			{Name: "Topic", Value: topic, Inline: false},
		},
	}

	_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
	return true
}

func handleRoleListTool(s *discordgo.Session, i *discordgo.InteractionCreate) bool {
	var roles []*discordgo.Role
	guild, err := s.State.Guild(i.GuildID)
	if err == nil && guild != nil && len(guild.Roles) > 0 {
		roles = guild.Roles
	} else {
		roles, err = s.GuildRoles(i.GuildID)
		if err != nil {
			embed := embeds.Error("Error", "Failed to retrieve guild roles.")
			_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
			return true
		}
	}

	var sb strings.Builder
	for idx, role := range roles {
		sb.WriteString(fmt.Sprintf("• %s (ID: `%s`, Pos: `%d`)\n", role.Mention(), role.ID, role.Position))
		if idx >= 30 {
			sb.WriteString(fmt.Sprintf("...and %d more roles.", len(roles)-30))
			break
		}
	}

	embed := &discordgo.MessageEmbed{
		Title:       "Roles List",
		Description: sb.String(),
		Color:       0x5865F2,
	}

	_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
	return true
}

func handleAuditLogTool(s *discordgo.Session, i *discordgo.InteractionCreate, call *ai.ToolCall) bool {
	count := call.Count
	if count == 0 {
		count = 5
	}
	if count > 20 {
		count = 20
	}

	logData, err := s.GuildAuditLog(i.GuildID, "", "", 0, count)
	if err != nil {
		embed := embeds.Error("Permission Denied", fmt.Sprintf("Failed to fetch audit log: %v", err))
		_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
		return true
	}

	var sb strings.Builder
	if len(logData.AuditLogEntries) == 0 {
		sb.WriteString("No recent audit log entries found.")
	} else {
		for idx, entry := range logData.AuditLogEntries {
			actorName := entry.UserID
			for _, user := range logData.Users {
				if user.ID == entry.UserID {
					actorName = user.Username
					break
				}
			}
			targetName := entry.TargetID
			if targetName == "" {
				targetName = "N/A"
			}

			actionTypes := map[discordgo.AuditLogAction]string{
				discordgo.AuditLogActionMemberKick:        "Kicked Member",
				discordgo.AuditLogActionMemberBanAdd:      "Banned Member",
				discordgo.AuditLogActionMemberBanRemove:   "Unbanned Member",
				discordgo.AuditLogActionMemberUpdate:      "Updated Member",
				discordgo.AuditLogActionMemberRoleUpdate:  "Updated Member Roles",
				discordgo.AuditLogActionChannelCreate:     "Created Channel",
				discordgo.AuditLogActionChannelUpdate:     "Updated Channel",
				discordgo.AuditLogActionChannelDelete:     "Deleted Channel",
				discordgo.AuditLogActionRoleCreate:        "Created Role",
				discordgo.AuditLogActionRoleUpdate:        "Updated Role",
				discordgo.AuditLogActionRoleDelete:        "Deleted Role",
				discordgo.AuditLogActionMessagePin:        "Pinned Message",
				discordgo.AuditLogActionMessageUnpin:      "Unpinned Message",
				discordgo.AuditLogActionMemberDisconnect:  "Voice Disconnect",
				discordgo.AuditLogActionMemberMove:        "Voice Move",
				discordgo.AuditLogActionMessageBulkDelete: "Bulk Delete Messages",
			}

			var actionType discordgo.AuditLogAction
			if entry.ActionType != nil {
				actionType = *entry.ActionType
			}

			actionTypeStr, ok := actionTypes[actionType]
			if !ok {
				actionTypeStr = fmt.Sprintf("Action ID %d", actionType)
			}

			reason := entry.Reason
			if reason == "" {
				reason = "No reason provided."
			}

			sb.WriteString(fmt.Sprintf("**%d. %s** by **%s** on target `%s`\nReason: `%s`\n\n", idx+1, actionTypeStr, actorName, targetName, reason))
		}
	}

	embed := &discordgo.MessageEmbed{
		Title:       "Recent Audit Log Entries",
		Description: sb.String(),
		Color:       0x5865F2,
	}

	_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
	return true
}

func handleVoiceStatusTool(s *discordgo.Session, i *discordgo.InteractionCreate) bool {
	guild, err := s.State.Guild(i.GuildID)
	if err != nil || guild == nil {
		guild, err = s.Guild(i.GuildID)
		if err != nil {
			embed := embeds.Error("Error", "Failed to retrieve voice status.")
			_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
			return true
		}
	}

	// Map to keep track of channels and their connected members
	voiceChannels := make(map[string][]string)
	for _, vs := range guild.VoiceStates {
		voiceChannels[vs.ChannelID] = append(voiceChannels[vs.ChannelID], vs.UserID)
	}

	var sb strings.Builder
	if len(guild.VoiceStates) == 0 {
		sb.WriteString("No members are currently in voice channels.")
	} else {
		for chID, userIDs := range voiceChannels {
			chName := chID
			if ch, err := s.Channel(chID); err == nil && ch != nil {
				chName = ch.Name
			}
			sb.WriteString(fmt.Sprintf("**🔈 %s (%s)**\n", chName, chID))
			for _, uID := range userIDs {
				memberName := uID
				if m, err := s.GuildMember(i.GuildID, uID); err == nil && m != nil {
					memberName = m.DisplayName()
				}
				sb.WriteString(fmt.Sprintf("  • <@%s> (%s)\n", uID, memberName))
			}
			sb.WriteString("\n")
		}
	}

	embed := &discordgo.MessageEmbed{
		Title:       "Server Voice Status",
		Description: sb.String(),
		Color:       0x57F287, // Green
	}

	_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
	return true
}
