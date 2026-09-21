package idempotency_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/application/idempotency"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
)

const (
	referencePlayer = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
	referenceWallet = "0192f291-27dd-7d3f-8071-5f8685deef37"
	referenceDigest = "629836932b79106b99523d06a1e7fa80689b0ea1e1c47aa3f0a5a2c87d0c4344"
	reversalDigest  = "d4a1494bf003c53fb7f83d4ba1ed9486418b3d5acfc44e35ce76030160e80749"
)

func referencePayload(t *testing.T) idempotency.Payload {
	t.Helper()
	amount, err := money.Parse("25.00", "BRL")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	return idempotency.Payload{
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		PlayerID:              uuid.MustParse(referencePlayer),
		WalletID:              uuid.MustParse(referenceWallet),
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  wagering.Bet,
		Amount:                amount,
	}
}

func hashOf(t *testing.T, payload idempotency.Payload) string {
	t.Helper()
	digest, err := idempotency.Hash(payload)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}
	return digest
}

func TestHash(t *testing.T) {
	t.Run("Given the reference payload/When it is hashed/Then the digest is stable and lowercase hexadecimal", func(t *testing.T) {
		// Given
		payload := referencePayload(t)

		// When
		digest := hashOf(t, payload)

		// Then
		if len(digest) != 64 {
			t.Fatalf("digest length = %d, want 64", len(digest))
		}
		for _, character := range digest {
			if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
				t.Fatalf("digest %q must be lowercase hexadecimal", digest)
			}
		}
		if digest != hashOf(t, referencePayload(t)) {
			t.Error("the same payload must always produce the same digest")
		}
	})

	t.Run("Given the same business content from HTTP and SQS/When both are hashed/Then the digests match", func(t *testing.T) {
		// Given
		fromHTTP := referencePayload(t)
		fromQueue := referencePayload(t)
		fromQueue.PlayerID = uuid.MustParse(referencePlayer)

		// When
		httpDigest := hashOf(t, fromHTTP)
		queueDigest := hashOf(t, fromQueue)

		// Then
		if httpDigest != queueDigest {
			t.Errorf("digests differ: %s and %s", httpDigest, queueDigest)
		}
	})

	t.Run("Given identifiers in uppercase/When they are hashed/Then the digest does not change", func(t *testing.T) {
		// Given
		lowercase := referencePayload(t)
		uppercase := referencePayload(t)
		uppercase.PlayerID = uuid.MustParse("0192F28F-5DC0-7D58-BDB2-814AD6A0F4A1")
		uppercase.WalletID = uuid.MustParse("0192F291-27DD-7D3F-8071-5F8685DEEF37")

		// When, Then
		if hashOf(t, lowercase) != hashOf(t, uppercase) {
			t.Error("UUID casing must not change the digest")
		}
	})

	t.Run("Given a change in any business field/When it is hashed/Then the digest changes", func(t *testing.T) {
		scenarios := []struct {
			name   string
			mutate func(*idempotency.Payload)
		}{
			{"provider", func(p *idempotency.Payload) { p.ProviderID = "provider-b" }},
			{"external id", func(p *idempotency.Payload) { p.ExternalTransactionID = "transaction-124" }},
			{"player", func(p *idempotency.Payload) { p.PlayerID = uuid.Must(uuid.NewV7()) }},
			{"wallet", func(p *idempotency.Payload) { p.WalletID = uuid.Must(uuid.NewV7()) }},
			{"round", func(p *idempotency.Payload) { p.RoundID = "round-000" }},
			{"game", func(p *idempotency.Payload) { p.GameID = "other-game" }},
			{"kind", func(p *idempotency.Payload) { p.Kind = wagering.Win }},
			{"amount", func(p *idempotency.Payload) {
				value, err := money.Parse("30.00", "BRL")
				if err != nil {
					t.Fatalf("Parse failed: %v", err)
				}
				p.Amount = value
			}},
			{"currency", func(p *idempotency.Payload) {
				value, err := money.Parse("25.00", "USD")
				if err != nil {
					t.Fatalf("Parse failed: %v", err)
				}
				p.Amount = value
			}},
			{"reference added", func(p *idempotency.Payload) { p.ReferenceExternalID = "transaction-100" }},
		}

		baseline := hashOf(t, referencePayload(t))
		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given
				payload := referencePayload(t)
				scenario.mutate(&payload)

				// When
				digest := hashOf(t, payload)

				// Then
				if digest == baseline {
					t.Errorf("changing the %s should change the digest", scenario.name)
				}
			})
		}
	})

	t.Run("Given the documented reference payloads/When they are hashed/Then the digests match the fixed vectors", func(t *testing.T) {
		// Given
		bet := referencePayload(t)
		reversal := referencePayload(t)
		reversal.Kind = wagering.Rollback
		reversal.ReferenceExternalID = "transaction-100"

		// When
		betDigest := hashOf(t, bet)
		reversalDigestFound := hashOf(t, reversal)

		// Then
		if betDigest != referenceDigest {
			t.Errorf("bet digest = %s, want %s", betDigest, referenceDigest)
		}
		if reversalDigestFound != reversalDigest {
			t.Errorf("reversal digest = %s, want %s", reversalDigestFound, reversalDigest)
		}
	})
}
