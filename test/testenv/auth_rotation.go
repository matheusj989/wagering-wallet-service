//go:build integration

package testenv

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// RotateSigningKey installs a higher-priority RSA signing key in the real test
// realm, retaining the previous key for already-issued tokens. Cleanup restores
// the realm before the next scenario.
func (a *Auth) RotateSigningKey() {
	a.t.Helper()
	form := url.Values{"grant_type": {"password"}, "client_id": {"admin-cli"}, "username": {"admin"}, "password": {"admin"}}
	response, err := a.transport.PostForm(a.baseURL+"/realms/master/protocol/openid-connect/token", form)
	if err != nil {
		a.t.Fatal(err)
	}
	defer response.Body.Close()
	var granted struct {
		Token string `json:"access_token"`
	}
	if response.StatusCode != 200 {
		a.t.Fatalf("admin token status=%d", response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(&granted); err != nil {
		a.t.Fatal(err)
	}
	request := func(method, path string, payload []byte) *http.Response {
		a.t.Helper()
		req, err := http.NewRequest(method, a.baseURL+path, bytes.NewReader(payload))
		if err != nil {
			a.t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+granted.Token)
		req.Header.Set("Content-Type", "application/json")
		res, err := a.transport.Do(req)
		if err != nil {
			a.t.Fatal(err)
		}
		return res
	}
	realm := request("GET", "/admin/realms/wallet", nil)
	var info struct {
		ID string `json:"id"`
	}
	err = json.NewDecoder(realm.Body).Decode(&info)
	realm.Body.Close()
	if err != nil || realm.StatusCode != 200 {
		a.t.Fatalf("realm status=%d err=%v", realm.StatusCode, err)
	}
	payload, _ := json.Marshal(map[string]any{"name": "test-rotation-" + uniqueSuffix(), "parentId": info.ID, "providerId": "rsa-generated", "providerType": "org.keycloak.keys.KeyProvider", "config": map[string][]string{"priority": {"200"}, "enabled": {"true"}, "active": {"true"}, "algorithm": {"RS256"}, "keySize": {"2048"}}})
	created := request("POST", "/admin/realms/wallet/components", payload)
	raw, _ := io.ReadAll(created.Body)
	created.Body.Close()
	if created.StatusCode != 201 {
		a.t.Fatalf("key creation status=%d body=%s", created.StatusCode, raw)
	}
	location, err := url.Parse(created.Header.Get("Location"))
	if err != nil || location.Path == "" {
		a.t.Fatal("missing created key location")
	}
	a.t.Cleanup(func() {
		deleted := request("DELETE", location.Path, nil)
		deleted.Body.Close()
		if deleted.StatusCode != 204 {
			a.t.Errorf("key cleanup status=%d", deleted.StatusCode)
		}
	})
}

func (a *Auth) KeyID(token string) string {
	a.t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		a.t.Fatal("invalid jwt")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		a.t.Fatal(err)
	}
	var header struct {
		KeyID string `json:"kid"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		a.t.Fatal(err)
	}
	return header.KeyID
}
