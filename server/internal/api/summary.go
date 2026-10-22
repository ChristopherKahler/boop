package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/chrisgreg/boop/server/internal/settings"
	"github.com/chrisgreg/boop/server/internal/summary"
)

// buildSummary reads ?month=2026-10 (default: this month) and ?tz= (default:
// BOOP_TIMEZONE) and builds the client's monthly summary.
func (s *Server) buildSummary(w http.ResponseWriter, r *http.Request) (summary.Summary, *time.Location, bool) {
	tz := r.URL.Query().Get("tz")
	if tz == "" {
		tz = s.Config.Timezone
	}
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "tz must be a timezone name such as America/Chicago")
		return summary.Summary{}, nil, false
	}
	now := time.Now()
	month := r.URL.Query().Get("month")
	if month == "" {
		month = now.In(loc).Format("2006-01")
	}
	sum, err := summary.Build(r.Context(), s.DB, r.PathValue("id"), month, loc, now)
	switch {
	case errors.Is(err, summary.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return sum, nil, false
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "client not found")
		return sum, nil, false
	case err != nil:
		s.fail(w, err)
		return sum, nil, false
	}
	// Events (not alerts) are pruned by retention; say so when it bites.
	if days, err := s.Settings.GetInt(r.Context(), settings.KeyRetentionDays, s.Config.RetentionDays); err == nil && days > 0 {
		from, _, _ := summary.MonthBounds(month, loc)
		if from.Before(now.AddDate(0, 0, -days)) {
			sum.RetentionNotes = fmt.Sprintf("Events are kept for %d days, so event and push counts for this month may be incomplete. Alert figures are kept in full.", days)
		}
	}
	return sum, loc, true
}

func (s *Server) clientSummary(w http.ResponseWriter, r *http.Request) {
	if sum, _, ok := s.buildSummary(w, r); ok {
		writeJSON(w, http.StatusOK, sum)
	}
}

// clientSummaryMarkdown is the same report as a Markdown file to send the client.
func (s *Server) clientSummaryMarkdown(w http.ResponseWriter, r *http.Request) {
	sum, loc, ok := s.buildSummary(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-monitoring-%s.md"`, sum.ClientSlug, sum.Month))
	_, _ = w.Write([]byte(summary.Markdown(sum, loc)))
}
