package usecase

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/application/dto"
	"github.com/matheusj989/wagering-wallet-service/internal/application/port"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/repositories"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
	"github.com/matheusj989/wagering-wallet-service/internal/domain/wallet"
)

type OpenWallet interface {
	Execute(ctx context.Context, input dto.OpenWalletInput) (*wallet.Wallet, error)
}

type openWallet struct {
	unitOfWork repositories.UnitOfWork
	clock      port.Clock
	ids        port.IDGenerator
	logger     *slog.Logger
}

func NewOpenWallet(
	unitOfWork repositories.UnitOfWork,
	clock port.Clock,
	ids port.IDGenerator,
	logger *slog.Logger,
) OpenWallet {
	return &openWallet{unitOfWork: unitOfWork, clock: clock, ids: ids, logger: logger}
}

func (u *openWallet) Execute(ctx context.Context, input dto.OpenWalletInput) (*wallet.Wallet, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}

	u.logger.InfoContext(ctx, "opening a wallet",
		slog.String(logCorrelationID, input.CorrelationID),
		slog.String(logPlayerID, input.PlayerID.String()),
		slog.String(logCurrency, input.InitialBalance.Currency().String()),
		slog.Bool("funded", !input.InitialBalance.IsZero()),
	)

	now := u.clock.Now()
	openingID := u.ids.New()

	var opened *wallet.Wallet
	err := u.unitOfWork.Do(ctx, func(ctx context.Context, registry repositories.Registry) error {
		created, entry, err := wallet.Open(u.ids.New(), input.PlayerID, input.InitialBalance, openingID, u.ids.New(), now)
		if err != nil {
			return err
		}
		if err := registry.Wallets().Create(ctx, created); err != nil {
			return err
		}
		if entry != nil {
			if err := u.recordOpeningBalance(ctx, registry, created, entry, input, openingID, now); err != nil {
				return err
			}
		}

		opened = created
		return nil
	})
	if err != nil {
		if errors.Is(err, wallet.ErrAlreadyExists) {
			return nil, u.conflict(ctx, input)
		}
		u.logger.ErrorContext(ctx, "the wallet could not be opened",
			slog.String(logCorrelationID, input.CorrelationID),
			slog.String(logPlayerID, input.PlayerID.String()),
			slog.String(logCause, err.Error()),
		)
		return nil, err
	}

	u.logger.InfoContext(ctx, "wallet opened",
		slog.String(logCorrelationID, input.CorrelationID),
		slog.String(logWalletID, opened.ID().String()),
		slog.String(logPlayerID, opened.PlayerID().String()),
		slog.String(logCurrency, opened.Currency().String()),
		slog.Int64("version", opened.Version()),
	)
	return opened, nil
}

func (u *openWallet) recordOpeningBalance(
	ctx context.Context,
	registry repositories.Registry,
	created *wallet.Wallet,
	entry *wallet.LedgerEntry,
	input dto.OpenWalletInput,
	openingID uuid.UUID,
	now time.Time,
) error {
	opening, err := wagering.NewOpening(openingID, created.ID(), input.PlayerID, input.InitialBalance, input.CorrelationID, now)
	if err != nil {
		return err
	}
	if _, err := registry.Transactions().Insert(ctx, opening); err != nil {
		return err
	}
	if err := registry.Ledger().Append(ctx, *entry); err != nil {
		return err
	}
	return appendSettlementEvents(ctx, registry, u.ids, now, opening, entry, created.Balance())
}

func (u *openWallet) conflict(ctx context.Context, input dto.OpenWalletInput) error {
	var existing *wallet.Wallet
	err := u.unitOfWork.Read(ctx, func(ctx context.Context, registry repositories.Registry) error {
		found, err := registry.Wallets().FindByPlayer(ctx, input.PlayerID, input.InitialBalance.Currency())
		if err != nil {
			return err
		}
		existing = found
		return nil
	})
	if err != nil || existing == nil {
		return wallet.ErrAlreadyExists
	}

	u.logger.WarnContext(ctx, "wallet opening refused, the player already has one in this currency",
		slog.String(logCorrelationID, input.CorrelationID),
		slog.String(logPlayerID, input.PlayerID.String()),
		slog.String(logCurrency, input.InitialBalance.Currency().String()),
		slog.String(logWalletID, existing.ID().String()),
	)
	return &WalletConflictError{WalletID: existing.ID()}
}
