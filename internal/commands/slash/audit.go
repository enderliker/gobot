package slash

import (
	"strings"

	"gobot/internal/audit"

	"github.com/bwmarrin/discordgo"
)

func auditInteraction(i *discordgo.InteractionCreate, actionType, outcome string, fields map[string]any) {
	if i == nil || i.Interaction == nil {
		return
	}

	audit.Log(audit.Event{
		GuildID:    i.GuildID,
		ChannelID:  i.ChannelID,
		ActorID:    interactionUserID(i),
		ActionType: actionType,
		Outcome:    outcome,
		Fields:     fields,
	})
}

func toolAuditFields(call *aiToolAuditView) map[string]any {
	fields := map[string]any{
		"tool":             call.Tool,
		"requested_target": call.RequestedTarget,
	}
	if call.TargetID != "" {
		fields["target_id"] = call.TargetID
	}
	if call.Reason != "" {
		fields["reason"] = call.Reason
	}
	return fields
}

type aiToolAuditView struct {
	Tool            string
	RequestedTarget string
	TargetID        string
	Reason          string
}

func auditViewFromToolCall(tool, requestedTarget, targetID, reason string) *aiToolAuditView {
	if targetID == "" {
		targetID = inferredDiscordID(requestedTarget)
	}
	return &aiToolAuditView{
		Tool:            strings.TrimSpace(tool),
		RequestedTarget: strings.TrimSpace(requestedTarget),
		TargetID:        strings.TrimSpace(targetID),
		Reason:          strings.TrimSpace(reason),
	}
}

func inferredDiscordID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 17 || len(value) > 20 {
		return ""
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return value
}
