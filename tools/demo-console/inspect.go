package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type containerInfo struct {
	Service  string   `json:"service"`
	ID       string   `json:"id"`
	State    string   `json:"state"`
	Health   string   `json:"health"`
	Started  string   `json:"started"`
	Restarts int      `json:"restarts"`
	Ports    []string `json:"ports"`
}

func (c *console) containers(ctx context.Context) ([]containerInfo, error) {
	ids, err := c.runner.capture(ctx, "docker", c.cfg.compose("--profile", "demo", "ps", "--all", "-q")...)
	if err != nil {
		return nil, err
	}
	result := []containerInfo{}
	if len(strings.Fields(string(ids))) == 0 {
		return result, nil
	}
	data, err := c.runner.capture(ctx, "docker", append([]string{"inspect"}, strings.Fields(string(ids))...)...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID     string `json:"Id"`
		Config struct{ Labels map[string]string }
		State  struct {
			Status    string
			StartedAt string
			Health    *struct{ Status string }
		}
		RestartCount    int
		NetworkSettings struct {
			Ports map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string
			}
		}
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	for _, item := range raw {
		if item.Config.Labels["com.docker.compose.project"] != c.cfg.project {
			return nil, errors.New("container project differs from the selected project")
		}
		entry := containerInfo{Service: item.Config.Labels["com.docker.compose.service"], ID: item.ID, State: item.State.Status, Started: item.State.StartedAt, Restarts: item.RestartCount, Ports: []string{}}
		if item.State.Health != nil {
			entry.Health = item.State.Health.Status
		}
		for _, bindings := range item.NetworkSettings.Ports {
			for _, binding := range bindings {
				entry.Ports = append(entry.Ports, binding.HostIP+":"+binding.HostPort)
			}
		}
		result = append(result, entry)
	}
	return result, nil
}

// Check the published address before HTTP controls can touch a different project.
func (c *console) guardHTTP(ctx context.Context, url string) error {
	for _, target := range []struct{ service, key, port string }{{"client-api", "API_HTTP_PORT", "8080"}, {"auth", "AUTH_HTTP_PORT", "8084"}, {"bmkg-mock", "BMKG_MOCK_HTTP_PORT", "8081"}, {"pvmbg-mock", "PVMBG_MOCK_HTTP_PORT", "8082"}, {"aggregator", "AGGREGATOR_HTTP_PORT", "8083"}} {
		if !strings.HasPrefix(url, c.cfg.endpoint(target.key, target.port)+"/") {
			continue
		}
		containers, err := c.containers(ctx)
		if err != nil {
			return err
		}
		address := "127.0.0.1:" + c.cfg.value(target.key, target.port)
		for _, container := range containers {
			if container.Service == target.service && container.State == "running" {
				for _, port := range container.Ports {
					if port == address {
						return nil
					}
				}
			}
		}
		return fmt.Errorf("%s is not running at %s in project %s", target.service, address, c.cfg.project)
	}
	return errors.New("HTTP target is outside the selected profile")
}

func (c *console) status(w http.ResponseWriter, r *http.Request) {
	v, err := c.containers(r.Context())
	if err != nil {
		fail(w, 503, err)
		return
	}
	respond(w, 200, v)
}
func (c *console) logs(w http.ResponseWriter, r *http.Request) {
	service := r.PathValue("service")
	allowed := false
	for _, s := range []string{"aggregator", "client-api", "auth", "bmkg-mock", "pvmbg-mock", "nats", "postgres", "dashboard-consumer", "notifier-consumer", "third-consumer"} {
		if s == service {
			allowed = true
		}
	}
	if !allowed {
		http.NotFound(w, r)
		return
	}
	data, err := c.runner.capture(r.Context(), "docker", c.cfg.compose("--profile", "demo", "logs", "--no-color", "--tail", "100", service)...)
	if err != nil {
		fail(w, 503, err)
		return
	}
	respond(w, 200, map[string]string{"text": c.runner.redact(string(data))})
}

func (c *console) inspect(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind == "api" || kind == "mapping" {
		c.runner.mu.Lock()
		measuring := c.runner.active != nil && c.runner.active.record.Action == "load-trial"
		c.runner.mu.Unlock()
		if measuring {
			fail(w, 409, errors.New("API inspection is paused during the load trial to preserve its measurements"))
			return
		}
	}
	value, err := c.inspection(r.Context(), kind, r.URL.Query())
	if err != nil {
		fail(w, 503, err)
		return
	}
	data, err := c.runner.redactedJSON(value)
	if err != nil {
		fail(w, 500, err)
		return
	}
	respond(w, 200, map[string]string{"json": string(data)})
}

func (c *console) inspection(ctx context.Context, kind string, query url.Values) (any, error) {
	var value any
	var err error
	switch kind {
	case "containers":
		value, err = c.containers(ctx)
	case "profile":
		value = c.runner.maskedProfile()
	case "tracked-config":
		value, err = c.trackedConfig(ctx)
	case "config":
		service := query.Get("service")
		if !validService(service) {
			return nil, errors.New("unknown service")
		}
		var data []byte
		data, err = c.runner.capture(ctx, "docker", c.cfg.compose("--profile", "demo", "config", "--format", "json")...)
		if err == nil {
			var configuration struct {
				Services map[string]struct {
					Environment map[string]any
					Networks    json.RawMessage
				}
			}
			err = json.Unmarshal(data, &configuration)
			selected := configuration.Services[service]
			value = map[string]any{"service": service, "environment": selected.Environment, "networks": selected.Networks}
		}

	case "mapping":
		value, err = c.mapping(ctx, query.Get("source"), query.Get("ref"))
	case "api":
		identity := query.Get("identity")
		if identity == "" {
			identity = "field-team"
		}
		if identity != "field-team" && identity != "media" && identity != "bnpb-internal" {
			return nil, errors.New("invalid identity")
		}
		source := query.Get("source")
		if source == "" {
			source = "BMKG"
		}
		if source != "BMKG" && source != "PVMBG" {
			return nil, errors.New("invalid source")
		}
		var token string
		token, err = c.login(ctx, identity)
		if err == nil {
			view := "full"
			if identity == "media" {
				view = "summary"
			}
			var status int
			var data []byte
			status, data, err = c.api(ctx, "GET", c.cfg.endpoint("API_HTTP_PORT", "8080")+"/v1/hazards?source="+source+"&view="+view+"&limit=10", nil, map[string]string{"Authorization": "Bearer " + token})
			value = map[string]any{"status": status, "identity": identity, "response": json.RawMessage(data)}
		}
	case "history":
		source, ref := query.Get("source"), query.Get("ref")
		if (source != "BMKG" && source != "PVMBG") || !refPattern.MatchString(ref) {
			return nil, errors.New("choose a source and enter its source_ref_id")
		}
		revisions := query.Get("revisions")
		if revisions == "" {
			revisions = "1,2"
		}
		if revisions != "1" && revisions != "1,2" {
			return nil, errors.New("choose revision 1 or 1,2")
		}
		var data []byte
		data, err = c.runner.capture(ctx, "docker", c.cfg.compose("exec", "-T", "aggregator", "/aggregator", "history-audit", "--source", source, "--source-ref-id", ref, "--revisions", revisions)...)
		if err == nil {
			value = json.RawMessage(data)
		}
	case "store":
		var data []byte
		data, err = c.runner.capture(ctx, "docker", c.cfg.compose("exec", "-T", "aggregator", "/aggregator", "store-audit")...)
		if err == nil {
			value = json.RawMessage(data)
		}
	case "events":
		value, err = c.eventState(ctx)
	case "schema":
		name := query.Get("name")
		if name == "" {
			name = "hazard-event"
		}
		file := map[string]string{"hazard-event": "hazard-event.schema.json", "hazard-envelope": "hazard-envelope.schema.json", "hazard-summary": "hazard-summary.schema.json"}[name]
		if file == "" {
			return nil, errors.New("unknown schema")
		}
		var data []byte
		data, err = os.ReadFile(filepath.Join(c.cfg.repo, "contracts", file))
		if err == nil {
			value = json.RawMessage(data)
		}
	case "historical":
		root := filepath.Join(c.cfg.repo, "docs/evidence")
		files := []string{}
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if entry.IsDir() && entry.Name() == "private" {
				return filepath.SkipDir
			}
			if !entry.IsDir() && entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") && len(files) < 100 {
				rel, _ := filepath.Rel(root, path)
				files = append(files, rel)
			}
			return nil
		})
		value = files
	default:
		return nil, errors.New("unknown inspection")
	}
	return value, err
}

func (c *console) mapping(ctx context.Context, source, pinned string) (any, error) {
	if source == "" {
		source = "PVMBG"
	}
	if source != "PVMBG" && source != "BMKG" {
		return nil, errors.New("invalid source")
	}
	if pinned != "" && !refPattern.MatchString(pinned) {
		return nil, errors.New("invalid source reference")
	}
	url := c.cfg.endpoint("PVMBG_MOCK_HTTP_PORT", "8082") + "/volcanic-reports"
	headers := map[string]string{"Authorization": "Bearer " + c.cfg.values["PVMBG_TOKEN"]}
	refKey := "report_id"
	if source == "BMKG" {
		url = c.cfg.endpoint("BMKG_MOCK_HTTP_PORT", "8081") + "/seismic-events"
		headers = map[string]string{"X-BMKG-Key": c.cfg.values["BMKG_API_KEY"]}
		refKey = "event_id"
	}
	status, raw, err := c.api(ctx, "GET", url, nil, headers)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("source: HTTP %d", status)
	}
	var records []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil, err
	}
	status, canonical, err := c.api(ctx, "GET", c.cfg.endpoint("AGGREGATOR_HTTP_PORT", "8083")+"/internal/v1/hazards?source="+source+"&view=full&limit=50", nil, map[string]string{"X-Internal-Key": c.cfg.values["AGGREGATOR_API_KEY"]})
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("Aggregator: HTTP %d", status)
	}
	var result struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(canonical, &result); err != nil {
		return nil, err
	}
	for i := len(records) - 1; i >= 0; i-- {
		var ref string
		_ = json.Unmarshal(records[i][refKey], &ref)
		if pinned != "" && pinned != ref {
			continue
		}
		for _, event := range result.Data {
			var mapped string
			_ = json.Unmarshal(event["source_ref_id"], &mapped)
			if mapped == ref {
				data, _ := json.Marshal(records[i])
				eventData, _ := json.Marshal(event)
				return map[string]any{"source": json.RawMessage(data), "canonical": json.RawMessage(eventData), "source_ref_id": ref}, nil
			}
		}
	}
	return nil, errors.New("no mapped source record yet; wait for a polling cycle and refresh")
}

func (c *console) eventState(ctx context.Context) (any, error) {
	data, err := c.runner.capture(ctx, "docker", c.cfg.compose("exec", "-T", "nats", "wget", "-qO-", "http://127.0.0.1:8222/jsz?streams=true&consumers=true")...)
	if err != nil {
		return nil, err
	}
	var broker struct {
		Accounts []struct {
			Streams []json.RawMessage `json:"stream_detail"`
		} `json:"account_details"`
	}
	if err := json.Unmarshal(data, &broker); err != nil {
		return nil, err
	}
	var stream json.RawMessage
	for _, account := range broker.Accounts {
		for _, raw := range account.Streams {
			var info struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(raw, &info)
			if info.Name == c.cfg.value("NATS_STREAM_NAME", "HAZARD_EVENTS") {
				stream = raw
			}
		}
	}
	if stream == nil {
		return nil, errors.New("hazard stream has not been created yet")
	}
	result := map[string]any{"stream": stream, "consumers": map[string]any{}}
	consumers := result["consumers"].(map[string]any)
	for _, service := range []string{"dashboard-consumer", "notifier-consumer", "third-consumer"} {
		binary := "/notifier-consumer"
		if service == "dashboard-consumer" {
			binary = "/dashboard-consumer"
		}
		data, err := c.runner.capture(ctx, "docker", c.cfg.compose("--profile", "demo", "exec", "-T", service, binary, "inspect")...)
		if err != nil {
			consumers[service] = map[string]string{"unavailable": err.Error()}
		} else {
			var counts map[string]json.RawMessage
			_ = json.Unmarshal(data, &counts)
			delete(counts, "snapshots")
			delete(counts, "notifications")
			consumers[service] = counts
		}
	}
	return result, nil
}

func (c *console) historical(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("file")
	file = filepath.Clean(file)
	if !filepath.IsLocal(file) || !strings.HasSuffix(file, ".json") || strings.HasPrefix(file, "private/") {
		http.NotFound(w, r)
		return
	}
	root := filepath.Join(c.cfg.repo, "docs/evidence")
	path := root
	for _, part := range strings.Split(filepath.Clean(file), string(filepath.Separator)) {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			http.NotFound(w, r)
			return
		}
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fail(w, 500, err)
		return
	}
	respond(w, 200, map[string]string{"json": c.runner.redact(prettyJSON(data))})
}
