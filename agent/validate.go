// validate.go contains input validation for the incident prompt.
package agent

import (
	"strings"
	"unicode/utf8"

	"github.com/org/incident-agent/models"
)

// maxPromptChars is the maximum allowed prompt length, measured in characters
// (Unicode code points), per Requirements 1.1 and 1.3.
const maxPromptChars = 10_000

// ValidatePrompt returns a *models.PromptValidationError if the prompt is
// empty/whitespace-only or exceeds 10,000 characters. Returns nil otherwise.
//
// The length check counts characters (runes) via utf8.RuneCountInString rather
// than bytes (len), because Requirements 1.1 and 1.3 specify the limit in
// "characters". Using len() would incorrectly reject valid multi-byte UTF-8
// prompts (e.g. accented Latin, CJK, emoji) whose byte length exceeds the rune
// count. For pure ASCII input the two measures are identical.
func ValidatePrompt(prompt string) error {
	if strings.TrimSpace(prompt) == "" {
		return &models.PromptValidationError{Message: "prompt must not be empty or whitespace-only"}
	}
	if utf8.RuneCountInString(prompt) > maxPromptChars {
		return &models.PromptValidationError{Message: "prompt must not exceed 10,000 characters"}
	}
	return nil
}
