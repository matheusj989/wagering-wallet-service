package http

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/application/dto"
	"github.com/matheusj989/wagering-wallet-service/internal/application/dto/validation"
	"github.com/matheusj989/wagering-wallet-service/internal/application/usecase"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/money"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/auth"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/failpoint"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/logging"
	"github.com/matheusj989/wagering-wallet-service/internal/infrastructure/observability"
)

const (
	defaultLedgerLimit = 50
	maxLedgerLimit     = 100
	cursorPrefix       = "v1:"
)

type Handlers struct {
	open            usecase.OpenWallet
	process         usecase.ProcessWagerTransaction
	findWallet      usecase.FindWallet
	listLedger      usecase.ListWalletLedger
	reconcileWallet usecase.ReconcileWallet
	findTransaction usecase.FindWagerTransaction
	verifier        *auth.Verifier
	health          *observability.Health
	metrics         *observability.Metrics
	logger          *slog.Logger
	limiter         limiter
	maxBodyBytes    int64
}

type HandlersSettings struct {
	WriteConcurrency int
	WriteQueueWait   Duration
	MaxBodyBytes     int64
}

func NewHandlers(
	open usecase.OpenWallet,
	process usecase.ProcessWagerTransaction,
	findWallet usecase.FindWallet,
	listLedger usecase.ListWalletLedger,
	reconcileWallet usecase.ReconcileWallet,
	findTransaction usecase.FindWagerTransaction,
	verifier *auth.Verifier,
	health *observability.Health,
	metrics *observability.Metrics,
	logger *slog.Logger,
	settings HandlersSettings,
) *Handlers {
	return &Handlers{
		open:            open,
		process:         process,
		findWallet:      findWallet,
		listLedger:      listLedger,
		reconcileWallet: reconcileWallet,
		findTransaction: findTransaction,
		verifier:        verifier,
		health:          health,
		metrics:         metrics,
		logger:          logging.Component(logger, "http"),
		limiter:         newLimiter(settings.WriteConcurrency, settings.WriteQueueWait.Value),
		maxBodyBytes:    settings.MaxBodyBytes,
	}
}

func (h *Handlers) openWallet(w http.ResponseWriter, r *http.Request) {
	var request openWalletRequest
	if !h.decode(w, r, &request) {
		return
	}

	input, err := request.toInput(correlationFrom(r.Context()))
	if err != nil {
		h.failValidation(w, r, err)
		return
	}

	opened, err := h.open.Execute(r.Context(), input)
	if err != nil {
		var conflict *usecase.WalletConflictError
		if errors.As(err, &conflict) {
			h.writeProblem(w, r, http.StatusConflict, CodeWalletExists,
				"this player already has a wallet in this currency", withWallet(conflict.WalletID))
			return
		}
		h.failValidation(w, r, err)
		return
	}

	h.respond(w, r, http.StatusCreated, walletOf(opened))
}

func (h *Handlers) getWallet(w http.ResponseWriter, r *http.Request) {
	walletID, ok := h.pathUUID(w, r, "walletId")
	if !ok {
		return
	}

	found, err := h.findWallet.Execute(r.Context(), walletID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.respond(w, r, http.StatusOK, walletOf(found))
}

func (h *Handlers) getLedger(w http.ResponseWriter, r *http.Request) {
	walletID, ok := h.pathUUID(w, r, "walletId")
	if !ok {
		return
	}

	limit := defaultLedgerLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxLedgerLimit {
			h.writeProblem(w, r, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("limit must be between 1 and %d", maxLedgerLimit))
			return
		}
		limit = parsed
	}

	afterVersion := int64(0)
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		decoded, err := decodeCursor(raw)
		if err != nil {
			h.writeProblem(w, r, http.StatusBadRequest, CodeInvalidCursor, "the cursor could not be read")
			return
		}
		afterVersion = decoded
	}

	page, err := h.listLedger.Execute(r.Context(), dto.ListLedgerInput{
		WalletID:     walletID,
		AfterVersion: afterVersion,
		Limit:        limit,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}

	var nextCursor *string
	if page.HasMore {
		encoded := encodeCursor(page.NextCursor)
		nextCursor = &encoded
	}
	h.respond(w, r, http.StatusOK, ledgerOf(walletID, page, nextCursor))
}

func (h *Handlers) reconcile(w http.ResponseWriter, r *http.Request) {
	walletID, ok := h.pathUUID(w, r, "walletId")
	if !ok {
		return
	}

	report, err := h.reconcileWallet.Execute(r.Context(), walletID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.respond(w, r, http.StatusOK, reconciliationOf(report))
}

func (h *Handlers) submitTransaction(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())

	var request wagerRequest
	if !h.decode(w, r, &request) {
		return
	}

	if request.ProviderID != principal.ProviderID {
		h.writeProblem(w, r, http.StatusForbidden, CodeForbidden,
			"the operation must carry the provider of the authenticated credential")
		return
	}

	input, err := request.toInput(r.Header.Get("Idempotency-Key"), correlationFrom(r.Context()))
	if err != nil {
		h.failValidation(w, r, err)
		return
	}

	result, err := h.process.Execute(r.Context(), input)
	if err != nil {
		h.failProcessing(w, r, err)
		return
	}

	if result.Status == wagering.PendingReference {
		failpoint.Hit("wagering.after_pending_reference_commit")
	}
	h.respond(w, r, statusOf(result.Status), resultOf(result))
}

func (h *Handlers) getTransaction(w http.ResponseWriter, r *http.Request) {
	transactionID, ok := h.pathUUID(w, r, "transactionId")
	if !ok {
		return
	}
	principal, _ := principalFrom(r.Context())

	found, err := h.findTransaction.ByID(r.Context(), transactionID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !principal.Internal() && found.ProviderID() != principal.ProviderID {
		h.writeProblem(w, r, http.StatusNotFound, CodeTransactionNotFnd, "this transaction does not exist")
		return
	}
	h.respond(w, r, http.StatusOK, transactionOf(found))
}

func (h *Handlers) getProviderTransaction(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	providerID := chi.URLParam(r, "providerId")
	externalTransactionID := chi.URLParam(r, "externalTransactionId")

	if !principal.Internal() && providerID != principal.ProviderID {
		h.writeProblem(w, r, http.StatusForbidden, CodeForbidden, "providers can only read their own operations")
		return
	}

	found, err := h.findTransaction.ByExternalID(r.Context(), providerID, externalTransactionID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.respond(w, r, http.StatusOK, transactionOf(found))
}

func (h *Handlers) live(w http.ResponseWriter, r *http.Request) {
	h.respond(w, r, http.StatusOK, map[string]string{"status": "UP"})
}

func (h *Handlers) ready(w http.ResponseWriter, r *http.Request) {
	ready, results := h.health.Ready(r.Context())

	checks := make(map[string]map[string]string, len(results))
	for _, result := range results {
		entry := map[string]string{"status": "UP"}
		if !result.Up {
			entry["status"] = "DOWN"
			entry["error"] = result.Reason
		}
		checks[result.Name] = entry
	}

	status := http.StatusOK
	overall := "UP"
	if !ready {
		status = http.StatusServiceUnavailable
		overall = "DOWN"
	}
	h.respond(w, r, status, map[string]any{"status": overall, "checks": checks})
}

func (h *Handlers) decode(w http.ResponseWriter, r *http.Request, target any) bool {
	if contentType := r.Header.Get("Content-Type"); contentType != "" &&
		!strings.HasPrefix(contentType, "application/json") {
		h.writeProblem(w, r, http.StatusBadRequest, CodeValidationFailed, "the request must be application/json")
		return false
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.writeProblem(w, r, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "the request body is too large")
			return false
		}
		h.writeProblem(w, r, http.StatusBadRequest, CodeValidationFailed, "the request body could not be read")
		return false
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		h.writeProblem(w, r, http.StatusBadRequest, CodeValidationFailed, decodeMessage(err))
		return false
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		h.writeProblem(w, r, http.StatusBadRequest, CodeValidationFailed, "the body must contain exactly one JSON document")
		return false
	}
	return true
}

func decodeMessage(err error) string {
	switch {
	case errors.Is(err, money.ErrInvalidAmount):
		return "money.amount must be a decimal string with exactly two places"
	case errors.Is(err, money.ErrInvalidCurrency):
		return "money.currency must be BRL or USD"
	case errors.Is(err, money.ErrOverflow):
		return "money.amount is above the representable limit"
	default:
		return fmt.Sprintf("the body could not be read: %s", err.Error())
	}
}

func (h *Handlers) pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	parsed, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil || parsed == uuid.Nil {
		h.writeProblem(w, r, http.StatusBadRequest, CodeValidationFailed, name+" must be a UUID")
		return uuid.Nil, false
	}
	return parsed, true
}

func (h *Handlers) respond(w http.ResponseWriter, r *http.Request, status int, payload any) {
	if err := writeJSON(w, status, payload); err != nil {
		h.logger.WarnContext(r.Context(), "could not write the response",
			slog.String(logging.FieldCorrelationID, correlationFrom(r.Context())),
			slog.String("cause", err.Error()))
	}
}

func (h *Handlers) failValidation(w http.ResponseWriter, r *http.Request, err error) {
	var problems *validation.Error
	if errors.As(err, &problems) {
		h.writeProblem(w, r, http.StatusBadRequest, CodeValidationFailed, problems.Error(), withFields(problems.Fields))
		return
	}
	h.fail(w, r, err)
}

func (h *Handlers) failProcessing(w http.ResponseWriter, r *http.Request, err error) {
	var conflict *usecase.ConflictError
	if errors.As(err, &conflict) {
		code := CodeKeyConflict
		detail := "this idempotency key was already used with a different payload"
		if conflict.Kind == usecase.ConflictExternalID {
			code = CodeExternalConflict
			detail = "this external transaction id was already used with another idempotency key"
		}
		h.writeProblem(w, r, http.StatusConflict, code, detail, withTransaction(conflict.TransactionID))
		return
	}
	h.failValidation(w, r, err)
}

func (h *Handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, wallet.ErrNotFound):
		h.writeProblem(w, r, http.StatusNotFound, CodeWalletNotFound, "this wallet does not exist")
	case errors.Is(err, wagering.ErrNotFound):
		h.writeProblem(w, r, http.StatusNotFound, CodeTransactionNotFnd, "this transaction does not exist")
	case errors.Is(err, repositories.ErrCommitOutcomeUnknown):
		h.logger.ErrorContext(r.Context(), "the commit result is unknown",
			slog.String(logging.FieldCorrelationID, correlationFrom(r.Context())),
			slog.String("cause", err.Error()))
		h.writeProblem(w, r, http.StatusServiceUnavailable, CodeCommitUnknown,
			"the result of this operation is unknown, retry with the same provider, key, external id and payload")
	case errors.Is(err, repositories.ErrLockTimeout), errors.Is(err, repositories.ErrTransient):
		h.writeProblem(w, r, http.StatusServiceUnavailable, CodeUnavailable, "the service is busy, please retry")
	default:
		h.logger.ErrorContext(r.Context(), "request failed",
			slog.String(logging.FieldCorrelationID, correlationFrom(r.Context())),
			slog.String("path", r.URL.Path),
			slog.String("cause", err.Error()))
		h.writeProblem(w, r, http.StatusInternalServerError, CodeInternalError, "the request could not be completed")
	}
}

func encodeCursor(version int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(cursorPrefix + strconv.FormatInt(version, 10)))
}

func decodeCursor(raw string) (int64, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, err
	}
	value, found := strings.CutPrefix(string(decoded), cursorPrefix)
	if !found {
		return 0, errors.New("http: unknown cursor version")
	}
	version, err := strconv.ParseInt(value, 10, 64)
	if err != nil || version < 0 {
		return 0, errors.New("http: cursor does not carry a wallet version")
	}
	return version, nil
}
