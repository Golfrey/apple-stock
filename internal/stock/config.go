package stock

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Product struct {
	Part    string `json:"part"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type Store struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type Config struct {
	ZIP          string    `json:"zip"`
	ProductPage  string    `json:"product_page"`
	Products     []Product `json:"products"`
	Stores       []Store   `json:"stores"`
	SlackWebhook string    `json:"slack_webhook,omitempty"`
	PickupPolicy string    `json:"pickup_policy"` // any or today
}

var partPattern = regexp.MustCompile(`^[A-Z0-9]{5,12}/A$`)
var storePattern = regexp.MustCompile(`^R[0-9]{3,5}$`)
var zipPattern = regexp.MustCompile(`^[0-9]{5}$`)

func DefaultDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ".apple-stock"
	}
	return filepath.Join(dir, "apple-stock")
}

func DefaultConfig() Config {
	c := Config{ZIP: "10001", ProductPage: "https://www.apple.com/shop/buy-iphone/iphone-18-pro", PickupPolicy: "any"}
	for _, s := range []Store{
		{"R095", "Fifth Avenue", true}, {"R415", "Grand Central", true},
		{"R251", "Upper West Side", true}, {"R582", "Upper East Side", true},
		{"R032", "SoHo", true}, {"R250", "West 14th Street", true},
	} {
		c.Stores = append(c.Stores, s)
	}
	for _, p := range []Product{
		{"MJW44LL/A", "iPhone 18 Pro Max 256GB Black", true},
		{"MJW64LL/A", "iPhone 18 Pro Max 256GB Burgundy", true},
		{"MJW74LL/A", "iPhone 18 Pro Max 256GB Glacier", true},
		{"MJW54LL/A", "iPhone 18 Pro Max 256GB Silver", true},
	} {
		c.Products = append(c.Products, p)
	}
	return c
}

func (c Config) Validate() error {
	if !zipPattern.MatchString(c.ZIP) {
		return errors.New("ZIP must contain five digits")
	}
	if err := ValidatePage(c.ProductPage); err != nil {
		return err
	}
	if c.PickupPolicy != "any" && c.PickupPolicy != "today" {
		return errors.New("pickup_policy must be any or today")
	}
	seen := map[string]bool{}
	n := 0
	for _, p := range c.Products {
		if !partPattern.MatchString(p.Part) || p.Name == "" {
			return fmt.Errorf("invalid product %q", p.Part)
		}
		if seen[p.Part] {
			return fmt.Errorf("duplicate part %s", p.Part)
		}
		seen[p.Part] = true
		if p.Enabled {
			n++
		}
	}
	if n == 0 {
		return errors.New("select at least one product")
	}
	if n > 32 {
		return errors.New("select at most 32 products per minute")
	}
	seen = map[string]bool{}
	n = 0
	for _, s := range c.Stores {
		if !storePattern.MatchString(s.ID) || s.Name == "" {
			return fmt.Errorf("invalid store %q", s.ID)
		}
		if seen[s.ID] {
			return fmt.Errorf("duplicate store %s", s.ID)
		}
		seen[s.ID] = true
		if s.Enabled {
			n++
		}
	}
	if n == 0 {
		return errors.New("select at least one store")
	}
	if n > 30 {
		return errors.New("select at most 30 stores")
	}
	if c.SlackWebhook != "" {
		return ValidateWebhook(c.SlackWebhook)
	}
	return nil
}

func ValidateWebhook(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || (u.Host != "hooks.slack.com" && u.Host != "hooks.slack-gov.com") || !strings.HasPrefix(u.Path, "/services/") || len(strings.Split(strings.Trim(u.Path, "/"), "/")) != 4 || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("enter a Slack incoming-webhook URL: https://hooks.slack.com/services/…")
	}
	return nil
}

func ValidatePage(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "www.apple.com" || !strings.HasPrefix(u.Path, "/shop/buy-iphone/") || u.User != nil {
		return errors.New("product page must be a US Apple /shop/buy-iphone/ URL")
	}
	return nil
}

func LoadConfig(dir string) (Config, error) {
	var c Config
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, errors.New("config.json contains invalid JSON")
	}
	return c, c.Validate()
}

func SaveConfig(dir string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return WriteJSON(filepath.Join(dir, "config.json"), c)
}

func WriteJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
