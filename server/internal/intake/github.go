package intake

import (
	"encoding/json"
	"strconv"

	"github.com/chrisgreg/boop/server/internal/events"
	"github.com/chrisgreg/boop/server/internal/events/levels"
)

// GitHub Actions: a repository (or organization) webhook with the "Workflow
// runs" event. A completed run that failed, timed out or could not start
// opens an alert for that workflow and branch; the next successful run on
// the same branch resolves it. Other conclusions (cancelled, skipped...) are
// ignored. Set the webhook's secret to the intake token (or the intake's
// signing secret) and X-Hub-Signature-256 is verified.

func mapGitHub(r Request) (Result, error) {
	if sig := r.Header.Get("X-Hub-Signature-256"); sig != "" {
		key := r.Secret
		if key == "" {
			key = r.Token
		}
		if !validHMAC(key, r.Body, sig, "sha256=") {
			return Result{}, ErrSignature
		}
	} else if r.Secret != "" {
		return Result{}, ErrSignature
	}
	switch ev := r.Header.Get("X-GitHub-Event"); ev {
	case "ping":
		return ignored("GitHub ping (the webhook is connected)")
	case "workflow_run":
	default:
		return ignored("GitHub " + ev + " event (only workflow runs are used)")
	}
	var p struct {
		Action      string `json:"action"`
		WorkflowRun struct {
			ID           int64  `json:"id"`
			Name         string `json:"name"`
			DisplayTitle string `json:"display_title"`
			HeadBranch   string `json:"head_branch"`
			HeadSHA      string `json:"head_sha"`
			RunNumber    int    `json:"run_number"`
			RunAttempt   int    `json:"run_attempt"`
			Event        string `json:"event"`
			Conclusion   string `json:"conclusion"`
			HTMLURL      string `json:"html_url"`
			WorkflowID   int64  `json:"workflow_id"`
			UpdatedAt    string `json:"updated_at"`
			Actor        struct {
				Login string `json:"login"`
			} `json:"actor"`
		} `json:"workflow_run"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(r.Body, &p); err != nil {
		return Result{}, ErrPayload
	}
	run := p.WorkflowRun
	if p.Action != "completed" {
		return ignored("workflow run " + p.Action)
	}
	branch := firstNonEmpty(run.HeadBranch, "(no branch)")
	in := events.Input{
		Source: "github-actions", ExternalID: strconv.FormatInt(run.ID, 10), OccurredAt: run.UpdatedAt,
		Fingerprint: "github:" + p.Repository.FullName + ":" + strconv.FormatInt(run.WorkflowID, 10) + ":" + branch,
		Body:        joinNonEmpty(" · ", p.Repository.FullName, run.DisplayTitle, "run #"+strconv.Itoa(run.RunNumber), prefixed("by ", run.Actor.Login)),
		Data: data(map[string]any{"repository": p.Repository.FullName, "workflow": run.Name, "branch": branch, "sha": run.HeadSHA,
			"run": run.RunNumber, "attempt": run.RunAttempt, "trigger": run.Event, "conclusion": run.Conclusion}),
	}
	if run.HTMLURL != "" {
		in.Actions = []events.Action{{Label: "Open run", URL: run.HTMLURL}}
	}
	switch run.Conclusion {
	case "failure", "timed_out", "startup_failure":
		in.Type, in.Level = run.Conclusion, levels.Error
		in.Title = run.Name + " failed on " + branch
		if run.Conclusion == "timed_out" {
			in.Title = run.Name + " timed out on " + branch
		}
	case "success":
		in.Type, in.Level, in.Resolve = "success", levels.Success, true
		in.Title = run.Name + " passed on " + branch
	default:
		return ignored("workflow run " + run.Conclusion)
	}
	return Result{Events: []events.Input{in}}, nil
}
