package summary

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/chrisgreg/boop/server/internal/ids"
)

// Duration renders seconds as "45s", "12m", "3h 05m", "2d 4h".
func Duration(sec float64) string {
	s := int64(math.Round(sec))
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm", s/60)
	case s < 86400:
		return fmt.Sprintf("%dh %02dm", s/3600, (s%3600)/60)
	default:
		return fmt.Sprintf("%dd %dh", s/86400, (s%86400)/3600)
	}
}

func when(ts string, loc *time.Location) string {
	t, err := ids.Parse(ts)
	if err != nil {
		return ts
	}
	return t.In(loc).Format("Jan 2, 15:04")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// cell makes text safe inside a Markdown table cell.
func cell(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	if len([]rune(s)) > 90 {
		s = string([]rune(s)[:90]) + "…"
	}
	return s
}

func levelWord(l string) string {
	switch l {
	case "critical":
		return "Critical"
	case "error":
		return "Error"
	case "warning":
		return "Warning"
	}
	return strings.ToUpper(l[:1]) + l[1:]
}

func describe(d Durations, what string) string {
	if d.Count == 0 {
		return fmt.Sprintf("no alerts were %s", what)
	}
	if d.Count == 1 {
		return fmt.Sprintf("%s (1 alert)", Duration(d.Median))
	}
	return fmt.Sprintf("median %s, average %s, fastest %s, slowest %s (%d alerts)", Duration(d.Median), Duration(d.Average), Duration(d.Fastest), Duration(d.Slowest), d.Count)
}

// Markdown renders the summary as a document to send to the client.
func Markdown(s Summary, loc *time.Location) string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	p("# Monitoring summary: %s, %s\n\n", s.ClientName, s.MonthLabel)
	p("An alert is one problem reported by the monitoring services (a warning, an error or a critical failure). ")
	p("Repeats of the same problem count as one alert. Times are %s.\n\n", s.Timezone)

	p("## At a glance\n\n")
	if s.Totals.Alerts == 0 {
		p("- **No alerts** this month.\n")
	} else {
		var parts []string
		for _, l := range s.ByLevel {
			if l.Count > 0 {
				word := strings.ToLower(levelWord(l.Level))
				if l.Count > 1 && word != "critical" {
					word += "s"
				}
				parts = append(parts, fmt.Sprintf("%d %s", l.Count, word))
			}
		}
		p("- **%s** (%s) across %s\n", plural(s.Totals.Alerts, "alert", "alerts"), strings.Join(parts, ", "), plural(len(s.Projects), "project", "projects"))
		p("- **%d resolved**: %d recovered on their own, %d resolved by hand", s.Totals.Resolved, s.Totals.Recovered, s.Totals.ResolvedByHand)
		if s.Totals.StillOpen > 0 {
			p("; **%d still open**", s.Totals.StillOpen)
		}
		p("\n")
		p("- **Time to acknowledge**: %s\n", describe(s.Acknowledge, "acknowledged"))
		p("- **Time to resolve**: %s\n", describe(s.Resolve, "resolved"))
		if s.Notify.Count > 0 {
			p("- **Time to notify**: %s\n", describe(s.Notify, "pushed"))
		}
	}
	p("- %s received in total, %s sent\n", plural(s.Totals.Events, "event", "events"), plural(s.Totals.Pushes, "push notification", "push notifications"))
	if s.CarriedOver == 1 {
		p("- 1 alert from before this month was still open when it began\n")
	} else if s.CarriedOver > 1 {
		p("- %d alerts from before this month were still open when it began\n", s.CarriedOver)
	}
	if s.RetentionNotes != "" {
		p("\n_%s_\n", s.RetentionNotes)
	}

	if s.Totals.Alerts > 0 {
		p("\n## Alerts by level\n\n| Level | Alerts |\n|---|---:|\n")
		for _, l := range s.ByLevel {
			p("| %s | %d |\n", levelWord(l.Level), l.Count)
		}
		if len(s.Projects) > 1 {
			p("\n## By project\n\n| Project | Alerts | Still open |\n|---|---:|---:|\n")
			for _, pc := range s.Projects {
				p("| %s | %d | %d |\n", cell(pc.Project), pc.Alerts, pc.Open)
			}
		}
		p("\n## What broke\n\n| First seen | Project | Problem | Level | Occurrences | Acknowledged after | Resolved after | Outcome |\n|---|---|---|---|---:|---|---|---|\n")
		for _, a := range s.Alerts {
			ack, res := "—", "—"
			if a.ToAcknowledge != nil {
				ack = Duration(*a.ToAcknowledge)
			}
			if a.ToResolve != nil {
				res = Duration(*a.ToResolve)
			}
			outcome := map[string]string{"recovered": "Recovered on its own", "resolved": "Resolved by hand", "open": "Still open"}[a.Status]
			p("| %s | %s | %s | %s | %d | %s | %s | %s |\n", when(a.FirstSeen, loc), cell(a.Project), cell(a.Title), levelWord(a.Level), a.Count, ack, res, outcome)
		}
	}
	if len(s.Recoveries) > 0 {
		p("\n## Recoveries\n\nThese problems cleared without anyone stepping in:\n\n")
		for _, a := range s.Recoveries {
			down := ""
			if a.ToResolve != nil {
				down = ", lasted " + Duration(*a.ToResolve)
			}
			p("- %s: %s (%s%s)\n", when(a.FirstSeen, loc), cell(a.Title), cell(a.Project), down)
		}
	}
	p("\n---\nGenerated %s.\n", when(s.GeneratedAt, loc))
	return b.String()
}
