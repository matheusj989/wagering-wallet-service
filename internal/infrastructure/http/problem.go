package http

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/application/dto/validation"
)

const (
	CodeValidationFailed  = "VALIDATION_FAILED"
	CodeInvalidCursor     = "INVALID_CURSOR"
	CodeUnauthenticated   = "UNAUTHENTICATED"
	CodeForbidden         = "FORBIDDEN"
	CodeWalletNotFound    = "WALLET_NOT_FOUND"
	CodeTransactionNotFnd = "TRANSACTION_NOT_FOUND"
	CodeWalletExists      = "WALLET_ALREADY_EXISTS"
	CodeKeyConflict       = "IDEMPOTENCY_KEY_CONFLICT"
	CodeExternalConflict  = "EXTERNAL_TRANSACTION_ID_CONFLICT"
	CodePayloadTooLarge   = "PAYLOAD_TOO_LARGE"
	CodeUnavailable       = "SERVICE_UNAVAILABLE"
	CodeCommitUnknown     = "COMMIT_OUTCOME_UNKNOWN"
	CodeInternalError     = "INTERNAL_ERROR"
)

type problem struct {
	Type          string                  `json:"type"`
	Title         string                  `json:"title"`
	Status        int                     `json:"status"`
	Detail        string                  `json:"detail"`
	Code          string                  `json:"code"`
	CorrelationID string                  `json:"correlationId"`
	Errors        []validation.FieldError `json:"errors,omitempty"`
	TransactionID *uuid.UUID              `json:"transactionId,omitempty"`
	WalletID      *uuid.UUID              `json:"walletId,omitempty"`
}

type problemOption func(*problem)

func withFields(fields []validation.FieldError) problemOption {
	return func(p *problem) { p.Errors = fields }
}

func withTransaction(id uuid.UUID) problemOption {
	return func(p *problem) { p.TransactionID = &id }
}

func withWallet(id uuid.UUID) problemOption {
	return func(p *problem) { p.WalletID = &id }
}

func (h *Handlers) writeProblem(w http.ResponseWriter, r *http.Request, status int, code string, detail string, options ...problemOption) {
	body := problem{
		Type:          "about:blank",
		Title:         http.StatusText(status),
		Status:        status,
		Detail:        detail,
		Code:          code,
		CorrelationID: correlationFrom(r.Context()),
	}
	for _, option := range options {
		option(&body)
	}

	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		h.logger.WarnContext(r.Context(), "could not write the error response", slog.String("cause", err.Error()))
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(payload)
}
