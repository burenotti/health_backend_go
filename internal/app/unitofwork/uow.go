package unitofwork

import (
	"context"
	"errors"
	"fmt"
	"github.com/burenotti/go_health_backend/internal/adapter/storage"
	"github.com/burenotti/go_health_backend/internal/domain"
	"log/slog"
)

var (
	ErrRollback = errors.New("rollback")
)

type AtomicContext interface {
	Commit() error
	Close() error
	CollectEvents() []domain.Event
}

type MessageBus interface {
	PublishEvents(events ...domain.Event) error
}

type UnitOfWork[T AtomicContext] struct {
	db         storage.DB
	newContext func(storage.DBContext) (T, error)
	msgBus     MessageBus
	logger     *slog.Logger
}

func New[T AtomicContext](
	db storage.DB,
	newCtx func(storage.DBContext) (T, error),
	msgBus MessageBus,
	logger *slog.Logger,
) *UnitOfWork[T] {
	return &UnitOfWork[T]{
		db:         db,
		newContext: newCtx,
		msgBus:     msgBus,
		logger:     logger,
	}
}

func (uow *UnitOfWork[T]) Atomic(
	ctx context.Context,
	do func(context.Context, T) error,
) (err error) {
	tx, err := uow.db.Begin(ctx)
	if err != nil {
		return stateRollbackError(err)
	}

	atomicCtx, err := uow.newContext(tx)
	if err != nil {
		return stateRollbackError(err)
	}

	defer func() {
		if r := recover(); r != nil {
			if err := tx.Rollback(); err != nil {
				uow.logger.Error("failed to rollback transaction", "error", err)
			}
			panic(r)
		}
	}()

	txCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := do(txCtx, atomicCtx); err != nil {
		if err := tx.Rollback(); err != nil {
			uow.logger.Error("failed to rollback transaction", "error", err)
		}
		return stateRollbackError(err)
	}

	if err := uow.msgBus.PublishEvents(atomicCtx.CollectEvents()...); err != nil {
		uow.logger.Error("failed to publish events", "error", err)
		return err
	}

	return nil
}

func stateRollbackError(err error) error {
	return errors.Join(fmt.Errorf("state rollback: %w", err), ErrRollback)
}
