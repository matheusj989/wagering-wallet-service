package app_test

import (
	"testing"

	"go.uber.org/fx"

	"github.com/matheusj989/wagering-wallet-service/internal/app"
)

func TestOptions(t *testing.T) {
	t.Run("Given the application graph/When it is validated/Then no dependency is missing", func(t *testing.T) {
		// Given, When
		err := fx.ValidateApp(app.Options())

		// Then
		if err != nil {
			t.Fatalf("the dependency graph is not complete: %v", err)
		}
	})
}
