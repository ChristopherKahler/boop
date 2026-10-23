package routing

import (
	"testing"

	"github.com/chrisgreg/boop/server/internal/alerts"
)

func TestDecide(t *testing.T) {
	problems := []string{"warning", "error", "critical"}
	base := Input{Notify: true, PushLevels: problems, PushRecoveries: true}
	cases := []struct {
		name string
		mod  func(*Input)
		push bool
		kind string
	}{
		{"new error alert pushes", func(i *Input) { i.Change, i.Level, i.AlertLevel = alerts.Opened, "error", "error" }, true, KindAlert},
		{"repeat never pushes", func(i *Input) { i.Change, i.Level, i.AlertLevel = alerts.Repeated, "critical", "critical" }, false, ""},
		{"escalation into a push level pushes", func(i *Input) { i.Change, i.Level, i.AlertLevel = alerts.Escalated, "critical", "critical" }, true, KindEscalated},
		{"escalation outside push levels stays in inbox", func(i *Input) {
			i.PushLevels = []string{"critical"}
			i.Change, i.Level, i.AlertLevel = alerts.Escalated, "error", "error"
		}, false, ""},
		{"recovery of a pushed alert pushes", func(i *Input) { i.Change, i.Level, i.AlertLevel = alerts.Resolved, "success", "error" }, true, KindResolved},
		{"recovery with recoveries off", func(i *Input) {
			i.PushRecoveries = false
			i.Change, i.Level, i.AlertLevel = alerts.Resolved, "success", "error"
		}, false, ""},
		{"recovery of an alert that never pushed", func(i *Input) {
			i.PushLevels = []string{"critical"}
			i.Change, i.Level, i.AlertLevel = alerts.Resolved, "success", "warning"
		}, false, ""},
		{"info event stays in inbox by default", func(i *Input) { i.Level = "info" }, false, ""},
		{"success event pushes when routed", func(i *Input) { i.PushLevels = []string{"success"}; i.Level = "success" }, true, KindEvent},
		{"silenced never pushes", func(i *Input) { i.Silenced = true; i.Change, i.Level = alerts.Opened, "critical" }, false, ""},
		{"project switch off never pushes", func(i *Input) { i.Notify = false; i.Change, i.Level = alerts.Opened, "critical" }, false, ""},
		{"warning alert below routing", func(i *Input) {
			i.PushLevels = []string{"error", "critical"}
			i.Change, i.Level, i.AlertLevel = alerts.Opened, "warning", "warning"
		}, false, ""},
	}
	for _, c := range cases {
		in := base
		in.PushLevels = append([]string(nil), base.PushLevels...)
		c.mod(&in)
		d := Decide(in)
		if d.Push != c.push || d.Kind != c.kind || d.Reason == "" {
			t.Errorf("%s: got %+v, want push=%v kind=%q", c.name, d, c.push, c.kind)
		}
	}
}
