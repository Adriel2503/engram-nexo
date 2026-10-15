package cloudserver

import (
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/dashboard"
)

// The original login contract must also hold after a Nexo server has enabled
// its process-wide brand, regardless of the order of the package's tests.
func TestNexoDashboardLoginFlow(t *testing.T) {
	dashboard.EnableNexoBrand()
	t.Run("nexo", TestHandlerDashboardLoginFlowSetsCookieForBrowserUse)
}
