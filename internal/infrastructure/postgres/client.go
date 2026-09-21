package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	connectionLifetime = 30 * time.Minute
	connectionIdleTime = 5 * time.Minute
)

type ClientSettings struct {
	URL      string
	MaxConns int32
	MinConns int32
}

// Client owns the connection pool. Everything that talks to PostgreSQL goes
// through it, so there is a single place to size, check and close connections.
type Client struct {
	pool *pgxpool.Pool
}

func NewClient(ctx context.Context, settings ClientSettings) (*Client, error) {
	configuration, err := pgxpool.ParseConfig(settings.URL)
	if err != nil {
		return nil, fmt.Errorf("postgres: invalid connection string: %w", err)
	}
	configuration.MaxConns = settings.MaxConns
	configuration.MinConns = settings.MinConns
	configuration.MaxConnLifetime = connectionLifetime
	configuration.MaxConnIdleTime = connectionIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, configuration)
	if err != nil {
		return nil, fmt.Errorf("postgres: could not create the connection pool: %w", err)
	}
	return &Client{pool: pool}, nil
}

func (c *Client) Ping(ctx context.Context, timeout time.Duration) error {
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := c.pool.Ping(pingCtx); err != nil {
		return fmt.Errorf("postgres: database is not reachable: %w", err)
	}
	return nil
}

func (c *Client) Close() {
	c.pool.Close()
}
