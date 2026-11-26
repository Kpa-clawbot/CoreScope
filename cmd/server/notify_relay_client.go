package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"
)

const (
	notifyRelayContractVersion = 1
	notifyRelayMaxBody         = 64 << 10
	notifyRelayClientTimeout   = 10 * time.Second
)

type notifyRelayClient struct {
	origin string
	http   *http.Client
}

type notifyRelayEnrollmentRequest struct {
	Code      string `json:"code"`
	RequestID string `json:"request_id"`
}

type notifyRelaySelfEnrollmentRequest struct {
	Origin           string `json:"origin"`
	PublicKey        string `json:"public_key"`
	SenderCredential string `json:"sender_credential"`
}

type notifyRelayEnrollment struct {
	AnalyzerID       string `json:"analyzer_id"`
	SenderCredential string `json:"sender_credential"`
}

type notifyRelayAnalyzer struct {
	AnalyzerID      string `json:"analyzer_id"`
	Origin          string `json:"origin"`
	ContractVersion int    `json:"contract_version"`
}

type notifyRelayLinkRequest struct {
	InstallationID string `json:"installation_id"`
	BindingID      string `json:"binding_id"`
	Proof          string `json:"proof"`
}

type notifyRelaySubscription struct {
	SubscriptionID string    `json:"subscription_id"`
	Status         string    `json:"status"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type notifyRelayDeliveryRequest struct {
	EventID        string    `json:"event_id"`
	SubscriptionID string    `json:"subscription_id"`
	Kind           string    `json:"kind"`
	NodePubkey     string    `json:"node_pubkey"`
	NodeName       string    `json:"node_name,omitempty"`
	OccurredAt     time.Time `json:"occurred_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type notifyRelayDelivery struct {
	DeliveryID string `json:"delivery_id"`
	Status     string `json:"status"`
}

type notifyRelayError struct {
	Status     int
	RetryAfter time.Duration
}

func (e *notifyRelayError) Error() string {
	return fmt.Sprintf("notification relay returned HTTP %d", e.Status)
}

func newNotifyRelayClient(origin string, transport http.RoundTripper) (*notifyRelayClient, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return nil, errors.New("notification relay requires an HTTPS origin")
	}
	return &notifyRelayClient{
		origin: "https://" + u.Host,
		http: &http.Client{
			Transport: transport,
			Timeout:   notifyRelayClientTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *notifyRelayClient) Enroll(ctx context.Context, code, requestID string) (notifyRelayEnrollment, error) {
	var result notifyRelayEnrollment
	request := notifyRelayEnrollmentRequest{Code: code, RequestID: requestID}
	if err := c.request(ctx, http.MethodPost, "/v1/analyzers/enroll", "", request, &result, http.StatusCreated, http.StatusOK); err != nil {
		return notifyRelayEnrollment{}, err
	}
	if result.AnalyzerID == "" || result.SenderCredential == "" {
		return notifyRelayEnrollment{}, errors.New("notification relay returned an incomplete enrollment")
	}
	return result, nil
}

func (c *notifyRelayClient) SelfEnroll(ctx context.Context, request notifyRelaySelfEnrollmentRequest) (notifyRelayEnrollment, error) {
	var result notifyRelayEnrollment
	if err := c.request(ctx, http.MethodPost, "/v1/analyzers/self-enroll", "", request, &result, http.StatusCreated); err != nil {
		return notifyRelayEnrollment{}, err
	}
	if result.AnalyzerID == "" || result.SenderCredential != request.SenderCredential {
		return notifyRelayEnrollment{}, errors.New("notification relay returned an invalid enrollment")
	}
	return result, nil
}

func (c *notifyRelayClient) Analyzer(ctx context.Context, credential string) (notifyRelayAnalyzer, error) {
	var result notifyRelayAnalyzer
	if err := c.request(ctx, http.MethodGet, "/v1/analyzer", credential, nil, &result, http.StatusOK); err != nil {
		return notifyRelayAnalyzer{}, err
	}
	if result.ContractVersion != notifyRelayContractVersion {
		return notifyRelayAnalyzer{}, errors.New("notification relay returned an unsupported contract version")
	}
	if result.AnalyzerID == "" || result.Origin == "" {
		return notifyRelayAnalyzer{}, errors.New("notification relay returned incomplete analyzer details")
	}
	return result, nil
}

func (c *notifyRelayClient) RevokeAnalyzer(ctx context.Context, credential string) error {
	return c.request(ctx, http.MethodDelete, "/v1/analyzer", credential, nil, nil, http.StatusNoContent, http.StatusOK)
}

func (c *notifyRelayClient) Link(ctx context.Context, credential string, request notifyRelayLinkRequest) (notifyRelaySubscription, error) {
	var result notifyRelaySubscription
	if err := c.request(ctx, http.MethodPost, "/v1/subscriptions", credential, request, &result, http.StatusCreated, http.StatusOK); err != nil {
		return notifyRelaySubscription{}, err
	}
	if result.SubscriptionID == "" || result.Status == "" || result.ExpiresAt.IsZero() {
		return notifyRelaySubscription{}, errors.New("notification relay returned an incomplete subscription")
	}
	return result, nil
}

func (c *notifyRelayClient) RevokeSubscription(ctx context.Context, credential, subscriptionID string) error {
	if subscriptionID == "" || subscriptionID == "." || subscriptionID == ".." {
		return errors.New("notification relay requires a subscription ID")
	}
	path := "/v1/subscriptions/" + url.PathEscape(subscriptionID)
	return c.request(ctx, http.MethodDelete, path, credential, nil, nil, http.StatusNoContent, http.StatusOK)
}

func (c *notifyRelayClient) Deliver(ctx context.Context, credential string, request notifyRelayDeliveryRequest) (notifyRelayDelivery, error) {
	var result notifyRelayDelivery
	if err := c.request(ctx, http.MethodPost, "/v1/deliveries", credential, request, &result, http.StatusAccepted, http.StatusOK); err != nil {
		return notifyRelayDelivery{}, err
	}
	if result.DeliveryID == "" || result.Status == "" {
		return notifyRelayDelivery{}, errors.New("notification relay returned an incomplete delivery")
	}
	return result, nil
}

func (c *notifyRelayClient) request(ctx context.Context, method, path, credential string, input, output any, accepted ...int) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil || len(data) > notifyRelayMaxBody {
			return errors.New("invalid notification relay request body")
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+path, body)
	if err != nil {
		return errors.New("invalid notification relay request")
	}
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("notification relay request failed")
	}
	defer resp.Body.Close()
	if !slices.Contains(accepted, resp.StatusCode) {
		return &notifyRelayError{
			Status:     resp.StatusCode,
			RetryAfter: notifyRelayRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, notifyRelayMaxBody+1))
	if err != nil || len(data) > notifyRelayMaxBody {
		return errors.New("invalid notification relay response size")
	}
	if output != nil && json.Unmarshal(data, output) != nil {
		return errors.New("invalid notification relay response JSON")
	}
	return nil
}

func notifyRelayRetryAfter(value string) time.Duration {
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if deadline, err := http.ParseTime(value); err == nil {
		return max(0, time.Until(deadline))
	}
	return 0
}
