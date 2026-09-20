package stock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type State struct {
	LastAttempt      time.Time         `json:"last_attempt"`
	LastSuccess      time.Time         `json:"last_success"`
	LastNotification time.Time         `json:"last_notification"`
	LastError        string            `json:"last_error,omitempty"`
	Notice           string            `json:"notice,omitempty"`
	Failures         int               `json:"failures"`
	NextAttempt      time.Time         `json:"next_attempt"`
	Results          map[string]Result `json:"results"`
	Notified         map[string]string `json:"notified"` // Last acknowledged date; presence suppresses alerts until observed unavailability.
}

func LoadState(dir string) (State, error) {
	s := State{Results: map[string]Result{}, Notified: map[string]string{}}
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal(b, &s); err != nil {
		return s, errors.New("state.json is corrupt; restore it or move it aside before checking")
	}
	if s.Results == nil {
		s.Results = map[string]Result{}
	}
	if s.Notified == nil {
		s.Notified = map[string]string{}
	}
	return s, nil
}

func Key(r Result) string { return r.Part + "@" + r.StoreID }
func eligible(r Result, policy string) bool {
	return r.Available && (policy != "today" || strings.EqualFold(r.Quote, "Available Today"))
}

// Pending commits observed unavailability, but successful notifications are committed separately.
func Pending(s *State, rows []Result, policy string) []Result {
	var pending []Result
	for _, r := range rows {
		k := Key(r)
		s.Results[k] = r
		if !r.Available {
			delete(s.Notified, k)
			continue
		}
		if _, notified := s.Notified[k]; eligible(r, policy) && !notified {
			pending = append(pending, r)
		}
	}
	return pending
}

func SortedResults(s State, c Config) []Result {
	parts, stores := map[string]bool{}, map[string]bool{}
	for _, p := range c.Products {
		parts[p.Part] = p.Enabled
	}
	for _, v := range c.Stores {
		stores[v.ID] = v.Enabled
	}
	rows := []Result{}
	for _, r := range s.Results {
		if parts[r.Part] && stores[r.StoreID] {
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Available != rows[j].Available {
			return rows[i].Available
		}
		if rows[i].Product != rows[j].Product {
			return rows[i].Product < rows[j].Product
		}
		return rows[i].Store < rows[j].Store
	})
	return rows
}

var ErrBusy = errors.New("another availability check is already running")

func acquireLock(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "check.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, ErrBusy
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

type Monitor struct {
	Client *Client
	Notify NotifyFunc
	Now    func() time.Time
}

func Run(ctx context.Context, dir string, notify bool) (State, error) {
	c, err := LoadConfig(dir)
	if err != nil {
		return State{}, err
	}
	m := Monitor{Client: NewClient(c.ProductPage), Notify: SendSlack, Now: time.Now}
	return m.Run(ctx, dir, c, notify)
}

func (m Monitor) Run(ctx context.Context, dir string, c Config, notify bool) (State, error) {
	unlock, err := acquireLock(dir)
	if err != nil {
		return State{}, err
	}
	defer unlock()
	s, err := LoadState(dir)
	if err != nil {
		return s, err
	}
	now := m.Now()
	if now.Before(s.NextAttempt) {
		s.Notice = "Backing off until " + s.NextAttempt.Local().Format("15:04:05")
		return s, nil
	}
	s.LastAttempt = now
	s.Notice = ""
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var rows []Result
	var failures []string
	var retry time.Duration
	if err = m.Client.SetLocation(ctx, c.ZIP); err != nil {
		failures = append(failures, err.Error())
		var api *APIError
		if errors.As(err, &api) {
			retry = api.RetryAfter
		}
	} else {
		for _, p := range c.Products {
			if !p.Enabled {
				continue
			}
			r, fetchErr := m.Client.Pickup(ctx, p, c.Stores, m.Now())
			if fetchErr != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", p.Name, fetchErr))
				var api *APIError
				if errors.As(fetchErr, &api) {
					if api.RetryAfter > retry {
						retry = api.RetryAfter
					}
					break
				}
				if ctx.Err() != nil {
					break
				}
				continue
			}
			rows = append(rows, r...)
		}
	}
	// Hide disabled targets, but retain acknowledgements until observed unavailability.
	active := map[string]bool{}
	for _, p := range c.Products {
		if p.Enabled {
			for _, store := range c.Stores {
				if store.Enabled {
					active[p.Part+"@"+store.ID] = true
				}
			}
		}
	}
	for k := range s.Results {
		if !active[k] {
			delete(s.Results, k)
		}
	}
	pending := Pending(&s, rows, c.PickupPolicy)
	if len(failures) > 0 {
		s.Failures++
		delay := time.Minute * time.Duration(1<<min(s.Failures-1, 4))
		if retry > delay {
			delay = retry
		}
		s.NextAttempt = now.Add(delay)
		s.LastError = strings.Join(failures, "; ")
	} else {
		s.Failures = 0
		s.NextAttempt = time.Time{}
		s.LastError = ""
		s.LastSuccess = m.Now()
	}
	// Persist observations before sending; a failed delivery must remain eligible for retry.
	if err = WriteJSON(filepath.Join(dir, "state.json"), s); err != nil {
		return s, err
	}
	if notify && len(pending) > 0 {
		if c.SlackWebhook == "" {
			s.Notice = fmt.Sprintf("%d pickup alerts pending — configure the Slack webhook in Settings", len(pending))
		} else {
			// Keep messages small enough for Slack and commit each acknowledged batch.
			for start := 0; start < len(pending); start += 12 {
				batch := pending[start:min(start+12, len(pending))]
				if err = m.Notify(ctx, c.SlackWebhook, AlertText(batch)); err != nil {
					s.Notice = err.Error()
					break
				}
				for _, r := range batch {
					s.Notified[Key(r)] = r.Date
				}
				s.LastNotification = m.Now()
				if err = WriteJSON(filepath.Join(dir, "state.json"), s); err != nil {
					return s, err
				}
			}
		}
	} else if !notify && len(pending) > 0 {
		s.Notice = fmt.Sprintf("%d available options; this check did not send alerts", len(pending))
	}
	if c.SlackWebhook == "" && s.Notice == "" {
		s.Notice = "Monitoring; Slack webhook is not configured yet"
	}
	if saveErr := WriteJSON(filepath.Join(dir, "state.json"), s); saveErr != nil {
		return s, saveErr
	}
	if len(failures) > 0 {
		return s, errors.New(s.LastError)
	}
	return s, err
}
