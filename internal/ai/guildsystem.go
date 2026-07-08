package ai

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type GuildSystemPromptValidationError struct {
	Code    string
	Message string
}

func (e *GuildSystemPromptValidationError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

type GuildSystemPromptDenyRule struct {
	Name    string
	Pattern *regexp.Regexp
}

// GuildSystemPromptDenyRules blocks obvious prompt-injection scaffolding at
// write time. This is defense in depth only; persisted prompts still go through
// PromptEnvelope sectioning plus the BaseSystem-over-GuildSystem precedence
// rule in prompt.go.
var GuildSystemPromptDenyRules = []GuildSystemPromptDenyRule{
	{
		Name:    "system_block_headings",
		Pattern: regexp.MustCompile(`(?im)(?:^|\n)\s*(?:BASE SYSTEM|GUILD SYSTEM)\b|(?:^|\n)\s*SYSTEM:\s*`),
	},
	{
		Name:    "markdown_system_markers",
		Pattern: regexp.MustCompile(`(?m)^\s*#{3,}\s*\S`),
	},
	{
		Name:    "system_instruction_tokens",
		Pattern: regexp.MustCompile("(?is)\\[inst\\]|<\\|[^>\\r\\n]{1,100}\\|>|```\\s*system\\b"),
	},
	{
		Name:    "direct_override_phrases",
		Pattern: regexp.MustCompile(`(?i)(?:\b(?:ignore previous instructions?|ignore all previous rules|disregard the above|you are now|forget your instructions|ignora las instrucciones anteriores|olvida tus reglas|a partir de ahora sos)\b|new instructions:)`),
	},
	{
		Name:    "role_impersonation",
		Pattern: regexp.MustCompile(`(?i)(?:system role:|developer message:|\bas the system\b)`),
	},
}

func NormalizeGuildSystemPrompt(prompt string) string {
	prompt = strings.ReplaceAll(prompt, "\r\n", "\n")
	prompt = strings.ReplaceAll(prompt, "\r", "\n")
	return strings.TrimSpace(prompt)
}

func ValidateGuildSystemPrompt(prompt string) error {
	prompt = NormalizeGuildSystemPrompt(prompt)
	if prompt == "" {
		return &GuildSystemPromptValidationError{
			Code:    "empty_prompt",
			Message: "The guild system prompt cannot be empty. Use Clear if you want to remove it.",
		}
	}

	length := utf8.RuneCountInString(prompt)
	if length > MaxGuildSystemPromptChars {
		return &GuildSystemPromptValidationError{
			Code: "length_exceeded",
			Message: fmt.Sprintf(
				"This prompt is %d characters long. The maximum allowed length is %d characters.",
				length,
				MaxGuildSystemPromptChars,
			),
		}
	}

	for _, r := range prompt {
		if unicode.IsControl(r) && r != '\n' {
			return &GuildSystemPromptValidationError{
				Code:    "control_characters",
				Message: "The prompt contains unsupported control characters. Only normal line breaks are allowed.",
			}
		}
	}

	if matchesGuildSystemDenylist(prompt) {
		return &GuildSystemPromptValidationError{
			Code:    "denylist_match",
			Message: "This content appears to be trying to overwrite system instructions. Remove system/developer-style directives and try again.",
		}
	}

	return nil
}

func matchesGuildSystemDenylist(prompt string) bool {
	for _, rule := range GuildSystemPromptDenyRules {
		if rule.Pattern.MatchString(prompt) {
			return true
		}
	}
	return false
}
