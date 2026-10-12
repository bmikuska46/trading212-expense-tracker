package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Turnstile struct {
	SecretKey string
	Endpoint  string
	HTTP      *http.Client
}

func NewTurnstile(secretKey string) *Turnstile {
	return &Turnstile{
		SecretKey: secretKey,
		Endpoint:  "https://challenges.cloudflare.com/turnstile/v0/siteverify",
		HTTP:      &http.Client{Timeout: 10 * time.Second},
	}
}

func (t *Turnstile) Verify(ctx context.Context, token string) (bool, error) {
	if token == "" || len(token) > 2048 {
		return false, nil
	}
	if t.SecretKey == "" {
		return false, errors.New("Turnstile secret is missing")
	}
	form := url.Values{"secret": {t.SecretKey}, "response": {token}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, errors.New("could not create Turnstile request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := t.HTTP.Do(request)
	if err != nil {
		return false, errors.New("Turnstile is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, errors.New("Turnstile returned an unexpected status")
	}
	var result struct {
		Success bool   `json:"success"`
		Action  string `json:"action"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	if err := decoder.Decode(&result); err != nil {
		return false, errors.New("Turnstile returned an invalid response")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return false, errors.New("Turnstile returned an invalid response")
	}
	// Cloudflare's public test key may omit the action for dummy tokens.
	testAction := t.SecretKey == "1x0000000000000000000000000000000AA" && (result.Action == "test" || result.Action == "")
	return result.Success && (result.Action == "login" || testAction), nil
}
