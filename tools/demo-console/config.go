package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type config struct {
	repo, envFile, project, evidence, specDir string
	values                                    map[string]string
}

var projectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
var secretPattern = regexp.MustCompile(`(?i)secret|password|token|api_key|demo_key|private_key`)

func loadConfig(repo, envFile, evidence, specDir string) (config, error) {
	c := config{values: make(map[string]string)}
	var err error
	c.repo, err = filepath.Abs(repo)
	if err != nil {
		return c, err
	}
	if _, err := os.Stat(filepath.Join(c.repo, "docker-compose.yml")); err != nil {
		return c, errors.New("choose the repository containing docker-compose.yml with --repo")
	}
	if envFile == "" {
		envFile = filepath.Join(c.repo, ".env")
	}
	c.envFile, err = filepath.Abs(envFile)
	if err != nil {
		return c, err
	}
	f, err := os.Open(c.envFile)
	if err != nil {
		return c, fmt.Errorf("read local configuration: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return c, errors.New("configuration must contain KEY=value lines")
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			if len(value) < 2 || value[len(value)-1] != value[0] {
				return c, fmt.Errorf("unclosed quote for %s", key)
			}
			value = value[1 : len(value)-1]
		} else if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}
		// One literal configuration profile is used by Compose, HTTP and Python.
		if strings.Contains(value, "$") {
			return c, fmt.Errorf("use a literal value for %s; environment expansion is not supported", key)
		}
		c.values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return c, err
	}
	c.project = c.values["COMPOSE_PROJECT_NAME"]
	if !projectPattern.MatchString(c.project) {
		return c, errors.New("COMPOSE_PROJECT_NAME must be explicit and contain lowercase letters, digits, underscores or hyphens")
	}
	for _, key := range []string{"BMKG_API_KEY", "PVMBG_TOKEN", "AGGREGATOR_API_KEY", "CONSUMER_DEMO_KEY", "POSTGRES_PASSWORD", "NATS_TOKEN", "MEDIA_CLIENT_SECRET", "FIELD_TEAM_CLIENT_SECRET", "BNPB_INTERNAL_CLIENT_SECRET"} {
		if c.values[key] == "" {
			return c, fmt.Errorf("set %s in the local configuration", key)
		}
	}
	for key, fallback := range map[string]string{"API_HTTP_PORT": "8080", "AUTH_HTTP_PORT": "8084", "BMKG_MOCK_HTTP_PORT": "8081", "PVMBG_MOCK_HTTP_PORT": "8082", "AGGREGATOR_HTTP_PORT": "8083"} {
		port, err := strconv.Atoi(c.value(key, fallback))
		if err != nil || port < 1 || port > 65535 {
			return c, fmt.Errorf("invalid port in %s", key)
		}
	}
	version := c.value("PVMBG_SCHEMA_VERSION", "1")
	min, minErr := strconv.Atoi(c.value("PVMBG_DELAY_MIN_MS", "500"))
	max, maxErr := strconv.Atoi(c.value("PVMBG_DELAY_MAX_MS", "3000"))
	_, outageErr := strconv.ParseBool(c.value("PVMBG_OUTAGE_ENABLED", "false"))
	poll, pollErr := time.ParseDuration(c.value("SOURCE_POLL_INTERVAL", "5s"))
	if (version != "1" && version != "2") || minErr != nil || maxErr != nil || min < 0 || max < min || outageErr != nil || pollErr != nil || poll <= 0 {
		return c, errors.New("invalid source defaults: check schema, delay, outage and polling interval")
	}
	if evidence == "" {
		evidence = filepath.Join(c.repo, "docs/evidence/private/demo-console")
	}
	c.evidence, err = filepath.Abs(evidence)
	if err != nil {
		return c, err
	}
	if specDir == "" {
		specDir = filepath.Dir(c.repo)
	}
	c.specDir, err = filepath.Abs(specDir)
	return c, err
}

func (c config) value(key, fallback string) string {
	if value := c.values[key]; value != "" {
		return value
	}
	return fallback
}

func (c config) processEnv() []string {
	values := make(map[string]string)
	for _, item := range os.Environ() {
		k, v, _ := strings.Cut(item, "=")
		if !strings.HasPrefix(k, "COMPOSE_") {
			values[k] = v
		}
	}
	for k, v := range c.values {
		values[k] = v
	}
	values["COMPOSE_PROJECT_NAME"] = c.project
	values["COMPOSE_FILE"] = filepath.Join(c.repo, "docker-compose.yml")
	values["COMPOSE_ENV_FILES"] = c.envFile
	values["COMPOSE_DISABLE_ENV_FILE"] = "1"
	values["PYTHONUNBUFFERED"] = "1"
	values["API_URL"] = c.endpoint("API_HTTP_PORT", "8080")
	values["AUTH_URL"] = c.endpoint("AUTH_HTTP_PORT", "8084")
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, k := range keys {
		result = append(result, k+"="+values[k])
	}
	return result
}

func (c config) endpoint(key, fallback string) string {
	return "http://127.0.0.1:" + c.value(key, fallback)
}

func (c config) compose(args ...string) []string {
	return append([]string{"compose", "--project-name", c.project, "--env-file", c.envFile, "-f", filepath.Join(c.repo, "docker-compose.yml")}, args...)
}
