package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/chrisgreg/boop/server/internal/database"
	"github.com/chrisgreg/boop/server/internal/events"
	"github.com/chrisgreg/boop/server/internal/events/redact"
	"github.com/chrisgreg/boop/server/internal/projects"
)

type fixture struct {
	t       *testing.T
	session *sdk.ClientSession
	uini    projects.Project
	infra   projects.Project
	ids     []string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "boop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	ps, es := projects.New(db), events.New(db)
	name := func(s string) *string { return &s }
	// Every project belongs to a client; migration 0004 creates "internal".
	internal := name("cli_internal")
	uini, _, err := ps.Create(ctx, projects.Input{Name: name("Uini"), ClientID: internal})
	if err != nil {
		t.Fatal(err)
	}
	infra, _, _ := ps.Create(ctx, projects.Input{Name: name("Infra"), ClientID: internal})
	r := redact.New()
	f := &fixture{t: t, uini: uini, infra: infra}
	post := func(p projects.Project, in events.Input) {
		e, err := es.Create(ctx, p.ID, in, r)
		if err != nil {
			t.Fatal(err)
		}
		f.ids = append(f.ids, e.ID)
	}
	for i := 0; i < 3; i++ {
		post(uini, events.Input{Title: "KeyError", Body: "key :can_palette? not found", Level: "error", Source: "error_tracker", Fingerprint: "keyerror",
			Data: json.RawMessage(`{"exception":{"type":"KeyError"},"tags":{"environment":"production"},"context":{"password":"hunter2","user_id":"u1"}}`)})
	}
	post(uini, events.Input{Title: "Deploy complete", Level: "success", Source: "github_actions", Actions: []events.Action{{Label: "Open run", URL: "https://github.com/x/y/actions/runs/1"}}})
	post(infra, events.Input{Title: "Disk 91% full", Level: "critical", Source: "cron", Body: "on db-1"})

	srv := NewServer(Stores{Projects: ps, Events: es}, "test")
	ct, st := sdk.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, st) }()
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	f.session = sess
	return f
}

// call invokes a tool and decodes its structured content into out.
func (f *fixture) call(name string, args map[string]any, out any) *sdk.CallToolResult {
	f.t.Helper()
	res, err := f.session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		f.t.Fatalf("%s: %v", name, err)
	}
	if res.IsError || out == nil {
		return res
	}
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, out); err != nil {
		f.t.Fatalf("%s: decode %v: %s", name, err, b)
	}
	return res
}

func TestToolsAreReadOnlyAndListed(t *testing.T) {
	f := setup(t)
	res, err := f.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
		if tl.Annotations == nil || !tl.Annotations.ReadOnlyHint {
			t.Errorf("%s must be annotated read-only", tl.Name)
		}
		if tl.Description == "" || tl.InputSchema == nil {
			t.Errorf("%s needs a description and input schema", tl.Name)
		}
	}
	want := "get_event,get_event_group,list_events,list_projects,search_events"
	if got := strings.Join(sorted(names), ","); got != want {
		t.Errorf("tools = %s, want %s", got, want)
	}
}

func TestListProjects(t *testing.T) {
	f := setup(t)
	var out ListProjectsOut
	f.call("list_projects", nil, &out)
	// min_level reports the lowest level that pushes: new projects inherit
	// the default client's warning-and-above routing.
	if len(out.Projects) != 2 || out.Projects[0].Slug == "" || out.Projects[0].MinLevel != "warning" {
		t.Errorf("projects = %+v", out.Projects)
	}
}

func TestListEventsFiltersAndGrouping(t *testing.T) {
	f := setup(t)
	var out EventsOut
	f.call("list_events", nil, &out)
	if len(out.Events) != 5 || out.Events[0].Title != "Disk 91% full" || out.Events[0].Project != "Infra" {
		t.Fatalf("all: %+v", out.Events)
	}
	if out.Events[1].Actions == nil || out.Events[1].Actions[0].Label != "Open run" {
		t.Errorf("actions should be in summaries: %+v", out.Events[1])
	}
	// Summaries carry the data keys but never the payload itself.
	ke := out.Events[2]
	if strings.Join(sorted(ke.DataKeys), ",") != "context,exception,tags" {
		t.Errorf("data keys: %v", ke.DataKeys)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "hunter2") || strings.Contains(string(raw), "can_palette") == false {
		t.Errorf("summary leaked payload or lost body: %s", raw)
	}

	f.call("list_events", map[string]any{"project": "uini", "level": "error", "grouped": true}, &out)
	if len(out.Events) != 1 || out.Events[0].Group == nil || out.Events[0].Group.Count != 3 {
		t.Errorf("grouped: %+v", out.Events)
	}
	f.call("list_events", map[string]any{"project": f.infra.ID}, &out)
	if len(out.Events) != 1 || out.Events[0].Level != "critical" {
		t.Errorf("by id: %+v", out.Events)
	}
	f.call("list_events", map[string]any{"limit": 2}, &out)
	if len(out.Events) != 2 || out.NextCursor == "" {
		t.Fatalf("paged: %+v", out)
	}
	f.call("list_events", map[string]any{"limit": 2, "before": out.NextCursor}, &out)
	if len(out.Events) != 2 || out.Events[0].Title != "KeyError" {
		t.Errorf("page 2: %+v", out.Events)
	}
	if res := f.call("list_events", map[string]any{"level": "fatal"}, nil); !res.IsError {
		t.Error("bad level should be a tool error")
	}
	if res := f.call("list_events", map[string]any{"since": "last night"}, nil); !res.IsError {
		t.Error("bad since should be a tool error")
	}
}

func TestSearchEvents(t *testing.T) {
	f := setup(t)
	var out EventsOut
	f.call("search_events", map[string]any{"query": "PALETTE"}, &out)
	if len(out.Events) != 3 {
		t.Errorf("body search: %+v", out.Events)
	}
	f.call("search_events", map[string]any{"query": "db-1"}, &out)
	if len(out.Events) != 1 || out.Events[0].Title != "Disk 91% full" {
		t.Errorf("search: %+v", out.Events)
	}
	f.call("search_events", map[string]any{"query": "production", "project": "uini"}, &out) // inside data payload
	if len(out.Events) != 3 {
		t.Errorf("payload search: %+v", out.Events)
	}
	f.call("search_events", map[string]any{"query": "nothing-like-this"}, &out)
	if len(out.Events) != 0 {
		t.Errorf("no match: %+v", out.Events)
	}
	if res := f.call("search_events", map[string]any{"query": "  "}, nil); !res.IsError {
		t.Error("empty query should be a tool error")
	}
}

func TestGetEvent(t *testing.T) {
	f := setup(t)
	var out EventOut
	f.call("get_event", map[string]any{"id": f.ids[0]}, &out)
	if out.ID != f.ids[0] || out.ProjectSlug != "uini" || out.Fingerprint != "keyerror" {
		t.Errorf("event: %+v", out)
	}
	data := out.Data
	if data["exception"].(map[string]any)["type"] != "KeyError" || data["context"].(map[string]any)["password"] != "[REDACTED]" {
		t.Errorf("data should be the stored (redacted) payload: %s", out.Data)
	}
	if res := f.call("get_event", map[string]any{"id": "evt_nope"}, nil); !res.IsError {
		t.Error("missing event should be a tool error, not a protocol error")
	}
}

func TestGetEventGroup(t *testing.T) {
	f := setup(t)
	var out EventGroupOut
	f.call("get_event_group", map[string]any{"project": "uini", "fingerprint": "keyerror"}, &out)
	if out.Count != 3 || len(out.Occurrences) != 3 || out.Latest == nil || out.Latest.ID != f.ids[2] || out.FirstSeen == "" || out.FirstSeen > out.LastSeen {
		t.Errorf("group: %+v", out)
	}
	if len(out.Latest.Data) == 0 {
		t.Error("latest should carry the full payload")
	}
	f.call("get_event_group", map[string]any{"project": f.uini.ID, "fingerprint": "keyerror", "limit": 2}, &out)
	if len(out.Occurrences) != 2 || out.NextCursor == "" || out.Count != 3 {
		t.Errorf("paged group: %+v", out)
	}
	if res := f.call("get_event_group", map[string]any{"project": "infra", "fingerprint": "keyerror"}, nil); !res.IsError {
		t.Error("fingerprint in another project should be an error")
	}
	if res := f.call("get_event_group", map[string]any{"project": "uini"}, nil); !res.IsError {
		t.Error("missing fingerprint should be an error")
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

var _ = slog.New(slog.NewTextHandler(io.Discard, nil))
