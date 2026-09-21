//go:build integration

package testenv

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	ClientProviderA  = "provider-a"
	ClientProviderB  = "provider-b"
	ClientShortLived = "provider-c-short-lived"
	ClientInternal   = "wallet-internal"
)

var clientSecrets = map[string]string{
	ClientProviderA:  "provider-a-secret",
	ClientProviderB:  "provider-b-secret",
	ClientShortLived: "provider-c-secret",
	ClientInternal:   "wallet-internal-secret",
}

type grantedToken struct {
	value     string
	expiresAt time.Time
}

// Auth hands out real client_credentials tokens from the imported realm and keeps
// them until shortly before they expire.
type Auth struct {
	t         *testing.T
	baseURL   string
	mutex     sync.Mutex
	granted   map[string]grantedToken
	transport *http.Client
}

func newAuth(t *testing.T, baseURL string) *Auth {
	return &Auth{
		t:         t,
		baseURL:   baseURL,
		granted:   make(map[string]grantedToken),
		transport: &http.Client{Timeout: 15 * time.Second},
	}
}

func (a *Auth) Token(client string) string {
	a.t.Helper()

	a.mutex.Lock()
	defer a.mutex.Unlock()

	if cached, ok := a.granted[client]; ok && time.Now().Before(cached.expiresAt) {
		return cached.value
	}

	token, lifetime := a.request(client)
	a.granted[client] = grantedToken{value: token, expiresAt: time.Now().Add(lifetime - 5*time.Second)}
	return token
}

// FreshToken always asks the identity provider for a new token, which the expiry
// scenario needs so it can wait for that exact token to die.
func (a *Auth) FreshToken(client string) (string, time.Duration) {
	a.t.Helper()
	return a.request(client)
}

func (a *Auth) request(client string) (string, time.Duration) {
	a.t.Helper()

	secret, ok := clientSecrets[client]
	if !ok {
		a.t.Fatalf("client %s is not part of the test realm", client)
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {client},
		"client_secret": {secret},
	}
	endpoint := a.baseURL + "/realms/wallet/protocol/openid-connect/token"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		a.t.Fatalf("token request could not be built: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := a.transport.Do(request)
	if err != nil {
		a.t.Fatalf("identity provider is not reachable: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		a.t.Fatalf("token for %s was refused with status %d", client, response.StatusCode)
	}

	var granted struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(response.Body).Decode(&granted); err != nil {
		a.t.Fatalf("token response could not be read: %v", err)
	}
	return granted.AccessToken, time.Duration(granted.ExpiresIn) * time.Second
}

func (a *Auth) Claims(token string) map[string]any {
	a.t.Helper()

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		a.t.Fatalf("the token is not a jwt")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		a.t.Fatalf("token claims could not be decoded: %v", err)
	}

	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		a.t.Fatalf("token claims could not be read: %v", err)
	}
	return claims
}

func (a *Auth) DiscoveryURL() string {
	return fmt.Sprintf("%s/realms/wallet", a.baseURL)
}

func (a *Auth) JWKSURL() string {
	return fmt.Sprintf("%s/realms/wallet/protocol/openid-connect/certs", a.baseURL)
}
