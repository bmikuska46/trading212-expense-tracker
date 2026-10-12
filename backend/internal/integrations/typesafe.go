package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	askAttemptTimeout = 30 * time.Second
	askRetries        = 2
)

// Question types sent to TypeSafe.
const (
	QuestionChoice = "choice"
	QuestionNoul   = "noul"
)

// Question is one typed TypeSafe question. Instructions and Criteria are
// sent as JSON; a choice's criteria map each option to its description.
type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Answer is a TypeSafe answer. Choice answers fill Choice, Probabilities,
// and Confidence; noul answers fill Noul, the probability of yes.
type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
	Noul          float64            `json:"noul"`
}

// SuggestError carries a message written here; it never includes response
// bodies or sample values.
type SuggestError struct{ Message string }

func (e *SuggestError) Error() string { return e.Message }

// CheckTypeSafe verifies the key and that it can use the mapping model.
func (c *Client) CheckTypeSafe(ctx context.Context, apiKey string) Check {
	result, err := c.get(ctx, strings.TrimRight(c.TypeSafe, "/")+"/models", bearer(apiKey))
	if err != nil {
		return failed("Could not reach TypeSafe. Check the network connection and try again.")
	}
	item := Item{Key: "model", Label: "Model " + JevModel}
	switch result.status {
	case http.StatusOK:
		var list struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if json.Unmarshal(result.body, &list) != nil {
			return failed("TypeSafe returned an unexpected response. Try again later.")
		}
		item.State, item.Detail = StateMissing, "The model is not available to this key"
		for _, model := range list.Models {
			if model.Name == JevModel {
				item.State, item.Detail = StateOK, ""
			}
		}
	case http.StatusUnauthorized:
		return failed("TypeSafe rejected the API key.")
	case http.StatusTooManyRequests, 529:
		item.State, item.Detail = StateUnverified, "Rate limited or overloaded"
	default:
		return failed("TypeSafe returned an unexpected response. Try again later.")
	}
	return summarize("Connected to TypeSafe", []Item{item})
}

// Ask evaluates questions about state with Jev and returns an answer of the
// asked type for every question. Callers decide what state holds; retries
// resend the same request body.
func (c *Client) Ask(ctx context.Context, apiKey string, state any, questions map[string]Question) (map[string]Answer, error) {
	body, err := json.Marshal(struct {
		State     any                 `json:"state"`
		Model     string              `json:"model"`
		Questions map[string]Question `json:"questions"`
	}{state, JevModel, questions})
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(c.TypeSafe, "/") + "/systemone"
	var result response
	for attempt := 0; ; attempt++ {
		result, err = c.post(ctx, url, body, bearer(apiKey))
		retry := err != nil || result.status == http.StatusTooManyRequests || result.status >= 500
		if !retry || attempt == askRetries {
			break
		}
		select {
		case <-ctx.Done():
			return nil, &SuggestError{"The mapping suggestion was canceled"}
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
	switch {
	case err != nil:
		return nil, &SuggestError{"Could not reach TypeSafe. Map the columns manually or try again later."}
	case result.status == http.StatusUnauthorized:
		return nil, &SuggestError{"TypeSafe rejected the API key. Check the TypeSafe integration."}
	case result.status == http.StatusTooManyRequests || result.status == 529:
		return nil, &SuggestError{"TypeSafe is rate limited or overloaded. Map the columns manually or try again later."}
	case result.status != http.StatusOK:
		return nil, &SuggestError{"TypeSafe returned an unexpected response. Map the columns manually."}
	}
	var decoded struct {
		Answers map[string]Answer `json:"answers"`
	}
	if err := json.Unmarshal(result.body, &decoded); err != nil {
		return nil, &SuggestError{"TypeSafe returned an unreadable response. Map the columns manually."}
	}
	for id, question := range questions {
		if answer, ok := decoded.Answers[id]; !ok || answer.Type != question.Type {
			return nil, &SuggestError{"TypeSafe returned an incomplete answer. Map the columns manually."}
		}
	}
	return decoded.Answers, nil
}

func (c *Client) post(ctx context.Context, url string, body []byte, authorize func(*http.Request)) (response, error) {
	ctx, cancel := context.WithTimeout(ctx, askAttemptTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return response{}, errUnreachable
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "budget-tracking")
	authorize(request)
	// The shared client's timeout suits quick checks; model responses take longer.
	client := *c.HTTP
	client.Timeout = askAttemptTimeout
	result, err := client.Do(request)
	if err != nil {
		return response{}, errors.Join(errUnreachable, err)
	}
	defer result.Body.Close()
	data, err := io.ReadAll(io.LimitReader(result.Body, maxResponseSize))
	if err != nil {
		return response{}, errUnreachable
	}
	return response{status: result.StatusCode, body: data}, nil
}
