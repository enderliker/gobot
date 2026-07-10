package slash

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"gobot/internal/ai"
	"gobot/internal/database"
	"gobot/internal/embeds"
	"gobot/internal/lifecycle"
	"gobot/internal/registry"

	"github.com/bwmarrin/discordgo"
)

const toolConfirmationTimeout = 2 * time.Minute
const toolTargetPageSize = 25

const promptLeakRefusalMessage = "I can't disclose hidden system or server instructions, but I can still help with the task itself."

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
				Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			}); err != nil {
				lease.Release()
				log.Printf("[ASK] initial respond: %v", err)
				return
			}

			lifecycle.Go(func(ctx context.Context) {
				defer lease.Release()

				reqTools := request.Tools
				reqPrompt := request.Prompt
				if ai.IsImageModel(cfg.Model) {
					reqTools = nil
					reqPrompt.BaseSystem = ""
					reqPrompt.GuildSystem = ""
				}

				result, err := provider.Ask(ctx, cfg.APIKey, cfg.Model, reqPrompt, reqTools)

				var embed *discordgo.MessageEmbed
				if err != nil {
					if ctx.Err() != nil || lifecycle.IsShuttingDown() {
						return
					}
					if !ai.IsUserFacingError(err) {
						log.Printf("[ASK] provider %s: %v", cfg.Provider, err)
					}
					embed = embeds.Error("AI Error", ai.UserFacingError(err))
					if err := editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{
						Embeds:          &[]*discordgo.MessageEmbed{embed},
						Components:      &[]discordgo.MessageComponent{},
						AllowedMentions: allowedMentionsForActor(i),
					}); err != nil {
						log.Printf("[ASK] original response edit failed after retries: %v", err)
					}
				} else {
					if ctx.Err() != nil || lifecycle.IsShuttingDown() {
						return
					}
					if len(result.ImageData) > 0 {
						filename := "generated.png"
						if strings.Contains(result.ImageMimeType, "jpeg") || strings.Contains(result.ImageMimeType, "jpg") {
							filename = "generated.jpg"
						}
						file := &discordgo.File{
							Name:        filename,
							ContentType: result.ImageMimeType,
							Reader:      bytes.NewReader(result.ImageData),
						}
						if err := editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{
							Files:           []*discordgo.File{file},
							AllowedMentions: allowedMentionsForActor(i),
						}); err != nil {
							log.Printf("[ASK] original response edit with image attachment failed: %v", err)
						}
						return
					}

					if call := structuredToolCallFromAskResult(result); call != nil {
						if call.Tool == "web_search" {
							answer, err := executeWebSearchAndSynthesize(ctx, provider, cfg, call.Query, reqPrompt)
							if err != nil {
								if ctx.Err() != nil || lifecycle.IsShuttingDown() {
									return
								}
								if !ai.IsUserFacingError(err) {
									log.Printf("[ASK] web_search synthesize %s: %v", cfg.Provider, err)
								}
								embed = embeds.Error("Web Search Error", ai.UserFacingError(err))
								_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{
									Embeds:          &[]*discordgo.MessageEmbed{embed},
									Components:      &[]discordgo.MessageComponent{},
									AllowedMentions: allowedMentionsForActor(i),
								})
								return
							}
							answer = sanitizeAssistantVisibleText(answer, reqPrompt)
							if err := editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{
								Content:         stringPtr(plainAskResponseContent(answer)),
								AllowedMentions: allowedMentionsForActor(i),
							}); err != nil {
								log.Printf("[ASK] web_search response edit failed: %v", err)
							}
							return
						}
						if handleDirectReadTool(s, i, call) {
							return
						}
						auditInteraction(i, "tool_call_proposed", "success", toolAuditFields(auditViewFromToolCall(call.Tool, call.User, "", call.Reason)))
						if err := presentToolConfirmation(s, i, call); err != nil {
							log.Printf("[ASK] tool confirmation: %v", err)
							embed = toolExecutionErrorEmbed(err)
							if err := editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{
								Embeds:          &[]*discordgo.MessageEmbed{embed},
								Components:      &[]discordgo.MessageComponent{},
								AllowedMentions: allowedMentionsForActor(i),
							}); err != nil {
								log.Printf("[ASK] original response edit failed after retries: %v", err)
							}
						}
						return
					}

					answer := sanitizeAssistantVisibleText(result.Text, reqPrompt)
					if err := editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{
						Content:         stringPtr(plainAskResponseContent(answer)),
						AllowedMentions: allowedMentionsForActor(i),
					}); err != nil {
						log.Printf("[ASK] original response edit failed after retries: %v", err)
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

func sanitizeAssistantVisibleText(answer string, prompt ai.PromptEnvelope) string {
	answer = cleanThoughtTags(answer)
	if looksLikePromptLeak(answer, prompt) {
		return promptLeakRefusalMessage
	}
	return answer
}

func looksLikePromptLeak(answer string, prompt ai.PromptEnvelope) bool {
	normalizedAnswer := normalizePromptLeakText(answer)
	if normalizedAnswer == "" {
		return false
	}

	suspiciousMarkers := []string{
		"base system (highest priority)",
		"guild system (lower priority",
		"system prompt",
		"developer prompt",
		"developer message",
		"hidden instructions",
		"internal instructions",
		"must never override base system",
	}
	for _, marker := range suspiciousMarkers {
		if strings.Contains(normalizedAnswer, marker) {
			return true
		}
	}

	sections := []string{
		prompt.BaseSystem,
		prompt.GuildSystem,
		ai.BaseSystemPrompt,
	}
	for _, section := range sections {
		if promptSectionLeaked(normalizedAnswer, section) {
			return true
		}
	}

	return false
}

func promptSectionLeaked(normalizedAnswer, section string) bool {
	normalizedSection := normalizePromptLeakText(section)
	if normalizedSection == "" {
		return false
	}
	if strings.Contains(normalizedAnswer, normalizedSection) {
		return true
	}

	const minFragmentLen = 48
	if len(normalizedSection) < minFragmentLen {
		return false
	}
	for start := 0; start+minFragmentLen <= len(normalizedSection); start += 12 {
		fragment := strings.TrimSpace(normalizedSection[start : start+minFragmentLen])
		if fragment != "" && strings.Contains(normalizedAnswer, fragment) {
			return true
		}
	}
	return false
}

func normalizePromptLeakText(s string) string {
	s = strings.ToLower(s)

	var sb strings.Builder
	sb.Grow(len(s))

	prevSpace := true
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			if !prevSpace {
				sb.WriteByte(' ')
				prevSpace = true
			}
		case unicode.IsControl(r):
			continue
		default:
			sb.WriteRune(r)
			prevSpace = false
		}
	}

	return strings.TrimSpace(sb.String())
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

func toolRequiresMemberResolution(tool string) bool {
	switch tool {
	case "ban", "kick", "timeout", "untimeout", "warn", "clear_warnings",
		"move_to_voice", "disconnect_voice", "deafen", "undeafen", "assign_role", "remove_role":
		return true
	}
	return false
}

func presentToolConfirmation(s *discordgo.Session, i *discordgo.InteractionCreate, call *ai.ToolCall) error {
	var candidates []ai.MemberCandidate
	if toolRequiresMemberResolution(call.Tool) {
		var err error
		candidates, err = ai.ResolveMembers(s, i.GuildID, call.User)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			return fmt.Errorf("no members matched %q", call.User)
		}
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
	if toolRequiresMemberResolution(call.Tool) {
		if len(candidates) == 1 {
			candidate := candidates[0]
			selected = &candidate
			call.User = candidate.Member.User.ID
			call.Confirmation = confirmationMessage(call, candidate)
		}
	} else {
		call.Confirmation = ai.ConfirmationText(call, "")
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
			notice := "The proposed moderation action has expired."
			_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{
				Content: &notice,
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
			notice := "The proposed moderation action was cancelled."
			_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{
				Content: &notice,
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

		if selected == nil && toolRequiresMemberResolution(call.Tool) {
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
		var notice string
		if data.CustomID == confirmID {
			if err := ai.ExecuteTool(s, i.GuildID, i.ChannelID, i.Member, call); err != nil {
				auditInteraction(ic, "tool_call_confirmed", toolExecutionOutcome(err), mergeAuditFields(
					toolAuditFields(auditViewFromToolCall(call.Tool, requestedTarget, call.User, call.Reason)),
					map[string]any{"error": err.Error()},
				))
				embed = toolExecutionErrorEmbed(err)
				notice = "The proposed moderation action was confirmed but failed to execute."
			} else {
				auditInteraction(ic, "tool_call_confirmed", "success", toolAuditFields(auditViewFromToolCall(call.Tool, requestedTarget, call.User, call.Reason)))
				embed = embeds.AIConfirmation("Moderation action completed.")
				notice = "The proposed moderation action has been confirmed and executed."
			}
		}

		_ = s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Embeds:     []*discordgo.MessageEmbed{embed},
				Components: []discordgo.MessageComponent{},
			},
		})

		if notice != "" {
			_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{
				Content: &notice,
			})
		}

		once.Do(func() {
			close(done)
			removeHandler()
		})
	})

	var initialEmbed *discordgo.MessageEmbed
	var initialComponents []discordgo.MessageComponent
	if selected != nil || !toolRequiresMemberResolution(call.Tool) {
		initialEmbed = embeds.AIConfirmation(call.Confirmation)
		initialComponents = confirmationComponents(confirmID, cancelID)
	} else {
		initialEmbed, initialComponents = memberSelectionView(call, candidates, 0, selectID, pagePrefix, cancelID)
	}

	publicNotice := "A moderation action requires private confirmation from the requester."
	if err := editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{
		Content:         &publicNotice,
		AllowedMentions: allowedMentionsForActor(i),
		Components:      &[]discordgo.MessageComponent{},
	}); err != nil {
		removeHandler()
		return err
	}

	if err := sendEphemeralFollowupWithRetry(s, i, initialEmbed, initialComponents); err != nil {
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
			notice := "The proposed moderation action has expired."
			_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{
				Content: &notice,
			})
		}
	})

	return nil
}

func toolExecutionErrorEmbed(err error) *discordgo.MessageEmbed {
	return embeds.Error("AI Action Failed", ai.UserFacingToolExecutionError(err))
}

func allowedMentionsForActor(i *discordgo.InteractionCreate) *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{
		Parse: []discordgo.AllowedMentionType{},
	}
}

// isNonRetryableDiscordEditError returns true for Discord errors that mean the
// target message or interaction no longer exists. Retrying would always fail.
func isNonRetryableDiscordEditError(err error) bool {
	var restErr *discordgo.RESTError
	if !errors.As(err, &restErr) || restErr == nil || restErr.Message == nil {
		return false
	}
	switch restErr.Message.Code {
	case discordgo.ErrCodeUnknownMessage,  // 10008 — message deleted (e.g. by purge)
		10062:                              // 10062 — interaction expired / unknown interaction
		return true
	}
	return false
}

func editDeferredInteractionResponseWithRetry(s *discordgo.Session, i *discordgo.InteractionCreate, edit *discordgo.WebhookEdit) error {
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
		_, lastErr = s.InteractionResponseEdit(i.Interaction, edit)
		if lastErr == nil {
			return nil
		}
		// These errors mean the message/interaction no longer exists — no point retrying.
		if isNonRetryableDiscordEditError(lastErr) {
			return nil
		}
		log.Printf("[ASK] deferred edit attempt %d/%d failed: %v", attempt+1, maxAttempts, lastErr)
	}

	return lastErr
}

func sendEphemeralFollowupWithRetry(s *discordgo.Session, i *discordgo.InteractionCreate, embed *discordgo.MessageEmbed, components []discordgo.MessageComponent) error {
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
		_, lastErr = s.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
			Embeds:          []*discordgo.MessageEmbed{embed},
			Components:      components,
			AllowedMentions: allowedMentionsForActor(i),
			Flags:           discordgo.MessageFlagsEphemeral,
		})
		if lastErr == nil {
			return nil
		}
		log.Printf("[ASK] ephemeral followup attempt %d/%d failed: %v", attempt+1, maxAttempts, lastErr)
	}

	return lastErr
}

func plainAskResponseContent(answer string) string {
	const maxContentLen = 2000
	const truncationSuffix = "\n\n*Response truncated...*"

	answer = strings.TrimSpace(answer)
	if len(answer) <= maxContentLen {
		return answer
	}

	runes := []rune(answer)
	suffixRunes := []rune(truncationSuffix)
	limit := maxContentLen - len(suffixRunes)
	if limit <= 0 {
		return string(suffixRunes[:maxContentLen])
	}
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes) + truncationSuffix
}

func stringPtr(s string) *string {
	return &s
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

func handleDirectReadTool(s *discordgo.Session, i *discordgo.InteractionCreate, call *ai.ToolCall) bool {
	if call.Tool != "member_info" {
		return handleReadTool(s, i, call)
	}

	candidates, err := ai.ResolveMembers(s, i.GuildID, call.User)
	if err != nil || len(candidates) == 0 {
		embed := embeds.Error("Member Not Found", fmt.Sprintf("No members matched %q", call.User))
		_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{
			Embeds: &[]*discordgo.MessageEmbed{embed},
		})
		return true
	}

	candidate := candidates[0]
	if candidate.Member == nil || candidate.Member.User == nil {
		embed := embeds.Error("Member Not Found", "Failed to resolve member details.")
		_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{
			Embeds: &[]*discordgo.MessageEmbed{embed},
		})
		return true
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

	embed := &discordgo.MessageEmbed{
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
				Name:   "Account Created At",
				Value:  createdAt,
				Inline: false,
			},
			{
				Name:   "Roles",
				Value:  rolesDesc,
				Inline: false,
			},
		},
	}

	_ = editDeferredInteractionResponseWithRetry(s, i, &discordgo.WebhookEdit{
		Embeds: &[]*discordgo.MessageEmbed{embed},
	})
	return true
}

func executeWebSearchAndSynthesize(ctx context.Context, provider ai.Provider, cfg *database.GuildConfig, query string, basePrompt ai.PromptEnvelope) (string, error) {
	results, err := ai.CallTavilySearch(ctx, query)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("Web search results for: ")
	sb.WriteString(query)
	sb.WriteString("\n\n")
	for idx, r := range results {
		sb.WriteString(fmt.Sprintf("[%d] %s\n%s\n\n", idx+1, r.Title, r.Content))
	}

	synthesisPrompt := ai.PromptEnvelope{
		BaseSystem:  basePrompt.BaseSystem,
		GuildSystem: basePrompt.GuildSystem,
		UserPrompt:  sb.String() + "Using only the information above, answer the user's original question: " + query,
	}

	result, err := provider.Ask(ctx, cfg.APIKey, cfg.Model, synthesisPrompt, nil)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}
