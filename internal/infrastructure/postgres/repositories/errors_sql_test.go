package repositories

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	contracts "github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
)

func TestRetryClassification(t *testing.T) {
	for _, code := range []string{"40001", "40P01"} {
		t.Run("Given SQLSTATE "+code+"/When an aborted transaction is classified/Then retry exhaustion remains transient", func(t *testing.T) {
			// Given
			failure := &pgconn.PgError{Code: code, Message: "transaction aborted"}
			// When
			err := Classify(failure)
			// Then
			if !Retryable(err) || !errors.Is(err, contracts.ErrTransient) {
				t.Fatalf("lost retry classification: %v", err)
			}
			if errors.Is(err, contracts.ErrCommitOutcomeUnknown) {
				t.Fatal("known abort must not be uncertain")
			}
		})
	}
}
