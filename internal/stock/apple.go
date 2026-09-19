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

// NearbyStores uses the full regional store list, not the stock-dependent eligibleStores list.
func (c *Client) NearbyStores(ctx context.Context, zip, part string) ([]Store, error) {
	if !zipPattern.MatchString(zip) || !partPattern.MatchString(part) {
		return nil, errors.New("store lookup requires a valid ZIP and product part number")
	}
	if err := c.SetLocation(ctx, zip); err != nil {
		return nil, err
	}
	var region struct {
		ProductMeta struct {
			StoreIDs *string `json:"retailStoreIds"`
		} `json:"productMeta"`
	}
	if err := c.get(ctx, "/shop/sba/d/product-recommendations", url.Values{"product": {part}}, &region); err != nil {
		return nil, err
	}
	if region.ProductMeta.StoreIDs == nil {
		return nil, errors.New("Apple did not return a nearby-store list")
	}
	ids := strings.Split(*region.ProductMeta.StoreIDs, ",")
	q := url.Values{"product": {part}}
	wanted := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !storePattern.MatchString(id) {
			return nil, errors.New("Apple returned an invalid nearby-store ID")
		}
		if wanted[id] {
			continue
		}
		q.Set(fmt.Sprintf("stores.%d", len(wanted)), id)
		wanted[id] = true
	}
	if len(wanted) == 0 {
		return nil, fmt.Errorf("Apple found no nearby stores for ZIP %s", zip)
	}
	if len(wanted) > 30 {
		return nil, errors.New("Apple returned more than 30 nearby stores")
	}
	var body struct {
		Content []Pickup `json:"content"`
	}
	if err := c.get(ctx, "/shop/sba/pickup-detail", q, &body); err != nil {
		return nil, err
	}
	stores := make([]Store, 0, len(wanted))
	seen := map[string]bool{}
	for _, p := range body.Content {
		name := strings.TrimSpace(strings.TrimPrefix(p.Address.Name, "Apple "))
		if !wanted[p.StoreID] || seen[p.StoreID] || name == "" {
			return nil, errors.New("Apple returned incomplete or unexpected store details")
		}
		seen[p.StoreID] = true
		stores = append(stores, Store{ID: p.StoreID, Name: name, Enabled: true})
	}
	if len(seen) != len(wanted) {
		return nil, errors.New("Apple did not return details for every nearby store")
	}
	sort.Slice(stores, func(i, j int) bool { return stores[i].Name < stores[j].Name })
	return stores, nil
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
