package system

import (
	"errors"
	"strings"
	"testing"
)

func TestKilledExplainsOOM(t *testing.T) {
	if e := Killed(errors.New("dnf: signal: killed")); !strings.Contains(e.Error(), "内存不足") || !strings.Contains(e.Error(), "out of memory") {
		t.Fatalf("not explained: %v", e)
	}
	other := errors.New("dnf: exit status 1")
	if Killed(other) != other || Killed(nil) != nil {
		t.Fatal("other errors must pass through")
	}
}
