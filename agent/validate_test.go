// validate_test.go contains unit tests (task 4.4) and property-based tests
// (tasks 4.2, 4.3) for ValidatePrompt.
//
// Length is validated in characters (runes) — see the ValidatePrompt doc
// comment in validate.go for the bytes-vs-runes decision.
package agent_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/org/incident-agent/agent"
	"github.com/org/incident-agent/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// maxPromptChars mirrors the limit enforced by ValidatePrompt.
const maxPromptChars = 10_000

// requirePromptValidationError asserts that err is a non-nil
// *models.PromptValidationError.
func requirePromptValidationError(t require.TestingT, err error) {
	require.Error(t, err)
	var pve *models.PromptValidationError
	require.True(t, errors.As(err, &pve), "expected *models.PromptValidationError, got %T", err)
}

// Task 4.4 — Unit tests for ValidatePrompt.
// Requirements: 1.1, 1.2, 1.3
func TestValidatePrompt_EmptyString(t *testing.T) {
	err := agent.ValidatePrompt("")
	requirePromptValidationError(t, err)
}

func TestValidatePrompt_SpacesOnly(t *testing.T) {
	err := agent.ValidatePrompt("     ")
	requirePromptValidationError(t, err)
}

func TestValidatePrompt_TabsAndNewlines(t *testing.T) {
	err := agent.ValidatePrompt("\t\n\t")
	requirePromptValidationError(t, err)
}

func TestValidatePrompt_ExactlyMaxLength(t *testing.T) {
	prompt := strings.Repeat("a", maxPromptChars)
	err := agent.ValidatePrompt(prompt)
	assert.NoError(t, err)
}

func TestValidatePrompt_OneOverMaxLength(t *testing.T) {
	prompt := strings.Repeat("a", maxPromptChars+1)
	err := agent.ValidatePrompt(prompt)
	requirePromptValidationError(t, err)
}

func TestValidatePrompt_ValidMultiLine(t *testing.T) {
	prompt := "INC-1234 is failing.\nUsers see 5xx errors on checkout.\nStarted ~10 minutes ago."
	err := agent.ValidatePrompt(prompt)
	assert.NoError(t, err)
}

// Task 4.2 — Property-based test.
// Feature: incident-investigator, Property 1: Whitespace and empty prompts are always rejected
// Validates: Requirements 1.2
func TestProperty1_WhitespacePromptsRejected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Build a string composed only of whitespace runes. Length 0..1000
		// covers the empty string and arbitrary whitespace-only prompts.
		wsRunes := rapid.SliceOfN(
			rapid.SampledFrom([]rune{' ', '\t', '\n', '\r'}),
			0, 1000,
		).Draw(t, "whitespace_runes")

		prompt := string(wsRunes)

		err := agent.ValidatePrompt(prompt)
		requirePromptValidationError(t, err)
	})
}

// Task 4.3 — Property-based test.
// Feature: incident-investigator, Property 2: Over-length prompts are always rejected
// Validates: Requirements 1.3
func TestProperty2_OverlengthPromptsRejected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Draw a length strictly greater than the max, then build a
		// non-whitespace string of that many characters so the over-length
		// check is exercised (not the empty/whitespace check). Vary the
		// filler character across iterations to avoid a fixed-content test.
		n := rapid.IntRange(maxPromptChars+1, 12_000).Draw(t, "length")
		fill := rapid.SampledFrom([]string{"a", "b", "c", "x", "z", "1", "λ"}).Draw(t, "fill")
		prompt := strings.Repeat(fill, n)

		// Sanity: the generated prompt is non-whitespace and over the limit.
		require.NotEmpty(t, strings.TrimSpace(prompt))

		err := agent.ValidatePrompt(prompt)
		requirePromptValidationError(t, err)
	})
}
