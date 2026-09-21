//go:build integration

package integration_test

import (
	"os"
	"testing"

	"github.com/matheusj989/wagering-wallet-service/test/testenv"
)

func TestMain(m *testing.M) {
	os.Exit(testenv.Run(m))
}
