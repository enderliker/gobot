package ai

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
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
	normalizedPrompt := normalizeGuildSystemPromptLineEndings(prompt)
	if strings.TrimSpace(normalizedPrompt) == "" {
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

	for _, r := range normalizedPrompt {
		if unicode.IsControl(r) && r != '\n' {
			return &GuildSystemPromptValidationError{
				Code:    "control_characters",
				Message: "The prompt contains unsupported control characters. Only normal line breaks are allowed.",
			}
		}
	}

	if matchesGuildSystemDenylist(normalizedPrompt) {
		return &GuildSystemPromptValidationError{
			Code:    "denylist_match",
			Message: "This content appears to be trying to overwrite system instructions. Remove system/developer-style directives and try again.",
		}
	}

	return nil
}

func matchesGuildSystemDenylist(prompt string) bool {
	prompt = normalizeGuildSystemPromptForDenylist(prompt)
	for _, rule := range GuildSystemPromptDenyRules {
		if rule.Pattern.MatchString(prompt) {
			return true
		}
	}
	return false
}

func normalizeGuildSystemPromptLineEndings(prompt string) string {
	prompt = strings.ReplaceAll(prompt, "\r\n", "\n")
	return strings.ReplaceAll(prompt, "\r", "\n")
}

func normalizeGuildSystemPromptForDenylist(prompt string) string {
	prompt = normalizeGuildSystemPromptLineEndings(prompt)
	prompt = norm.NFKC.String(prompt)

	var sb strings.Builder
	sb.Grow(len(prompt))

	prevSpace := true
	for _, r := range prompt {
		if mapped, ok := guildSystemPromptHomoglyphs[r]; ok {
			r = mapped
		}

		switch {
		case shouldStripInvisiblePromptRune(r):
			continue
		case unicode.IsSpace(r):
			if !prevSpace {
				sb.WriteByte(' ')
				prevSpace = true
			}
		case unicode.IsControl(r):
			continue
		default:
			sb.WriteRune(unicode.ToLower(r))
			prevSpace = false
		}
	}

	return strings.TrimSpace(sb.String())
}

func shouldStripInvisiblePromptRune(r rune) bool {
	if unicode.Is(unicode.Cf, r) {
		return true
	}
	switch r {
	case '\u00ad', '\u034f', '\u180e', '\u200b', '\u200c', '\u200d', '\u2060', '\ufeff':
		return true
	default:
		return false
	}
}

var guildSystemPromptHomoglyphs = map[rune]rune{
	'\u0391': 'a',
	'\u03b1': 'a',
	'\u0410': 'a',
	'\u0430': 'a',
	'\u0392': 'b',
	'\u0412': 'b',
	'\u03f2': 'c',
	'\u0421': 'c',
	'\u0441': 'c',
	'\u0395': 'e',
	'\u03b5': 'e',
	'\u0415': 'e',
	'\u0435': 'e',
	'\u0397': 'h',
	'\u041d': 'h',
	'\u043d': 'h',
	'\u0399': 'i',
	'\u03b9': 'i',
	'\u0406': 'i',
	'\u0456': 'i',
	'\u039a': 'k',
	'\u03ba': 'k',
	'\u041a': 'k',
	'\u043a': 'k',
	'\u039c': 'm',
	'\u03bc': 'm',
	'\u041c': 'm',
	'\u043c': 'm',
	'\u039d': 'n',
	'\u03bd': 'n',
	'\u039f': 'o',
	'\u03bf': 'o',
	'\u041e': 'o',
	'\u043e': 'o',
	'\u03a1': 'p',
	'\u03c1': 'p',
	'\u0420': 'p',
	'\u0440': 'p',
	'\u03a4': 't',
	'\u03c4': 't',
	'\u0422': 't',
	'\u0442': 't',
	'\u03a5': 'y',
	'\u03c5': 'y',
	'\u0423': 'y',
	'\u0443': 'y',
	'\u03a7': 'x',
	'\u03c7': 'x',
	'\u0425': 'x',
	'\u0445': 'x',
	'\u0408': 'j',
	'\u0458': 'j',
}
