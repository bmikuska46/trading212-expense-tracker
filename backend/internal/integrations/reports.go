package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Trading 212 report statuses.
const (
	ReportQueued     = "Queued"
	ReportProcessing = "Processing"
	ReportRunning    = "Running"
	ReportCanceled   = "Canceled"
	ReportFailed     = "Failed"
	ReportFinished   = "Finished"
)

const (
	// Trading 212 allows one report request per 30 seconds and one report
	// list per minute.
	ReportRequestInterval = 30 * time.Second
	ReportListInterval    = time.Minute

	downloadTimeout = 2 * time.Minute
	// MaxReportSize matches the upload limit for CSV files.
	MaxReportSize = 20 << 20
)

// Report is one generated history report.
type Report struct {
	ID           string
	Status       string
	DownloadLink string
	TimeFrom     time.Time
	TimeTo       time.Time
}

// ReportError describes a failed report call. Messages are written here and
// never copy response bodies.
type ReportError struct {
	Code      string
	Detail    string
	Permanent bool
}

func (e *ReportError) Error() string { return e.Detail }

func reportFailure(status int, action string) *ReportError {
	switch status {
	case http.StatusUnauthorized:
		return &ReportError{Code: "unauthorized", Permanent: true,
			Detail: "Trading 212 rejected the API key or secret. Update them in Integrations."}
	case http.StatusForbidden:
		return &ReportError{Code: "forbidden", Permanent: true,
			Detail: "The API key lacks the History reports permission. Enable it in Trading 212 and check the integration."}
	case http.StatusBadRequest:
		return &ReportError{Code: "rejected", Permanent: true,
			Detail: fmt.Sprintf("Trading 212 rejected the request to %s.", action)}
	case http.StatusTooManyRequests:
		return &ReportError{Code: "rate_limited", Detail: "Trading 212 is rate limiting report requests."}
	case http.StatusRequestTimeout:
		return &ReportError{Code: "timeout", Detail: fmt.Sprintf("Trading 212 timed out while trying to %s.", action)}
	}
	return &ReportError{Code: "unexpected", Detail: fmt.Sprintf("Trading 212 returned an unexpected response while trying to %s.", action)}
}

func (c *Client) trading212Auth(credentials Trading212Credentials) (string, func(*http.Request)) {
	base := strings.TrimRight(c.trading212Base(credentials.Environment), "/")
	return base, func(request *http.Request) { request.SetBasicAuth(credentials.APIKey, credentials.APISecret) }
}

// reportTime is the timestamp format sent to Trading 212.
func reportTime(instant time.Time) string { return instant.UTC().Format(time.RFC3339) }

// RequestReport asks for a transactions-only report of [from, to) and returns
// its ID. A network failure leaves it unknown whether a report was created.
func (c *Client) RequestReport(ctx context.Context, credentials Trading212Credentials, from, to time.Time) (string, error) {
	base, authorize := c.trading212Auth(credentials)
	body, _ := json.Marshal(map[string]any{
		"dataIncluded": map[string]bool{"includeTransactions": true, "includeOrders": false,
			"includeDividends": false, "includeInterest": false},
		"timeFrom": reportTime(from), "timeTo": reportTime(to),
	})
	result, err := c.post(ctx, base+"/equity/history/exports", body, authorize)
	if err != nil {
		return "", &ReportError{Code: "unreachable", Detail: "Could not reach Trading 212 to request the report."}
	}
	if result.status != http.StatusOK {
		return "", reportFailure(result.status, "create the report")
	}
	var created struct {
		ReportID json.Number `json:"reportId"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(result.body)))
	decoder.UseNumber()
	if err := decoder.Decode(&created); err != nil || created.ReportID.String() == "" {
		return "", &ReportError{Code: "unreadable", Detail: "Trading 212 returned a report ID this app cannot read."}
	}
	return created.ReportID.String(), nil
}

// ListReports returns the account's generated reports.
func (c *Client) ListReports(ctx context.Context, credentials Trading212Credentials) ([]Report, error) {
	base, authorize := c.trading212Auth(credentials)
	result, err := c.get(ctx, base+"/equity/history/exports", authorize)
	if err != nil {
		return nil, &ReportError{Code: "unreachable", Detail: "Could not reach Trading 212 to check the report."}
	}
	if result.status != http.StatusOK {
		return nil, reportFailure(result.status, "list reports")
	}
	var listed []struct {
		ReportID     json.Number `json:"reportId"`
		Status       string      `json:"status"`
		DownloadLink string      `json:"downloadLink"`
		TimeFrom     string      `json:"timeFrom"`
		TimeTo       string      `json:"timeTo"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(result.body)))
	decoder.UseNumber()
	if err := decoder.Decode(&listed); err != nil {
		return nil, &ReportError{Code: "unreadable", Detail: "Trading 212 returned a report list this app cannot read."}
	}
	reports := make([]Report, 0, len(listed))
	for _, item := range listed {
		report := Report{ID: item.ReportID.String(), Status: item.Status, DownloadLink: item.DownloadLink}
		report.TimeFrom, _ = time.Parse(time.RFC3339Nano, item.TimeFrom)
		report.TimeTo, _ = time.Parse(time.RFC3339Nano, item.TimeTo)
		reports = append(reports, report)
	}
	return reports, nil
}

// DownloadReport fetches a finished report. The link is pre-authorized, so
// Trading 212 credentials are never sent with it, and only HTTPS is used.
func (c *Client) DownloadReport(ctx context.Context, link string) ([]byte, error) {
	target, err := url.Parse(link)
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil {
		return nil, &ReportError{Code: "bad_link", Permanent: true, Detail: "Trading 212 returned a download link this app does not trust."}
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, &ReportError{Code: "bad_link", Permanent: true, Detail: "Trading 212 returned a download link this app does not trust."}
	}
	request.Header.Set("User-Agent", "budget-tracking")
	client := *c.HTTP
	client.Timeout = downloadTimeout
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if next.URL.Scheme != "https" || len(via) >= 5 {
			return errors.New("redirect refused")
		}
		return nil
	}
	result, err := client.Do(request)
	if err != nil {
		return nil, &ReportError{Code: "unreachable", Detail: "Could not download the report."}
	}
	defer result.Body.Close()
	switch {
	case result.StatusCode == http.StatusForbidden || result.StatusCode == http.StatusNotFound:
		// Download links expire; listing again returns a fresh one.
		return nil, &ReportError{Code: "link_expired", Detail: "The report download link expired."}
	case result.StatusCode != http.StatusOK:
		return nil, &ReportError{Code: "unexpected", Detail: "The report download returned an unexpected response."}
	}
	data, err := io.ReadAll(io.LimitReader(result.Body, MaxReportSize+1))
	if err != nil {
		return nil, &ReportError{Code: "unreachable", Detail: "The report download was interrupted."}
	}
	if len(data) > MaxReportSize {
		return nil, &ReportError{Code: "too_large", Permanent: true,
			Detail: fmt.Sprintf("The report is larger than %d MiB.", MaxReportSize>>20)}
	}
	return data, nil
}
