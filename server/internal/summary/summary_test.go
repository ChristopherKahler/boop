package summary

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrisgreg/boop/server/internal/database"
)

func setup(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "boop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	now := "2026-10-01T00:00:00.000000000Z"
	exec(`INSERT INTO clients (id, name, slug, created_at, updated_at) VALUES ('cli_a','Acme Motors','acme',?,?), ('cli_b','Other','other',?,?)`, now, now, now, now)
	for _, p := range [][]string{{"prj_site", "Dealer site", "cli_a"}, {"prj_sync", "Inventory sync", "cli_a"}, {"prj_other", "Other app", "cli_b"}} {
		exec(`INSERT INTO projects (id, name, slug, api_key_hash, client_id, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`, p[0], p[1], p[0], "h"+p[0], p[2], now, now)
	}
	type g struct {
		id, project, title, level, first, last string
		count                                  int
		notified, acked, ackedBy, res, resBy   any
	}
	for _, a := range []g{
		// Down 10:00, pushed 10:00:30, acknowledged after 4m, recovered after 30m.
		{"alr_1", "prj_site", "Dealer site is down", "critical", "2026-10-03T15:00:00.000000000Z", "2026-10-03T15:20:00.000000000Z", 5,
			"2026-10-03T15:00:30.000000000Z", "2026-10-03T15:04:00.000000000Z", "phone", "2026-10-03T15:30:00.000000000Z", "recovery"},
		// Acknowledged after 10m, resolved by hand after 2h.
		{"alr_2", "prj_sync", "Nightly sync missed", "error", "2026-10-10T12:00:00.000000000Z", "2026-10-10T12:00:00.000000000Z", 1,
			nil, "2026-10-10T12:10:00.000000000Z", "web", "2026-10-10T14:00:00.000000000Z", "web"},
		// Still open; first seen 22:00 on Oct 31 in Chicago (03:00 UTC on Nov 1).
		{"alr_3", "prj_site", "Slow checkout", "warning", "2026-11-01T03:00:00.000000000Z", "2026-11-01T03:00:00.000000000Z", 2, nil, nil, "", nil, ""},
		// November in Chicago: not in October's report.
		{"alr_4", "prj_site", "November thing", "error", "2026-11-01T06:00:00.000000000Z", "2026-11-01T06:00:00.000000000Z", 1, nil, nil, "", nil, ""},
		// September, still open in October: carried over.
		{"alr_5", "prj_sync", "Old problem", "error", "2026-09-20T12:00:00.000000000Z", "2026-09-20T12:00:00.000000000Z", 1, nil, nil, "", nil, ""},
		// Another client's alert.
		{"alr_6", "prj_other", "Someone else's", "critical", "2026-10-05T12:00:00.000000000Z", "2026-10-05T12:00:00.000000000Z", 1, nil, nil, "", nil, ""},
	} {
		exec(`INSERT INTO alert_groups (id, project_id, group_key, title, level, event_count, first_seen, last_seen, notified_at, acknowledged_at, acknowledged_by, resolved_at, resolved_by, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.id, a.project, a.id, a.title, a.level, a.count, a.first, a.last, a.notified, a.acked, a.ackedBy, a.res, a.resBy, now, now)
	}
	for i, e := range [][]string{{"prj_site", "critical", "2026-10-03T15:00:00.000000000Z"}, {"prj_site", "info", "2026-10-04T10:00:00.000000000Z"},
		{"prj_sync", "error", "2026-10-10T12:00:00.000000000Z"}, {"prj_other", "error", "2026-10-05T12:00:00.000000000Z"}, {"prj_site", "error", "2026-09-30T12:00:00.000000000Z"}} {
		exec(`INSERT INTO events (id, project_id, level, title, occurred_at, created_at) VALUES (?,?,?,?,?,?)`, "evt_"+string(rune('a'+i)), e[0], e[1], "t", e[2], e[2])
	}
	exec(`INSERT INTO devices (id, credential_hash, created_at, updated_at) VALUES ('dev_1','c',?,?)`, now, now)
	exec(`INSERT INTO deliveries (id, event_id, device_id, status, attempted_at, created_at) VALUES ('d1','evt_a','dev_1','sent','2026-10-03T15:00:30.000000000Z','x'), ('d2','evt_a','dev_1','failed','2026-10-03T15:00:31.000000000Z','x')`)
	return db
}

func TestBuild(t *testing.T) {
	db := setup(t)
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Build(context.Background(), db, "acme", "2026-10", chicago, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if s.ClientName != "Acme Motors" || s.MonthLabel != "October 2026" || s.From != "2026-10-01T05:00:00.000000000Z" || s.To != "2026-11-01T05:00:00.000000000Z" {
		t.Errorf("header = %+v", s)
	}
	tot := s.Totals
	if tot.Alerts != 3 || tot.Resolved != 2 || tot.Recovered != 1 || tot.ResolvedByHand != 1 || tot.StillOpen != 1 || tot.Acknowledged != 2 || tot.Occurrences != 8 {
		t.Errorf("totals = %+v", tot)
	}
	if tot.Events != 3 || tot.Pushes != 1 {
		t.Errorf("events %d pushes %d (only this client, this month, delivered)", tot.Events, tot.Pushes)
	}
	if s.Acknowledge.Count != 2 || s.Acknowledge.Median != 420 || s.Acknowledge.Fastest != 240 || s.Acknowledge.Slowest != 600 {
		t.Errorf("acknowledge = %+v", s.Acknowledge)
	}
	if s.Resolve.Count != 2 || s.Resolve.Median != 4500 || s.Notify.Count != 1 || s.Notify.Median != 30 {
		t.Errorf("resolve %+v notify %+v", s.Resolve, s.Notify)
	}
	want := map[string]int{"critical": 1, "error": 1, "warning": 1}
	for _, l := range s.ByLevel {
		if want[l.Level] != l.Count {
			t.Errorf("by level %s = %d", l.Level, l.Count)
		}
	}
	if len(s.Alerts) != 3 || s.Alerts[0].ID != "alr_1" || s.Alerts[2].ID != "alr_3" || s.Alerts[0].Status != "recovered" || s.Alerts[2].Status != "open" {
		t.Errorf("alerts = %+v", s.Alerts)
	}
	if len(s.Recoveries) != 1 || s.CarriedOver != 1 || len(s.Projects) != 2 || s.Projects[0].Project != "Dealer site" || s.Projects[0].Open != 1 {
		t.Errorf("recoveries %d carried %d projects %+v", len(s.Recoveries), s.CarriedOver, s.Projects)
	}
	if s.PreviousMonth != "2026-09" || s.NextMonth != "2026-11" {
		t.Errorf("navigation %s %s", s.PreviousMonth, s.NextMonth)
	}

	md := Markdown(s, chicago)
	for _, want := range []string{
		"# Monitoring summary: Acme Motors, October 2026",
		"**3 alerts** (1 critical, 1 error, 1 warning) across 2 projects",
		"**2 resolved**: 1 recovered on their own, 1 resolved by hand; **1 still open**",
		"**Time to acknowledge**: median 7m, average 7m, fastest 4m, slowest 10m (2 alerts)",
		"| Oct 3, 10:00 | Dealer site | Dealer site is down | Critical | 5 | 4m | 30m | Recovered on its own |",
		"| Oct 31, 22:00 | Dealer site | Slow checkout | Warning | 2 | — | — | Still open |",
		"- Oct 3, 10:00: Dealer site is down (Dealer site, lasted 30m)",
		"1 alert from before this month was still open",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown is missing %q\n%s", want, md)
		}
	}
	if strings.Contains(md, "November thing") || strings.Contains(md, "Someone else") {
		t.Errorf("markdown leaks another month or client:\n%s", md)
	}

	if s2 := (Summary{ByLevel: []LevelCount{{"critical", 2}, {"error", 3}, {"warning", 1}}, Totals: Totals{Alerts: 6}}); !strings.Contains(Markdown(s2, chicago), "(2 critical, 3 errors, 1 warning)") {
		t.Errorf("level words are not pluralised:\n%s", Markdown(s2, chicago))
	}

	empty, err := Build(context.Background(), db, "acme", "2026-07", chicago, time.Now())
	if err != nil || empty.Totals.Alerts != 0 || !strings.Contains(Markdown(empty, chicago), "**No alerts** this month") {
		t.Errorf("empty month: %+v %v", empty.Totals, err)
	}
	if _, err := Build(context.Background(), db, "acme", "October", chicago, time.Now()); err == nil {
		t.Error("bad month accepted")
	}
	if _, err := Build(context.Background(), db, "nobody", "2026-10", chicago, time.Now()); err == nil {
		t.Error("unknown client accepted")
	}
}

func TestDuration(t *testing.T) {
	for sec, want := range map[float64]string{12: "12s", 240: "4m", 4500: "1h 15m", 93600: "1d 2h"} {
		if got := Duration(sec); got != want {
			t.Errorf("Duration(%v) = %q, want %q", sec, got, want)
		}
	}
}
