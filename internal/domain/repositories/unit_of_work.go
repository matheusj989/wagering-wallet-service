package repositories

import "context"

type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context, repositories Registry) error) error
	Read(ctx context.Context, fn func(ctx context.Context, repositories Registry) error) error
}
