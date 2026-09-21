package stock

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := NewClient("https://www.apple.com/shop/buy-iphone/iphone-18-pro")
	c.Base = server.URL
	c.MinInterval = 0
	return c
}

func respond(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"head":{"status":"200"},"body":`+body+`}`)
}

func oneTarget() Config {
	c := DefaultConfig()
	c.Products = c.Products[:1]
	c.Stores = c.Stores[:1]
	c.SlackWebhook = "https://hooks.slack.com/services/TEST/TEST/TEST"
	return c
}

func TestLocationSessionAndExplicitStore(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/shop/address/location/update":
			if r.URL.Query().Get("postalCode") != "10001" {
				t.Error("wrong ZIP")
			}
			http.SetCookie(w, &http.Cookie{Name: "as_loc", Value: "10001", Path: "/"})
			respond(w, `{"content":{"address":{"postalCode":"10001"}}}`)
		case "/shop/sba/pickup-detail":
			cookie, err := r.Cookie("as_loc")
			if err != nil || cookie.Value != "10001" {
				t.Error("location cookie missing")
			}
			if r.URL.Query().Get("product") != "MJW44LL/A" || r.URL.Query().Get("stores.0") != "R095" {
				t.Error("wrong target")
			}
			respond(w, `{"content":[{"storeId":"R095","pickupSearchQuote":"Available Today","pickupEncodedUpperDateString":"20260919"}]}`)
		default:
			t.Error("unexpected path", r.URL.Path)
		}
	})
	if err := c.SetLocation(context.Background(), "10001"); err != nil {
		t.Fatal(err)
	}
	cfg := oneTarget()
	rows, err := c.Pickup(context.Background(), cfg.Products[0], cfg.Stores, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].Available || rows[0].StoreID != "R095" {
		t.Fatalf("unexpected results: %+v", rows)
	}
}

func TestInvalidResponsesAreUnknownNotOutOfStock(t *testing.T) {
	for name, content := range map[string]string{
		"missing stores": `[]`,
		"wrong store":    `[{"storeId":"R047","pickupSearchQuote":"Currently unavailable"}]`,
		"unknown status": `[{"storeId":"R095","pickupSearchQuote":"Service unavailable, try later","pickupEncodedUpperDateString":"20260919"}]`,
		"missing date":   `[{"storeId":"R095","pickupSearchQuote":"Available Today"}]`,
		"duplicate":      `[{"storeId":"R095","pickupSearchQuote":"Currently unavailable"},{"storeId":"R095","pickupSearchQuote":"Currently unavailable"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { respond(w, `{"content":`+content+`}`) })
			cfg := oneTarget()
			if _, err := c.Pickup(context.Background(), cfg.Products[0], cfg.Stores, time.Now()); err == nil {
				t.Fatal("expected schema/status error")
			}
		})
	}
}

func TestAlertsRetryDeduplicateAndRecover(t *testing.T) {
	now := time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)
	available := true
	date := "20260919"
	apiFail := false
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if apiFail {
			w.WriteHeader(541)
			return
		}
		if strings.Contains(r.URL.Path, "location") {
			respond(w, `{"content":{"address":{"postalCode":"10001"}}}`)
			return
		}
		if available {
			respond(w, fmt.Sprintf(`{"content":[{"storeId":"R095","pickupSearchQuote":"Available Today","pickupEncodedUpperDateString":"%s"}]}`, date))
		} else {
			respond(w, `{"content":[{"storeId":"R095","pickupSearchQuote":"Currently unavailable"}]}`)
		}
	})
	dir := t.TempDir()
	cfg := oneTarget()
	attempts := 0
	failDelivery := true
	m := Monitor{Client: c, Now: func() time.Time { return now }, Notify: func(context.Context, string, string) error {
		attempts++
		if failDelivery {
			return errors.New("Slack unavailable")
		}
		return nil
	}}
	run := func() (State, error) { now = now.Add(time.Minute); return m.Run(context.Background(), dir, cfg, true) }
	s, err := run()
	if err == nil || len(s.Notified) != 0 {
		t.Fatal("failed Slack delivery must stay pending")
	}
	failDelivery = false
	s, err = run()
	if err != nil || attempts != 2 || len(s.Notified) != 1 {
		t.Fatalf("retry failed: %v %+v", err, s)
	}
	if _, err = run(); err != nil || attempts != 2 {
		t.Fatal("unchanged availability sent duplicate")
	}
	date = "20260920"
	if _, err = run(); err != nil || attempts != 2 {
		t.Fatal("changed pickup date sent duplicate while still in stock")
	}
	cfg.Products[0].Enabled = false
	if _, err = run(); err != nil {
		t.Fatal(err)
	}
	cfg.Products[0].Enabled = true
	if _, err = run(); err != nil || attempts != 2 {
		t.Fatal("re-enabling a target sent duplicate without observed unavailability")
	}
	apiFail = true
	s, err = run()
	if err == nil || len(s.Notified) != 1 {
		t.Fatal("API error cleared notification history")
	}
	apiFail = false
	if _, err = run(); err != nil || attempts != 2 {
		t.Fatal("API recovery sent duplicate")
	}
	available = false
	failDelivery = true
	s, err = run()
	if err == nil || attempts != 3 || len(s.Notified) != 0 {
		t.Fatal("unavailable did not send an alert and reset availability acknowledgement")
	}
	failDelivery = false
	if _, err = run(); err != nil || attempts != 4 {
		t.Fatal("failed unavailable alert was not retried after loading state")
	}
	if _, err = run(); err != nil || attempts != 4 {
		t.Fatal("unchanged unavailability sent duplicate")
	}
	available = true
	if _, err = run(); err != nil || attempts != 5 {
		t.Fatal("restock did not notify")
	}
}

func TestUnavailableAlertsAcrossChecks(t *testing.T) {
	for _, restockBeforeDelivery := range []bool{false, true} {
		t.Run(fmt.Sprintf("restock_before_delivery=%t", restockBeforeDelivery), func(t *testing.T) {
			now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
			available, apiFail := false, false
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				if apiFail {
					w.WriteHeader(503)
					return
				}
				if strings.Contains(r.URL.Path, "location") {
					respond(w, `{"content":{"address":{"postalCode":"10001"}}}`)
				} else if available {
					respond(w, `{"content":[{"storeId":"R095","pickupSearchQuote":"Available Today","pickupEncodedUpperDateString":"20260920"}]}`)
				} else {
					respond(w, `{"content":[{"storeId":"R095","pickupSearchQuote":"Currently unavailable"}]}`)
				}
			})
			var messages []string
			m := Monitor{Client: c, Now: func() time.Time { return now }, Notify: func(_ context.Context, _, message string) error {
				messages = append(messages, message)
				return nil
			}}
			dir := t.TempDir()
			run := func(notify bool) error {
				now = now.Add(time.Minute)
				_, err := m.Run(context.Background(), dir, oneTarget(), notify)
				return err
			}
			if err := run(true); err != nil || len(messages) != 0 {
				t.Fatal("initial unavailable observation should be silent", err)
			}
			available = true
			if err := run(true); err != nil || len(messages) != 1 {
				t.Fatal("initial availability should notify", err)
			}
			available = false
			if err := run(false); err != nil || len(messages) != 1 {
				t.Fatal("check without notifications sent an alert", err)
			}
			apiFail = true
			if err := run(true); err == nil || len(messages) != 1 {
				t.Fatal("API failure sent an unverified stock alert")
			}
			apiFail = false
			available = restockBeforeDelivery
			if err := run(true); err != nil || len(messages) != 2 {
				t.Fatal("pending transition did not notify after recovery", err)
			}
			want := "Unavailable"
			if restockBeforeDelivery {
				want = "Available Today"
			}
			if !strings.Contains(messages[1], want) || strings.Contains(messages[1], "()") {
				t.Fatalf("alert does not describe current availability: %s", messages[1])
			}
			if err := run(true); err != nil || len(messages) != 2 {
				t.Fatal("unchanged state sent duplicate", err)
			}
		})
	}
}

func TestAlertTextAvailability(t *testing.T) {
	available := Result{Product: "iPhone", Store: "World Trade Center", Available: true, Quote: "Available Today", Date: "20260920"}
	unavailable := Result{Product: "iPhone", Store: "SoHo", Quote: "Currently unavailable"}
	for _, tt := range []struct {
		name string
		rows []Result
		want string
	}{
		{"available", []Result{available}, "🟢 iPhone · World Trade Center · Available Today (Sep 20)"},
		{"unavailable", []Result{unavailable}, "🔴 iPhone · SoHo · Unavailable"},
		{"mixed", []Result{available, unavailable}, "🟢 iPhone · World Trade Center · Available Today (Sep 20)\n🔴 iPhone · SoHo · Unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			message := AlertText(tt.rows)
			if message != tt.want {
				t.Fatalf("alert = %q; want %q", message, tt.want)
			}
		})
	}
}

func TestPolicyAndDateChanges(t *testing.T) {
	s := State{Results: map[string]Result{}, Notified: map[string]string{}}
	r := Result{Part: "MJW44LL/A", StoreID: "R095", Quote: "Available Tomorrow", Date: "20260920", Available: true}
	if len(Pending(&s, []Result{r}, "today")) != 0 {
		t.Fatal("tomorrow alerted under today policy")
	}
	if len(Pending(&s, []Result{r}, "any")) != 1 {
		t.Fatal("tomorrow not alerted under any policy")
	}
	s.Notified[Key(r)] = r.Date
	r.Quote = "Available Today" // Same pickup date as yesterday's alert: no repeat.
	if len(Pending(&s, []Result{r}, "any")) != 0 {
		t.Fatal("repeated same pickup date")
	}
	r.Date = "20260919"
	if len(Pending(&s, []Result{r}, "any")) != 0 {
		t.Fatal("improved pickup date repeated an acknowledged alert")
	}
	r.Date = "20260921"
	if len(Pending(&s, []Result{r}, "today")) != 0 {
		t.Fatal("changed pickup date or policy repeated an acknowledged alert")
	}
	otherStore := r
	otherStore.StoreID = "R815"
	otherModel := r
	otherModel.Part = "MJW64LL/A"
	if len(Pending(&s, []Result{r, otherStore, otherModel}, "any")) != 2 {
		t.Fatal("acknowledged alert suppressed a different store or model")
	}
}

func TestRateLimitBackoffAndLock(t *testing.T) {
	now := time.Now()
	calls := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "180")
		w.WriteHeader(429)
	})
	dir := t.TempDir()
	m := Monitor{Client: c, Now: func() time.Time { return now }, Notify: func(context.Context, string, string) error { t.Fatal("unexpected notification"); return nil }}
	s, err := m.Run(context.Background(), dir, oneTarget(), true)
	if err == nil || s.NextAttempt.Sub(now) != 3*time.Minute {
		t.Fatal("Retry-After not honored")
	}
	now = now.Add(time.Minute)
	if _, err = m.Run(context.Background(), dir, oneTarget(), true); err != nil || calls != 1 {
		t.Fatal("backoff made extra requests")
	}
	unlock, err := acquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err = m.Run(context.Background(), dir, oneTarget(), true); !errors.Is(err, ErrBusy) {
		t.Fatal("overlapping check was allowed")
	}
}

func TestWrongLocationStopsLookup(t *testing.T) {
	calls := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		respond(w, `{"content":{"address":{"postalCode":"60601"}}}`)
	})
	m := Monitor{Client: c, Now: time.Now}
	s, err := m.Run(context.Background(), t.TempDir(), oneTarget(), false)
	if err == nil || calls != 1 || !s.LastSuccess.IsZero() {
		t.Fatal("wrong location accepted")
	}
}

func TestProductDiscoveryAndSelection(t *testing.T) {
	html := `<script>var data={"products":[{"partNumber":"MJW44LL/A","name":"iPhone\u00a018 Pro\u00a0Max 256GB Black"},{"partNumber":"OTHERCH/A","name":"iPhone 18 Pro"}]};</script>`
	p, err := ParseProducts(html)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 1 || p[0].Name != "iPhone 18 Pro Max 256GB Black" {
		t.Fatalf("unexpected products: %+v", p)
	}
	merged := MergeProducts(DefaultConfig().Products, p)
	if len(merged) != 4 {
		t.Fatal("refresh lost products")
	}
	for _, p := range merged {
		if !p.Enabled {
			t.Fatal("refresh lost selections")
		}
	}
}

func TestCronPreservesOtherJobs(t *testing.T) {
	original := "MAILTO=user@example.com\n0 12 * * * /usr/bin/true\n"
	text, err := CronText(original, "/Users/a/Library/Application Support/apple-stock/apple-stock", "/Users/a/Library/Application Support/apple-stock", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, original) || !strings.Contains(text, "* * * * * '") {
		t.Fatal(text)
	}
	again, err := CronText(text, "/Users/a/Library/Application Support/apple-stock/apple-stock", "/Users/a/Library/Application Support/apple-stock", true)
	if err != nil || again != text {
		t.Fatal("install not idempotent")
	}
	removed, err := CronText(text, "/tmp/bin", "/tmp/state", false)
	if err != nil || removed != original {
		t.Fatal("remove changed unrelated jobs")
	}
	if _, err := CronText(original, "/tmp/100%bad", "/tmp/state", true); err == nil {
		t.Fatal("unsafe cron path accepted")
	}
	if _, err := WithoutJob(cronBegin + "\n"); err == nil {
		t.Fatal("malformed cron block accepted")
	}
}

func TestSlackAcknowledgementAndSecretRedaction(t *testing.T) {
	for _, reply := range []string{"ok", "invalid_payload"} {
		t.Run(reply, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("bad request")
				}
				fmt.Fprint(w, reply)
			}))
			defer server.Close()
			err := sendSlack(context.Background(), server.Client(), server.URL, "test")
			if (err == nil) != (reply == "ok") {
				t.Fatal("incorrect acknowledgement handling")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := sendSlack(ctx, &http.Client{}, "https://hooks.slack.com/services/SECRET/SECRET/SECRET", "test")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("webhook secret leaked")
	}
	for _, url := range []string{"http://hooks.slack.com/services/a/b/c", "https://example.com/services/a/b/c", "https://hooks.slack.com.evil.test/services/a/b/c"} {
		if ValidateWebhook(url) == nil {
			t.Fatal("invalid webhook accepted")
		}
	}
}

func TestConfigFilePermissions(t *testing.T) {
	dir := t.TempDir()
	c := oneTarget()
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("config must be private")
	}
	got, err := LoadConfig(dir)
	if err != nil || got.SlackWebhook != c.SlackWebhook {
		t.Fatal("config did not round trip")
	}
}
