// analyse.go extracts a structured IncidentContext from the raw prompt.
package agent

import (
	"regexp"

	"github.com/org/incident-agent/models"
)

var incidentIDRe = regexp.MustCompile(`INC-\d+`)

// AnalysePrompt extracts incident_id and context fields from the prompt.
// Full implementation arrives in task 4.5; this stub satisfies compilation.
func AnalysePrompt(prompt string) models.IncidentContext {
	ic := models.IncidentContext{
		RawPrompt:        prompt,
		AffectedServices: []string{},
	}

	if m := incidentIDRe.FindString(prompt); m != "" {
		ic.IncidentID = m
	}

	if len(prompt) > 500 {
		ic.ContextText = prompt[:500]
	} else {
		ic.ContextText = prompt
	}

	if len(prompt) > 100 {
		ic.SymptomHint = prompt[:100]
	} else {
		ic.SymptomHint = prompt
	}

	return ic
}
