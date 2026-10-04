package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type commandPlan struct {
	script  string
	targets []string
}

func shellCommand(program string, args ...string) string {
	return strings.TrimPrefix(formatCommand(program, args), "$ ")
}

func quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func python(code string) string { return "python3 - <<'PY'\n" + strings.TrimSpace(code) + "\nPY" }

// Recipes contain the complete commands, including pipes, waits and cleanup.
// These are the defaults supplied by helpers; Enter may execute a user edit.
func (c *console) commandPlan(id string, p map[string]string) (commandPlan, error) {
	if _, err := validateAction(id, p); err != nil {
		return commandPlan{}, err
	}
	docker := func(args ...string) string { return composeCommand(c.cfg, args...) }
	pvmbg := c.cfg.endpoint("PVMBG_MOCK_HTTP_PORT", "8082")
	admin := func(path string, body any) string {
		data, _ := json.Marshal(body)
		return jsonFormat(`curl --silent --show-error --fail-with-body --max-time 10 \
  --dump-header /dev/stderr \
  --header "Authorization: Bearer $PVMBG_TOKEN" \
  --header 'Content-Type: application/json' \
  --data `+quote(string(data))+` \
  `+quote(pvmbg+"/admin/"+path), p["format"])
	}
	minDelay, _ := strconv.Atoi(c.cfg.value("PVMBG_DELAY_MIN_MS", "500"))
	maxDelay, _ := strconv.Atoi(c.cfg.value("PVMBG_DELAY_MAX_MS", "3000"))
	defaultDelay := admin("delay", map[string]int{"min_ms": minDelay, "max_ms": maxDelay})
	var lines []string
	var targets []string
	format := p["format"]
	switch id {
	case "source-read":
		url, header := c.sourceEndpoint(p["source"], p["credential"])
		lines = append(lines, jsonReadQuery(url, header, format, `  --get --data-urlencode "since=$(date -u -d '2 minutes ago' +'%Y-%m-%dT%H:%M:%SZ')" \
`))
		targets = append(targets, url)
	case "client-login":
		lines = append(lines, c.sessionLogin(p["identity"], format))
		targets = append(targets, c.cfg.endpoint("AUTH_HTTP_PORT", "8084")+"/v1/auth/login")
	case "client-read":
		lines = append(lines, sessionAccess(p["identity"]+".json"), jsonRead(c.clientURL(p["source"], p["view"]), "Authorization: Bearer $access_token", format))
		targets = append(targets, c.cfg.endpoint("API_HTTP_PORT", "8080")+"/health")
	case "token-old-read":
		lines = append(lines, sessionAccess("field-team-old.json"), jsonRead(c.clientURL("BMKG", "full"), "Authorization: Bearer $access_token", format))
		targets = append(targets, c.cfg.endpoint("API_HTTP_PORT", "8080")+"/health")
	case "token-wait":
		lines = append(lines, `python3 - "$AAT_DEMO_SESSION_DIR/field-team.json" <<'PY'
import base64, json, sys, time
session = json.load(open(sys.argv[1]))
encoded = session['access_token'].split('.')[1]
claims = json.loads(base64.urlsafe_b64decode(encoded + '=='))
expires_at = claims['exp'] + 2
print(f"Waiting for token expiry: exp={claims['exp']}, TTL={session['expires_in']}s", flush=True)
while time.time() < expires_at:
    time.sleep(1)
print('Expiry timestamp reached; read the API to check rejection.', flush=True)
PY`)

	case "token-refresh":
		lines = append(lines, c.sessionRefresh(format))
		targets = append(targets, c.cfg.endpoint("AUTH_HTTP_PORT", "8084")+"/v1/auth/refresh")
	case "service-start", "service-rebuild":
		args := []string{"--profile", "demo", "up", "-d", "--no-deps", "--wait", "--wait-timeout", "90"}
		if id == "service-rebuild" {
			args = append(args, "--build", "--force-recreate")
		}
		lines = append(lines, docker(append(args, p["service"])...))
	case "service-stop":
		lines = append(lines, docker("--profile", "demo", "stop", p["service"]))
	case "service-logs":
		lines = append(lines, docker("--profile", "demo", "logs", "--follow", "--no-color", "--tail", "50", p["service"]))
	case "dashboard-stop":
		lines = append(lines, docker("stop", "dashboard-consumer"))
	case "dashboard-start":
		lines = append(lines, docker("start", "dashboard-consumer"))
	case "dashboard-rebuild":
		lines = append(lines, docker("up", "-d", "--build", "--no-deps", "--force-recreate", "--wait", "dashboard-consumer"))
	case "third-start":
		lines = append(lines, docker("--profile", "demo", "up", "-d", "--no-deps", "--wait", "--wait-timeout", "120", "third-consumer"))
	case "third-stop":
		lines = append(lines, docker("--profile", "demo", "stop", "third-consumer"))
	case "stack-start", "stack-build":
		args := []string{"up", "-d", "--wait", "--wait-timeout", "180"}
		if id == "stack-build" {
			args = append(args, "--build")
		}
		lines = append(lines, docker(args...))
	case "keys-setup":
		lines = append(lines, `mkdir -p .secrets
chmod 700 .secrets
if [ ! -e .secrets/jwt-private.pem ] && [ ! -e .secrets/jwt-public.pem ]; then
  (umask 077; openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out .secrets/jwt-private.pem)
  openssl pkey -in .secrets/jwt-private.pem -pubout -out .secrets/jwt-public.pem
fi
python3 scripts/setup-jwt-keys.py`)
	case "source-schema":
		version, _ := strconv.Atoi(p["version"])
		lines = append(lines, admin("schema-version", map[string]int{"version": version}))
		targets = append(targets, pvmbg+"/admin/schema-version")
	case "source-outage":
		lines = append(lines, admin("outage", map[string]bool{"enabled": p["enabled"] == "true"}))
		targets = append(targets, pvmbg+"/admin/outage")
	case "source-delay":
		command := admin("delay", map[string]int{"min_ms": 3000, "max_ms": 3000})
		if p["preset"] == "default" {
			command = defaultDelay
		}
		lines = append(lines, command)
		targets = append(targets, pvmbg+"/admin/delay")
	case "restore-defaults":
		outage, _ := strconv.ParseBool(c.cfg.value("PVMBG_OUTAGE_ENABLED", "false"))
		version, _ := strconv.Atoi(c.cfg.value("PVMBG_SCHEMA_VERSION", "1"))
		lines = append(lines, admin("outage", map[string]bool{"enabled": outage}), admin("schema-version", map[string]int{"version": version}), defaultDelay)
		targets = append(targets, pvmbg+"/admin/outage")
	case "smoke-trial", "token-trial", "events-trial":
		scenario := map[string]string{"smoke-trial": "smoke", "token-trial": "tokens", "events-trial": "events"}[id]
		lines = append(lines, "python3 scripts/verify-integration.py --env-file "+quote(c.cfg.envFile)+" --scenario "+scenario+` --output "$AAT_DEMO_OUTPUT"`)
		for _, target := range []string{pvmbg, c.cfg.endpoint("API_HTTP_PORT", "8080"), c.cfg.endpoint("AUTH_HTTP_PORT", "8084"), c.cfg.endpoint("BMKG_MOCK_HTTP_PORT", "8081")} {
			targets = append(targets, target+"/health")
		}
	case "latency-trial":
		lines = append(lines, jsonFormat(`python3 scripts/load/hazards-monotonic.py \
  --seconds 90 \
  --vus-per-source 30 \
  --output "$AAT_DEMO_OUTPUT"`, format))
		targets = append(targets, c.cfg.endpoint("API_HTTP_PORT", "8080")+"/health", c.cfg.endpoint("AUTH_HTTP_PORT", "8084")+"/health")
	case "load-run":
		lines = append(lines, `umask 077
mkdir -p "$AAT_DEMO_SESSION_DIR/load"
rm -f "$AAT_DEMO_SESSION_DIR/load/load.json" "$AAT_DEMO_SESSION_DIR/load/tcp.json"
`+loadCleanup(":")+`
python3 scripts/load/capture-tcp-connections.py \
  --api-url "$API_URL" --seconds 115 \
  --output "$AAT_DEMO_SESSION_DIR/load/tcp.json" > /dev/null &
tcp_pid=$!
python3 scripts/load/hazards-monotonic.py \
  --seconds 90 --vus-per-source 30 \
  --output "$AAT_DEMO_SESSION_DIR/load/load.json" > /dev/null &
load_pid=$!
wait "$load_pid"
load_pid=
wait "$tcp_pid"
tcp_pid=
printf 'Measurements saved. Read load metrics and TCP samples next.\n'`)
		targets = append(targets, c.cfg.endpoint("API_HTTP_PORT", "8080")+"/health", c.cfg.endpoint("AUTH_HTTP_PORT", "8084")+"/health")
	case "load-trial":
		lines = append(lines, "# Stop measurement processes and restore delay on completion or Ctrl+C.\n"+loadCleanup(defaultDelay), "# Set the source delay, then allow a polling cycle.\n"+admin("delay", map[string]int{"min_ms": 3000, "max_ms": 3000}), fmt.Sprintf("sleep %g", pollWait(c.cfg)), `curl --max-time 10 --fail-with-body --silent --show-error --output /dev/null --write-out 'PVMBG HTTP %{http_code}; latency %{time_total}s\n' -H "Authorization: Bearer $PVMBG_TOKEN" `+quote(pvmbg+"/volcanic-reports?since=2050-01-01T00:00:00Z"), `# Measure connections and HTTP latency concurrently.
python3 scripts/load/capture-tcp-connections.py --api-url "$API_URL" --seconds 115 --output "$AAT_DEMO_RUN_DIR/tcp.json" > /dev/null &
tcp_pid=$!
python3 scripts/load/hazards-monotonic.py --seconds 90 --vus-per-source 30 --output "$AAT_DEMO_RUN_DIR/load.json" > /dev/null &
load_pid=$!
load_exit=0
wait "$load_pid" || load_exit=$?
load_pid=
tcp_exit=0
wait "$tcp_pid" || tcp_exit=$?
tcp_pid=
test "$load_exit" -eq 0 && test "$tcp_exit" -eq 0`, jsonFormat(`cat "$AAT_DEMO_RUN_DIR/load.json"`, format), jsonFormat(`cat "$AAT_DEMO_RUN_DIR/tcp.json"`, format), "# Verify the sustained TCP interval and save combined evidence.\n"+python(tcpCheck))
		targets = append(targets, pvmbg+"/admin/delay", c.cfg.endpoint("API_HTTP_PORT", "8080")+"/health", c.cfg.endpoint("AUTH_HTTP_PORT", "8084")+"/health")
	case "access-check":
		lines = c.accessCommands()
		targets = append(targets, c.cfg.endpoint("API_HTTP_PORT", "8080")+"/health", c.cfg.endpoint("AUTH_HTTP_PORT", "8084")+"/health", pvmbg+"/health", c.cfg.endpoint("BMKG_MOCK_HTTP_PORT", "8081")+"/health")
	case "inspect":
		return c.inspectionPlan(p)
	default:
		return commandPlan{}, errors.New("unimplemented command")
	}
	return commandPlan{script: strings.Join(lines, "\n"), targets: targets}, nil
}

func (c *console) loginCommand(identity string) string {
	key := map[string]string{"media": "MEDIA_CLIENT_SECRET", "field-team": "FIELD_TEAM_CLIENT_SECRET", "bnpb-internal": "BNPB_INTERNAL_CLIENT_SECRET"}[identity]
	return `access_token=$(curl --max-time 10 --fail-with-body --silent --show-error -H 'Content-Type: application/json' --data "$(python3 -c ` + quote("import json, os; print(json.dumps({'client_id': '"+identity+"', 'client_secret': os.environ['"+key+"']}))") + `)" ` + quote(c.cfg.endpoint("AUTH_HTTP_PORT", "8084")+"/v1/auth/login") + ` | python3 -c 'import json, sys; print(json.load(sys.stdin)["access_token"])')`
}

func (c *console) inspectionPlan(p map[string]string) (commandPlan, error) {
	docker := func(args ...string) string { return composeCommand(c.cfg, args...) }
	var lines, targets []string
	switch p["kind"] {
	case "load":
		lines = append(lines, jsonFormat(`cat "$AAT_DEMO_SESSION_DIR/load/`+p["measurement"]+`.json"`, p["format"]))
	case "canonical":
		source := p["source"]
		if source == "" {
			source = "PVMBG"
		}
		url := c.cfg.endpoint("AGGREGATOR_HTTP_PORT", "8083") + "/internal/v1/hazards?source=" + source + "&view=full&limit=5"
		lines = append(lines, jsonFormat(jsonRead(url, "X-Internal-Key: $AGGREGATOR_API_KEY", "raw")+` | tee "$AAT_DEMO_OUTPUT"`, p["format"]))
		targets = append(targets, url)
	case "broker":
		lines = append(lines, jsonFormat(docker("exec", "-T", "nats", "wget", "-qO-", "http://127.0.0.1:8222/jsz?streams=true&consumers=true"), p["format"]))
	case "consumer":
		binary := "/notifier-consumer"
		if p["service"] == "dashboard-consumer" {
			binary = "/dashboard-consumer"
		}
		lines = append(lines, jsonFormat(docker("--profile", "demo", "exec", "-T", p["service"], binary, "inspect"), p["format"]))
	case "containers":
		lines = append(lines, docker("--profile", "demo", "ps", "--all"))
	case "store":
		lines = append(lines, jsonFormat(docker("exec", "-T", "aggregator", "/aggregator", "store-audit"), p["format"]))
	case "history":
		revisions := p["revisions"]
		if revisions == "" {
			revisions = "1,2"
		}
		lines = append(lines, jsonFormat(docker("exec", "-T", "aggregator", "/aggregator", "history-audit", "--source", p["source"], "--source-ref-id", p["ref"], "--revisions", revisions), p["format"]))
	case "schema":
		name := p["name"]
		if name == "" {
			name = "hazard-event"
		}
		lines = append(lines, jsonFormat(shellCommand("cat", "contracts/"+name+".schema.json"), p["format"]))
	case "tracked-config":
		lines = append(lines, "git ls-files -- .env .env.example .secrets")
	case "profile":
		keys := make([]string, 0, len(c.cfg.values))
		for key := range c.cfg.values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		lines = append(lines, jsonFormat(shellCommand("python3", append([]string{"-c", `import json, os, sys; print(json.dumps({key: os.environ.get(key) for key in sys.argv[1:]}))`}, keys...)...), p["format"]))
	case "config":
		lines = append(lines, jsonFormat(docker("--profile", "demo", "config", "--format", "json")+" | "+shellCommand("python3", "-c", `import json, sys; service=json.load(sys.stdin)['services'][sys.argv[1]]; print(json.dumps({'environment':service.get('environment'),'networks':service.get('networks')}))`, p["service"]), p["format"]))
	case "events":
		lines = append(lines, jsonFormat(docker("exec", "-T", "nats", "wget", "-qO-", "http://127.0.0.1:8222/jsz?streams=true&consumers=true"), p["format"]))
		for _, service := range []string{"dashboard-consumer", "notifier-consumer", "third-consumer"} {
			binary := "/notifier-consumer"
			if service == "dashboard-consumer" {
				binary = "/dashboard-consumer"
			}
			lines = append(lines, "if [ -n \"$("+docker("--profile", "demo", "ps", "--status", "running", "-q", service)+")\" ]; then\n  "+jsonFormat(docker("--profile", "demo", "exec", "-T", service, binary, "inspect"), p["format"])+"\nfi")
		}
	case "api":
		identity, source := p["identity"], p["source"]
		if identity == "" {
			identity = "field-team"
		}
		if source == "" {
			source = "BMKG"
		}
		view := "full"
		if identity == "media" {
			view = "summary"
		}
		lines = append(lines, sessionAccess(identity+".json"), jsonRead(c.clientURL(source, view), "Authorization: Bearer $access_token", p["format"]))
		targets = append(targets, c.cfg.endpoint("API_HTTP_PORT", "8080")+"/health")
	case "mapping":
		source := p["source"]
		if source == "" {
			source = "PVMBG"
		}
		url, header, refKey := c.cfg.endpoint("PVMBG_MOCK_HTTP_PORT", "8082")+"/volcanic-reports", `Authorization: Bearer $PVMBG_TOKEN`, "report_id"
		if source == "BMKG" {
			url = c.cfg.endpoint("BMKG_MOCK_HTTP_PORT", "8081") + "/seismic-events"
			header = `X-BMKG-Key: $BMKG_API_KEY`
			refKey = "event_id"
		}
		canonical := c.cfg.endpoint("AGGREGATOR_HTTP_PORT", "8083") + "/internal/v1/hazards?source=" + source + "&view=full&limit=50"
		lines = append(lines, `curl --max-time 10 --fail-with-body --silent --show-error -H "`+header+`" `+quote(url)+` --output "$AAT_DEMO_RUN_DIR/source.json"`, `curl --max-time 10 --fail-with-body --silent --show-error -H "X-Internal-Key: $AGGREGATOR_API_KEY" `+quote(canonical)+` --output "$AAT_DEMO_RUN_DIR/canonical.json"`, `python3 - "$AAT_DEMO_RUN_DIR/source.json" "$AAT_DEMO_RUN_DIR/canonical.json" `+quote(refKey)+" "+quote(p["ref"])+" <<'PY'\n"+strings.TrimSpace(mappingCheck)+"\nPY")
		targets = append(targets, url, canonical)
	default:
		return commandPlan{}, errors.New("unimplemented inspection")
	}
	return commandPlan{script: strings.Join(lines, "\n"), targets: targets}, nil
}

const mappingCheck = `
import json, os, sys
source_path, canonical_path, ref_key, pinned = sys.argv[1:]
source = json.load(open(source_path))
canonical = json.load(open(canonical_path))['data']
for raw in reversed(source):
    ref = raw[ref_key]
    if pinned and ref != pinned:
        continue
    for event in canonical:
        if event['source_ref_id'] == ref:
            result = {'source': raw, 'canonical': event, 'source_ref_id': ref}
            print(json.dumps(result, indent=2))
            with open(os.environ['AAT_DEMO_OUTPUT'], 'w') as output:
                json.dump(result, output)
            sys.exit(0)
raise SystemExit('No mapped source record yet; wait for a polling cycle.')`

const tcpCheck = `
import json, os
from pathlib import Path
directory = Path(os.environ['AAT_DEMO_RUN_DIR'])
tcp = json.loads((directory / 'tcp.json').read_text())
longest, start, previous = 0, None, None
for sample in tcp['samples']:
    elapsed, count = sample['elapsed_seconds'], sample['established_connections_to_api']
    if count < 50 or previous is not None and (elapsed < previous or elapsed - previous > 2 * tcp['sampling_interval_seconds']):
        start = None
    if count >= 50:
        if start is None:
            start = elapsed
        longest = max(longest, elapsed - start)
    previous = elapsed
load = json.loads((directory / 'load.json').read_text())
result = {'load':load, 'tcp':tcp, 'sustained_tcp_seconds':longest}
with open(os.environ['AAT_DEMO_OUTPUT'], 'w') as output:
    json.dump(result, output)
print(f'Observed >=50 TCP connections for {longest:.1f} consecutive sampled seconds')
assert longest >= 60, 'TCP criterion failed: fewer than 50 connections sustained for 60 seconds'
assert load['passed'], 'Load thresholds failed'`

func (c *console) accessCommands() []string {
	var lines []string
	for _, identity := range []string{"media", "field-team", "bnpb-internal"} {
		view := "full"
		if identity == "media" {
			view = "summary"
		}
		lines = append(lines, c.loginCommand(identity), `curl --max-time 10 --fail-with-body --silent --show-error -H "Authorization: Bearer $access_token" `+quote(c.cfg.endpoint("API_HTTP_PORT", "8080")+"/v1/hazards?source=BMKG&view="+view+"&limit=1")+` --output "$AAT_DEMO_RUN_DIR/access.json"`, `python3 -m json.tool "$AAT_DEMO_RUN_DIR/access.json"`, shellCommand("python3", "-c", `import json, sys; data=json.load(open(sys.argv[2]))['data']; assert data, 'No records'; expected={'hazard_id','source','hazard_type','severity','area_name','occurred_at','ingested_at'}; assert all(set(row)==expected for row in data) if sys.argv[1]=='media' else all('attributes' in row for row in data)`, identity)+` "$AAT_DEMO_RUN_DIR/access.json"`)
		if identity == "media" {
			for _, query := range []string{"view=full", "fields=attributes"} {
				lines = append(lines, rejectionCommand(c.cfg.endpoint("API_HTTP_PORT", "8080")+"/v1/hazards?"+query, `Authorization: Bearer $access_token`))
			}
		}
	}
	lines = append(lines, rejectionCommand(c.cfg.endpoint("BMKG_MOCK_HTTP_PORT", "8081")+"/seismic-events", `X-BMKG-Key: $PVMBG_TOKEN`), rejectionCommand(c.cfg.endpoint("PVMBG_MOCK_HTTP_PORT", "8082")+"/volcanic-reports", `Authorization: Bearer $BMKG_API_KEY`))
	return lines
}

func rejectionCommand(url, header string) string {
	return `status=$(curl --max-time 10 --silent --show-error -H "` + header + `" ` + quote(url) + ` --output "$AAT_DEMO_RUN_DIR/rejection.json" --write-out '%{http_code}')
cat "$AAT_DEMO_RUN_DIR/rejection.json"
printf '\nHTTP %s\n' "$status"
test "$status" -eq 401 || test "$status" -eq 403`
}

func pollWait(cfg config) float64 {
	interval, _ := time.ParseDuration(cfg.value("SOURCE_POLL_INTERVAL", "5s"))
	return interval.Seconds() + 3
}

func loadCleanup(restore string) string {
	return `load_pid=
tcp_pid=
cleanup() {
  for pid in "$load_pid" "$tcp_pid"; do
    if [ -n "$pid" ]; then
      kill -TERM "$pid" 2>/dev/null || true
      wait "$pid" || true
    fi
  done
  ` + restore + `
}
trap cleanup EXIT
trap 'exit 130' INT TERM`
}

// Curl headers/status go to stderr; only the JSON body enters the visible pipe.
func jsonRead(url, header, format string) string {
	return jsonReadQuery(url, header, format, "")
}

func jsonReadQuery(url, header, format, query string) string {
	return jsonFormat(`curl --silent --show-error --max-time 10 \
`+query+`  --dump-header /dev/stderr \
  --write-out '%{stderr}\nHTTP %{http_code}\n' \
  --header "`+header+`" \
  `+quote(url), format)
}

func jsonFormat(command, format string) string {
	if format == "raw" {
		return command
	}
	return command + " | python3 -m json.tool"
}

func (c *console) sourceEndpoint(source, credential string) (string, string) {
	key := "$BMKG_API_KEY"
	if credential == "PVMBG" {
		key = "$PVMBG_TOKEN"
	}
	if source == "BMKG" {
		return c.cfg.endpoint("BMKG_MOCK_HTTP_PORT", "8081") + "/seismic-events", "X-BMKG-Key: " + key
	}
	return c.cfg.endpoint("PVMBG_MOCK_HTTP_PORT", "8082") + "/volcanic-reports", "Authorization: Bearer " + key
}

func (c *console) clientURL(source, view string) string {
	query := "view=" + view
	if view == "attributes" {
		query = "fields=attributes"
	}
	return c.cfg.endpoint("API_HTTP_PORT", "8080") + "/v1/hazards?source=" + source + "&" + query + "&limit=5"
}

func sessionAccess(name string) string {
	return `access_token=$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["access_token"])' "$AAT_DEMO_SESSION_DIR/` + name + `")`
}

func (c *console) sessionLogin(identity, format string) string {
	key := map[string]string{"media": "MEDIA_CLIENT_SECRET", "field-team": "FIELD_TEAM_CLIENT_SECRET", "bnpb-internal": "BNPB_INTERNAL_CLIENT_SECRET"}[identity]
	payload := shellCommand("python3", "-c", `import json, os; print(json.dumps({'client_id': '`+identity+`', 'client_secret': os.environ['`+key+`']}))`)
	return `umask 077
curl --silent --show-error --fail-with-body --max-time 10 \
  --dump-header /dev/stderr \
  --header 'Content-Type: application/json' \
  --data "$(` + payload + `)" \
  --output "$AAT_DEMO_RUN_DIR/login.json" \
  ` + quote(c.cfg.endpoint("AUTH_HTTP_PORT", "8084")+"/v1/auth/login") + `
cp "$AAT_DEMO_RUN_DIR/login.json" "$AAT_DEMO_SESSION_DIR/` + identity + `.json"
rm "$AAT_DEMO_RUN_DIR/login.json"
` + jsonFormat(`cat "$AAT_DEMO_SESSION_DIR/`+identity+`.json"`, format)
}

func (c *console) sessionRefresh(format string) string {
	return `umask 077
curl --silent --show-error --fail-with-body --max-time 10 \
  --dump-header /dev/stderr \
  --header 'Content-Type: application/json' \
  --data "$(python3 -c 'import json, sys; print(json.dumps({"refresh_token":json.load(open(sys.argv[1]))["refresh_token"]}))' "$AAT_DEMO_SESSION_DIR/field-team.json")" \
  --output "$AAT_DEMO_RUN_DIR/refreshed.json" \
  ` + quote(c.cfg.endpoint("AUTH_HTTP_PORT", "8084")+"/v1/auth/refresh") + `
cp "$AAT_DEMO_SESSION_DIR/field-team.json" "$AAT_DEMO_SESSION_DIR/field-team-old.json"
cp "$AAT_DEMO_RUN_DIR/refreshed.json" "$AAT_DEMO_SESSION_DIR/field-team.json"
rm "$AAT_DEMO_RUN_DIR/refreshed.json"
` + jsonFormat(`cat "$AAT_DEMO_SESSION_DIR/field-team.json"`, format)
}

func composeCommand(cfg config, args ...string) string {
	prefix := cfg.compose()
	return `docker compose \
  ` + strings.Join([]string{
		strings.TrimPrefix(shellCommand("args", prefix[1:3]...), "args "),
		strings.TrimPrefix(shellCommand("args", prefix[3:5]...), "args "),
		strings.TrimPrefix(shellCommand("args", prefix[5:7]...), "args "),
		strings.TrimPrefix(shellCommand("args", args...), "args "),
	}, ` \
  `)
}
