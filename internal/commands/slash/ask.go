package slash

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gobot/internal/ai"
	"gobot/internal/database"
	"gobot/internal/embeds"
	"gobot/internal/lifecycle"
	"gobot/internal/registry"

	"github.com/bwmarrin/discordgo"
)

const toolConfirmationTimeout = 2 * time.Minute
const toolTargetPageSize = 25

func init() {
	if err := registry.RegisterCommand(&registry.Command{
		Module: "AI",
		Data: &discordgo.ApplicationCommand{
			Name:        "ask",
			Description: "Ask a question to the configured AI",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "question",
					Description: "The question to ask",
					Required:    true,
				},
			},
		},
		Execute: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			if i.GuildID == "" {
				ephemeral(s, i, embeds.Error("Server Only", "This command can only be used in a server."))
				return
			}

			rawQuestion := i.ApplicationCommandData().Options[0].StringValue()
			cfg, err := database.Default.GetGuildConfig(i.GuildID)
			if err != nil {
				log.Printf("[ASK] db: %v", err)
				ephemeral(s, i, embeds.Error("Database Error", "Failed to retrieve server configuration."))
				return
			}
			if cfg == nil {
				ephemeral(s, i, embeds.Error(
					"No API Key Configured",
					"This server has no AI API key set up.\nUse `/setkey` to configure one.",
				))
				return
			}

			guildSystem, err := database.Default.GetGuildSystemPrompt(i.GuildID)
			if err != nil {
				log.Printf("[ASK] prompt db: %v", err)
				ephemeral(s, i, embeds.Error("Database Error", "Failed to retrieve the guild system prompt."))
				return
			}

			request, err := prepareAskProviderRequest(s, i, rawQuestion, guildSystem)
			if err != nil {
				if limitErr, ok := err.(*askLimitError); ok {
					ephemeral(s, i, embeds.Error(limitErr.Title, limitErr.Message))
					return
				}
				ephemeral(s, i, embeds.Error("Invalid Question", "The question could not be prepared for the AI provider."))
				return
			}

			provider := ai.DefaultManager.Get(cfg.Provider)
			if provider == nil {
				ephemeral(s, i, embeds.Error("Unknown Provider", fmt.Sprintf("Provider **%s** is no longer supported.", cfg.Provider)))
				return
			}

			lease, err := defaultAskLimiter.Acquire(interactionUserID(i), i.GuildID)
			if err != nil {
				if limitErr, ok := err.(*askLimitError); ok {
					ephemeral(s, i, embeds.Error(limitErr.Title, limitErr.Message))
					return
				}
				ephemeral(s, i, embeds.Error("Rate Limited", "This `/ask` request could not be accepted right now. Please try again shortly."))
				return
			}

			if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Embeds: []*discordgo.MessageEmbed{embeds.AIConfirmation("Processing your AI request...")},
					Flags:  discordgo.MessageFlagsEphemeral,
				},
			}); err != nil {
				lease.Release()
				log.Printf("[ASK] initial respond: %v", err)
				return
			}

			lifecycle.Go(func(ctx context.Context) {
				defer lease.Release()

				result, err := provider.Ask(ctx, cfg.APIKey, cfg.Model, request.Prompt, request.Tools)

				var embed *discordgo.MessageEmbed
				if err != nil {
					if ctx.Err() != nil || lifecycle.IsShuttingDown() {
						return
					}
					log.Printf("[ASK] provider %s: %v", cfg.Provider, err)
					embed = embeds.Error("AI Error", ai.UserFacingError(err))
					if err := sendChannelMessageWithRetry(s, i, embed, nil); err != nil {
						log.Printf("[ASK] channel send failed after retries: %v", err)
					}
				} else {
					if ctx.Err() != nil || lifecycle.IsShuttingDown() {
						return
					}
					if call := structuredToolCallFromAskResult(result); call != nil {
						auditInteraction(i, "tool_call_proposed", "success", toolAuditFields(auditViewFromToolCall(call.Tool, call.User, "", call.Reason)))
						if err := presentToolConfirmation(s, i, call); err != nil {
							log.Printf("[ASK] tool confirmation: %v", err)
							embed = embeds.Error("AI Action Error", err.Error())
							if err := sendChannelMessageWithRetry(s, i, embed, nil); err != nil {
								log.Printf("[ASK] channel send failed after retries: %v", err)
							}
						}
						return
					}

					answer := cleanThoughtTags(result.Text)
					embed = embeds.AIResponse(cfg.Provider, cfg.Model, request.Prompt.UserPrompt, answer)
					if err := sendChannelMessageWithRetry(s, i, embed, nil); err != nil {
						log.Printf("[ASK] channel send failed after retries: %v", err)
					}
				}
			})
		},
	}); err != nil {
		panic(err)
	}
}

// cleanThoughtTags strips `<thought>...</thought>` and `<thinking>...</thinking>`
// tag blocks (closed or unclosed due to truncation) from any model output.
func cleanThoughtTags(s string) string {
	s = removeTag(s, "thought")
	s = removeTag(s, "thinking")
	return strings.TrimSpace(s)
}

func removeTag(s, tagName string) string {
	startTag := "<" + tagName + ">"
	endTag := "</" + tagName + ">"

	for {
		startIdx := strings.Index(strings.ToLower(s), startTag)
		if startIdx == -1 {
			break
		}
		endIdx := strings.Index(strings.ToLower(s), endTag)
		if endIdx != -1 && endIdx > startIdx {
			s = s[:startIdx] + s[endIdx+len(endTag):]
		} else {
			s = s[:startIdx]
			break
		}
	}
	return s
}

type askProviderRequest struct {
	Prompt ai.PromptEnvelope
	Tools  []ai.ToolDefinition
}

func prepareAskProviderRequest(s *discordgo.Session, i *discordgo.InteractionCreate, rawQuestion, guildSystem string) (*askProviderRequest, error) {
	prompt, err := buildAskPromptEnvelope(rawQuestion, guildSystem)
	if err != nil {
		return nil, err
	}

	return &askProviderRequest{
		Prompt: prompt,
		Tools:  askToolsForActor(s, i),
	}, nil
}

func askToolsForActor(s *discordgo.Session, i *discordgo.InteractionCreate) []ai.ToolDefinition {
	if i == nil {
		return nil
	}
	return ai.ModerationToolsForMember(s, i.GuildID, i.Member)
}

func structuredToolCallFromAskResult(result *ai.AskResult) *ai.ToolCall {
	if result == nil {
		return nil
	}
	return result.ToolCall
}

// sendChannelMessageWithRetry sends a regular channel message instead of an
// interaction follow-up. This avoids the webhook-token path that is being
// aggressively rate-limited by Cloudflare on the current host/IP.
func sendChannelMessageWithRetry(s *discordgo.Session, i *discordgo.InteractionCreate, embed *discordgo.MessageEmbed, components []discordgo.MessageComponent) error {
	const maxAttempts = 3
	delays := []time.Duration{1 * time.Second, 3 * time.Second}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if lifecycle.IsShuttingDown() {
			return context.Canceled
		}
		if attempt > 0 {
			time.Sleep(delays[attempt-1])
		}
		params := &discordgo.MessageSend{
			Content:    actorMention(i),
			Embeds:     []*discordgo.MessageEmbed{embed},
			Components: components,
		}
		_, lastErr = s.ChannelMessageSendComplex(i.ChannelID, params)
		if lastErr == nil {
			return nil
		}
		log.Printf("[ASK] channel send attempt %d/%d failed: %v", attempt+1, maxAttempts, lastErr)
	}

	return lastErr
}

func presentToolConfirmation(s *discordgo.Session, i *discordgo.InteractionCreate, call *ai.ToolCall) error {
	candidates, err := ai.ResolveMembers(s, i.GuildID, call.User)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return fmt.Errorf("no members matched %q", call.User)
	}

	flowID := strconv.FormatInt(time.Now().UnixNano(), 10)
	selectID := "ai_target_select_" + flowID
	pagePrefix := "ai_target_page_" + flowID + "_"
	confirmID := "ai_confirm_" + flowID
	cancelID := "ai_cancel_" + flowID
	actorID := interactionUserID(i)
	requestedTarget := call.User

	done := make(chan struct{})
	var once sync.Once
	var expired atomic.Bool
	candidatesByID := make(map[string]ai.MemberCandidate, len(candidates))
	for _, candidate := range candidates {
		if candidate.Member == nil || candidate.Member.User == nil {
			continue
		}
		candidatesByID[candidate.Member.User.ID] = candidate
	}

	var selected *ai.MemberCandidate
	if len(candidates) == 1 {
		candidate := candidates[0]
		selected = &candidate
		call.User = candidate.Member.User.ID
		call.Confirmation = confirmationMessage(call, candidate)
	}

	var removeHandler func()
	removeHandler = s.AddHandler(func(s *discordgo.Session, ic *discordgo.InteractionCreate) {
		if ic.Type != discordgo.InteractionMessageComponent {
			return
		}

		data := ic.MessageComponentData()
		if data.CustomID != selectID && data.CustomID != confirmID && data.CustomID != cancelID && !strings.HasPrefix(data.CustomID, pagePrefix) {
			return
		}

		if interactionUserID(ic) != actorID {
			auditInteraction(ic, "tool_call_interaction", "denied", map[string]any{
				"reason":            "wrong_actor",
				"expected_actor_id": actorID,
				"tool":              call.Tool,
				"requested_target":  requestedTarget,
			})
			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "You cannot confirm or cancel someone else's AI action.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		if expired.Load() {
			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseUpdateMessage,
				Data: &discordgo.InteractionResponseData{
					Embeds:     []*discordgo.MessageEmbed{embeds.Error("Confirmation Expired", "The AI moderation action was not confirmed in time.")},
					Components: []discordgo.MessageComponent{},
				},
			})
			once.Do(func() {
				close(done)
				removeHandler()
			})
			return
		}

		switch {
		case data.CustomID == cancelID:
			auditInteraction(ic, "tool_call_cancelled", "success", toolAuditFields(auditViewFromToolCall(call.Tool, requestedTarget, selectedTargetID(selected), call.Reason)))
			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseUpdateMessage,
				Data: &discordgo.InteractionResponseData{
					Embeds:     []*discordgo.MessageEmbed{embeds.AIConfirmation("Moderation action cancelled.")},
					Components: []discordgo.MessageComponent{},
				},
			})
			once.Do(func() {
				close(done)
				removeHandler()
			})
			return
		case data.CustomID == selectID:
			if len(data.Values) == 0 {
				_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: "Select a member before confirming the action.",
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				})
				return
			}

			candidate, ok := candidatesByID[data.Values[0]]
			if !ok {
				_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: "That member is no longer available in this selection flow.",
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				})
				return
			}

			selected = &candidate
			call.User = candidate.Member.User.ID
			call.Confirmation = confirmationMessage(call, candidate)
			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseUpdateMessage,
				Data: &discordgo.InteractionResponseData{
					Embeds:     []*discordgo.MessageEmbed{embeds.AIConfirmation(call.Confirmation)},
					Components: confirmationComponents(confirmID, cancelID),
				},
			})
			return
		case strings.HasPrefix(data.CustomID, pagePrefix):
			page, err := strconv.Atoi(strings.TrimPrefix(data.CustomID, pagePrefix))
			if err != nil {
				return
			}

			embed, components := memberSelectionView(call, candidates, page, selectID, pagePrefix, cancelID)
			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseUpdateMessage,
				Data: &discordgo.InteractionResponseData{
					Embeds:     []*discordgo.MessageEmbed{embed},
					Components: components,
				},
			})
			return
		}

		if selected == nil {
			_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "Select a member before confirming the action.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		var embed *discordgo.MessageEmbed
		if data.CustomID == confirmID {
			if err := ai.ExecuteTool(s, i.GuildID, i.Member, call); err != nil {
				auditInteraction(ic, "tool_call_confirmed", toolExecutionOutcome(err), mergeAuditFields(
					toolAuditFields(auditViewFromToolCall(call.Tool, requestedTarget, call.User, call.Reason)),
					map[string]any{"error": err.Error()},
				))
				embed = toolExecutionErrorEmbed(err)
			} else {
				auditInteraction(ic, "tool_call_confirmed", "success", toolAuditFields(auditViewFromToolCall(call.Tool, requestedTarget, call.User, call.Reason)))
				embed = embeds.AIConfirmation("Moderation action completed.")
			}
		}

		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Embeds:     []*discordgo.MessageEmbed{embed},
				Components: []discordgo.MessageComponent{},
			},
		})

		once.Do(func() {
			close(done)
			removeHandler()
		})
	})

	var initialEmbed *discordgo.MessageEmbed
	var initialComponents []discordgo.MessageComponent
	if selected != nil {
		initialEmbed = embeds.AIConfirmation(call.Confirmation)
		initialComponents = confirmationComponents(confirmID, cancelID)
	} else {
		initialEmbed, initialComponents = memberSelectionView(call, candidates, 0, selectID, pagePrefix, cancelID)
	}

	if err := sendChannelMessageWithRetry(s, i, initialEmbed, initialComponents); err != nil {
		removeHandler()
		return err
	}

	lifecycle.Go(func(ctx context.Context) {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-time.After(toolConfirmationTimeout):
			expired.Store(true)
		}
	})

	return nil
}

func toolExecutionErrorEmbed(err error) *discordgo.MessageEmbed {
	return embeds.Error("AI Action Failed", ai.UserFacingToolExecutionError(err))
}

func actorMention(i *discordgo.InteractionCreate) string {
	if userID := interactionUserID(i); userID != "" {
		return "<@" + userID + ">"
	}
	return ""
}

func ephemeral(s *discordgo.Session, i *discordgo.InteractionCreate, embed *discordgo.MessageEmbed) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
			Flags:  discordgo.MessageFlagsEphemeral,
		},
	}); err != nil {
		log.Printf("[ASK] respond: %v", err)
	}
}

func buildAskPromptEnvelope(question, guildSystem string) (ai.PromptEnvelope, error) {
	if err := validateAskQuestionLength(defaultAskLimits, question); err != nil {
		return ai.PromptEnvelope{}, err
	}

	envelope := ai.DefaultPromptEnvelope(question)
	envelope.GuildSystem = guildSystem
	return envelope, nil
}

func memberSelectionView(call *ai.ToolCall, candidates []ai.MemberCandidate, page int, selectID, pagePrefix, cancelID string) (*discordgo.MessageEmbed, []discordgo.MessageComponent) {
	totalPages := (len(candidates) + toolTargetPageSize - 1) / toolTargetPageSize
	if totalPages == 0 {
		totalPages = 1
	}

	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
	}

	start := page * toolTargetPageSize
	end := start + toolTargetPageSize
	if end > len(candidates) {
		end = len(candidates)
	}

	options := make([]discordgo.SelectMenuOption, 0, end-start)
	for _, candidate := range candidates[start:end] {
		if candidate.Member == nil || candidate.Member.User == nil {
			continue
		}
		options = append(options, discordgo.SelectMenuOption{
			Label:       truncateDiscordComponent(memberChoiceLabel(candidate), 100),
			Value:       candidate.Member.User.ID,
			Description: truncateDiscordComponent(memberChoiceDescription(candidate), 100),
		})
	}

	pageInfo := ""
	if totalPages > 1 {
		pageInfo = fmt.Sprintf(" (page %d/%d)", page+1, totalPages)
	}

	embed := embeds.AIConfirmation(
		fmt.Sprintf("Multiple members matched `%s` (%d found). Select the correct target%s, then confirm the moderation action.", sanitizeChoiceText(call.User), len(candidates), pageInfo),
	)

	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					CustomID:    selectID,
					Placeholder: "Choose the target member...",
					Options:     options,
				},
			},
		},
	}

	var buttons []discordgo.MessageComponent
	if page > 0 {
		buttons = append(buttons, discordgo.Button{
			CustomID: pagePrefix + strconv.Itoa(page-1),
			Label:    "◀ Previous",
			Style:    discordgo.SecondaryButton,
		})
	}
	buttons = append(buttons, discordgo.Button{
		CustomID: cancelID,
		Label:    "Cancel",
		Emoji:    &discordgo.ComponentEmoji{Name: "❌"},
		Style:    discordgo.DangerButton,
	})
	if page < totalPages-1 {
		buttons = append(buttons, discordgo.Button{
			CustomID: pagePrefix + strconv.Itoa(page+1),
			Label:    "Next ▶",
			Style:    discordgo.SecondaryButton,
		})
	}

	components = append(components, discordgo.ActionsRow{Components: buttons})
	return embed, components
}

func confirmationComponents(confirmID, cancelID string) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					CustomID: confirmID,
					Label:    "Confirm",
					Emoji:    &discordgo.ComponentEmoji{Name: "✅"},
					Style:    discordgo.SuccessButton,
				},
				discordgo.Button{
					CustomID: cancelID,
					Label:    "Cancel",
					Emoji:    &discordgo.ComponentEmoji{Name: "❌"},
					Style:    discordgo.DangerButton,
				},
			},
		},
	}
}

func confirmationMessage(call *ai.ToolCall, candidate ai.MemberCandidate) string {
	display := memberTargetDisplay(candidate)
	return ai.ConfirmationText(call, display)
}

func memberTargetDisplay(candidate ai.MemberCandidate) string {
	if candidate.Member == nil || candidate.Member.User == nil {
		return "unknown member"
	}

	displayName := candidate.Member.DisplayName()
	if displayName == "" {
		return candidate.Member.Mention()
	}
	return fmt.Sprintf("%s (%s)", candidate.Member.Mention(), sanitizeChoiceText(displayName))
}

func memberChoiceLabel(candidate ai.MemberCandidate) string {
	if candidate.Member == nil || candidate.Member.User == nil {
		return "unknown member"
	}

	displayName := candidate.Member.DisplayName()
	if displayName == "" {
		displayName = candidate.Member.User.Username
	}
	return displayName
}

func memberChoiceDescription(candidate ai.MemberCandidate) string {
	if candidate.Member == nil || candidate.Member.User == nil {
		return "Unknown match"
	}

	parts := []string{"@" + candidate.Member.User.Username, fmt.Sprintf("%.0f%% match", candidate.Score*100)}
	return strings.Join(parts, " • ")
}

func truncateDiscordComponent(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func selectedTargetID(candidate *ai.MemberCandidate) string {
	if candidate == nil || candidate.Member == nil || candidate.Member.User == nil {
		return ""
	}
	return candidate.Member.User.ID
}

func toolExecutionOutcome(err error) string {
	if err == nil {
		return "success"
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "missing "), strings.Contains(msg, "role hierarchy"):
		return "denied"
	default:
		return "error"
	}
}

func mergeAuditFields(base, extra map[string]any) map[string]any {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}

	merged := make(map[string]any, len(base)+len(extra))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

func sanitizeChoiceText(s string) string {
	s = strings.ReplaceAll(s, "`", "'")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}
