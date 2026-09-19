package stock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	HTTP        *http.Client
	Base        string
	Referer     string
	MinInterval time.Duration
	lastRequest time.Time
}

func NewClient(page string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{HTTP: &http.Client{Jar: jar, Timeout: 12 * time.Second}, Base: "https://www.apple.com", Referer: page, MinInterval: time.Second}
}

type apiEnvelope struct {
	Head struct {
		Status json.RawMessage `json:"status"`
	} `json:"head"`
	Body json.RawMessage `json:"body"`
}

type APIError struct {
	Status     int
	RetryAfter time.Duration
}

func (e *APIError) Error() string { return fmt.Sprintf("Apple HTTP %d", e.Status) }

func (c *Client) get(ctx context.Context, path string, values url.Values, dest any) error {
	if wait := c.MinInterval - time.Since(c.lastRequest); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.lastRequest = time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", c.Base+path+"?"+values.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", c.Referer)
	req.Header.Set("User-Agent", "AppleStock/1.0")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("Apple request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return &APIError{Status: resp.StatusCode, RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}
	}
	var envelope apiEnvelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&envelope); err != nil {
		return errors.New("Apple returned invalid JSON; availability is unknown")
	}
	if s := strings.Trim(string(envelope.Head.Status), `"`); s != "200" {
		return fmt.Errorf("Apple application status %q", s)
	}
	if err := json.Unmarshal(envelope.Body, dest); err != nil {
		return errors.New("Apple response schema changed; availability is unknown")
	}
	return nil
}

func retryAfter(s string) time.Duration {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(s); err == nil {
		return time.Until(t)
	}
	return 0
}

func (c *Client) SetLocation(ctx context.Context, zip string) error {
	var body struct {
		Content struct {
			Address struct {
				PostalCode string `json:"postalCode"`
			} `json:"address"`
		} `json:"content"`
	}
	if err := c.get(ctx, "/shop/address/location/update", url.Values{"postalCode": {zip}}, &body); err != nil {
		return err
	}
	if body.Content.Address.PostalCode != zip {
		return fmt.Errorf("Apple did not confirm ZIP %s; refusing results for another location", zip)
	}
	return nil
}

type Pickup struct {
	StoreID string `json:"storeId"`
	Quote   string `json:"pickupSearchQuote"`
	Date    string `json:"pickupEncodedUpperDateString"`
	City    string `json:"city"`
	Address struct {
		Name   string `json:"address"`
		Street string `json:"address2"`
		ZIP    string `json:"postalCode"`
	} `json:"address"`
}

type Result struct {
	Part      string    `json:"part"`
	Product   string    `json:"product"`
	StoreID   string    `json:"store_id"`
	Store     string    `json:"store"`
	Quote     string    `json:"quote"`
	Date      string    `json:"date,omitempty"`
	Available bool      `json:"available"`
	CheckedAt time.Time `json:"checked_at"`
}

func (c *Client) Pickup(ctx context.Context, product Product, stores []Store, now time.Time) ([]Result, error) {
	q := url.Values{"product": {product.Part}}
	wanted := map[string]Store{}
	for _, s := range stores {
		if s.Enabled {
			q.Set(fmt.Sprintf("stores.%d", len(wanted)), s.ID)
			wanted[s.ID] = s
		}
	}
	var body struct {
		Content []Pickup `json:"content"`
	}
	if err := c.get(ctx, "/shop/sba/pickup-detail", q, &body); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	results := make([]Result, 0, len(wanted))
	for _, p := range body.Content {
		s, ok := wanted[p.StoreID]
		if !ok {
			return nil, fmt.Errorf("Apple returned unexpected store %s", p.StoreID)
		}
		if seen[p.StoreID] {
			return nil, fmt.Errorf("Apple returned duplicate store %s", p.StoreID)
		}
		seen[p.StoreID] = true
		available, err := pickupAvailable(p.Quote, p.Date)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.Name, err)
		}
		results = append(results, Result{Part: product.Part, Product: product.Name, StoreID: p.StoreID, Store: s.Name, Quote: p.Quote, Date: p.Date, Available: available, CheckedAt: now})
	}
	if len(seen) != len(wanted) {
		return nil, fmt.Errorf("Apple returned %d of %d requested stores; missing results are unknown", len(seen), len(wanted))
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Store < results[j].Store })
	return results, nil
}

func pickupAvailable(quote, date string) (bool, error) {
	lower := strings.ToLower(strings.TrimSpace(quote))
	if lower == "currently unavailable" || lower == "unavailable" || lower == "not available" {
		return false, nil
	}
	if strings.HasPrefix(lower, "available") {
		if _, err := time.Parse("20060102", date); err != nil {
			return false, errors.New("available pickup is missing a valid date")
		}
		return true, nil
	}
	return false, fmt.Errorf("unrecognized pickup status %q", quote)
}

// Discover decodes product arrays embedded in Apple's shopping HTML, without executing JavaScript.
func (c *Client) Discover(ctx context.Context, page string) ([]Product, error) {
	if err := ValidatePage(page); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", page, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, &APIError{Status: resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return ParseProducts(string(b))
}

func ParseProducts(html string) ([]Product, error) {
	products := map[string]Product{}
	for _, tail := range strings.Split(html, `"products":`)[1:] {
		var entries []struct {
			Part string `json:"partNumber"`
			Name string `json:"name"`
		}
		if json.NewDecoder(strings.NewReader(tail)).Decode(&entries) != nil {
			continue
		}
		for _, p := range entries {
			name := strings.Join(strings.Fields(p.Name), " ")
			if !strings.HasPrefix(name, "iPhone ") || !strings.HasSuffix(p.Part, "LL/A") || !partPattern.MatchString(p.Part) {
				continue
			}
			products[p.Part] = Product{Part: p.Part, Name: name}
		}
	}
	if len(products) == 0 {
		return nil, errors.New("no US iPhone part numbers found in Apple page")
	}
	result := make([]Product, 0, len(products))
	for _, p := range products {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func MergeProducts(old, fresh []Product) []Product {
	byPart := map[string]Product{}
	for _, p := range old {
		byPart[p.Part] = p
	}
	for _, p := range fresh {
		p.Enabled = byPart[p.Part].Enabled
		byPart[p.Part] = p
	}
	result := make([]Product, 0, len(byPart))
	for _, p := range byPart {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
