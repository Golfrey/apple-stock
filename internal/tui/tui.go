package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"apple-stock/internal/stock"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var accent = lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true)
var muted = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
var selected = lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62"))
var warning = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))

type model struct {
	dir                            string
	config                         stock.Config
	state                          stock.State
	tab, cursor, width, height     int
	filter                         string
	edit, entry                    string
	dirty, busy, cron, confirmQuit bool
	message                        string
}

type tickMsg time.Time
type checkMsg struct {
	state stock.State
	err   error
}
type productsMsg struct {
	products []stock.Product
	err      error
}
type cronMsg struct {
	enabled bool
	err     error
}
type notificationMsg struct{ err error }

func Run(dir string) error {
	c, err := stock.LoadConfig(dir)
	if err != nil {
		return err
	}
	s, err := stock.LoadState(dir)
	if err != nil {
		return err
	}
	m := model{dir: dir, config: c, state: s, width: 100, height: 32, message: "Select products and stores, then press s to save."}
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func tick() tea.Cmd           { return tea.Tick(3*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }
func cronStatus() tea.Msg     { enabled, err := stock.ScheduleEnabled(); return cronMsg{enabled, err} }
func (m model) Init() tea.Cmd { return tea.Batch(tick(), cronStatus) }

func (m model) productIndexes() []int {
	var result []int
	for i, p := range m.config.Products {
		if strings.Contains(strings.ToLower(p.Name+" "+p.Part), strings.ToLower(m.filter)) {
			result = append(result, i)
		}
	}
	return result
}
func (m model) count() int {
	switch m.tab {
	case 0:
		return len(stock.SortedResults(m.state, m.config))
	case 1:
		return len(m.productIndexes())
	case 2:
		return len(m.config.Stores)
	default:
		return 6
	}
}
func (m *model) save() bool {
	if err := stock.SaveConfig(m.dir, m.config); err != nil {
		m.message = err.Error()
		return false
	}
	m.dirty = false
	m.message = "Saved. The next scheduled check will use these settings."
	return true
}
func (m *model) applyEdit() {
	v := strings.TrimSpace(m.entry)
	switch m.edit {
	case "filter":
		m.filter = v
		m.cursor = 0
		m.edit = ""
		return
	case "zip":
		if len(v) != 5 || strings.Trim(v, "0123456789") != "" {
			m.message = "ZIP must contain five digits"
			return
		}
		m.config.ZIP = v
	case "page":
		if err := stock.ValidatePage(v); err != nil {
			m.message = err.Error()
			return
		}
		m.config.ProductPage = v
	case "webhook":
		if v != "" {
			if err := stock.ValidateWebhook(v); err != nil {
				m.message = err.Error()
				return
			}
		}
		m.config.SlackWebhook = v
	case "product", "store":
		fields := strings.SplitN(v, "|", 2)
		if len(fields) != 2 || strings.TrimSpace(fields[1]) == "" {
			m.message = "Use ID | Display name"
			return
		}
		id, name := strings.TrimSpace(fields[0]), strings.TrimSpace(fields[1])
		candidate := m.config
		if m.edit == "product" {
			candidate.Products = append(append([]stock.Product{}, m.config.Products...), stock.Product{Part: id, Name: name, Enabled: true})
		} else {
			candidate.Stores = append(append([]stock.Store{}, m.config.Stores...), stock.Store{ID: id, Name: name, Enabled: true})
		}
		if err := candidate.Validate(); err != nil {
			m.message = err.Error()
			return
		}
		m.config = candidate
	}
	m.dirty = true
	m.edit = ""
	m.entry = ""
	m.message = "Changed. Press s to save."
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		if s, err := stock.LoadState(m.dir); err == nil {
			m.state = s
		}
		return m, tick()
	case checkMsg:
		m.busy = false
		if !msg.state.LastAttempt.IsZero() {
			m.state = msg.state
		}
		if msg.err != nil {
			m.message = msg.err.Error()
		} else {
			m.message = "Check complete. " + msg.state.Notice
		}
	case productsMsg:
		m.busy = false
		if msg.err != nil {
			m.message = msg.err.Error()
		} else {
			m.config.Products = stock.MergeProducts(m.config.Products, msg.products)
			m.dirty = true
			m.message = fmt.Sprintf("Loaded %d products from Apple. Press s to save.", len(msg.products))
		}
	case cronMsg:
		m.busy = false
		if msg.err != nil {
			m.message = msg.err.Error()
		} else {
			m.cron = msg.enabled
		}
	case notificationMsg:
		m.busy = false
		if msg.err != nil {
			m.message = msg.err.Error()
		} else {
			m.message = "Slack acknowledged the test message."
		}
	case tea.KeyMsg:
		key := msg.String()
		if key == "ctrl+c" {
			return m, tea.Quit
		}
		if m.edit != "" {
			switch key {
			case "esc":
				m.edit = ""
				m.entry = ""
			case "enter":
				m.applyEdit()
			case "backspace", "ctrl+h":
				r := []rune(m.entry)
				if len(r) > 0 {
					m.entry = string(r[:len(r)-1])
				}
			case "ctrl+u":
				m.entry = ""
			default:
				if msg.Type == tea.KeyRunes && len(m.entry) < 4096 {
					m.entry += string(msg.Runes)
				}
			}
			return m, nil
		}
		if key != "q" {
			m.confirmQuit = false
		}
		switch key {
		case "q":
			if m.dirty && !m.confirmQuit {
				m.confirmQuit = true
				m.message = "Unsaved changes. Press s to save, or q again to discard and quit."
				return m, nil
			}
			return m, tea.Quit
		case "tab", "right":
			m.tab = (m.tab + 1) % 4
			m.cursor = 0
		case "shift+tab", "left":
			m.tab = (m.tab + 3) % 4
			m.cursor = 0
		case "1", "2", "3", "4":
			m.tab = int(key[0] - '1')
			m.cursor = 0
		case "up", "k":
			m.cursor = max(0, m.cursor-1)
		case "down", "j":
			m.cursor = min(max(0, m.count()-1), m.cursor+1)
		case "s":
			m.save()
		case " ":
			if m.tab == 1 {
				idx := m.productIndexes()
				if len(idx) > m.cursor {
					p := &m.config.Products[idx[m.cursor]]
					p.Enabled = !p.Enabled
					m.dirty = true
				}
			}
			if m.tab == 2 && len(m.config.Stores) > m.cursor {
				p := &m.config.Stores[m.cursor]
				p.Enabled = !p.Enabled
				m.dirty = true
			}
		case "/":
			if m.tab == 1 {
				m.edit = "filter"
				m.entry = m.filter
			}
		case "a":
			if m.tab == 1 {
				m.edit = "product"
				m.entry = ""
			}
			if m.tab == 2 {
				m.edit = "store"
				m.entry = ""
			}
		case "r":
			if m.tab == 1 && !m.busy {
				m.busy = true
				m.message = "Loading product catalogue…"
				page := m.config.ProductPage
				return m, func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					p, err := stock.NewClient(page).Discover(ctx, page)
					return productsMsg{p, err}
				}
			}
		case "c":
			if !m.busy {
				if m.dirty {
					m.message = "Save your changes with s before checking."
					return m, nil
				}
				m.busy = true
				m.message = "Checking Apple…"
				dir := m.dir
				return m, func() tea.Msg { s, err := stock.Run(context.Background(), dir, true); return checkMsg{s, err} }
			}
		case "enter":
			if m.tab != 3 {
				break
			}
			switch m.cursor {
			case 0:
				m.edit = "zip"
				m.entry = m.config.ZIP
			case 1:
				m.edit = "page"
				m.entry = m.config.ProductPage
			case 2:
				m.edit = "webhook"
				m.entry = m.config.SlackWebhook
			case 3:
				if m.config.PickupPolicy == "any" {
					m.config.PickupPolicy = "today"
				} else {
					m.config.PickupPolicy = "any"
				}
				m.dirty = true
			case 4:
				if !m.busy {
					if m.dirty {
						m.message = "Save your changes before changing the schedule."
						return m, nil
					}
					dir, enabled := m.dir, !m.cron
					m.busy = true
					return m, func() tea.Msg { err := stock.SetSchedule(dir, enabled); return cronMsg{enabled, err} }
				}
			case 5:
				if !m.busy {
					if m.dirty {
						m.message = "Save your changes before sending a test."
						return m, nil
					}
					if m.config.SlackWebhook == "" {
						m.message = "Enter and save the Slack webhook first."
						return m, nil
					}
					hook := m.config.SlackWebhook
					m.busy = true
					return m, func() tea.Msg {
						err := stock.SendSlack(context.Background(), hook, "Apple Stock test: Slack alerts are connected. The monitor checks your selected iPhones and stores every minute while your Mac is awake.")
						return notificationMsg{err}
					}
				}
			}
		}
	}
	m.cursor = min(m.cursor, max(0, m.count()-1))
	return m, nil
}

func checkmark(on bool) string {
	if on {
		return "[x]"
	}
	return "[ ]"
}
func stamp(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Local().Format("Jan 02 15:04:05")
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString(accent.Render("APPLE STOCK") + "  " + muted.Render("Manhattan defaults · US pickup monitor") + "\n\n")
	for i, title := range []string{"1 Overview", "2 Products", "3 Stores", "4 Settings"} {
		if i == m.tab {
			b.WriteString(selected.Render(" " + title + " "))
		} else {
			b.WriteString(muted.Render(" " + title + " "))
		}
		b.WriteString("  ")
	}
	b.WriteString("\n\n")
	pc, sc := 0, 0
	for _, p := range m.config.Products {
		if p.Enabled {
			pc++
		}
	}
	for _, s := range m.config.Stores {
		if s.Enabled {
			sc++
		}
	}
	schedule := "paused"
	if m.cron {
		schedule = "every minute"
	}
	dirty := ""
	if m.dirty {
		dirty = " · UNSAVED"
	}
	fmt.Fprintf(&b, "ZIP %s · %d products · %d stores · %s %s%s\n", m.config.ZIP, pc, sc, stock.SchedulerName(), schedule, dirty)
	fmt.Fprintf(&b, "Last complete check: %s   Last Slack alert: %s\n\n", stamp(m.state.LastSuccess), stamp(m.state.LastNotification))
	var rows []string
	switch m.tab {
	case 0:
		results := stock.SortedResults(m.state, m.config)
		for _, r := range results {
			status := strings.TrimPrefix(r.Quote, "Available ")
			if !r.Available {
				status = "Unavailable"
			}
			if r.Date != "" {
				if d, err := time.Parse("20060102", r.Date); err == nil {
					status += " (" + d.Format("Jan 2") + ")"
				}
			}
			line := fmt.Sprintf("%-28s  %-16s  %s", strings.TrimPrefix(r.Product, "iPhone "), r.Store, status)
			if r.CheckedAt.Before(m.state.LastAttempt) {
				line += " [older result]"
			}
			rows = append(rows, line)
		}
		if len(rows) == 0 {
			rows = []string{"No results yet. Press c to check now."}
		}
	case 1:
		for _, i := range m.productIndexes() {
			p := m.config.Products[i]
			rows = append(rows, fmt.Sprintf("%s  %-42s %s", checkmark(p.Enabled), p.Name, p.Part))
		}
		if len(rows) == 0 {
			rows = []string{"No matching products. Press / to change the filter."}
		}
	case 2:
		for _, s := range m.config.Stores {
			rows = append(rows, fmt.Sprintf("%s  %-24s %s", checkmark(s.Enabled), s.Name, s.ID))
		}
	case 3:
		hook := "not configured"
		if m.config.SlackWebhook != "" {
			hook = "configured (hidden)"
		}
		policy := "Any available pickup date"
		if m.config.PickupPolicy == "today" {
			policy = "Today only"
		}
		rows = []string{"ZIP code             " + m.config.ZIP, "Product page         " + m.config.ProductPage, "Slack webhook        " + hook, "Alert when           " + policy, "Schedule             " + schedule + "  (Enter to toggle)", "Send a Slack test message"}
	}
	visible := max(3, m.height-17)
	start := max(0, m.cursor-visible+1)
	end := min(len(rows), start+visible)
	for i := start; i < end; i++ {
		line := lipgloss.NewStyle().MaxWidth(max(20, m.width-4)).Render(rows[i])
		if i == m.cursor {
			b.WriteString(selected.Render("› " + line))
		} else {
			b.WriteString("  " + line)
		}
		b.WriteByte('\n')
	}
	if len(rows) > visible {
		fmt.Fprintf(&b, "  %s\n", muted.Render(fmt.Sprintf("%d–%d of %d", start+1, end, len(rows))))
	}
	b.WriteByte('\n')
	if m.state.LastError != "" {
		b.WriteString(warning.Render("Last check error: "+m.state.LastError) + "\n")
	}
	if m.state.Notice != "" {
		b.WriteString(muted.Render(m.state.Notice) + "\n")
	}
	if m.edit != "" {
		value := m.entry
		if m.edit == "webhook" {
			value = strings.Repeat("•", min(len([]rune(value)), 60))
		}
		label := m.edit
		if label == "product" || label == "store" {
			label += " (ID | Display name)"
		}
		b.WriteString(accent.Render("Edit "+label+": ") + value + "▌\n")
		b.WriteString(muted.Render("Enter apply · Esc cancel · Ctrl+U clear") + "\n")
	} else {
		b.WriteString(lipgloss.NewStyle().MaxWidth(max(20, m.width-2)).Render(m.message) + "\n")
		help := "Tab sections · ↑↓ move · s save · c check & alert · q quit"
		if m.tab == 1 {
			help += "\nSpace toggle · / filter · r refresh catalogue · a add product"
		}
		if m.tab == 2 {
			help += "\nSpace toggle · a add store (Apple store ID | name)"
		}
		if m.tab == 3 {
			help += "\nEnter edit / activate · Slack webhook is stored locally with mode 0600"
		}
		b.WriteString(muted.Render(help) + "\n")
	}
	return b.String()
}
