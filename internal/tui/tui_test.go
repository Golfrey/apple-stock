package tui

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"apple-stock/internal/stock"
	tea "github.com/charmbracelet/bubbletea"
)

func TestZIPEditReplacesStoresAtomically(t *testing.T) {
	c := stock.DefaultConfig()
	m := model{dir: t.TempDir(), config: c, edit: "zip", entry: "80202"}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if cmd == nil || !m.loadingStores || !m.busy {
		t.Fatal("ZIP edit did not start lookup")
	}
	if !reflect.DeepEqual(m.config, c) {
		t.Fatal("ZIP or stores changed before lookup completed")
	}
	if m.save() {
		t.Fatal("allowed saving while the new store list was pending")
	}
	if _, err := os.Stat(filepath.Join(m.dir, "config.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote incomplete config")
	}
	updated, _ = m.Update(cronMsg{enabled: true})
	m = updated.(model)
	if !m.busy {
		t.Fatal("background schedule status cleared lookup lock")
	}
	newStores := []stock.Store{{ID: "R047", Name: "Cherry Creek", Enabled: true}, {ID: "R228", Name: "Park Meadows", Enabled: true}}
	updated, _ = m.Update(storesMsg{zip: "80202", stores: newStores})
	m = updated.(model)
	for i := range newStores {
		newStores[i].Enabled = false
	}
	if m.config.ZIP != "80202" || !reflect.DeepEqual(m.config.Stores, newStores) || !m.dirty || m.tab != 2 || m.busy || m.loadingStores {
		t.Fatalf("incorrect replacement: %+v", m)
	}
	if m.save() {
		t.Fatal("saved new stores without an explicit selection")
	}
	m.config.Stores[0].Enabled = true
	newStores[0].Enabled = true
	if !m.save() {
		t.Fatal(m.message)
	}
	saved, err := stock.LoadConfig(m.dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ZIP != "80202" || !reflect.DeepEqual(saved.Stores, newStores) {
		t.Fatal("saved ZIP/store pair inconsistent")
	}
	oldState := stock.State{Results: map[string]stock.Result{"old": {Part: c.Products[0].Part, StoreID: "R095", Store: "Fifth Avenue"}}}
	if len(stock.SortedResults(oldState, saved)) != 0 {
		t.Fatal("old Manhattan results still visible after ZIP change")
	}
}

func TestStoreLookupFailureKeepsPreviousConfiguration(t *testing.T) {
	for _, err := range []error{errors.New("network unavailable"), nil} {
		c := stock.DefaultConfig()
		m := model{config: c, busy: true, loadingStores: true}
		updated, _ := m.Update(storesMsg{zip: "80202", err: err})
		m = updated.(model)
		if !reflect.DeepEqual(m.config, c) || m.dirty || m.busy || m.loadingStores {
			t.Fatal("failed/empty lookup changed configuration")
		}
	}
}

func TestStoreRefreshPreservesExplicitSelections(t *testing.T) {
	c := stock.DefaultConfig()
	c.Stores = c.Stores[:2]
	c.Stores[1].Enabled = false
	m := model{config: c, busy: true, loadingStores: true}
	fresh := []stock.Store{{ID: "R095", Name: "Fifth Avenue", Enabled: true}, {ID: "R415", Name: "Grand Central", Enabled: true}, {ID: "R715", Name: "Downtown Brooklyn", Enabled: true}}
	updated, _ := m.Update(storesMsg{zip: "10022", stores: fresh})
	m = updated.(model)
	if !m.config.Stores[0].Enabled || m.config.Stores[1].Enabled || m.config.Stores[2].Enabled {
		t.Fatal("refresh broadened explicit store selections")
	}
}

func TestInvalidZIPDoesNotStartStoreLookup(t *testing.T) {
	m := model{config: stock.DefaultConfig(), edit: "zip", entry: "abcde"}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if cmd != nil || m.busy || m.config.ZIP != "10001" {
		t.Fatal("invalid ZIP changed configuration")
	}
}
