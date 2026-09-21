//go:build integration

package testenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/compose"
)

const (
	templateDatabase = "wallet_template"
	ownerRole        = "wallet_owner"
	ownerPassword    = "wallet_owner"
	appRole          = "wallet_app"
	appPassword      = "wallet_app"
	publicIssuer     = "http://localhost:8180/realms/wallet"
)

type endpoint struct {
	Host string
	Port string
}

func (e endpoint) address() string {
	return e.Host + ":" + e.Port
}

type suite struct {
	root      string
	stack     compose.ComposeStack
	postgres  endpoint
	keycloak  endpoint
	ministack endpoint
	binary    string
	admin     *pgxpool.Pool
}

var current *suite

// Run boots the shared infrastructure, runs the package tests and tears everything
// down before the exit code reaches os.Exit.
func Run(m *testing.M) int {
	ctx := context.Background()

	booted, err := boot(ctx)
	if booted != nil {
		defer booted.shutdown(ctx)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration suite could not start: %v\n", err)
		return 1
	}

	current = booted
	return m.Run()
}

func boot(ctx context.Context) (*suite, error) {
	root, err := projectRoot()
	if err != nil {
		return nil, err
	}
	booted := &suite{root: root}

	stack, err := compose.NewDockerComposeWith(
		compose.StackIdentifier("wallet-integration"),
		compose.WithStackFiles(
			filepath.Join(root, "deployments", "docker", "postgres.yml"),
			filepath.Join(root, "deployments", "docker", "keycloak.yml"),
			filepath.Join(root, "deployments", "docker", "ministack.yml"),
		),
	)
	if err != nil {
		return booted, fmt.Errorf("compose stack could not be read: %w", err)
	}
	booted.stack = stack

	upCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()

	err = stack.WithEnv(map[string]string{
		"POSTGRES_PORT":         "0",
		"KEYCLOAK_PORT":         "0",
		"MINISTACK_PORT":        "0",
		"KEYCLOAK_PUBLIC_URL":   "http://localhost:8180",
		"WALLET_OWNER_PASSWORD": ownerPassword,
		"WALLET_APP_PASSWORD":   appPassword,
	}).Up(upCtx, compose.Wait(true))
	if err != nil {
		return booted, fmt.Errorf("infrastructure did not start: %w", err)
	}

	if booted.postgres, err = mapped(upCtx, stack, "postgres", "5432/tcp"); err != nil {
		return booted, err
	}
	if booted.keycloak, err = mapped(upCtx, stack, "keycloak", "8080/tcp"); err != nil {
		return booted, err
	}
	if booted.ministack, err = mapped(upCtx, stack, "ministack", "4566/tcp"); err != nil {
		return booted, err
	}

	if err := booted.prepareTemplate(upCtx); err != nil {
		return booted, err
	}
	if err := booted.buildBinary(upCtx); err != nil {
		return booted, err
	}
	return booted, nil
}

func (s *suite) prepareTemplate(ctx context.Context) error {
	admin, err := pgxpool.New(ctx, s.ownerDSN("wallet"))
	if err != nil {
		return fmt.Errorf("administrative pool could not be created: %w", err)
	}
	s.admin = admin

	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+templateDatabase); err != nil {
		return fmt.Errorf("template database could not be reset: %w", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+templateDatabase); err != nil {
		return fmt.Errorf("template database could not be created: %w", err)
	}

	migrations, err := migrate.New("file://"+filepath.Join(s.root, "migrations"), s.migrateDSN(templateDatabase))
	if err != nil {
		return fmt.Errorf("migrations could not be loaded: %w", err)
	}
	defer func() {
		sourceErr, databaseErr := migrations.Close()
		_ = sourceErr
		_ = databaseErr
	}()

	if err := migrations.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrations could not be applied: %w", err)
	}
	return nil
}

func (s *suite) buildBinary(ctx context.Context) error {
	s.binary = filepath.Join(os.TempDir(), fmt.Sprintf("wallet-api-%d", os.Getpid()))

	build := exec.CommandContext(ctx, "go", "build",
		"-buildvcs=false", "-race", "-tags", "failpoints", "-o", s.binary, "./cmd/api")
	build.Dir = s.root
	build.Env = append(os.Environ(), "CGO_ENABLED=1")

	if output, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("service binary could not be built with the race detector: %w\n%s", err, output)
	}
	return nil
}

func (s *suite) shutdown(ctx context.Context) {
	if s.admin != nil {
		s.admin.Close()
	}
	if s.binary != "" {
		_ = os.Remove(s.binary)
	}
	if s.stack != nil {
		downCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if err := s.stack.Down(downCtx, compose.RemoveOrphans(true), compose.RemoveVolumes(true)); err != nil {
			fmt.Fprintf(os.Stderr, "infrastructure could not be removed: %v\n", err)
		}
	}
}

func (s *suite) ownerDSN(database string) string {
	return fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable",
		ownerRole, ownerPassword, s.postgres.address(), database)
}

func (s *suite) appDSN(database string) string {
	return fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable",
		appRole, appPassword, s.postgres.address(), database)
}

func (s *suite) migrateDSN(database string) string {
	return fmt.Sprintf("pgx5://%s:%s@%s/%s?sslmode=disable",
		ownerRole, ownerPassword, s.postgres.address(), database)
}

func (s *suite) sqsEndpoint() string {
	return "http://" + s.ministack.address()
}

func (s *suite) keycloakURL() string {
	return "http://" + s.keycloak.address()
}

func mapped(ctx context.Context, stack compose.ComposeStack, service string, port string) (endpoint, error) {
	container, err := stack.ServiceContainer(ctx, service)
	if err != nil {
		return endpoint{}, fmt.Errorf("service %s is not part of the stack: %w", service, err)
	}
	host, err := container.Host(ctx)
	if err != nil {
		return endpoint{}, fmt.Errorf("host of %s could not be read: %w", service, err)
	}
	mappedPort, err := container.MappedPort(ctx, port)
	if err != nil {
		return endpoint{}, fmt.Errorf("port %s of %s could not be read: %w", port, service, err)
	}
	return endpoint{Host: host, Port: mappedPort.Port()}, nil
}

func projectRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", errors.New("go.mod was not found above the test directory")
		}
		directory = parent
	}
}
