package slash

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gobot/internal/ai"
	"gobot/internal/database"

	"github.com/bwmarrin/discordgo"
)

// ExecuteReadTool executes a read-only tool and returns its generated embed.
func ExecuteReadTool(ctx context.Context, s *discordgo.Session, guildID, defaultChannelID string, call *ai.ToolCall) (*discordgo.MessageEmbed, error) {
	switch call.Tool {
	case "warnings":
		return executeWarningsTool(ctx, s, guildID, call)
	case "role_info":
		return executeRoleInfoTool(s, guildID, call)
	case "server_info":
		return executeServerInfoTool(s, guildID)
	case "channel_info":
		return executeChannelInfoTool(s, guildID, defaultChannelID, call)
	case "role_list":
		return executeRoleListTool(s, guildID)
	case "audit_log":
		return executeAuditLogTool(s, guildID, call)
	case "voice_status":
		return executeVoiceStatusTool(s, guildID)
	case "member_info":
		return executeMemberInfoTool(ctx, s, guildID, call)
	}
	return nil, fmt.Errorf("unknown read tool: %s", call.Tool)
}

// IsReadTool returns true if the tool is read-only.
func IsReadTool(toolName string) bool {
	readOnlyTools := map[string]bool{
		"member_info":  true,
		"warnings":     true,
		"role_info":    true,
		"server_info":  true,
		"channel_info": true,
		"role_list":    true,
		"audit_log":    true,
		"voice_status": true,
		"web_search":   true,
	}
	return readOnlyTools[toolName]
}

func executeWarningsTool(ctx context.Context, s *discordgo.Session, guildID string, call *ai.ToolCall) (*discordgo.MessageEmbed, error) {
	candidates, err := ai.ResolveMembers(ctx, s, guildID, call.User)
	if err != nil || len(candidates) == 0 {
		return nil, fmt.Errorf("no members matched %q", call.User)
	}

	target := candidates[0].Member
	wList, err := database.Default.GetWarnings(guildID, target.User.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch warnings: %w", err)
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

	return &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("Warnings for %s", target.User.Username),
		Description: desc.String(),
		Color:       0xFEE75C, // Yellow
		Thumbnail: &discordgo.MessageEmbedThumbnail{
			URL: target.User.AvatarURL("256"),
		},
	}, nil
}

func executeRoleInfoTool(s *discordgo.Session, guildID string, call *ai.ToolCall) (*discordgo.MessageEmbed, error) {
	role, err := ai.ResolveRole(s, guildID, call.RoleID)
	if err != nil {
		return nil, fmt.Errorf("no roles matched %q", call.RoleID)
	}

	colorHex := fmt.Sprintf("#%06X", role.Color)
	if role.Color == 0 {
		colorHex = "Default/None"
	}

	return &discordgo.MessageEmbed{
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
	}, nil
}

func executeServerInfoTool(s *discordgo.Session, guildID string) (*discordgo.MessageEmbed, error) {
	guild, err := s.State.Guild(guildID)
	if err != nil || guild == nil {
		guild, err = s.Guild(guildID)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve server information: %w", err)
		}
	}

	createdAtTime, err := discordgo.SnowflakeTimestamp(guild.ID)
	createdAt := "Unknown"
	if err == nil {
		createdAt = fmt.Sprintf("<t:%d:F> (<t:%d:R>)", createdAtTime.Unix(), createdAtTime.Unix())
	}

	return &discordgo.MessageEmbed{
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
	}, nil
}

func executeChannelInfoTool(s *discordgo.Session, guildID, defaultChannelID string, call *ai.ToolCall) (*discordgo.MessageEmbed, error) {
	chRef := call.TargetChannel
	if chRef == "" {
		chRef = defaultChannelID
	}

	ch, err := ai.ResolveChannel(s, guildID, chRef)
	if err != nil {
		return nil, fmt.Errorf("no channels matched %q", chRef)
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

	return &discordgo.MessageEmbed{
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
	}, nil
}

func executeRoleListTool(s *discordgo.Session, guildID string) (*discordgo.MessageEmbed, error) {
	var roles []*discordgo.Role
	guild, err := s.State.Guild(guildID)
	if err == nil && guild != nil && len(guild.Roles) > 0 {
		roles = guild.Roles
	} else {
		roles, err = s.GuildRoles(guildID)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve guild roles: %w", err)
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

	return &discordgo.MessageEmbed{
		Title:       "Roles List",
		Description: sb.String(),
		Color:       0x5865F2,
	}, nil
}

func executeAuditLogTool(s *discordgo.Session, guildID string, call *ai.ToolCall) (*discordgo.MessageEmbed, error) {
	count := call.Count
	if count == 0 {
		count = 5
	}
	if count > 20 {
		count = 20
	}

	logData, err := s.GuildAuditLog(guildID, "", "", 0, count)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch audit log: %w", err)
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

	return &discordgo.MessageEmbed{
		Title:       "Recent Audit Log Entries",
		Description: sb.String(),
		Color:       0x5865F2,
	}, nil
}

func executeVoiceStatusTool(s *discordgo.Session, guildID string) (*discordgo.MessageEmbed, error) {
	guild, err := s.State.Guild(guildID)
	if err != nil || guild == nil {
		guild, err = s.Guild(guildID)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve voice status: %w", err)
		}
	}

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
				if m, err := s.GuildMember(guildID, uID); err == nil && m != nil {
					memberName = m.DisplayName()
				}
				sb.WriteString(fmt.Sprintf("  • <@%s> (%s)\n", uID, memberName))
			}
			sb.WriteString("\n")
		}
	}

	return &discordgo.MessageEmbed{
		Title:       "Server Voice Status",
		Description: sb.String(),
		Color:       0x57F287, // Green
	}, nil
}

func executeMemberInfoTool(ctx context.Context, s *discordgo.Session, guildID string, call *ai.ToolCall) (*discordgo.MessageEmbed, error) {
	candidates, err := ai.ResolveMembers(ctx, s, guildID, call.User)
	if err != nil || len(candidates) == 0 {
		return nil, fmt.Errorf("no members matched %q", call.User)
	}

	candidate := candidates[0]
	if candidate.Member == nil || candidate.Member.User == nil {
		return nil, fmt.Errorf("failed to resolve member details")
	}

	m := candidate.Member
	u := m.User

	rolesDesc := "No roles"
	if len(m.Roles) > 0 {
		roleMentions := make([]string, len(m.Roles))
		for idx, rID := range m.Roles {
			roleMentions[idx] = "<@&" + rID + ">"
		}
		rolesDesc = strings.Join(roleMentions, ", ")
	}

	joinedAt := "Unknown"
	if !m.JoinedAt.IsZero() {
		joinedAt = fmt.Sprintf("<t:%d:F> (<t:%d:R>)", m.JoinedAt.Unix(), m.JoinedAt.Unix())
	}

	createdAtTime, err := discordgo.SnowflakeTimestamp(u.ID)
	createdAt := "Unknown"
	if err == nil {
		createdAt = fmt.Sprintf("<t:%d:F> (<t:%d:R>)", createdAtTime.Unix(), createdAtTime.Unix())
	}

	return &discordgo.MessageEmbed{
		Title:       "Member Information",
		Description: fmt.Sprintf("Information for %s", m.Mention()),
		Color:       0x5865F2,
		Thumbnail: &discordgo.MessageEmbedThumbnail{
			URL: u.AvatarURL("256"),
		},
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "Username",
				Value:  fmt.Sprintf("%s (%s)", u.Username, u.ID),
				Inline: true,
			},
			{
				Name:   "Nickname / Display Name",
				Value:  m.DisplayName(),
				Inline: true,
			},
			{
				Name:   "Is Bot?",
				Value:  strconv.FormatBool(u.Bot),
				Inline: true,
			},
			{
				Name:   "Joined Server At",
				Value:  joinedAt,
				Inline: false,
			},
			{
				Name:   "Created Account At",
				Value:  createdAt,
				Inline: false,
			},
			{
				Name:   "Roles",
				Value:  rolesDesc,
				Inline: false,
			},
		},
	}, nil
}
