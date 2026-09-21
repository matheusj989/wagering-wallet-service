package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"github.com/go-jose/go-jose/v4"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestStartupJWKS(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	valid, _ := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "key", Use: "sig", Algorithm: "RS256"}}})
	encryption, _ := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "key", Use: "enc", Algorithm: "RS256"}}})
	scenarios := []struct {
		name, body string
		status     int
		wantError  bool
	}{
		{"usable RSA signing key", string(valid), 200, false},
		{"unavailable endpoint", "unavailable", 503, true},
		{"malformed key document", "not-json", 200, true},
		{"no signing keys", `{"keys":[]}`, 200, true},
		{"encryption key only", string(encryption), 200, true},
		{"trailing JSON token", string(valid) + "]", 200, true},
	}
	for _, scenario := range scenarios {
		t.Run("Given "+scenario.name+"/When startup validates JWKS/Then readiness requires a usable signing document", func(t *testing.T) {
			// Given
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: scenario.status, Body: io.NopCloser(strings.NewReader(scenario.body))}, nil
			})}
			// When
			err := checkJWKS(context.Background(), client, "http://identity.test/certs")
			// Then
			if (err != nil) != scenario.wantError {
				t.Fatalf("validation=%v wantError=%v", err, scenario.wantError)
			}
		})
	}
}
