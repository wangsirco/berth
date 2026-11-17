package desktop

import (
	"context"
	"os"
	"testing"
)

// TestInstallLive installs the basic desktop for real; run on a throwaway machine:
// BERTH_LIVE_DESKTOP=1 ./desktop.test -test.run TestInstallLive -test.v
func TestInstallLive(t *testing.T) {
	if os.Getenv("BERTH_LIVE_DESKTOP") == "" {
		t.Skip("set BERTH_LIVE_DESKTOP=1 on a throwaway machine")
	}
	err := Install(context.Background(), []string{"editor"}, func(s string) { t.Log(s) })
	if err != nil {
		t.Fatal(err)
	}
}
