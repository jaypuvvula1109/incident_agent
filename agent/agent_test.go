// agent_test.go contains tests for InvestigatorAgent.
// Full tests are added in tasks 10.2–10.6.
package agent_test

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
