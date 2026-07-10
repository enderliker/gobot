package ai

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
)

var (
	ErrUnknownProvider          = errors.New("unknown AI provider")
	genericProviderErrorMessage = "The AI provider returned an unexpected error. Please try again later."
	querySecretPattern          = regexp.MustCompile(`(?i)([?&](?:key|api[_-]?key|access[_-]?token)=)[^&\s]+`)
	bearerSecretPattern         = regexp.MustCompile(`(?i)(bearer\s+)[^\s,;]+`)
	inlineSecretPattern         = regexp.MustCompile(`(?i)((?:x-)?api[_-]?key(?:=|:)\s*)[^\s,;]+`)
)

func IsUserFacingError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "401"), strings.Contains(msg, "unauthorized"), strings.Contains(msg, "invalid api key"):
		return true
	case strings.Contains(msg, "403"):
		return true
	case strings.Contains(msg, "429"), strings.Contains(msg, "quota"), strings.Contains(msg, "rate limit"), strings.Contains(msg, "resource_exhausted"):
		return true
	}
	return false
}

func UserFacingError(err error) string {
	if err == nil {
		return "Unknown error"
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "The AI provider took too long to respond. Please try again in a moment."
	}

	msg, ok := sanitizeErrorMessage(err.Error())
	if !ok {
		return genericProviderErrorMessage
	}

	msg = strings.ToLower(msg)

	switch {
	case strings.Contains(msg, "401"), strings.Contains(msg, "unauthorized"), strings.Contains(msg, "invalid api key"):
		return "The configured API key is invalid or has expired."
	case strings.Contains(msg, "403"):
		return "Access to the selected AI model is not permitted for this API key."
	case strings.Contains(msg, "error 1020"), strings.Contains(msg, "access denied"):
		return "The AI provider blocked the request through its security protection layer."
	case strings.Contains(msg, "error 1015"):
		return "The AI provider is temporarily rate limiting requests. Please try again in a few minutes."
	case strings.Contains(msg, "429"), strings.Contains(msg, "quota"), strings.Contains(msg, "rate limit"):
		return "The provider quota or rate limit has been reached. Please try again later."
	case strings.Contains(msg, "<html"), strings.Contains(msg, "cloudflare"), strings.Contains(msg, "attention required"):
		return "The AI provider returned a protection or verification page instead of a normal response."
	case strings.Contains(msg, "no choices"), strings.Contains(msg, "no content"), strings.Contains(msg, "no candidates"):
		return "The AI provider returned an empty response."
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "no such host"), strings.Contains(msg, "dial tcp"):
		return "Unable to connect to the AI provider."
	default:
		return genericProviderErrorMessage
	}
}

type sanitizedError struct {
	msg   string
	cause error
}

func (e *sanitizedError) Error() string { return e.msg }

func (e *sanitizedError) Unwrap() error { return e.cause }

func sanitizeProviderError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}

	msg, ok := sanitizeErrorMessage(err.Error(), secrets...)
	if !ok {
		msg = "request to AI provider failed"
	}

	return &sanitizedError{
		msg:   msg,
		cause: err,
	}
}

func sanitizeErrorMessage(msg string, secrets ...string) (string, bool) {
	sanitized := msg
	secretForms := make([]string, 0, len(secrets)*3)

	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		secretForms = append(secretForms, secret, url.QueryEscape(secret), url.PathEscape(secret))
	}

	for _, secret := range secretForms {
		sanitized = strings.ReplaceAll(sanitized, secret, "[REDACTED]")
	}

	sanitized = querySecretPattern.ReplaceAllString(sanitized, "${1}[REDACTED]")
	sanitized = bearerSecretPattern.ReplaceAllString(sanitized, "${1}[REDACTED]")
	sanitized = inlineSecretPattern.ReplaceAllString(sanitized, "${1}[REDACTED]")

	for _, secret := range secretForms {
		if secret != "" && strings.Contains(sanitized, secret) {
			return "", false
		}
	}

	return sanitized, true
}

func IsImageModel(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "imagen-") || strings.Contains(m, "dall-e")
}
