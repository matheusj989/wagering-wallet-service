//go:build integration

package testenv

import (
	"os/exec"
	"testing"
)

func TestProcessExitValidation(t *testing.T) {
	scenarios := []struct {
		name, script, output  string
		kill, stop, wantError bool
	}{
		{"expected graceful exit", "exit 0", "", false, true, false},
		{"unexpected successful exit", "exit 0", "", false, false, true},
		{"unexpected error while failpoint armed", "exit 1", "", true, false, true},
		{"expected SIGKILL", "kill -KILL $$", "", true, false, false},
		{"unexpected SIGKILL", "kill -KILL $$", "", false, false, true},
		{"race exit while failpoint armed", "exit 66", "", true, false, true},
		{"race diagnostic despite expected kill", "kill -KILL $$", "WARNING: DATA RACE", true, false, true},
	}
	for _, scenario := range scenarios {
		t.Run("Given "+scenario.name+"/When the supervisor checks the process/Then only the expected exit is accepted", func(t *testing.T) {
			// Given
			command := exec.Command("sh", "-c", scenario.script)
			// When
			err := checkProcessExit(command.Run(), scenario.output, scenario.kill, scenario.stop)
			// Then
			if (err != nil) != scenario.wantError {
				t.Fatalf("validation=%v wantError=%v", err, scenario.wantError)
			}
		})
	}
}
