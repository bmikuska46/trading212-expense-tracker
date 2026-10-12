// Package integrations connects to Trading 212, TypeSafe, and Resend.
package integrations

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

const (
	Trading212LiveURL = "https://live.trading212.com/api/v0"
	Trading212DemoURL = "https://demo.trading212.com/api/v0"
	TypeSafeURL       = "https://api.typesafe.ai/v1"
	ResendURL         = "https://api.resend.com"
	JevModel          = "jev-latest"

	requestTimeout  = 10 * time.Second
	maxResponseSize = 1 << 20
)

// Check states for individual permissions and for the whole connection.
const (
	StateOK         = "ok"
	StateMissing    = "missing"
	StateUnverified = "unverified"

	StatusOK      = "ok"
	StatusPartial = "partial"
	StatusFailed  = "failed"
)

// Item reports one permission or capability. Messages are written here and
// never copy response bodies, which could echo credentials.
type Item struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

type Check struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Items   []Item `json:"items"`
}

func failed(message string, items ...Item) Check {
	return Check{Status: StatusFailed, Message: message, Items: nonNil(items)}
}

func nonNil(items []Item) []Item {
	if items == nil {
		return []Item{}
	}
	return items
}

// summarize derives the overall status from permission items.
func summarize(message string, items []Item) Check {
	status := StatusOK
	for _, item := range items {
		if item.State == StateMissing {
			status = StatusPartial
			message = "Connected, but some permissions are missing"
		}
	}
	return Check{Status: status, Message: message, Items: nonNil(items)}
}

// Client holds the base URLs so tests can point them at local servers.
type Client struct {
	HTTP           *http.Client
	Trading212Live string
	Trading212Demo string
	TypeSafe       string
	Resend         string
}

func NewClient() *Client {
	return &Client{
		HTTP: &http.Client{
			Timeout: requestTimeout,
			// Credentials are only sent to the configured endpoints.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		Trading212Live: Trading212LiveURL, Trading212Demo: Trading212DemoURL,
		TypeSafe: TypeSafeURL, Resend: ResendURL,
	}
}

var errUnreachable = errors.New("service unreachable")

type response struct {
	status int
	body   []byte
}

func (c *Client) get(ctx context.Context, url string, authorize func(*http.Request)) (response, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return response{}, errUnreachable
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "budget-tracking")
	authorize(request)
	result, err := c.HTTP.Do(request)
	if err != nil {
		return response{}, errUnreachable
	}
	defer result.Body.Close()
	body, err := io.ReadAll(io.LimitReader(result.Body, maxResponseSize))
	if err != nil {
		return response{}, errUnreachable
	}
	return response{status: result.StatusCode, body: body}, nil
}

func bearer(token string) func(*http.Request) {
	return func(request *http.Request) { request.Header.Set("Authorization", "Bearer "+token) }
}
