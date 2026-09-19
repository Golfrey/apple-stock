package stock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type NotifyFunc func(context.Context, string, string) error

func SendSlack(ctx context.Context, webhook, text string) error {
	if err := ValidateWebhook(webhook); err != nil {
		return err
	}
	// Disable redirects so a webhook credential cannot be forwarded elsewhere.
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return sendSlack(ctx, client, webhook, text)
}

func sendSlack(ctx context.Context, client *http.Client, webhook, text string) error {
	payload, _ := json.Marshal(map[string]any{"text": text, "mrkdwn": false, "unfurl_links": false, "unfurl_media": false})
	req, err := http.NewRequestWithContext(ctx, "POST", webhook, bytes.NewReader(payload))
	if err != nil {
		return errors.New("could not create Slack request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	// Never return the underlying error: it may contain the secret webhook URL.
	if err != nil {
		return errors.New("Slack request failed (network error or timeout)")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return errors.New("could not read Slack response")
	}
	if resp.StatusCode != 200 || strings.TrimSpace(string(body)) != "ok" {
		return fmt.Errorf("Slack did not acknowledge notification (HTTP %d)", resp.StatusCode)
	}
	return nil
}

func AlertText(rows []Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Apple pickup available — %d option(s)\n", len(rows))
	for _, r := range rows {
		date := r.Date
		if t, err := time.Parse("20060102", date); err == nil {
			date = t.Format("Jan 2")
		}
		fmt.Fprintf(&b, "\n%s\nApple %s — %s (%s)\n", r.Product, r.Store, r.Quote, date)
	}
	b.WriteString("\nhttps://www.apple.com/shop/buy-iphone\nAvailability may change before checkout.")
	return b.String()
}
