package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"gobot/internal/database"
	"gobot/internal/discordperm"

	"github.com/bwmarrin/discordgo"
)

type ToolCall struct {
	Tool          string `json:"tool"`
	User          string `json:"user"`
	RoleID        string `json:"role_id"`
	TargetChannel string `json:"target_channel"`
	MessageID     string `json:"message_id"`
	Text          string `json:"text"`
	Query         string `json:"query"`
	Color         string `json:"color"`
	Duration      int    `json:"duration"`
	Reason        string `json:"reason"`
	Minutes       int    `json:"minutes"`
	Count         int    `json:"count"`
	Confirmation  string `json:"confirmation"`
}

const (
	maxAuditReasonLen   = 512
	maxTimeoutMinutes   = 28 * 24 * 60
	maxUserReferenceLen = 100
)

const (
	toolErrTargetUserNotResolved                  = "target user is not resolved"
	toolErrTargetMemberNotFound                   = "target member not found"
	toolErrTargetMemberLookupFailed               = "unable to retrieve the target member"
	toolErrBotMemberNotFound                      = "bot member not found in this server"
	toolErrBotMemberLookupFailed                  = "unable to verify the bot's server membership"
	toolErrRoleHierarchyPreventsAction            = "role hierarchy prevents this action"
	toolErrBotRoleHierarchyPreventsAction         = "bot role hierarchy prevents this action"
	toolErrMissingBanMembersPermission            = "missing Ban Members permission"
	toolErrBotMissingBanMembersPermission         = "bot is missing Ban Members permission"
	toolErrMissingKickMembersPermission           = "missing Kick Members permission"
	toolErrBotMissingKickMembersPermission        = "bot is missing Kick Members permission"
	toolErrMissingModerateMembersPermission       = "missing Moderate Members permission"
	toolErrBotMissingModerateMembersPermission    = "bot is missing Moderate Members permission"
	toolErrMissingManageMessagesPermission        = "missing Manage Messages permission"
	toolErrBotMissingManageMessagesPermission     = "bot is missing Manage Messages permission"
	toolErrMissingMoveMembersPermission           = "missing Move Members permission"
	toolErrBotMissingMoveMembersPermission        = "bot is missing Move Members permission"
	toolErrMissingDeafenMembersPermission         = "missing Deafen Members permission"
	toolErrBotMissingDeafenMembersPermission      = "bot is missing Deafen Members permission"
	toolErrMissingManageChannelsPermission        = "missing Manage Channels permission"
	toolErrBotMissingManageChannelsPermission     = "bot is missing Manage Channels permission"
	toolErrMissingManageRolesPermission           = "missing Manage Roles permission"
	toolErrBotMissingManageRolesPermission        = "bot is missing Manage Roles permission"
	toolErrMissingViewAuditLogPermission          = "missing View Audit Log permission"
	toolErrBotMissingViewAuditLogPermission       = "bot is missing View Audit Log permission"
	toolErrRoleNotFound                           = "role not found"
	toolErrChannelNotFound                        = "channel not found"
	toolErrMessageNotFound                        = "message not found"
	toolErrMissingAdminPermission                 = "missing Administrator permission"
	toolErrDiscordMissingPermissions              = "Discord denied the moderation action due to missing permissions."
	toolErrDiscordActionRejected                  = "Discord rejected the moderation action. Please verify the member state and bot permissions."
	toolErrUnknownTool                            = "unknown tool"
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
	{
		Name:        "warn",
		Description: "Issue a formal warning to a server member and record it in the database.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user": map[string]any{
					"type":        "string",
					"description": "Target member reference. Can be an ID, mention, username, nickname, display name, initials, or close textual match.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "The reason for the warning.",
				},
			},
			"required":             []string{"user", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "warnings",
		Description: "View list of warnings recorded for a server member.",
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
	{
		Name:        "clear_warnings",
		Description: "Clear all warnings recorded for a server member.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user": map[string]any{
					"type":        "string",
					"description": "Target member reference. Can be an ID, mention, username, nickname, display name, initials, or close textual match.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Reason for clearing the warnings.",
				},
			},
			"required":             []string{"user", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "move_to_voice",
		Description: "Move a guild member to a different voice channel.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user": map[string]any{
					"type":        "string",
					"description": "Target member reference.",
				},
				"target_channel": map[string]any{
					"type":        "string",
					"description": "Voice channel name or ID to move the member to.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Reason for moving the member.",
				},
			},
			"required":             []string{"user", "target_channel", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "disconnect_voice",
		Description: "Disconnect a guild member from their current voice channel.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user": map[string]any{
					"type":        "string",
					"description": "Target member reference.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Reason for disconnecting the member.",
				},
			},
			"required":             []string{"user", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "deafen",
		Description: "Server deafen a guild member in a voice channel.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user": map[string]any{
					"type":        "string",
					"description": "Target member reference.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Reason for server deafening.",
				},
			},
			"required":             []string{"user", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "undeafen",
		Description: "Server undeafen a guild member in a voice channel.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user": map[string]any{
					"type":        "string",
					"description": "Target member reference.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Reason for server undeafening.",
				},
			},
			"required":             []string{"user", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "slowmode",
		Description: "Change slowmode settings on the current channel.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"duration": map[string]any{
					"type":        "integer",
					"description": "Slowmode duration in seconds. Set to 0 to disable.",
					"minimum":     0,
					"maximum":     21600,
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Reason for updating slowmode.",
				},
			},
			"required":             []string{"duration", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "lock_channel",
		Description: "Lock down the current channel so normal users cannot send messages.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reason": map[string]any{
					"type":        "string",
					"description": "Reason for locking the channel.",
				},
			},
			"required":             []string{"reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "unlock_channel",
		Description: "Unlock the current channel, restoring normal message-sending permissions.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reason": map[string]any{
					"type":        "string",
					"description": "Reason for unlocking the channel.",
				},
			},
			"required":             []string{"reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "set_topic",
		Description: "Set/update the topic description of the current channel.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "New topic content.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Reason for updating the topic.",
				},
			},
			"required":             []string{"text", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "rename_channel",
		Description: "Rename the current channel.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "New channel name.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Reason for renaming the channel.",
				},
			},
			"required":             []string{"text", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "assign_role",
		Description: "Assign a role to a server member.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user": map[string]any{
					"type":        "string",
					"description": "Target member reference.",
				},
				"role_id": map[string]any{
					"type":        "string",
					"description": "The name or ID of the role to assign.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Reason for assigning the role.",
				},
			},
			"required":             []string{"user", "role_id", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "remove_role",
		Description: "Remove a role from a server member.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user": map[string]any{
					"type":        "string",
					"description": "Target member reference.",
				},
				"role_id": map[string]any{
					"type":        "string",
					"description": "The name or ID of the role to remove.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Reason for removing the role.",
				},
			},
			"required":             []string{"user", "role_id", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "create_role",
		Description: "Create a new role on the server.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "Name of the new role.",
				},
				"color": map[string]any{
					"type":        "string",
					"description": "Hex color code (e.g. #00FF00) for the new role.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Reason for creating the role.",
				},
			},
			"required":             []string{"text", "color", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "delete_role",
		Description: "Delete an existing role from the server.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"role_id": map[string]any{
					"type":        "string",
					"description": "Name or ID of the role to delete.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Reason for deleting the role.",
				},
			},
			"required":             []string{"role_id", "reason"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "role_info",
		Description: "Retrieve detailed info about a specific server role.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"role_id": map[string]any{
					"type":        "string",
					"description": "Name or ID of the role.",
				},
			},
			"required":             []string{"role_id"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "server_info",
		Description: "Retrieve general information about the current Discord server.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
	},
	{
		Name:        "channel_info",
		Description: "Retrieve information about a specific channel in the server.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target_channel": map[string]any{
					"type":        "string",
					"description": "Name or ID of the channel. Defaults to current channel if not provided.",
				},
			},
			"additionalProperties": false,
		},
	},
	{
		Name:        "role_list",
		Description: "List all roles in this server with their configurations.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
	},
	{
		Name:        "audit_log",
		Description: "Retrieve the recent entries from the server's Discord Audit Log.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"count": map[string]any{
					"type":        "integer",
					"description": "Number of log entries to fetch. Must be between 1 and 20. Default is 5.",
					"minimum":     1,
					"maximum":     20,
				},
			},
			"additionalProperties": false,
		},
	},
	{
		Name:        "voice_status",
		Description: "Check current voice channel occupancy and member voice statuses.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
	},
	{
		Name:        "send_message",
		Description: "Send a message to a specific text channel.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target_channel": map[string]any{
					"type":        "string",
					"description": "Name or ID of the target channel.",
				},
				"text": map[string]any{
					"type":        "string",
					"description": "The text content of the message.",
				},
			},
			"required":             []string{"target_channel", "text"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "pin_message",
		Description: "Pin a message by its message ID.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message_id": map[string]any{
					"type":        "string",
					"description": "The exact ID of the message to pin.",
				},
			},
			"required":             []string{"message_id"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "unpin_message",
		Description: "Unpin a message by its message ID.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message_id": map[string]any{
					"type":        "string",
					"description": "The exact ID of the message to unpin.",
				},
			},
			"required":             []string{"message_id"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "create_thread",
		Description: "Create a new thread from the current channel.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "Name of the thread to create.",
				},
			},
			"required":             []string{"text"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "web_search",
		Description: "Perform a web search using Tavily to retrieve current or external information.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The search query to look up on the web.",
				},
			},
			"required":             []string{"query"},
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
	}
	// unban requires Administrator because the bot cannot verify who applied the original ban;
	// allowing a BanMembers-only mod to unban could circumvent a ban applied by a superior.
	if hasPermission(session, guildID, member, discordgo.PermissionAdministrator) {
		tools = append(tools, moderationToolNamed("unban"))
	}
	if hasPermission(session, guildID, member, discordgo.PermissionKickMembers) {
		tools = append(tools, moderationToolNamed("kick"))
		tools = append(tools, moderationToolNamed("warn"))
		tools = append(tools, moderationToolNamed("warnings"))
		tools = append(tools, moderationToolNamed("clear_warnings"))
	}
	if hasPermission(session, guildID, member, discordgo.PermissionModerateMembers) {
		tools = append(tools, moderationToolNamed("timeout"))
		tools = append(tools, moderationToolNamed("untimeout"))
	}
	if hasPermission(session, guildID, member, discordgo.PermissionManageMessages) {
		tools = append(tools, moderationToolNamed("purge"))
		tools = append(tools, moderationToolNamed("send_message"))
		tools = append(tools, moderationToolNamed("pin_message"))
		tools = append(tools, moderationToolNamed("unpin_message"))
		tools = append(tools, moderationToolNamed("create_thread"))
	}
	if hasPermission(session, guildID, member, discordgo.PermissionVoiceMoveMembers) {
		tools = append(tools, moderationToolNamed("move_to_voice"))
		tools = append(tools, moderationToolNamed("disconnect_voice"))
	}
	if hasPermission(session, guildID, member, discordgo.PermissionVoiceDeafenMembers) {
		tools = append(tools, moderationToolNamed("deafen"))
		tools = append(tools, moderationToolNamed("undeafen"))
	}
	if hasPermission(session, guildID, member, discordgo.PermissionManageChannels) {
		tools = append(tools, moderationToolNamed("slowmode"))
		tools = append(tools, moderationToolNamed("lock_channel"))
		tools = append(tools, moderationToolNamed("unlock_channel"))
		tools = append(tools, moderationToolNamed("set_topic"))
		tools = append(tools, moderationToolNamed("rename_channel"))
	}
	if hasPermission(session, guildID, member, discordgo.PermissionManageRoles) {
		tools = append(tools, moderationToolNamed("assign_role"))
		tools = append(tools, moderationToolNamed("remove_role"))
		tools = append(tools, moderationToolNamed("create_role"))
		tools = append(tools, moderationToolNamed("delete_role"))
		tools = append(tools, moderationToolNamed("role_info"))
	}
	if hasPermission(session, guildID, member, discordgo.PermissionViewAuditLogs) {
		tools = append(tools, moderationToolNamed("audit_log"))
	}

	// Public read-only tools
	tools = append(tools, moderationToolNamed("member_info"))
	tools = append(tools, moderationToolNamed("server_info"))
	tools = append(tools, moderationToolNamed("channel_info"))
	tools = append(tools, moderationToolNamed("role_list"))
	tools = append(tools, moderationToolNamed("voice_status"))

	if os.Getenv("TAVILY_API_KEY") != "" {
		tools = append(tools, moderationToolNamed("web_search"))
	}

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
	t.RoleID = strings.TrimSpace(t.RoleID)
	t.TargetChannel = strings.TrimSpace(t.TargetChannel)
	t.MessageID = strings.TrimSpace(t.MessageID)
	t.Text = strings.TrimSpace(t.Text)
	t.Query = strings.TrimSpace(t.Query)
	t.Color = strings.TrimSpace(t.Color)
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

func toolRequiresTargetUser(tool string) bool {
	switch tool {
	case "ban", "unban", "kick", "timeout", "untimeout", "member_info",
		"warn", "warnings", "clear_warnings", "move_to_voice", "disconnect_voice",
		"deafen", "undeafen", "assign_role", "remove_role":
		return true
	}
	return false
}

func toolRequiresHierarchyCheck(tool string) bool {
	switch tool {
	case "ban", "kick", "timeout", "untimeout", "warn", "clear_warnings",
		"move_to_voice", "disconnect_voice", "deafen", "undeafen", "assign_role", "remove_role":
		return true
	}
	return false
}

func ExecuteTool(session *discordgo.Session, guildID, channelID string, actor *discordgo.Member, call *ToolCall) error {
	if toolRequiresTargetUser(call.Tool) && !isDiscordID(call.User) {
		return fmt.Errorf(toolErrTargetUserNotResolved)
	}

	botMember, err := session.GuildMember(guildID, session.State.User.ID)
	if err != nil {
		if isDiscordRESTErrorCode(err, discordgo.ErrCodeUnknownMember) {
			return fmt.Errorf(toolErrBotMemberNotFound)
		}
		return fmt.Errorf(toolErrBotMemberLookupFailed)
	}

	if toolRequiresHierarchyCheck(call.Tool) {
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
		// Require Administrator: without the ban audit log we cannot verify who applied
		// the ban, so we restrict unban to admins/owner to prevent a lower-ranked mod
		// from reversing a ban applied by a superior.
		if !hasPermission(session, guildID, actor, discordgo.PermissionAdministrator) {
			return fmt.Errorf(toolErrMissingAdminPermission)
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
			return fmt.Errorf(toolErrMissingManageMessagesPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionManageMessages) {
			return fmt.Errorf(toolErrBotMissingManageMessagesPermission)
		}
		messages, err := session.ChannelMessages(channelID, call.Count, "", "", "")
		if err != nil {
			return normalizeDiscordToolActionError(err)
		}
		if len(messages) == 0 {
			return nil
		}
		// Exclude the bot's own messages to avoid deleting in-progress interaction
		// responses (e.g., the public notice posted right before this confirmation).
		botID := session.State.User.ID
		messageIDs := make([]string, 0, len(messages))
		for _, msg := range messages {
			if msg.Author != nil && msg.Author.ID == botID {
				continue
			}
			messageIDs = append(messageIDs, msg.ID)
		}
		if len(messageIDs) == 0 {
			return nil
		}
		return normalizeDiscordToolActionError(session.ChannelMessagesBulkDelete(channelID, messageIDs))
	case "warn":
		if !hasPermission(session, guildID, actor, discordgo.PermissionKickMembers) {
			return fmt.Errorf(toolErrMissingKickMembersPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionKickMembers) {
			return fmt.Errorf(toolErrBotMissingKickMembersPermission)
		}
		// Write to database via a package import helper/direct call
		if err := database.Default.AddWarning(guildID, call.User, actor.User.ID, call.Reason); err != nil {
			return fmt.Errorf("failed to save warning to database: %w", err)
		}
		// Try sending a DM notification to the warned user
		guildName := guildID
		if g, err := session.State.Guild(guildID); err == nil && g != nil {
			guildName = g.Name
		}
		dmChan, dmErr := session.UserChannelCreate(call.User)
		if dmErr == nil && dmChan != nil {
			_, _ = session.ChannelMessageSend(dmChan.ID, fmt.Sprintf("You have been warned in **%s** for: %s", guildName, call.Reason))
		}
		return nil

	case "clear_warnings":
		if !hasPermission(session, guildID, actor, discordgo.PermissionKickMembers) {
			return fmt.Errorf(toolErrMissingKickMembersPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionKickMembers) {
			return fmt.Errorf(toolErrBotMissingKickMembersPermission)
		}
		if err := database.Default.ClearWarnings(guildID, call.User); err != nil {
			return fmt.Errorf("failed to clear warnings from database: %w", err)
		}
		return nil

	case "move_to_voice":
		if !hasPermission(session, guildID, actor, discordgo.PermissionVoiceMoveMembers) {
			return fmt.Errorf(toolErrMissingMoveMembersPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionVoiceMoveMembers) {
			return fmt.Errorf(toolErrBotMissingMoveMembersPermission)
		}
		targetChan, err := ResolveChannel(session, guildID, call.TargetChannel)
		if err != nil {
			return fmt.Errorf(toolErrChannelNotFound)
		}
		return normalizeDiscordToolActionError(session.GuildMemberMove(guildID, call.User, &targetChan.ID))

	case "disconnect_voice":
		if !hasPermission(session, guildID, actor, discordgo.PermissionVoiceMoveMembers) {
			return fmt.Errorf(toolErrMissingMoveMembersPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionVoiceMoveMembers) {
			return fmt.Errorf(toolErrBotMissingMoveMembersPermission)
		}
		return normalizeDiscordToolActionError(session.GuildMemberMove(guildID, call.User, nil))

	case "deafen":
		if !hasPermission(session, guildID, actor, discordgo.PermissionVoiceDeafenMembers) {
			return fmt.Errorf(toolErrMissingDeafenMembersPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionVoiceDeafenMembers) {
			return fmt.Errorf(toolErrBotMissingDeafenMembersPermission)
		}
		return normalizeDiscordToolActionError(session.GuildMemberDeafen(guildID, call.User, true))

	case "undeafen":
		if !hasPermission(session, guildID, actor, discordgo.PermissionVoiceDeafenMembers) {
			return fmt.Errorf(toolErrMissingDeafenMembersPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionVoiceDeafenMembers) {
			return fmt.Errorf(toolErrBotMissingDeafenMembersPermission)
		}
		return normalizeDiscordToolActionError(session.GuildMemberDeafen(guildID, call.User, false))

	case "slowmode":
		if !hasPermission(session, guildID, actor, discordgo.PermissionManageChannels) {
			return fmt.Errorf(toolErrMissingManageChannelsPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionManageChannels) {
			return fmt.Errorf(toolErrBotMissingManageChannelsPermission)
		}
		rate := call.Duration
		_, err := session.ChannelEdit(channelID, &discordgo.ChannelEdit{
			RateLimitPerUser: &rate,
		})
		return normalizeDiscordToolActionError(err)

	case "lock_channel":
		if !hasPermission(session, guildID, actor, discordgo.PermissionManageChannels) {
			return fmt.Errorf(toolErrMissingManageChannelsPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionManageChannels) {
			return fmt.Errorf(toolErrBotMissingManageChannelsPermission)
		}
		ch, err := session.Channel(channelID)
		if err != nil {
			return normalizeDiscordToolActionError(err)
		}
		var deny, allow int64
		deny = discordgo.PermissionSendMessages
		for _, o := range ch.PermissionOverwrites {
			if o.ID == guildID && o.Type == discordgo.PermissionOverwriteTypeRole {
				deny |= o.Deny
				allow = o.Allow &^ discordgo.PermissionSendMessages
				break
			}
		}
		return normalizeDiscordToolActionError(session.ChannelPermissionSet(channelID, guildID, discordgo.PermissionOverwriteTypeRole, allow, deny))

	case "unlock_channel":
		if !hasPermission(session, guildID, actor, discordgo.PermissionManageChannels) {
			return fmt.Errorf(toolErrMissingManageChannelsPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionManageChannels) {
			return fmt.Errorf(toolErrBotMissingManageChannelsPermission)
		}
		ch, err := session.Channel(channelID)
		if err != nil {
			return normalizeDiscordToolActionError(err)
		}
		var deny, allow int64
		for _, o := range ch.PermissionOverwrites {
			if o.ID == guildID && o.Type == discordgo.PermissionOverwriteTypeRole {
				deny = o.Deny &^ discordgo.PermissionSendMessages
				allow = o.Allow &^ discordgo.PermissionSendMessages
				break
			}
		}
		if allow == 0 && deny == 0 {
			return normalizeDiscordToolActionError(session.ChannelPermissionDelete(channelID, guildID))
		}
		return normalizeDiscordToolActionError(session.ChannelPermissionSet(channelID, guildID, discordgo.PermissionOverwriteTypeRole, allow, deny))

	case "set_topic":
		if !hasPermission(session, guildID, actor, discordgo.PermissionManageChannels) {
			return fmt.Errorf(toolErrMissingManageChannelsPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionManageChannels) {
			return fmt.Errorf(toolErrBotMissingManageChannelsPermission)
		}
		_, err := session.ChannelEdit(channelID, &discordgo.ChannelEdit{
			Topic: call.Text,
		})
		return normalizeDiscordToolActionError(err)

	case "rename_channel":
		if !hasPermission(session, guildID, actor, discordgo.PermissionManageChannels) {
			return fmt.Errorf(toolErrMissingManageChannelsPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionManageChannels) {
			return fmt.Errorf(toolErrBotMissingManageChannelsPermission)
		}
		_, err := session.ChannelEdit(channelID, &discordgo.ChannelEdit{
			Name: call.Text,
		})
		return normalizeDiscordToolActionError(err)

	case "assign_role":
		if !hasPermission(session, guildID, actor, discordgo.PermissionManageRoles) {
			return fmt.Errorf(toolErrMissingManageRolesPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionManageRoles) {
			return fmt.Errorf(toolErrBotMissingManageRolesPermission)
		}
		role, err := ResolveRole(session, guildID, call.RoleID)
		if err != nil {
			return fmt.Errorf(toolErrRoleNotFound)
		}
		if !roleBelowMember(session, guildID, actor, role) {
			return fmt.Errorf(toolErrRoleHierarchyPreventsAction)
		}
		if !roleBelowMember(session, guildID, botMember, role) {
			return fmt.Errorf(toolErrBotRoleHierarchyPreventsAction)
		}
		return normalizeDiscordToolActionError(session.GuildMemberRoleAdd(guildID, call.User, role.ID))

	case "remove_role":
		if !hasPermission(session, guildID, actor, discordgo.PermissionManageRoles) {
			return fmt.Errorf(toolErrMissingManageRolesPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionManageRoles) {
			return fmt.Errorf(toolErrBotMissingManageRolesPermission)
		}
		role, err := ResolveRole(session, guildID, call.RoleID)
		if err != nil {
			return fmt.Errorf(toolErrRoleNotFound)
		}
		if !roleBelowMember(session, guildID, actor, role) {
			return fmt.Errorf(toolErrRoleHierarchyPreventsAction)
		}
		if !roleBelowMember(session, guildID, botMember, role) {
			return fmt.Errorf(toolErrBotRoleHierarchyPreventsAction)
		}
		return normalizeDiscordToolActionError(session.GuildMemberRoleRemove(guildID, call.User, role.ID))

	case "create_role":
		if !hasPermission(session, guildID, actor, discordgo.PermissionManageRoles) {
			return fmt.Errorf(toolErrMissingManageRolesPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionManageRoles) {
			return fmt.Errorf(toolErrBotMissingManageRolesPermission)
		}
		colorVal := parseHexColor(call.Color)
		_, err := session.GuildRoleCreate(guildID, &discordgo.RoleParams{
			Name:  call.Text,
			Color: colorVal,
		})
		return normalizeDiscordToolActionError(err)

	case "delete_role":
		if !hasPermission(session, guildID, actor, discordgo.PermissionManageRoles) {
			return fmt.Errorf(toolErrMissingManageRolesPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionManageRoles) {
			return fmt.Errorf(toolErrBotMissingManageRolesPermission)
		}
		role, err := ResolveRole(session, guildID, call.RoleID)
		if err != nil {
			return fmt.Errorf(toolErrRoleNotFound)
		}
		if !roleBelowMember(session, guildID, actor, role) {
			return fmt.Errorf(toolErrRoleHierarchyPreventsAction)
		}
		if !roleBelowMember(session, guildID, botMember, role) {
			return fmt.Errorf(toolErrBotRoleHierarchyPreventsAction)
		}
		return normalizeDiscordToolActionError(session.GuildRoleDelete(guildID, role.ID))

	case "send_message":
		if !hasPermission(session, guildID, actor, discordgo.PermissionManageMessages) {
			return fmt.Errorf(toolErrMissingManageMessagesPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionManageMessages) {
			return fmt.Errorf(toolErrBotMissingManageMessagesPermission)
		}
		targetChan, err := ResolveChannel(session, guildID, call.TargetChannel)
		if err != nil {
			return fmt.Errorf(toolErrChannelNotFound)
		}
		_, err = session.ChannelMessageSend(targetChan.ID, call.Text)
		return normalizeDiscordToolActionError(err)

	case "pin_message":
		if !hasPermission(session, guildID, actor, discordgo.PermissionManageMessages) {
			return fmt.Errorf(toolErrMissingManageMessagesPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionManageMessages) {
			return fmt.Errorf(toolErrBotMissingManageMessagesPermission)
		}
		return normalizeDiscordToolActionError(session.ChannelMessagePin(channelID, call.MessageID))

	case "unpin_message":
		if !hasPermission(session, guildID, actor, discordgo.PermissionManageMessages) {
			return fmt.Errorf(toolErrMissingManageMessagesPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionManageMessages) {
			return fmt.Errorf(toolErrBotMissingManageMessagesPermission)
		}
		return normalizeDiscordToolActionError(session.ChannelMessageUnpin(channelID, call.MessageID))

	case "create_thread":
		if !hasPermission(session, guildID, actor, discordgo.PermissionManageMessages) {
			return fmt.Errorf(toolErrMissingManageMessagesPermission)
		}
		if !hasPermission(session, guildID, botMember, discordgo.PermissionManageMessages) {
			return fmt.Errorf(toolErrBotMissingManageMessagesPermission)
		}
		_, err := session.ThreadStart(channelID, call.Text, discordgo.ChannelTypeGuildPublicThread, 1440)
		return normalizeDiscordToolActionError(err)

	case "member_info", "warnings", "role_info", "server_info", "channel_info", "role_list", "audit_log", "voice_status":
		// Read-only tools, actual output formatting is handled in ask.go directly.
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
		toolErrMissingManageMessagesPermission,
		toolErrBotMissingManageMessagesPermission,
		toolErrMissingMoveMembersPermission,
		toolErrBotMissingMoveMembersPermission,
		toolErrMissingDeafenMembersPermission,
		toolErrBotMissingDeafenMembersPermission,
		toolErrMissingManageChannelsPermission,
		toolErrBotMissingManageChannelsPermission,
		toolErrMissingManageRolesPermission,
		toolErrBotMissingManageRolesPermission,
		toolErrMissingViewAuditLogPermission,
		toolErrBotMissingViewAuditLogPermission,
		toolErrRoleNotFound,
		toolErrChannelNotFound,
		toolErrMessageNotFound,
		toolErrMissingAdminPermission,
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
	// List of all valid tools
	allTools := map[string]bool{
		"ban": true, "unban": true, "kick": true, "timeout": true, "untimeout": true, "purge": true, "member_info": true,
		"warn": true, "warnings": true, "clear_warnings": true,
		"move_to_voice": true, "disconnect_voice": true, "deafen": true, "undeafen": true,
		"slowmode": true, "lock_channel": true, "unlock_channel": true, "set_topic": true, "rename_channel": true,
		"assign_role": true, "remove_role": true, "create_role": true, "delete_role": true, "role_info": true,
		"server_info": true, "channel_info": true, "role_list": true, "audit_log": true, "voice_status": true,
		"send_message": true, "pin_message": true, "unpin_message": true, "create_thread": true,
		"web_search": true,
	}

	if !allTools[call.Tool] {
		return fmt.Errorf(toolErrUnknownTool)
	}

	// 1. Determine if it is a read-only tool (no reason required)
	readOnlyTools := map[string]bool{
		"member_info": true, "warnings": true, "role_info": true,
		"server_info": true, "channel_info": true, "role_list": true,
		"audit_log": true, "voice_status": true, "web_search": true,
	}

	isReadOnly := readOnlyTools[call.Tool]

	// 2. Validate reason if not read-only
	if !isReadOnly {
		if call.Reason == "" {
			return fmt.Errorf("missing moderation reason")
		}
		if len(call.Reason) > maxAuditReasonLen {
			return fmt.Errorf("reason too long")
		}
	}

	// 3. Validate specific field requirements based on tool
	switch call.Tool {
	case "ban", "unban", "kick", "timeout", "untimeout", "member_info",
		"warn", "warnings", "clear_warnings", "move_to_voice", "disconnect_voice", "deafen", "undeafen",
		"assign_role", "remove_role":
		if call.User == "" || len(call.User) > maxUserReferenceLen {
			return fmt.Errorf("invalid target user")
		}
	}

	switch call.Tool {
	case "timeout":
		if call.Minutes < 1 || call.Minutes > maxTimeoutMinutes {
			return fmt.Errorf("invalid timeout duration")
		}
	case "purge":
		if call.Count < 1 || call.Count > 100 {
			return fmt.Errorf("invalid purge count")
		}
	case "slowmode":
		if call.Duration < 0 || call.Duration > 21600 {
			return fmt.Errorf("invalid slowmode duration")
		}
	case "move_to_voice":
		if call.TargetChannel == "" {
			return fmt.Errorf("missing target channel")
		}
	case "assign_role", "remove_role", "delete_role", "role_info":
		if call.RoleID == "" {
			return fmt.Errorf("missing role ID/name")
		}
	case "set_topic", "rename_channel", "create_role", "create_thread":
		if call.Text == "" {
			return fmt.Errorf("missing text content")
		}
	case "send_message":
		if call.Text == "" {
			return fmt.Errorf("missing text content")
		}
		if call.TargetChannel == "" {
			return fmt.Errorf("missing target channel")
		}
	case "pin_message", "unpin_message":
		if call.MessageID == "" {
			return fmt.Errorf("missing message ID")
		}
	case "audit_log":
		if call.Count != 0 && (call.Count < 1 || call.Count > 20) {
			return fmt.Errorf("invalid log count")
		}
	case "web_search":
		if call.Query == "" {
			return fmt.Errorf("missing search query")
		}
	}

	return nil
}

func ConfirmationText(call *ToolCall, target string) string {
	if strings.TrimSpace(target) == "" {
		target = formatToolTarget(call.User)
	}

	reason := sanitizeInlineCode(call.Reason)
	textEscaped := sanitizeInlineCode(call.Text)
	roleEscaped := sanitizeInlineCode(call.RoleID)
	channelEscaped := sanitizeInlineCode(call.TargetChannel)
	msgIDEcaped := sanitizeInlineCode(call.MessageID)
	queryEscaped := sanitizeInlineCode(call.Query)

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
	case "warn":
		return fmt.Sprintf("Warn %s for `%s`?", target, reason)
	case "clear_warnings":
		return fmt.Sprintf("Clear all warnings for %s for `%s`?", target, reason)
	case "move_to_voice":
		return fmt.Sprintf("Move %s to voice channel `%s` for `%s`?", target, channelEscaped, reason)
	case "disconnect_voice":
		return fmt.Sprintf("Disconnect %s from voice for `%s`?", target, reason)
	case "deafen":
		return fmt.Sprintf("Server-deafen %s for `%s`?", target, reason)
	case "undeafen":
		return fmt.Sprintf("Server-undeafen %s for `%s`?", target, reason)
	case "slowmode":
		return fmt.Sprintf("Set slowmode to %d second(s) for `%s`?", call.Duration, reason)
	case "lock_channel":
		return fmt.Sprintf("Lock the current channel for `%s`?", reason)
	case "unlock_channel":
		return fmt.Sprintf("Unlock the current channel for `%s`?", reason)
	case "set_topic":
		return fmt.Sprintf("Set channel topic to `%s` for `%s`?", textEscaped, reason)
	case "rename_channel":
		return fmt.Sprintf("Rename this channel to `%s` for `%s`?", textEscaped, reason)
	case "assign_role":
		return fmt.Sprintf("Assign role `%s` to %s for `%s`?", roleEscaped, target, reason)
	case "remove_role":
		return fmt.Sprintf("Remove role `%s` from %s for `%s`?", roleEscaped, target, reason)
	case "create_role":
		return fmt.Sprintf("Create new role `%s` (color: `%s`) for `%s`?", textEscaped, call.Color, reason)
	case "delete_role":
		return fmt.Sprintf("Delete role `%s` for `%s`?", roleEscaped, reason)
	case "send_message":
		return fmt.Sprintf("Send message to channel `%s`: `%s`?", channelEscaped, textEscaped)
	case "pin_message":
		return fmt.Sprintf("Pin message `%s` for `%s`?", msgIDEcaped, reason)
	case "unpin_message":
		return fmt.Sprintf("Unpin message `%s` for `%s`?", msgIDEcaped, reason)
	case "create_thread":
		return fmt.Sprintf("Create new thread `%s` for `%s`?", textEscaped, reason)
	case "web_search":
		return fmt.Sprintf("Perform a web search for `%s`?", queryEscaped)
	default:
		return "Confirm this action?"
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
	// Fall back to the API if the state doesn't have this guild OR if the state
	// guild has no roles loaded (can happen after reconnection on large guilds).
	if err != nil || guild == nil || len(guild.Roles) == 0 {
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

func roleBelowMember(session *discordgo.Session, guildID string, member *discordgo.Member, role *discordgo.Role) bool {
	if discordperm.IsGuildOwner(session, guildID, member) {
		return true
	}

	guild, err := session.State.Guild(guildID)
	if err != nil || guild == nil || len(guild.Roles) == 0 {
		guild, err = session.Guild(guildID)
		if err != nil {
			return false
		}
	}

	return highestRolePosition(guild.Roles, member.Roles) > role.Position
}

func parseHexColor(hexStr string) *int {
	hexStr = strings.TrimPrefix(hexStr, "#")
	var val int
	_, err := fmt.Sscanf(hexStr, "%x", &val)
	if err != nil {
		return nil
	}
	return &val
}

var tavilySearchURL = "https://api.tavily.com/search"

type TavilySearchResult struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

type TavilySearchResponse struct {
	Results []TavilySearchResult `json:"results"`
}

type TavilySearchRequest struct {
	APIKey      string `json:"api_key"`
	Query       string `json:"query"`
	SearchDepth string `json:"search_depth,omitempty"`
	MaxResults  int    `json:"max_results,omitempty"`
}

func CallTavilySearch(ctx context.Context, query string) ([]TavilySearchResult, error) {
	apiKey := os.Getenv("TAVILY_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("TAVILY_API_KEY env var not set")
	}

	reqPayload := TavilySearchRequest{
		APIKey:      apiKey,
		Query:       query,
		SearchDepth: "basic",
		MaxResults:  5,
	}

	body, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tavilySearchURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("tavily search failed (status %d): %s", resp.StatusCode, string(b))
	}

	var searchResp TavilySearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, err
	}

	return searchResp.Results, nil
}
