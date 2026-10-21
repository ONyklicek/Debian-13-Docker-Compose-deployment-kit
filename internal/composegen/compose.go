package composegen

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/buildinfo"
)

func Render(configDir, stateDir, name string) ([]byte, error) {
	return RenderForContext(configDir, stateDir, name, "")
}

func RenderForContext(configDir, stateDir, name, dockerContext string) ([]byte, error) {
	return renderForContext(configDir, stateDir, name, dockerContext, nil)
}

func RenderBlueGreen(configDir, stateDir, name, blueImage, greenImage string) ([]byte, error) {
	images := map[string]string{"blue": blueImage, "green": greenImage}
	for _, slot := range []string{"blue", "green"} {
		image := images[slot]
		if strings.TrimSpace(image) == "" || strings.TrimSpace(image) != image || strings.IndexFunc(image, func(r rune) bool {
			return r <= ' ' || r == 0x7f || r == '$'
		}) >= 0 {
			return nil, fmt.Errorf("app %q has an invalid image for %s slot", name, slot)
		}
	}
	return renderForContext(configDir, stateDir, name, "", images)
}

func renderForContext(configDir, stateDir, name, dockerContext string, slotImages map[string]string) ([]byte, error) {
	if stateDir == "" {
		return nil, fmt.Errorf("state directory is not configured")
	}
	app, err := appconfig.Read(configDir, name)
	if err != nil {
		return nil, err
	}
	if !app.Valid {
		return nil, fmt.Errorf("app %q configuration is invalid: %s", name, strings.Join(app.Errors, "; "))
	}
	values := app.Values
	image := values["IMAGE"]
	if slotImages != nil {
		if values["SOURCE"] != "git" && values["SOURCE"] != "image" {
			return nil, fmt.Errorf("app %q uses unsupported SOURCE=%s", name, values["SOURCE"])
		}
		image = slotImages["blue"]
	} else if values["SOURCE"] == "git" {
		var info buildinfo.Info
		path := filepath.Join(stateDir, "apps", name, "build.json")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("app %q has no portable Git build; run `dkit app build %s` first: %w", name, name, err)
		}
		if err := json.Unmarshal(data, &info); err != nil {
			return nil, fmt.Errorf("parse Git build metadata for app %q: %w", name, err)
		}
		if dockerContext != "" && info.DockerContext != dockerContext {
			if info.DockerContext == "" {
				return nil, fmt.Errorf("app %q build metadata has no Docker context; run `dkit app build %s` again", name, name)
			}
			return nil, fmt.Errorf("app %q was built for Docker context %q, not selected context %q; run `dkit app build %s` again", name, info.DockerContext, dockerContext, name)
		}
		env, err := appconfig.ReadEnv(configDir, name)
		if err != nil {
			return nil, err
		}
		runtimeEnv := map[string]string{}
		runtimePath := filepath.Join(stateDir, "apps", name, "runtime.env")
		if data, err := os.ReadFile(runtimePath); err == nil {
			runtimeEnv, err = appconfig.ParseEnv(data)
			if err != nil {
				return nil, fmt.Errorf("parse app %q runtime environment: %w", name, err)
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read app %q runtime environment: %w", name, err)
		}
		fingerprint, err := buildinfo.Fingerprint(app, env, runtimeEnv)
		if err != nil {
			return nil, fmt.Errorf("fingerprint app %q build settings: %w", name, err)
		}
		if info.Fingerprint != fingerprint {
			return nil, fmt.Errorf("app %q build is stale for current source/build settings; run `dkit app build %s` again", name, name)
		}
		image = info.Image
	} else if values["SOURCE"] != "image" {
		return nil, fmt.Errorf("app %q uses unsupported SOURCE=%s", name, values["SOURCE"])
	}
	for _, unsupported := range []struct{ key, label string }{
		{"SHARED_SERVICES", "shared services"},
		{"S3_DOMAIN", "public S3 routing"},
		{"WEB_CMD", "custom web commands"},
	} {
		if strings.TrimSpace(values[unsupported.key]) != "" {
			return nil, fmt.Errorf("app %q sets %s; portable Compose rendering does not yet support %s", name, unsupported.key, unsupported.label)
		}
	}
	persist := strings.Fields(values["PERSIST"])
	if len(persist) > 0 && values["PERSIST_OWNER"] != "" {
		return nil, fmt.Errorf("app %q sets PERSIST_OWNER; portable named volumes do not yet support ownership changes", name)
	}
	workers := strings.Fields(strings.ReplaceAll(values["WORKERS"], ",", " "))
	if slotImages != nil {
		if len(workers) > 0 {
			return nil, fmt.Errorf("app %q configures workers; blue/green Compose rendering does not yet support worker version coordination", name)
		}
		if values["PUBLISH_PORT"] != "" {
			return nil, fmt.Errorf("app %q configures PUBLISH_PORT; blue/green slots cannot bind the same host port", name)
		}
	}
	healthPath := values["HEALTH_PATH"]
	if healthPath == "" {
		healthPath = "/"
	}
	healthTimeout := 90
	if values["HEALTH_TIMEOUT"] != "" {
		parsed, err := strconv.Atoi(values["HEALTH_TIMEOUT"])
		if err != nil || parsed < 1 || parsed > 240 {
			return nil, fmt.Errorf("app %q HEALTH_TIMEOUT must be an integer from 1 to 240 seconds for portable readiness checks", name)
		}
		healthTimeout = parsed
	}
	for _, service := range app.Services {
		if service == "s3" {
			return nil, fmt.Errorf("app %q requests S3; portable Compose rendering does not yet support S3 service provisioning", name)
		}
	}
	if strings.TrimSpace(image) == "" || strings.IndexFunc(image, func(r rune) bool {
		return r <= ' ' || r == 0x7f || r == '$'
	}) >= 0 {
		return nil, fmt.Errorf("app %q has an invalid IMAGE value", name)
	}

	appPath, err := appconfig.AppFilePath(configDir, name)
	if err != nil {
		return nil, err
	}
	envPath := filepath.Join(filepath.Dir(appPath), "env")
	envInfo, err := os.Lstat(envPath)
	if err == nil && !envInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("app %q environment %s is not a regular file", name, envPath)
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect app %q environment %s: %w", name, envPath, err)
	}
	if _, err := appconfig.ReadEnv(configDir, name); err != nil {
		return nil, err
	}
	if err := validateServiceFiles(configDir, stateDir, name); err != nil {
		return nil, err
	}

	port := values["WEB_PORT"]
	if port == "" {
		port = "8080"
	}
	public := values["PUBLIC"] != "0"
	stateAppDir := filepath.Join(stateDir, "apps", name)
	runtimePath := filepath.Join(stateAppDir, "runtime.env")
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by dkit for app %s. Review before running Compose.\n", name)
	fmt.Fprintf(&b, "name: %s\nservices:\n", yamlString("dkit-"+name))
	if slotImages == nil {
		writeWebService(&b, "app", image, "", name, port, values["PUBLISH_PORT"], values, app.Services, persist, filepath.ToSlash(runtimePath), envInfo != nil, filepath.ToSlash(envPath), public)
	} else {
		for _, slot := range []string{"blue", "green"} {
			writeWebService(&b, "web-"+slot, slotImages[slot], slot, name, port, "", values, app.Services, persist, filepath.ToSlash(runtimePath), envInfo != nil, filepath.ToSlash(envPath), public)
		}
	}
	for _, service := range app.Services {
		writeService(&b, service, dbName(name, values), dbUser(name, values))
	}
	for _, worker := range workers {
		workerPath := values["WORKER_"+worker+"_PATH"]
		workerPort := values["WORKER_"+worker+"_PORT"]
		writeWorker(&b, worker, values, app.Services, persist, image, filepath.ToSlash(runtimePath), envInfo != nil, filepath.ToSlash(envPath), public && workerPath != "")
		if public && workerPath != "" {
			writeTraefikLabels(&b, name, app.Domains, worker, worker, workerPort, workerPath, "10000", "")
		}
	}
	if slotImages == nil {
		writeHTTPHealthProbe(&b, port, healthPath, healthTimeout)
	} else {
		for _, slot := range []string{"blue", "green"} {
			writeHTTPHealthProbeForService(&b, "health-probe-"+slot, "web-"+slot, slot, port, healthPath, healthTimeout)
		}
	}
	fmt.Fprintln(&b, "networks:")
	fmt.Fprintln(&b, "  backend:")
	fmt.Fprintln(&b, "    internal: true")
	if public {
		fmt.Fprintln(&b, "  edge:")
		fmt.Fprintln(&b, "    external: true")
		fmt.Fprintln(&b, "    name: dkit_edge")
	}
	volumeNames := make(map[string]bool)
	for _, service := range app.Services {
		if service == "mariadb" || service == "postgres" || service == "redis" {
			volumeNames[service+"_data"] = true
		}
	}
	for _, entry := range persist {
		persistName := strings.SplitN(entry, ":", 2)[0]
		volumeNames["persist_"+persistName] = true
	}
	if len(volumeNames) > 0 {
		fmt.Fprintln(&b, "volumes:")
		for _, service := range []string{"mariadb", "postgres", "redis"} {
			if volumeNames[service+"_data"] {
				fmt.Fprintf(&b, "  %s_data: {}\n", service)
			}
		}
		for _, entry := range persist {
			persistName := strings.SplitN(entry, ":", 2)[0]
			fmt.Fprintf(&b, "  persist_%s: {}\n", persistName)
		}
	}
	return []byte(b.String()), nil
}

func writeWebService(b *strings.Builder, service, image, slot, app, port, publishPort string, values map[string]string, dependencies, persist []string, runtimePath string, hasAppEnv bool, appEnvPath string, public bool) {
	fmt.Fprintf(b, "  %s:\n    image: %s\n    restart: unless-stopped\n    init: true\n    security_opt:\n      - no-new-privileges:true\n    expose:\n      - %s\n",
		service, yamlString(image), yamlString(port))
	if publishPort != "" {
		fmt.Fprintf(b, "    ports:\n      - %s\n", yamlString("127.0.0.1:"+publishPort+":"+port))
	}
	if slot != "" {
		fmt.Fprintf(b, "    profiles: [%s]\n", yamlString(slot))
	}
	fmt.Fprintln(b, "    env_file:")
	fmt.Fprintf(b, "      - %s\n", yamlString(runtimePath))
	if hasAppEnv {
		fmt.Fprintf(b, "      - %s\n", yamlString(appEnvPath))
	}
	if len(dependencies) > 0 {
		fmt.Fprintln(b, "    depends_on:")
		for _, dependency := range dependencies {
			fmt.Fprintf(b, "      %s:\n        condition: service_healthy\n", dependency)
		}
	}
	if limit := values["MEMORY_LIMIT"]; limit != "" {
		fmt.Fprintf(b, "    mem_limit: %s\n", yamlString(limit))
	}
	writePersistMounts(b, persist)
	fmt.Fprintln(b, "    networks:")
	fmt.Fprintln(b, "      backend: {}")
	if public {
		fmt.Fprintln(b, "      edge:")
		if slot == "" {
			fmt.Fprintln(b, "        aliases:")
			fmt.Fprintf(b, "          - %s\n", yamlString("dkit-"+app+"-app"))
		} else {
			fmt.Fprintln(b, "        aliases:")
			fmt.Fprintf(b, "          - %s\n", yamlString("dkit-"+app+"-web-"+slot))
		}
	}
}

func writeWorker(b *strings.Builder, worker string, values map[string]string, services, persist []string, image, runtimePath string, hasAppEnv bool, appEnvPath string, publicRoute bool) {
	fmt.Fprintf(b, "  %s:\n    image: %s\n    restart: unless-stopped\n    init: true\n    security_opt:\n      - no-new-privileges:true\n",
		worker, yamlString(image))
	fmt.Fprintf(b, "    command: [\"sh\", \"-c\", %s]\n", yamlString(values["WORKER_"+worker+"_CMD"]))
	if user := values["WORKER_"+worker+"_USER"]; user != "" {
		fmt.Fprintf(b, "    user: %s\n", yamlString(user))
	}
	if port := values["WORKER_"+worker+"_PORT"]; port != "" {
		fmt.Fprintf(b, "    expose:\n      - %s\n", yamlString(port))
	}
	fmt.Fprintln(b, "    env_file:")
	fmt.Fprintf(b, "      - %s\n", yamlString(runtimePath))
	if hasAppEnv {
		fmt.Fprintf(b, "      - %s\n", yamlString(appEnvPath))
	}
	if len(services) > 0 {
		fmt.Fprintln(b, "    depends_on:")
		for _, service := range services {
			fmt.Fprintf(b, "      %s:\n        condition: service_healthy\n", service)
		}
	}
	writePersistMounts(b, persist)
	fmt.Fprintln(b, "    networks:")
	fmt.Fprintln(b, "      - backend")
	if publicRoute {
		fmt.Fprintln(b, "      - edge")
	}
}

func writeTraefikLabels(b *strings.Builder, app string, domains []string, router, service, port, path, priority, healthPath string) {
	rule := "Host(`" + strings.Join(domains, "`) || Host(`") + "`)"
	routerName := "dkit-" + app + "-" + router
	serviceName := "dkit-" + app + "-" + service
	middlewareName := routerName + "-headers"
	if path != "" {
		rule = "(" + rule + ") && PathPrefix(`" + path + "`)"
	}
	fmt.Fprintln(b, "    labels:")
	labels := []string{
		"traefik.enable=true",
		"traefik.docker.network=dkit_edge",
		"traefik.http.routers." + routerName + ".rule=" + rule,
		"traefik.http.routers." + routerName + ".entrypoints=websecure",
		"traefik.http.routers." + routerName + ".tls=true",
		"traefik.http.routers." + routerName + ".tls.certresolver=le",
		"traefik.http.routers." + routerName + ".service=" + serviceName,
		"traefik.http.routers." + routerName + ".middlewares=" + middlewareName,
		"traefik.http.middlewares." + middlewareName + ".headers.stsSeconds=31536000",
		"traefik.http.middlewares." + middlewareName + ".headers.contentTypeNosniff=true",
		"traefik.http.middlewares." + middlewareName + ".headers.referrerPolicy=strict-origin-when-cross-origin",
		"traefik.http.services." + serviceName + ".loadbalancer.server.port=" + port,
	}
	if healthPath != "" {
		labels = append(labels,
			"traefik.http.services."+serviceName+".loadbalancer.healthcheck.path="+healthPath,
			"traefik.http.services."+serviceName+".loadbalancer.healthcheck.interval=2s",
			"traefik.http.services."+serviceName+".loadbalancer.healthcheck.timeout=3s",
		)
	}
	if priority != "" {
		labels = append(labels, "traefik.http.routers."+routerName+".priority="+priority)
	}
	for _, label := range labels {
		fmt.Fprintf(b, "      - %s\n", yamlString(label))
	}
}

func writePersistMounts(b *strings.Builder, persist []string) {
	if len(persist) == 0 {
		return
	}
	fmt.Fprintln(b, "    volumes:")
	for _, entry := range persist {
		parts := strings.SplitN(entry, ":", 2)
		fmt.Fprintf(b, "      - %s\n", yamlString("persist_"+parts[0]+":"+parts[1]))
	}
}

func writeHTTPHealthProbe(b *strings.Builder, port, path string, timeout int) {
	writeHTTPHealthProbeForService(b, "health-probe", "app", "", port, path, timeout)
}

func writeHTTPHealthProbeForService(b *strings.Builder, name, service, slot, port, path string, timeout int) {
	fmt.Fprintf(b, "  %s:\n", name)
	if slot != "" {
		fmt.Fprintf(b, "    profiles: [%s]\n", yamlString(slot))
	}
	fmt.Fprintln(b, "    image: curlimages/curl:8.12.1")
	fmt.Fprintln(b, "    restart: unless-stopped")
	fmt.Fprintln(b, "    entrypoint: [\"\"]")
	fmt.Fprintln(b, "    command: [\"sh\", \"-c\", \"while true; do sleep 3600; done\"]")
	fmt.Fprintln(b, "    depends_on:")
	fmt.Fprintf(b, "      %s:\n", service)
	fmt.Fprintln(b, "        condition: service_started")
	fmt.Fprintln(b, "    healthcheck:")
	fmt.Fprintf(b, "      test: [\"CMD-SHELL\", %s]\n",
		yamlString("curl --fail --silent --show-error --max-time 2 "+shellQuote("http://"+service+":"+port+path)+" >/dev/null"))
	fmt.Fprintln(b, "      interval: 1s")
	fmt.Fprintln(b, "      timeout: 3s")
	fmt.Fprintf(b, "      retries: %d\n", timeout)
	fmt.Fprintln(b, "      start_period: 10s")
	fmt.Fprintln(b, "    networks:")
	fmt.Fprintln(b, "      - backend")
	fmt.Fprintln(b, "    security_opt:")
	fmt.Fprintln(b, "      - no-new-privileges:true")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func validateServiceFiles(configDir, stateDir, name string) error {
	appPath, err := appconfig.AppFilePath(configDir, name)
	if err != nil {
		return err
	}
	for _, path := range []string{filepath.Join(filepath.Dir(appPath), "services.env"), filepath.Join(stateDir, "apps", name, "runtime.env")} {
		info, err := os.Lstat(path)
		if err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("app %q service configuration %s is not a regular file", name, path)
		}
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("inspect app %q service configuration %s: %w", name, path, err)
		}
		if err == nil {
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read app %q service configuration %s: %w", name, path, err)
			}
			if _, err := appconfig.ParseEnv(data); err != nil {
				return fmt.Errorf("parse app %q service configuration %s: %w", name, path, err)
			}
		}
	}
	return nil
}

func Prepare(configDir, stateDir, name string) error {
	app, err := appconfig.Read(configDir, name)
	if err != nil {
		return err
	}
	if !app.Valid {
		return fmt.Errorf("app %q configuration is invalid: %s", name, strings.Join(app.Errors, "; "))
	}
	for _, service := range app.Services {
		if service == "s3" {
			return fmt.Errorf("app %q requests S3; portable Compose rendering does not yet support S3 service provisioning", name)
		}
	}
	if err := validateServiceFiles(configDir, stateDir, name); err != nil {
		return err
	}
	appPath, err := appconfig.AppFilePath(configDir, name)
	if err != nil {
		return err
	}
	servicesPath := filepath.Join(filepath.Dir(appPath), "services.env")
	if err := validateEnvFile(servicesPath, name); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "apps", name), 0o700); err != nil {
		return fmt.Errorf("create app state directory: %w", err)
	}
	original, err := os.ReadFile(servicesPath)
	if os.IsNotExist(err) {
		original = nil
	} else if err != nil {
		return fmt.Errorf("read service credentials %s: %w", servicesPath, err)
	}
	services, err := appconfig.ParseEnv(original)
	if err != nil {
		return fmt.Errorf("parse service credentials %s: %w", servicesPath, err)
	}
	required := serviceSecrets(app.Services)
	changed := false
	var additions strings.Builder
	for _, key := range required {
		if services[key] == "" {
			secret, err := randomSecret(32)
			if err != nil {
				return err
			}
			services[key] = secret
			fmt.Fprintf(&additions, "%s=%s\n", key, secret)
			changed = true
		}
	}
	if len(required) > 0 && (changed || !fileExists(servicesPath)) {
		data := append([]byte(nil), original...)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			data = append(data, '\n')
		}
		data = append(data, additions.String()...)
		if err := atomicWrite(servicesPath, data, 0o600); err != nil {
			return err
		}
	}
	if err := writeRuntimeEnv(filepath.Join(stateDir, "apps", name, "runtime.env"), name, app, services); err != nil {
		return err
	}
	return nil
}

func writeService(b *strings.Builder, service, database, user string) {
	fmt.Fprintf(b, "  %s:\n", service)
	fmt.Fprintf(b, "    image: %s\n", yamlString(serviceImage(service)))
	fmt.Fprintln(b, "    restart: unless-stopped")
	fmt.Fprintln(b, "    networks:")
	fmt.Fprintln(b, "      - backend")
	switch service {
	case "mariadb":
		fmt.Fprintf(b, "    environment:\n      MARIADB_DATABASE: %s\n      MARIADB_USER: %s\n      MARIADB_PASSWORD: ${MARIADB_PASSWORD:?}\n      MARIADB_ROOT_PASSWORD: ${MARIADB_ROOT_PASSWORD:?}\n      MARIADB_AUTO_UPGRADE: \"1\"\n", yamlString(database), yamlString(user))
		fmt.Fprintln(b, "    volumes:")
		fmt.Fprintln(b, "      - mariadb_data:/var/lib/mysql")
		fmt.Fprintln(b, "    healthcheck:")
		fmt.Fprintln(b, "      test: [\"CMD\", \"healthcheck.sh\", \"--connect\", \"--innodb_initialized\"]")
		fmt.Fprintln(b, "      interval: 10s")
		fmt.Fprintln(b, "      timeout: 5s")
		fmt.Fprintln(b, "      retries: 30")
		fmt.Fprintln(b, "      start_period: 30s")
	case "postgres":
		fmt.Fprintf(b, "    environment:\n      POSTGRES_DB: %s\n      POSTGRES_USER: %s\n      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:?}\n", yamlString(database), yamlString(user))
		fmt.Fprintln(b, "    volumes:")
		fmt.Fprintln(b, "      - postgres_data:/var/lib/postgresql/data")
		fmt.Fprintln(b, "    healthcheck:")
		fmt.Fprintln(b, "      test: [\"CMD-SHELL\", \"pg_isready -U \\\"$$POSTGRES_USER\\\" -d \\\"$$POSTGRES_DB\\\"\"]")
		fmt.Fprintln(b, "      interval: 10s")
		fmt.Fprintln(b, "      timeout: 5s")
		fmt.Fprintln(b, "      retries: 30")
		fmt.Fprintln(b, "      start_period: 20s")
	case "redis":
		fmt.Fprintln(b, "    command: [\"sh\", \"-ec\", \"exec redis-server --appendonly yes --requirepass \\\"$$REDIS_PASSWORD\\\"\"]")
		fmt.Fprintln(b, "    environment:")
		fmt.Fprintln(b, "      REDIS_PASSWORD: ${REDIS_PASSWORD:?}")
		fmt.Fprintln(b, "      REDISCLI_AUTH: ${REDIS_PASSWORD:?}")
		fmt.Fprintln(b, "    volumes:")
		fmt.Fprintln(b, "      - redis_data:/data")
		fmt.Fprintln(b, "    healthcheck:")
		fmt.Fprintln(b, "      test: [\"CMD-SHELL\", \"redis-cli ping | grep -q PONG\"]")
		fmt.Fprintln(b, "      interval: 10s")
		fmt.Fprintln(b, "      timeout: 5s")
		fmt.Fprintln(b, "      retries: 10")
	}
	fmt.Fprintln(b, "    security_opt:")
	fmt.Fprintln(b, "      - no-new-privileges:true")
}

func serviceImage(service string) string {
	switch service {
	case "mariadb":
		return "mariadb:11.4"
	case "postgres":
		return "postgres:17-alpine"
	case "redis":
		return "redis:7.4-alpine"
	default:
		return ""
	}
}

func dbName(name string, values map[string]string) string {
	if values["DB_NAME"] != "" {
		return values["DB_NAME"]
	}
	return strings.ReplaceAll(name, "-", "_")
}

func dbUser(name string, values map[string]string) string {
	if values["DB_USER"] != "" {
		return values["DB_USER"]
	}
	return strings.ReplaceAll(name, "-", "_")
}

func serviceSecrets(services []string) []string {
	var keys []string
	for _, service := range services {
		switch service {
		case "mariadb":
			keys = append(keys, "MARIADB_ROOT_PASSWORD", "MARIADB_PASSWORD")
		case "postgres":
			keys = append(keys, "POSTGRES_PASSWORD")
		case "redis":
			keys = append(keys, "REDIS_PASSWORD")
		}
	}
	return keys
}

func writeRuntimeEnv(path, name string, app appconfig.App, secrets map[string]string) error {
	values := make(map[string]string)
	if len(app.Domains) > 0 {
		values["APP_URL"] = "https://" + app.Domains[0]
	}
	database := dbName(name, app.Values)
	user := dbUser(name, app.Values)
	var databaseURL string
	for _, service := range app.Services {
		switch service {
		case "mariadb":
			password := secrets["MARIADB_PASSWORD"]
			values["MARIADB_HOST"] = "mariadb"
			values["MARIADB_PORT"] = "3306"
			values["MARIADB_DATABASE"] = database
			values["MARIADB_USER"] = user
			values["MARIADB_PASSWORD"] = password
			if databaseURL == "" {
				databaseURL = "mysql://" + user + ":" + password + "@mariadb:3306/" + database
				values["DB_HOST"] = "mariadb"
				values["DB_PORT"] = "3306"
				values["DB_DATABASE"] = database
				values["DB_USERNAME"] = user
				values["DB_PASSWORD"] = password
			}
		case "postgres":
			password := secrets["POSTGRES_PASSWORD"]
			values["POSTGRES_HOST"] = "postgres"
			values["POSTGRES_PORT"] = "5432"
			values["POSTGRES_DB"] = database
			values["POSTGRES_USER"] = user
			values["POSTGRES_PASSWORD"] = password
			if databaseURL == "" {
				databaseURL = "postgresql://" + user + ":" + password + "@postgres:5432/" + database
				values["DB_HOST"] = "postgres"
				values["DB_PORT"] = "5432"
				values["DB_DATABASE"] = database
				values["DB_USERNAME"] = user
				values["DB_PASSWORD"] = password
			}
		case "redis":
			password := secrets["REDIS_PASSWORD"]
			values["REDIS_HOST"] = "redis"
			values["REDIS_PORT"] = "6379"
			values["REDIS_PASSWORD"] = password
			values["REDIS_URL"] = "redis://:" + password + "@redis:6379/0"
		}
	}
	if databaseURL != "" {
		values["DATABASE_URL"] = databaseURL
	}
	return atomicWrite(path, encodeRuntimeEnv(values), 0o600)
}

func encodeRuntimeEnv(values map[string]string) []byte {
	keys := []string{
		"APP_URL", "MARIADB_HOST", "MARIADB_PORT", "MARIADB_DATABASE", "MARIADB_USER", "MARIADB_PASSWORD",
		"POSTGRES_HOST", "POSTGRES_PORT", "POSTGRES_DB", "POSTGRES_USER", "POSTGRES_PASSWORD",
		"DB_HOST", "DB_PORT", "DB_DATABASE", "DB_USERNAME", "DB_PASSWORD", "DATABASE_URL",
		"REDIS_HOST", "REDIS_PORT", "REDIS_PASSWORD", "REDIS_URL",
	}
	var b strings.Builder
	for _, key := range keys {
		if value, ok := values[key]; ok {
			fmt.Fprintf(&b, "%s=%s\n", key, value)
		}
	}
	return []byte(b.String())
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func validateEnvFile(path, name string) error {
	info, err := os.Lstat(path)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect app %q service credentials %s: %w", name, path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("app %q service credentials %s is not a regular file", name, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read app %q service credentials %s: %w", name, path, err)
	}
	if _, err := appconfig.ParseEnv(data); err != nil {
		return fmt.Errorf("parse app %q service credentials %s: %w", name, path, err)
	}
	return nil
}

func randomSecret(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate service secret: %w", err)
	}
	return hex.EncodeToString(data), nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dkit-service-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary file for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", path, err)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return fmt.Errorf("set permissions on %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func Write(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create Compose output directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to replace non-regular Compose output %s", path)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect Compose output %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dkit-compose-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary Compose output: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary Compose output: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary Compose output: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("set Compose output permissions: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace Compose output %s: %w", path, err)
	}
	return nil
}

func yamlString(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}
