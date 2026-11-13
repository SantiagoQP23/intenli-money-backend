package httpapi

import (
	"net/http"
	"net/url"
	"testing"
)

// Transactions can carry a time of day; lists sort by date, then time
// (no time counts as 00:00), and paging follows the same order.
func TestTransactionTime(t *testing.T) {
	ta := newTestAPI(t, nil)
	s, ws := ta.setupWorkspace("time@example.com")
	cash := ta.createAccount(s.access, ws, map[string]any{"name": "Cash", "type": "CASH", "currency": "USD", "opening_balance": "100"})
	add := func(day string, tod any, desc string) resp {
		body := map[string]any{"type": "EXPENSE", "occurred_on": day, "account_id": cash, "amount": "1", "description": desc}
		if tod != nil {
			body["occurred_time"] = tod
		}
		return ta.do("POST", ws+"/transactions", s.access, body)
	}

	morning := ta.expect(add("2026-11-10", "08:15", "morning"), http.StatusCreated)
	if morning.str("occurred_time") != "08:15" {
		t.Fatalf("time not returned: %s", morning.Raw)
	}
	ta.expect(add("2026-11-10", "21:40", "night"), http.StatusCreated)
	untimed := ta.expect(add("2026-11-10", nil, "no time"), http.StatusCreated)
	if untimed.Body["occurred_time"] != nil {
		t.Fatalf("expected null time: %s", untimed.Raw)
	}
	ta.expect(add("2026-11-11", "00:05", "next day"), http.StatusCreated)
	for _, bad := range []string{"25:00", "08:15:00", "noon"} {
		if r := add("2026-11-10", bad, "bad"); r.errCode() != "invalid_request" {
			t.Fatalf("%q accepted: %s", bad, r.Raw)
		}
	}

	// Page through one at a time: the order must hold across cursors.
	var got []string
	path := ws + "/transactions?limit=1"
	for {
		r := ta.expect(ta.do("GET", path, s.access, nil), http.StatusOK)
		for _, tx := range r.list() {
			got = append(got, tx["description"].(string))
		}
		next, _ := r.Body["next_cursor"].(string)
		if next == "" {
			break
		}
		path = ws + "/transactions?limit=1&cursor=" + url.QueryEscape(next)
	}
	want := []string{"next day", "night", "morning", "no time"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}

	// Editing can change or clear the time.
	id := morning.str("id")
	edit := func(tod any) resp {
		return ta.expect(ta.do("PUT", ws+"/transactions/"+id, s.access, map[string]any{
			"type": "EXPENSE", "occurred_on": "2026-11-10", "occurred_time": tod, "account_id": cash, "amount": "1",
		}), http.StatusOK)
	}
	if r := edit("12:00"); r.str("occurred_time") != "12:00" {
		t.Fatalf("edit time: %s", r.Raw)
	}
	if r := edit(nil); r.Body["occurred_time"] != nil {
		t.Fatalf("clear time: %s", r.Raw)
	}
}
