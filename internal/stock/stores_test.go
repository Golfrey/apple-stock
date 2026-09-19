package stock

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestNearbyStoresFollowsZIPAndIncludesUnavailableStores(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/shop/address/location/update" {
			zip := r.URL.Query().Get("postalCode")
			http.SetCookie(w, &http.Cookie{Name: "as_loc", Value: zip, Path: "/"})
			respond(w, fmt.Sprintf(`{"content":{"address":{"postalCode":%q}}}`, zip))
			return
		}
		cookie, err := r.Cookie("as_loc")
		if err != nil {
			t.Error("location cookie missing")
			w.WriteHeader(400)
			return
		}
		id, name := "R095", "Apple Fifth Avenue"
		if cookie.Value == "80202" {
			id, name = "R047", "Apple Cherry Creek"
		}
		if r.URL.Query().Get("product") != "MJW44LL/A" {
			t.Error("wrong product")
		}
		switch r.URL.Path {
		case "/shop/sba/d/product-recommendations":
			respond(w, fmt.Sprintf(`{"productMeta":{"retailStoreIds":%q,"products":[]}}`, id))
		case "/shop/sba/pickup-detail":
			if r.URL.Query().Get("stores.0") != id {
				t.Error("used stores from another ZIP")
			}
			respond(w, fmt.Sprintf(`{"content":[{"storeId":%q,"pickupSearchQuote":"Currently unavailable","address":{"address":%q}}]}`, id, name))
		default:
			t.Error("unexpected path", r.URL.Path)
		}
	})
	for _, tc := range []struct{ zip, id, name string }{{"10001", "R095", "Fifth Avenue"}, {"80202", "R047", "Cherry Creek"}} {
		stores, err := c.NearbyStores(context.Background(), tc.zip, "MJW44LL/A")
		if err != nil {
			t.Fatal(err)
		}
		if len(stores) != 1 || stores[0].ID != tc.id || stores[0].Name != tc.name || !stores[0].Enabled {
			t.Fatalf("wrong stores for %s: %+v", tc.zip, stores)
		}
	}
}

func TestNearbyStoresRejectsIncompleteResponses(t *testing.T) {
	for name, tc := range map[string]struct{ meta, detail string }{
		"missing list":   {`{"productMeta":{}}`, `{"content":[]}`},
		"empty list":     {`{"productMeta":{"retailStoreIds":""}}`, `{"content":[]}`},
		"invalid ID":     {`{"productMeta":{"retailStoreIds":"bad"}}`, `{"content":[]}`},
		"missing detail": {`{"productMeta":{"retailStoreIds":"R047"}}`, `{"content":[]}`},
		"wrong detail":   {`{"productMeta":{"retailStoreIds":"R047"}}`, `{"content":[{"storeId":"R095","address":{"address":"Apple Fifth Avenue"}}]}`},
		"missing name":   {`{"productMeta":{"retailStoreIds":"R047"}}`, `{"content":[{"storeId":"R047"}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/shop/address/location/update":
					respond(w, `{"content":{"address":{"postalCode":"80202"}}}`)
				case "/shop/sba/d/product-recommendations":
					respond(w, tc.meta)
				default:
					respond(w, tc.detail)
				}
			})
			if _, err := c.NearbyStores(context.Background(), "80202", "MJW44LL/A"); err == nil {
				t.Fatal("incomplete store discovery accepted")
			}
		})
	}
}
