package failpoint_test

import (
	"testing"

	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/failpoint"
)

func TestHit(t *testing.T) {
	t.Run("Given a binary built without the failpoints tag/When a failpoint is reached/Then nothing happens", func(t *testing.T) {
		// Given
		if failpoint.Enabled {
			t.Skip("this binary was built with failpoints enabled")
		}
		t.Setenv("FAILPOINT", "consumer.after_commit_before_delete")

		// When
		failpoint.Hit("consumer.after_commit_before_delete")
		failpoint.New().Hit("consumer.after_commit_before_delete")

		// Then
		t.Log("the process is still running, which is the expected behaviour")
	})
}
