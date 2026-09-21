package http

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/matheusj989/wagering-wallet-service/internal/application/dto"
	"github.com/matheusj989/wagering-wallet-service/internal/application/dto/validation"
	"github.com/matheusj989/wagering-wallet-service/internal/application/usecase"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/auth"
	"github.com/matheusj989/wagering-wallet-service/internal/mocks"
)

func principalOf(providerID string) auth.Principal {
	return auth.Principal{
		Subject:    "service-account-" + providerID,
		ProviderID: providerID,
		Roles:      []string{auth.RoleProvider},
	}
}

type endpoint struct {
	handlers        *Handlers
	open            *mocks.MockOpenWallet
	process         *mocks.MockProcessWagerTransaction
	findWallet      *mocks.MockFindWallet
	listLedger      *mocks.MockListWalletLedger
	reconcileWallet *mocks.MockReconcileWallet
	findTransaction *mocks.MockFindWagerTransaction
}

func newEndpoint(t *testing.T) *endpoint {
	t.Helper()

	ctrl := gomock.NewController(t)
	e := &endpoint{
		open:            mocks.NewMockOpenWallet(ctrl),
		process:         mocks.NewMockProcessWagerTransaction(ctrl),
		findWallet:      mocks.NewMockFindWallet(ctrl),
		listLedger:      mocks.NewMockListWalletLedger(ctrl),
		reconcileWallet: mocks.NewMockReconcileWallet(ctrl),
		findTransaction: mocks.NewMockFindWagerTransaction(ctrl),
	}
	e.handlers = &Handlers{
		open:            e.open,
		process:         e.process,
		findWallet:      e.findWallet,
		listLedger:      e.listLedger,
		reconcileWallet: e.reconcileWallet,
		findTransaction: e.findTransaction,
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		limiter:         newLimiter(1, time.Second),
		maxBodyBytes:    65536,
	}
	return e
}

func request(method string, body string, provider string) *http.Request {
	r := httptest.NewRequest(method, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(r.Context(), correlationKey, "correlation-1")
	if provider != "" {
		ctx = context.WithValue(ctx, principalKey, principalOf(provider))
	}
	return r.WithContext(ctx)
}

func decodeProblem(t *testing.T, recorder *httptest.ResponseRecorder) problem {
	t.Helper()

	var body problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("the problem body could not be read: %v", err)
	}
	return body
}

func TestSubmitTransaction(t *testing.T) {
	const payload = `{"providerId":"provider-a","externalTransactionId":"transaction-1",` +
		`"playerId":"0199c0d0-0000-7000-8000-000000000001","walletId":"0199c0d0-0000-7000-8000-000000000002",` +
		`"roundId":"round-1","gameId":"fortune-chimp","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}`

	t.Run("Given a settled bet/When it is submitted/Then the answer is 201 with the outcome", func(t *testing.T) {
		// Given
		e := newEndpoint(t)
		transactionID := uuid.Must(uuid.NewV7())
		balance, _ := money.Parse("975.00", "BRL")
		e.process.EXPECT().Execute(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, input dto.ProcessWagerInput) (dto.ProcessWagerResult, error) {
				if input.IdempotencyKey != "key-1" || input.Origin != wagering.HTTP {
					t.Errorf("input = key %q from %s", input.IdempotencyKey, input.Origin)
				}
				return dto.ProcessWagerResult{TransactionID: transactionID, Status: wagering.Processed, Balance: balance}, nil
			})

		// When
		recorder := httptest.NewRecorder()
		r := request(http.MethodPost, payload, "provider-a")
		r.Header.Set("Idempotency-Key", "key-1")
		e.handlers.submitTransaction(recorder, r)

		// Then
		if recorder.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201: %s", recorder.Code, recorder.Body.String())
		}
		var answer resultResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
			t.Fatalf("the body could not be read: %v", err)
		}
		if answer.TransactionID != transactionID || answer.Status != "PROCESSED" {
			t.Errorf("answer = %s / %s", answer.TransactionID, answer.Status)
		}
	})

	t.Run("Given an operation waiting for its reference/When it is submitted/Then the answer is 202", func(t *testing.T) {
		// Given
		e := newEndpoint(t)
		e.process.EXPECT().Execute(gomock.Any(), gomock.Any()).
			Return(dto.ProcessWagerResult{TransactionID: uuid.Must(uuid.NewV7()), Status: wagering.PendingReference}, nil)

		// When
		recorder := httptest.NewRecorder()
		e.handlers.submitTransaction(recorder, request(http.MethodPost, payload, "provider-a"))

		// Then
		if recorder.Code != http.StatusAccepted {
			t.Errorf("status = %d, want 202", recorder.Code)
		}
	})

	t.Run("Given a rejected operation/When it is submitted/Then the answer is 422 with the failure code", func(t *testing.T) {
		// Given
		e := newEndpoint(t)
		e.process.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(dto.ProcessWagerResult{
			TransactionID: uuid.Must(uuid.NewV7()),
			Status:        wagering.Rejected,
			FailureCode:   wagering.InsufficientFunds,
		}, nil)

		// When
		recorder := httptest.NewRecorder()
		e.handlers.submitTransaction(recorder, request(http.MethodPost, payload, "provider-a"))

		// Then
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", recorder.Code)
		}
		var answer resultResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
			t.Fatalf("the body could not be read: %v", err)
		}
		if answer.FailureCode == nil || *answer.FailureCode != "INSUFFICIENT_FUNDS" {
			t.Errorf("failure code = %v, want INSUFFICIENT_FUNDS", answer.FailureCode)
		}
	})

	t.Run("Given a body naming another provider/When it is submitted/Then the answer is 403 and nothing is executed", func(t *testing.T) {
		// Given
		e := newEndpoint(t)

		// When
		recorder := httptest.NewRecorder()
		e.handlers.submitTransaction(recorder, request(http.MethodPost, payload, "provider-b"))

		// Then
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", recorder.Code)
		}
		if decodeProblem(t, recorder).Code != CodeForbidden {
			t.Errorf("code = %s, want %s", decodeProblem(t, recorder).Code, CodeForbidden)
		}
	})

	t.Run("Given the key was used with another payload/When it is submitted/Then the answer is 409 naming the first transaction", func(t *testing.T) {
		// Given
		e := newEndpoint(t)
		existing := uuid.Must(uuid.NewV7())
		e.process.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(dto.ProcessWagerResult{},
			&usecase.ConflictError{Kind: usecase.ConflictExternalID, TransactionID: existing})

		// When
		recorder := httptest.NewRecorder()
		e.handlers.submitTransaction(recorder, request(http.MethodPost, payload, "provider-a"))

		// Then
		if recorder.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", recorder.Code)
		}
		body := decodeProblem(t, recorder)
		if body.Code != CodeExternalConflict || body.TransactionID == nil || *body.TransactionID != existing {
			t.Errorf("problem = %s pointing at %v", body.Code, body.TransactionID)
		}
	})

	t.Run("Given an input the application refuses/When it is submitted/Then the answer is 400 with the fields", func(t *testing.T) {
		// Given
		e := newEndpoint(t)
		e.process.EXPECT().Execute(gomock.Any(), gomock.Any()).
			Return(dto.ProcessWagerResult{}, validation.Invalid("money.amount", "must be greater than zero for BET"))

		// When
		recorder := httptest.NewRecorder()
		e.handlers.submitTransaction(recorder, request(http.MethodPost, payload, "provider-a"))

		// Then
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", recorder.Code)
		}
		body := decodeProblem(t, recorder)
		if body.Code != CodeValidationFailed || len(body.Errors) != 1 || body.Errors[0].Field != "money.amount" {
			t.Errorf("problem = %s with %v", body.Code, body.Errors)
		}
	})

	t.Run("Given the commit result is unknown/When it is submitted/Then the answer asks for a safe retry", func(t *testing.T) {
		// Given
		e := newEndpoint(t)
		e.process.EXPECT().Execute(gomock.Any(), gomock.Any()).
			Return(dto.ProcessWagerResult{}, repositories.ErrCommitOutcomeUnknown)

		// When
		recorder := httptest.NewRecorder()
		e.handlers.submitTransaction(recorder, request(http.MethodPost, payload, "provider-a"))

		// Then
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", recorder.Code)
		}
		if decodeProblem(t, recorder).Code != CodeCommitUnknown {
			t.Errorf("code = %s, want %s", decodeProblem(t, recorder).Code, CodeCommitUnknown)
		}
		if recorder.Header().Get("Retry-After") != "1" {
			t.Error("a 503 should tell the caller when to come back")
		}
	})
}

func TestOpenWalletEndpoint(t *testing.T) {
	const payload = `{"playerId":"0199c0d0-0000-7000-8000-000000000001","initialBalance":{"amount":"1000.00","currency":"BRL"}}`

	t.Run("Given a new player/When the wallet is opened/Then the answer is 201", func(t *testing.T) {
		// Given
		e := newEndpoint(t)
		balance, _ := money.Parse("1000.00", "BRL")
		account, err := wallet.Rehydrate(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), balance, 1, time.Now(), time.Now())
		if err != nil {
			t.Fatalf("the wallet could not be built: %v", err)
		}
		e.open.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(account, nil)

		// When
		recorder := httptest.NewRecorder()
		e.handlers.openWallet(recorder, request(http.MethodPost, payload, ""))

		// Then
		if recorder.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201: %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("Given a player that already has a wallet/When it is opened/Then the answer is 409 naming it", func(t *testing.T) {
		// Given
		e := newEndpoint(t)
		existing := uuid.Must(uuid.NewV7())
		e.open.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil, &usecase.WalletConflictError{WalletID: existing})

		// When
		recorder := httptest.NewRecorder()
		e.handlers.openWallet(recorder, request(http.MethodPost, payload, ""))

		// Then
		if recorder.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", recorder.Code)
		}
		body := decodeProblem(t, recorder)
		if body.WalletID == nil || *body.WalletID != existing {
			t.Errorf("problem points at %v, want %s", body.WalletID, existing)
		}
	})

	t.Run("Given a player that is not a UUID/When the wallet is opened/Then the answer is 400 and nothing is executed", func(t *testing.T) {
		// Given
		e := newEndpoint(t)

		// When
		recorder := httptest.NewRecorder()
		e.handlers.openWallet(recorder, request(http.MethodPost,
			`{"playerId":"not-a-uuid","initialBalance":{"amount":"1000.00","currency":"BRL"}}`, ""))

		// Then
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", recorder.Code)
		}
		if body := decodeProblem(t, recorder); len(body.Errors) != 1 || body.Errors[0].Field != "playerId" {
			t.Errorf("reported errors = %v, want playerId", body.Errors)
		}
	})

	t.Run("Given a body with an unknown field/When the wallet is opened/Then the answer is 400", func(t *testing.T) {
		// Given
		e := newEndpoint(t)

		// When
		recorder := httptest.NewRecorder()
		e.handlers.openWallet(recorder, request(http.MethodPost,
			`{"playerId":"0199c0d0-0000-7000-8000-000000000001","nickname":"chimp"}`, ""))

		// Then
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", recorder.Code)
		}
	})
}

func TestCursor(t *testing.T) {
	t.Run("Given a wallet version/When the cursor is encoded and read back/Then the version survives", func(t *testing.T) {
		// Given, When
		version, err := decodeCursor(encodeCursor(42))

		// Then
		if err != nil {
			t.Fatalf("the cursor should be readable, got %v", err)
		}
		if version != 42 {
			t.Errorf("version = %d, want 42", version)
		}
	})

	t.Run("Given a cursor the service did not write/When it is read/Then it is refused", func(t *testing.T) {
		scenarios := []struct {
			name  string
			value string
		}{
			{"not base64", "abc!"},
			{"without the version prefix", "YWJj"},
			{"with a negative version", "djE6LTE"},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Given, When
				_, err := decodeCursor(scenario.value)

				// Then
				if err == nil {
					t.Errorf("cursor %q should have been refused", scenario.value)
				}
			})
		}
	})
}
