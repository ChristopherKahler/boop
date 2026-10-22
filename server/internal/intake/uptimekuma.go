package intake

import (
	"bytes"
	"encoding/json"
	"mime"
	"mime/multipart"
	"strconv"
	"strings"

	"github.com/chrisgreg/boop/server/internal/events"
	"github.com/chrisgreg/boop/server/internal/events/levels"
)

// Uptime Kuma's Webhook notification posts {heartbeat, monitor, msg}, as
// JSON (the default), as multipart form-data (a "data" field holding the
// JSON), or as a GET with msg / heartbeat / monitor query parameters.
// heartbeat.status: 0 down, 1 up, 2 pending, 3 maintenance. Its "Test"
// button sends just a msg, which is stored as an info event so the
// connection shows in the inbox without pushing.

type kumaPayload struct {
	Msg       string `json:"msg"`
	Heartbeat *struct {
		Status    int    `json:"status"`
		Msg       string `json:"msg"`
		Time      string `json:"time"`
		Duration  int    `json:"duration"`
		Important bool   `json:"important"`
	} `json:"heartbeat"`
	Monitor *struct {
		ID       json.RawMessage `json:"id"`
		Name     string          `json:"name"`
		URL      string          `json:"url"`
		Hostname string          `json:"hostname"`
		Type     string          `json:"type"`
	} `json:"monitor"`
}

func readKuma(r Request) (kumaPayload, error) {
	var p kumaPayload
	ct, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch {
	case strings.ToUpper(r.Method) == "GET":
		p.Msg = r.Query.Get("msg")
		if h := r.Query.Get("heartbeat"); h != "" {
			if err := json.Unmarshal([]byte(h), &p.Heartbeat); err != nil {
				return p, ErrPayload
			}
		}
		if m := r.Query.Get("monitor"); m != "" {
			if err := json.Unmarshal([]byte(m), &p.Monitor); err != nil {
				return p, ErrPayload
			}
		}
		return p, nil
	case ct == "multipart/form-data":
		mr := multipart.NewReader(bytes.NewReader(r.Body), params["boundary"])
		form, err := mr.ReadForm(1 << 20)
		if err != nil || len(form.Value["data"]) == 0 {
			return p, ErrPayload
		}
		return p, json.Unmarshal([]byte(form.Value["data"][0]), &p)
	default:
		if err := json.Unmarshal(r.Body, &p); err != nil {
			return p, ErrPayload
		}
		return p, nil
	}
}

func mapUptimeKuma(r Request) (Result, error) {
	p, err := readKuma(r)
	if err != nil {
		return Result{}, ErrPayload
	}
	if p.Heartbeat == nil || p.Monitor == nil {
		if p.Msg == "" {
			return Result{}, ErrPayload
		}
		return Result{Events: []events.Input{{Source: "uptime-kuma", Type: "test", Level: levels.Info, Title: "Uptime Kuma: " + p.Msg}}}, nil
	}
	m, h := p.Monitor, p.Heartbeat
	id := strings.Trim(string(m.ID), `"`)
	name := firstNonEmpty(m.Name, m.Hostname, m.URL, "Monitor "+id)
	in := events.Input{
		Source: "uptime-kuma", Fingerprint: "uptime-kuma:" + firstNonEmpty(id, name), Body: h.Msg,
		Data: data(map[string]any{"monitor": name, "monitor_id": id, "type": m.Type, "url": m.URL, "time": h.Time, "duration_seconds": h.Duration}),
	}
	if strings.HasPrefix(m.URL, "http://") || strings.HasPrefix(m.URL, "https://") {
		in.Actions = []events.Action{{Label: "Open site", URL: m.URL}}
	}
	switch h.Status {
	case 0:
		in.Type, in.Level, in.Title = "down", levels.Error, name+" is down"
	case 1:
		in.Type, in.Level, in.Resolve, in.Title = "up", levels.Success, true, name+" is up"
		if h.Duration > 0 {
			in.Body = joinNonEmpty(" · ", h.Msg, "was down "+(strconv.Itoa(h.Duration/60))+"m")
		}
	case 2:
		in.Type, in.Level, in.Title = "pending", levels.Warning, name+" is not responding (retrying)"
	default:
		return ignored("Uptime Kuma maintenance status for " + name)
	}
	return Result{Events: []events.Input{in}}, nil
}
