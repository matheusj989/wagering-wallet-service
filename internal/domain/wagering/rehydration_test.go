package wagering_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
)

func validSnapshot(t *testing.T) wagering.Snapshot {
	t.Helper()
	return wagering.Snapshot{ID: uuid.New(), WalletID: uuid.New(), PlayerID: uuid.New(), Origin: wagering.HTTP, Kind: wagering.Bet,
		Status: wagering.Processed, Amount: brl(t, "25.00"), ResultBalance: brl(t, "75.00"), ProviderID: "provider-a",
		ExternalTransactionID: "bet", IdempotencyKey: "key", PayloadHash: hash, RoundID: "round", GameID: "game", CorrelationID: "correlation",
		CreatedAt: now, UpdatedAt: now, CompletedAt: now}
}

func TestTransactionRehydration(t *testing.T) {
	cases := []struct {
		name   string
		change func(*wagering.Snapshot)
	}{
		{"uninitialised money", func(s *wagering.Snapshot) { s.Amount = money.Money{} }},
		{"empty provider", func(s *wagering.Snapshot) { s.ProviderID = "" }},
		{"missing completion", func(s *wagering.Snapshot) { s.CompletedAt = time.Time{} }},
		{"missing result", func(s *wagering.Snapshot) { s.ResultBalance = money.Money{} }},
		{"unresolved reversal", func(s *wagering.Snapshot) { s.Kind = wagering.Refund; s.ReferenceExternalID = "bet-0" }},
		{"failure on processed", func(s *wagering.Snapshot) { s.FailureCode = wagering.InsufficientFunds }},
		{"pending bet", func(s *wagering.Snapshot) {
			s.Status = wagering.PendingReference
			s.CompletedAt = time.Time{}
			s.ResultBalance = money.Money{}
			s.NextAttemptAt = now
			s.ReferenceDeadlineAt = now.Add(time.Minute)
		}},
		{"terminal schedule", func(s *wagering.Snapshot) { s.NextAttemptAt = now }},
		{"negative attempts", func(s *wagering.Snapshot) { s.ReferenceAttempts = -1 }},
		{"self reference", func(s *wagering.Snapshot) { s.ReferenceTransactionID = s.ID }},
	}
	for _, tc := range cases {
		t.Run("Given "+tc.name+"/When persisted state is rehydrated/Then it is rejected", func(t *testing.T) {
			// Given
			snapshot := validSnapshot(t)
			tc.change(&snapshot)
			// When
			_, err := wagering.Rehydrate(snapshot)
			// Then
			if err == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
	t.Run("Given a valid snapshot/When its caller changes its copy/Then the restored transaction stays unchanged", func(t *testing.T) {
		// Given
		snapshot := validSnapshot(t)
		restored, err := wagering.Rehydrate(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		// When
		snapshot.Amount = brl(t, "99.00")
		snapshot.Status = wagering.Failed
		// Then
		if restored.Amount().String() != "25.00" || restored.Status() != wagering.Processed {
			t.Fatal("snapshot leaked mutable state")
		}
	})
}
