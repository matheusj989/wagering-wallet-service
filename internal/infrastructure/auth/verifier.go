package auth

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
)

const (
	RoleProvider = "wagering-provider"
	RoleInternal = "wallet-internal"
)

var ErrUnauthenticated = errors.New("auth: the credential is missing, malformed or not valid")

type Settings struct {
	Issuer       string
	DiscoveryURL string
	JWKSURL      string
	Audience     string
}

type Principal struct {
	Subject    string
	ProviderID string
	Roles      []string
}

func (p Principal) HasRole(role string) bool {
	return slices.Contains(p.Roles, role)
}

func (p Principal) Internal() bool {
	return p.HasRole(RoleInternal)
}

func (p Principal) Provider() bool {
	return p.HasRole(RoleProvider) && p.ProviderID != ""
}

type Verifier struct {
	settings Settings
	tokens   atomic.Pointer[oidc.IDTokenVerifier]
}

func NewVerifier(settings Settings) *Verifier {
	return &Verifier{settings: settings}
}

func (v *Verifier) Connect(ctx context.Context) error {
	if err := checkDiscovery(ctx, v.settings); err != nil {
		return err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	if err := checkJWKS(ctx, client, v.settings.JWKSURL); err != nil {
		return err
	}
	keys := oidc.NewRemoteKeySet(oidc.ClientContext(context.WithoutCancel(ctx), client), v.settings.JWKSURL)
	v.tokens.Store(oidc.NewVerifier(v.settings.Issuer, keys, &oidc.Config{
		ClientID:             v.settings.Audience,
		SupportedSigningAlgs: []string{oidc.RS256},
	}))
	return nil
}

func checkJWKS(ctx context.Context, client *http.Client, address string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return fmt.Errorf("auth: invalid JWKS url: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("auth: JWKS is not reachable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("auth: JWKS answered %d", response.StatusCode)
	}
	var keys jose.JSONWebKeySet
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&keys); err != nil {
		return fmt.Errorf("auth: JWKS could not be read: %w", err)
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return errors.New("auth: JWKS contains trailing content")
	}
	for _, key := range keys.Keys {
		_, rsaKey := key.Key.(*rsa.PublicKey)
		if key.Valid() && rsaKey && (key.Use == "sig" || key.Use == "") && (key.Algorithm == oidc.RS256 || key.Algorithm == "") {
			return nil
		}
	}
	return errors.New("auth: JWKS has no usable RS256 signing key")
}

func (v *Verifier) Verify(ctx context.Context, rawToken string) (Principal, error) {
	tokens := v.tokens.Load()
	if tokens == nil {
		return Principal{}, fmt.Errorf("%w: the identity provider is not connected yet", ErrUnauthenticated)
	}

	token, err := tokens.Verify(ctx, rawToken)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %w", ErrUnauthenticated, err)
	}

	var claims struct {
		ProviderID  string `json:"provider_id"`
		RealmAccess struct {
			Roles []string `json:"roles"`
		} `json:"realm_access"`
	}
	if err := token.Claims(&claims); err != nil {
		return Principal{}, fmt.Errorf("%w: claims could not be read", ErrUnauthenticated)
	}

	return Principal{
		Subject:    token.Subject,
		ProviderID: claims.ProviderID,
		Roles:      claims.RealmAccess.Roles,
	}, nil
}

func BearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	return token, true
}

func checkDiscovery(ctx context.Context, settings Settings) error {
	discoveryURL := strings.TrimSuffix(settings.DiscoveryURL, "/") + "/.well-known/openid-configuration"

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return fmt.Errorf("auth: invalid discovery url %s: %w", discoveryURL, err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("auth: identity provider is not reachable at %s: %w", discoveryURL, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("auth: discovery at %s answered %d", discoveryURL, response.StatusCode)
	}

	var document struct {
		Issuer string `json:"issuer"`
	}
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		return fmt.Errorf("auth: discovery document at %s could not be read: %w", discoveryURL, err)
	}
	if document.Issuer != settings.Issuer {
		return fmt.Errorf("auth: discovery announces issuer %q but %q was configured", document.Issuer, settings.Issuer)
	}
	return nil
}
