package probe

import (
	"testing"
	"time"
)

// TestTheQueueDrains waits with a fixed pause and hopes the background work
// finished. The program states the rule in one line: `time.Sleep` does not
// synchronize a test.
func TestTheQueueDrains(t *testing.T) {
	time.Sleep(50 * time.Millisecond)
	if !drained() {
		t.Errorf("the queue still holds work")
	}
}
