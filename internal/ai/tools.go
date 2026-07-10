package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"gobot/internal/discordperm"

	"github.com/bwmarrin/discordgo"
)

type ToolCall struct {
	Tool         string `json:"tool"`
	User         string `json:"user"`
	Reason       string `json:"reason"`
	Minutes      int    `json:"minutes"`
	Count        int    `json:"count"`
	Confirmation string `json:"confirmation"`
}

const (
	maxAuditReasonLen   = 512
	maxTimeoutMinutes   = 28 * 24 * 60
	maxUserReferenceLen = 100
)

const (
	toolErrTargetUserNotResolved               = "target user is not resolved"
	toolErrTargetMemberNotFound                = "target member not found"
	toolErrTargetMemberLookupFailed            = "unable to retrieve the target member"
	toolErrBotMemberNotFound                   = "bot member not found in this server"
	toolErrBotMemberLookupFailed               = "unable to verify the bot's server membership"
	toolErrRoleHierarchyPreventsAction         = "role hierarchy prevents this action"
	toolErrBotRoleHierarchyPreventsAction      = "bot role hierarchy prevents this action"
	toolErrMissingBanMembersPermission         = "missing Ban Members permission"
	toolErrBotMissingBanMembersPermission      = "bot is missing Ban Members permission"
	toolErrMissingKickMembersPermission        = "missing Kick Members permission"
	toolErrBotMissingKickMembersPermission     = "bot is missing Kick Members permission"
	toolErrMissingModerateMembersPermission    = "missing Moderate Members permission"
	toolErrBotMissingModerateMembersPermission = "bot is missing Moderate Members permission"
	toolErrDiscordMissingPermissions           = "Discord denied the moderation action due to missing permissions."
	toolErrDiscordActionRejected               = "Discord rejected the moderation action. Please verify the member state and bot permissions."
	toolErrUnknownTool                         = "unknown tool"
)

var moderationTools = []ToolDefinition{
	{
		Name:        "ban",
		Description: "Ban a guild member when the user explicitly requests a ban.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user": map[string]any{
					"type":        "string",
					"description": "Target member reference. Can be an ID, mention, username, nickname, display name, initials, or close textual match.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Short moderation reason suitable for the Discord audit log.",
				},
			},
			"required":             []string{"user", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "kick",
		Description: "Kick a guild member when the user explicitly requests a kick.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user": map[string]any{
					"type":        "string",
					"description": "Target member reference. Can be an ID, mention, username, nickname, display name, initials, or close textual match.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Short moderation reason suitable for the Discord audit log.",
				},
			},
			"required":             []string{"user", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "timeout",
		Description: "Temporarily timeout a guild member when the user explicitly requests a timeout or mute.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user": map[string]any{
					"type":        "string",
					"description": "Target member reference. Can be an ID, mention, username, nickname, display name, initials, or close textual match.",
				},
				"minutes": map[string]any{
					"type":        "integer",
					"description": "Timeout duration in minutes. Must be between 1 and 40320.",
					"minimum":     1,
					"maximum":     maxTimeoutMinutes,
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Short moderation reason suitable for the Discord audit log.",
				},
			},
			"required":             []string{"user", "minutes", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "unban",
		Description: "Unban a user when the user explicitly requests an unban.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user": map[string]any{
					"type":        "string",
					"description": "Target user reference. Can be a raw Discord user ID or close textual match.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Short reason for the audit log.",
				},
			},
			"required":             []string{"user", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "untimeout",
		Description: "Remove timeout/mute from a guild member when the user explicitly requests to remove their timeout or unmute them.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user": map[string]any{
					"type":        "string",
					"description": "Target member reference. Can be an ID, mention, username, nickname, display name, initials, or close textual match.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Short moderation reason suitable for the Discord audit log.",
				},
			},
			"required":             []string{"user", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "purge",
		Description: "Bulk delete messages from the current channel when the user explicitly requests to clear, purge, or delete messages.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"count": map[string]any{
					"type":        "integer",
					"description": "Number of messages to delete. Must be between 1 and 100.",
					"minimum":     1,
					"maximum":     100,
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Short moderation reason suitable for the Discord audit log.",
				},
			},
			"required":             []string{"count", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "member_info",
		Description: "Retrieve detailed information about a server member (join date, account creation date, roles, etc.) when the user asks for user/member information.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user": map[string]any{
					"type":        "string",
					"description": "Target member reference. Can be an ID, mention, username, nickname, display name, initials, or close textual match.",
				},
			},
			"required":             []string{"user"},
			"additionalProperties": false,
		},
	},
}

const moderationToolPrompt = "You are a Discord server assistant. Use the provided moderation tools when the user is asking to ban, kick, timeout, mute, or otherwise moderate a member. The tool input field 'user' may contain an ID, mention, username, nickname, display name, initials, or other close textual reference from the request. If no moderation tool is required, answer normally."

func ModerationTools() []ToolDefinition {
	out := make([]ToolDefinition, len(moderationTools))
	copy(out, moderationTools)
	return out
}

func ModerationToolsForMember(session *discordgo.Session, guildID string, member *discordgo.Member) []ToolDefinition {
	if session == nil || guildID == "" || member == nil || member.User == nil {
		return nil
	}

	tools := make([]ToolDefinition, 0, len(moderationTools))
	if hasPermission(session, guildID, member, discordgo.PermissionBanMembers) {
		tools = append(tools, moderationToolNamed("ban"))
		tools = append(tools, moderationToolNamed("unban"))
	}
	if hasPermission(session, guildID, member, discordgo.PermissionKickMembers) {
		tools = append(tools, moderationToolNamed("kick"))
	}
	if hasPermission(session, guildID, member, discordgo.PermissionModerateMembers) {
		tools = append(tools, moderationToolNamed("timeout"))
		tools = append(tools, moderationToolNamed("untimeout"))
	}
	if hasPermission(session, guildID, member, discordgo.PermissionManageMessages) {
		tools = append(tools, moderationToolNamed("purge"))
	}
	// member_info does not require moderation privileges as it's read-only
	tools = append(tools, moderationToolNamed("member_info"))

	if len(tools) == 0 {
		return nil
	}
	return tools
}

func moderationToolNamed(name string) ToolDefinition {
	for _, tool := range moderationTools {
		if tool.Name == name {
			return tool
		}
	}
	return ToolDefinition{}
}

func parseToolCallCandidate(s string) (*ToolCall, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()

	var t ToolCall
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("not a tool call")
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("not a tool call")
	}

	t.Tool = strings.TrimSpace(strings.ToLower(t.Tool))
	t.User = normalizeUserReference(t.User)
	t.Reason = strings.TrimSpace(t.Reason)

	if err := validateToolCall(&t); err != nil {
		return nil, fmt.Errorf("not a tool call")
	}

	t.Confirmation = ConfirmationText(&t, "")
	return &t, nil
}

func ParseToolArguments(toolName string, input map[string]any) (*ToolCall, error) {
	payload := make(map[string]any, len(input)+1)
	payload["tool"] = toolName
	for key, value := range input {
		payload[key] = value
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("not a tool call")
	}
	return parseToolCallCandidate(string(body))
}

func ParseToolArgumentsJSON(toolName, args string) (*ToolCall, error) {
	var input map[string]any
	if err := json.Unmarshal([]byte(args), &input); err != nil {
		return nil, fmt.Errorf("not a tool call")
	}
	return ParseToolArguments(toolName, input)
}

func ExecuteTool(session *discordgo.Session, guildID, channelID string, actor *discordgo.Member, call *ToolCall) error {
	if call.Tool != "purge" && !isDiscordID(call.User) {
		return fmt.Errorf(toolErrTargetUserNotResolved)
	}

	botMember, err := session.GuildMember(guildID, session.State.User.ID)
	if err != nil {
		if isDiscordRESTErrorCode(err, discordgo.ErrCodeUnknownMember) {
			return fmt.Errorf(toolErrBotMemberNotFound)
		}
		return fmt.Errorf(toolErrBotMemberLookupFailed)
	}

	if call.Tool != "unban" && call.Tool != "purge" && call.Tool != "member_info" {
		target, err := session.GuildMember(guildID, call.User)
		if err != nil {
			if isDiscordRESTErrorCode(err, discordgo.ErrCodeUnknownMember) {
				return fmt.Errorf(toolErrTargetMemberNotFound)
			}
			return fmt.Errorf(toolErrTargetMemberLookupFailed)
		}

		if !memberAbove(session, guildID, actor, target) {
			return fmt.Errorf(toolErrRoleHierarchyPreventsAction)
		}
		if !memberAbove(session, guildID, botMember, target) {
			return fmt.Errorf(toolErrBotRoleHierarchyPreventsAction)
		}
	}

	switch call.Tool {
	case "ban":
		if !hasPermission(session, guildID, actor, discordgo.PermissionBanMembers) {
			return fmt.Errorf(toolErrMissingBanMembersPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionBanMembers) {
			return fmt.Errorf(toolErrBotMissingBanMembersPermission)
		}
		return normalizeDiscordToolActionError(session.GuildBanCreateWithReason(guildID, call.User, call.Reason, 0))
	case "unban":
		if !hasPermission(session, guildID, actor, discordgo.PermissionBanMembers) {
			return fmt.Errorf(toolErrMissingBanMembersPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionBanMembers) {
			return fmt.Errorf(toolErrBotMissingBanMembersPermission)
		}
		return normalizeDiscordToolActionError(session.GuildBanDelete(guildID, call.User))
	case "kick":
		if !hasPermission(session, guildID, actor, discordgo.PermissionKickMembers) {
			return fmt.Errorf(toolErrMissingKickMembersPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionKickMembers) {
			return fmt.Errorf(toolErrBotMissingKickMembersPermission)
		}
		return normalizeDiscordToolActionError(session.GuildMemberDeleteWithReason(guildID, call.User, call.Reason))
	case "timeout":
		if !hasPermission(session, guildID, actor, discordgo.PermissionModerateMembers) {
			return fmt.Errorf(toolErrMissingModerateMembersPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionModerateMembers) {
			return fmt.Errorf(toolErrBotMissingModerateMembersPermission)
		}
		until := time.Now().Add(time.Duration(call.Minutes) * time.Minute)
		return normalizeDiscordToolActionError(session.GuildMemberTimeout(guildID, call.User, &until))
	case "untimeout":
		if !hasPermission(session, guildID, actor, discordgo.PermissionModerateMembers) {
			return fmt.Errorf(toolErrMissingModerateMembersPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionModerateMembers) {
			return fmt.Errorf(toolErrBotMissingModerateMembersPermission)
		}
		return normalizeDiscordToolActionError(session.GuildMemberTimeout(guildID, call.User, nil))
	case "purge":
		if !hasPermission(session, guildID, actor, discordgo.PermissionManageMessages) {
			return fmt.Errorf("missing Manage Messages permission")
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionManageMessages) {
			return fmt.Errorf("bot is missing Manage Messages permission")
		}
		messages, err := session.ChannelMessages(channelID, call.Count, "", "", "")
		if err != nil {
			return normalizeDiscordToolActionError(err)
		}
		if len(messages) == 0 {
			return nil
		}
		messageIDs := make([]string, 0, len(messages))
		for _, msg := range messages {
			messageIDs = append(messageIDs, msg.ID)
		}
		return normalizeDiscordToolActionError(session.ChannelMessagesBulkDelete(channelID, messageIDs))
	case "member_info":
		// Read-only tool, actual output formatting is handled in ask.go directly,
		// this execution is just a placeholder to prevent 'unknown tool' errors.
		return nil
	default:
		return fmt.Errorf(toolErrUnknownTool)
	}
}

func UserFacingToolExecutionError(err error) string {
	if err == nil {
		return "Unknown error"
	}

	msg := err.Error()
	if isToolExecutionBusinessMessage(msg) {
		return msg
	}

	var restErr *discordgo.RESTError
	if errors.As(err, &restErr) && restErr != nil && restErr.Message != nil {
		switch restErr.Message.Code {
		case discordgo.ErrCodeUnknownMember:
			return toolErrTargetMemberNotFound
		case discordgo.ErrCodeMissingPermissions:
			return toolErrDiscordMissingPermissions
		}
	}

	return toolErrDiscordActionRejected
}

func isToolExecutionBusinessMessage(msg string) bool {
	switch msg {
	case toolErrTargetUserNotResolved,
		toolErrTargetMemberNotFound,
		toolErrTargetMemberLookupFailed,
		toolErrBotMemberNotFound,
		toolErrBotMemberLookupFailed,
		toolErrRoleHierarchyPreventsAction,
		toolErrBotRoleHierarchyPreventsAction,
		toolErrMissingBanMembersPermission,
		toolErrBotMissingBanMembersPermission,
		toolErrMissingKickMembersPermission,
		toolErrBotMissingKickMembersPermission,
		toolErrMissingModerateMembersPermission,
		toolErrBotMissingModerateMembersPermission,
		toolErrDiscordMissingPermissions,
		toolErrDiscordActionRejected,
		toolErrUnknownTool:
		return true
	default:
		return false
	}
}

func normalizeDiscordToolActionError(err error) error {
	if err == nil {
		return nil
	}
	if isDiscordRESTErrorCode(err, discordgo.ErrCodeUnknownMember) {
		return fmt.Errorf(toolErrTargetMemberNotFound)
	}
	if isDiscordRESTErrorCode(err, discordgo.ErrCodeMissingPermissions) {
		return fmt.Errorf(toolErrDiscordMissingPermissions)
	}
	return fmt.Errorf(toolErrDiscordActionRejected)
}

func isDiscordRESTErrorCode(err error, code int) bool {
	var restErr *discordgo.RESTError
	if !errors.As(err, &restErr) || restErr == nil || restErr.Message == nil {
		return false
	}
	return restErr.Message.Code == code
}

func validateToolCall(call *ToolCall) error {
	switch call.Tool {
	case "ban", "unban", "kick", "untimeout", "member_info":
	case "timeout":
		if call.Minutes < 1 || call.Minutes > maxTimeoutMinutes {
			return fmt.Errorf("invalid timeout duration")
		}
	case "purge":
		if call.Count < 1 || call.Count > 100 {
			return fmt.Errorf("invalid purge count")
		}
	default:
		return fmt.Errorf(toolErrUnknownTool)
	}

	if call.Tool == "purge" {
		if call.Reason == "" {
			return fmt.Errorf("missing moderation reason")
		}
		if len(call.Reason) > maxAuditReasonLen {
			return fmt.Errorf("reason too long")
		}
		return nil
	}

	if call.User == "" || len(call.User) > maxUserReferenceLen {
		return fmt.Errorf("invalid target user")
	}

	if call.Tool != "member_info" {
		if call.Reason == "" {
			return fmt.Errorf("missing moderation reason")
		}
		if len(call.Reason) > maxAuditReasonLen {
			return fmt.Errorf("reason too long")
		}
	}

	return nil
}

func ConfirmationText(call *ToolCall, target string) string {
	if strings.TrimSpace(target) == "" {
		target = formatToolTarget(call.User)
	}

	reason := sanitizeInlineCode(call.Reason)
	switch call.Tool {
	case "ban":
		return fmt.Sprintf("Ban %s for `%s`?", target, reason)
	case "unban":
		return fmt.Sprintf("Unban %s for `%s`?", target, reason)
	case "kick":
		return fmt.Sprintf("Kick %s for `%s`?", target, reason)
	case "timeout":
		return fmt.Sprintf("Timeout %s for `%d` minute(s) for `%s`?", target, call.Minutes, reason)
	case "untimeout":
		return fmt.Sprintf("Remove timeout/mute from %s for `%s`?", target, reason)
	case "purge":
		return fmt.Sprintf("Purge %d message(s) from this channel for `%s`?", call.Count, reason)
	case "member_info":
		return fmt.Sprintf("Get member information for %s?", target)
	default:
		return "Confirm this moderation action?"
	}
}

func formatToolTarget(value string) string {
	if isDiscordID(value) {
		return "<@" + value + ">"
	}
	return "`" + sanitizeInlineCode(value) + "`"
}

func sanitizeInlineCode(s string) string {
	return strings.ReplaceAll(s, "`", "'")
}

func normalizeUserReference(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "<@") && strings.HasSuffix(s, ">") {
		s = strings.TrimPrefix(s, "<@")
		s = strings.TrimPrefix(s, "!")
		s = strings.TrimSuffix(s, ">")
	}
	return strings.TrimSpace(s)
}

func isDiscordID(s string) bool {
	if len(s) < 17 || len(s) > 20 {
		return false
	}

	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}

func hasPermission(session *discordgo.Session, guildID string, member *discordgo.Member, permission int64) bool {
	return discordperm.HasGuildPermission(session, guildID, member, permission)
}

func memberAbove(session *discordgo.Session, guildID string, actor, target *discordgo.Member) bool {
	guild, err := session.State.Guild(guildID)
	if err != nil || guild == nil {
		guild, err = session.Guild(guildID)
		if err != nil {
			return false
		}
	}

	if guild.OwnerID == target.User.ID {
		return false
	}

	return highestRolePosition(guild.Roles, actor.Roles) > highestRolePosition(guild.Roles, target.Roles)
}

func highestRolePosition(guildRoles []*discordgo.Role, memberRoles []string) int {
	positions := make([]int, 0, len(memberRoles))
	for _, memberRole := range memberRoles {
		for _, role := range guildRoles {
			if role.ID == memberRole {
				positions = append(positions, role.Position)
				break
			}
		}
	}

	if len(positions) == 0 {
		return 0
	}

	sort.Ints(positions)
	return positions[len(positions)-1]
}
