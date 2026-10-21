package composegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, root, appEnv, env string) {
	t.Helper()
	dir := filepath.Join(root, "apps", "demo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.env"), []byte(appEnv), 0o600); err != nil {
		t.Fatal(err)
	}
	if env != "" {
		if err := os.WriteFile(filepath.Join(dir, "env"), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

const imageApp = `DOMAINS=demo.example.com
SOURCE=image
IMAGE=ghcr.io/example/demo:1.2
WEB_PORT=3000
MEMORY_LIMIT=512m
`

func TestRenderImageAppCompose(t *testing.T) {
	configDir := t.TempDir()
	writeConfig(t, configDir, imageApp, "TOKEN=secret\n")
	got, err := Render(configDir, t.TempDir(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, want := range []string{
		`name: "dkit-demo"`,
		`image: "ghcr.io/example/demo:1.2"`,
		`expose:`,
		`- "3000"`,
		`env_file:`,
		`/apps/demo/env"`,
		`mem_limit: "512m"`,
		"no-new-privileges:true",
		`dkit-demo-app`,
		"name: dkit_edge",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Compose output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "traefik.http.routers.dkit-demo-web") {
		t.Fatalf("web route must be owned by the file provider, not Docker labels:\n%s", text)
	}
	if strings.Contains(text, "TOKEN=secret") {
		t.Fatal("Compose output leaked environment values")
	}
}

func TestRenderBlueGreenComposeUsesSlotProfilesAndSharedServices(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeConfig(t, configDir, imageApp+"SERVICES=postgres redis\nPERSIST=\"storage:/app/storage\"\n", "")
	got, err := RenderBlueGreen(configDir, stateDir, "demo", "registry.example/demo:blue", "registry.example/demo:green")
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, expected := range []string{
		`name: "dkit-demo"`,
		`  web-blue:`,
		`image: "registry.example/demo:blue"`,
		`profiles: ["blue"]`,
		`dkit-demo-web-blue`,
		`  web-green:`,
		`image: "registry.example/demo:green"`,
		`profiles: ["green"]`,
		`dkit-demo-web-green`,
		`  health-probe-blue:`,
		`http://web-blue:3000/`,
		`  health-probe-green:`,
		`http://web-green:3000/`,
		`  postgres:`,
		`  redis:`,
		`postgres_data: {}`,
		`redis_data: {}`,
		`"persist_storage:/app/storage"`,
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("blue/green Compose output missing %q:\n%s", expected, text)
		}
	}
	if strings.Count(text, "\n  postgres:\n") != 1 || strings.Count(text, "\n  redis:\n") != 1 {
		t.Fatalf("shared services must be declared only once:\n%s", text)
	}
	if strings.Contains(text, "\n  app:\n") || strings.Contains(text, "\n  health-probe:\n") {
		t.Fatalf("blue/green model unexpectedly includes single-service resources:\n%s", text)
	}
}

func TestRenderBlueGreenRejectsWorkerAndPublishedPort(t *testing.T) {
	for _, tc := range []struct {
		name, extra, want string
	}{
		{"workers", "WORKERS=queue\nWORKER_queue_CMD=run-queue\n", "worker version coordination"},
		{"published port", "PUBLISH_PORT=13000\n", "cannot bind the same host port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configDir := t.TempDir()
			writeConfig(t, configDir, imageApp+tc.extra, "")
			_, err := RenderBlueGreen(configDir, t.TempDir(), "demo", "demo:blue", "demo:green")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RenderBlueGreen() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRenderBlueGreenRejectsInvalidSlotImages(t *testing.T) {
	configDir := t.TempDir()
	writeConfig(t, configDir, imageApp, "")
	for _, tc := range []struct{ blue, green string }{
		{"", "demo:green"},
		{"demo:blue", "demo\nmalicious"},
		{"demo:$TAG", "demo:green"},
	} {
		if _, err := RenderBlueGreen(configDir, t.TempDir(), "demo", tc.blue, tc.green); err == nil {
			t.Errorf("RenderBlueGreen() accepted images %#v", tc)
		}
	}
}

func TestRenderBlueGreenComposeConfig(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker CLI is unavailable")
	}
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeConfig(t, configDir, imageApp+"SERVICES=postgres redis\n", "")
	serviceDir := filepath.Join(configDir, "apps", "demo")
	if err := os.WriteFile(filepath.Join(serviceDir, "services.env"), []byte("POSTGRES_PASSWORD=test-password\nREDIS_PASSWORD=test-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Join(stateDir, "apps", "demo")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "runtime.env"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	model, err := RenderBlueGreen(configDir, stateDir, "demo", "demo:blue", "demo:green")
	if err != nil {
		t.Fatal(err)
	}
	composePath := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(composePath, model, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(docker, "compose", "--env-file", filepath.Join(serviceDir, "services.env"), "-f", composePath, "config", "--quiet")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker compose config rejected the generated blue/green model: %v\n%s", err, output)
	}
	for _, tc := range []struct{ profile, active, inactive string }{
		{"blue", "web-blue", "web-green"},
		{"green", "web-green", "web-blue"},
	} {
		cmd := exec.Command(docker, "compose", "--profile", tc.profile, "--env-file", filepath.Join(serviceDir, "services.env"), "-f", composePath, "config", "--services")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("docker compose could not resolve %s profile: %v\n%s", tc.profile, err, output)
		}
		services := "\n" + string(output) + "\n"
		if !strings.Contains(services, "\n"+tc.active+"\n") || strings.Contains(services, "\n"+tc.inactive+"\n") {
			t.Fatalf("%s profile services = %q; want %s without %s", tc.profile, output, tc.active, tc.inactive)
		}
	}
}

func TestRenderOmitsMissingEnvFile(t *testing.T) {
	configDir := t.TempDir()
	writeConfig(t, configDir, imageApp, "")
	got, err := Render(configDir, t.TempDir(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "env_file:") ||
		strings.Contains(string(got), filepath.ToSlash(filepath.Join(configDir, "apps", "demo", "env"))) {
		t.Fatalf("Compose output should reference generated runtime env only:\n%s", got)
	}
}

func TestRenderPublishesOnlyToLoopbackWhenConfigured(t *testing.T) {
	configDir := t.TempDir()
	writeConfig(t, configDir, imageApp+"PUBLISH_PORT=13000\n", "")
	got, err := Render(configDir, t.TempDir(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, `"127.0.0.1:13000:3000"`) {
		t.Fatalf("Compose output missing loopback-only host mapping:\n%s", text)
	}
	if strings.Contains(text, `"0.0.0.0:13000:3000"`) {
		t.Fatalf("Compose output must not publish on all host interfaces:\n%s", text)
	}
}

func TestRenderRejectsUnsupportedOrUnsafeConfig(t *testing.T) {
	tests := []struct {
		name, appEnv, want string
	}{
		{"git source", "DOMAINS=demo.example.com\nSOURCE=git\nGIT_REPOSITORY=https://example.com/repo.git\n", "no portable Git build"},
		{"s3", imageApp + "SERVICES=s3\n", "S3 service provisioning"},
		{"persistence owner", imageApp + "PERSIST=data:/data\nPERSIST_OWNER=1000:1000\n", "ownership changes"},
		{"health timeout", imageApp + "HEALTH_TIMEOUT=999\n", "from 1 to 240 seconds"},
		{"unsafe image", strings.Replace(imageApp, "ghcr.io/example/demo:1.2", "image\tmalicious", 1), "invalid IMAGE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configDir := t.TempDir()
			writeConfig(t, configDir, tt.appEnv, "")
			_, err := Render(configDir, t.TempDir(), "demo")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Render error = %v, want text %q", err, tt.want)
			}
		})
	}
}

func TestRenderPublicRoutesForAppAndPathWorkers(t *testing.T) {
	configDir := t.TempDir()
	appEnv := `DOMAINS=demo.example.com, www.demo.example.com
SOURCE=image
IMAGE=ghcr.io/example/demo:1.2
WEB_PORT=3000
HEALTH_PATH=/ready
WORKERS=websocket
WORKER_websocket_CMD=node server.js
WORKER_websocket_PORT=8081
WORKER_websocket_PATH=/socket
`
	writeConfig(t, configDir, appEnv, "")
	got, err := Render(configDir, t.TempDir(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, expected := range []string{
		"traefik.http.routers.dkit-demo-websocket.rule=(Host(`demo.example.com`) || Host(`www.demo.example.com`)) \\u0026\\u0026 PathPrefix(`/socket`)",
		`traefik.http.routers.dkit-demo-websocket.priority=10000`,
		`traefik.http.services.dkit-demo-websocket.loadbalancer.server.port=8081`,
		`dkit-demo-app`,
		`name: dkit_edge`,
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("Compose output missing route setting %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, `traefik.http.services.dkit-demo-websocket.loadbalancer.healthcheck.`) {
		t.Fatal("worker route should not assume that its public path supports HTTP health checks")
	}
}

func TestRenderPublicZeroOmitsEdgeNetworkAndTraefikLabels(t *testing.T) {
	configDir := t.TempDir()
	writeConfig(t, configDir, "PUBLIC=0\nSOURCE=image\nIMAGE=ghcr.io/example/internal:1.2\n", "")
	got, err := Render(configDir, t.TempDir(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if strings.Contains(text, "traefik.") || strings.Contains(text, "dkit_edge") {
		t.Fatalf("PUBLIC=0 Compose model unexpectedly enabled public routing:\n%s", text)
	}
	if !strings.Contains(text, "internal: true") {
		t.Fatalf("PUBLIC=0 model lost its private backend network:\n%s", text)
	}
}

func TestRenderAddsHTTPHealthProbe(t *testing.T) {
	configDir := t.TempDir()
	writeConfig(t, configDir, imageApp+"HEALTH_PATH=/healthz?ready=true&check=yes\nHEALTH_TIMEOUT=60\n", "")
	got, err := Render(configDir, t.TempDir(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, expected := range []string{
		"  health-probe:",
		"image: curlimages/curl:8.12.1",
		`'http://app:3000/healthz?ready=true\u0026check=yes'`,
		"retries: 60",
		"start_period: 10s",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("Compose output missing health check %q:\n%s", expected, text)
		}
	}
}

func TestRenderPersistentVolumesForAppAndWorkers(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeConfig(t, configDir, imageApp+"PERSIST=\"storage:/var/www/storage cache:/var/www/cache\"\nWORKERS=queue\nWORKER_queue_CMD=\"php artisan queue:work\"\n", "")
	got, err := Render(configDir, stateDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, expected := range []string{
		`"persist_storage:/var/www/storage"`,
		`"persist_cache:/var/www/cache"`,
		"  persist_storage: {}",
		"  persist_cache: {}",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("Compose output missing persistent volume %q:\n%s", expected, text)
		}
	}
	if strings.Count(text, `"persist_storage:/var/www/storage"`) != 2 ||
		strings.Count(text, `"persist_cache:/var/www/cache"`) != 2 {
		t.Fatalf("app and worker should share persistent volumes:\n%s", text)
	}
}

func TestRenderWorkerServices(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeConfig(t, configDir, imageApp+"SERVICES=redis\nWORKERS=\"queue scheduler\"\nWORKER_queue_CMD=\"php artisan queue:work --tries=3\"\nWORKER_queue_USER=1000:1000\nWORKER_scheduler_CMD=\"php artisan schedule:work\"\nWORKER_scheduler_PORT=9000\n", "APP_KEY=secret\n")
	got, err := Render(configDir, stateDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, expected := range []string{
		"  queue:",
		`command: ["sh", "-c", "php artisan queue:work --tries=3"]`,
		`user: "1000:1000"`,
		"  scheduler:",
		`- "9000"`,
		"depends_on:",
		"condition: service_healthy",
		filepath.ToSlash(filepath.Join(stateDir, "apps", "demo", "runtime.env")),
		filepath.ToSlash(filepath.Join(configDir, "apps", "demo", "env")),
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("Compose output missing worker setting %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "APP_KEY=secret") {
		t.Fatal("Compose output leaked application environment values")
	}
}

func TestRenderRejectsInvalidEnvFilePath(t *testing.T) {
	configDir := t.TempDir()
	writeConfig(t, configDir, imageApp, "")
	envPath := filepath.Join(configDir, "apps", "demo", "env")
	if err := os.Mkdir(envPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(configDir, t.TempDir(), "demo"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Render error = %v, want non-regular env error", err)
	}
}

func TestRenderDatabaseAndRedisServices(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeConfig(t, configDir, imageApp+"SERVICES=\"mariadb redis\"\n", "APP_KEY=abc\n")
	got, err := Render(configDir, stateDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, expected := range []string{
		"condition: service_healthy",
		"image: \"mariadb:11.4\"",
		"image: \"redis:7.4-alpine\"",
		"MARIADB_PASSWORD: ${MARIADB_PASSWORD:?}",
		"REDIS_PASSWORD: ${REDIS_PASSWORD:?}",
		"internal: true",
		"mariadb_data:",
		"redis_data:",
		filepath.ToSlash(filepath.Join(stateDir, "apps", "demo", "runtime.env")),
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("Compose output missing %q:\n%s", expected, text)
		}
	}
	envFileIndex := strings.Index(text, "    env_file:")
	dependsOnIndex := strings.Index(text, "    depends_on:")
	appEnvIndex := strings.Index(text, filepath.ToSlash(filepath.Join(configDir, "apps", "demo", "env")))
	if envFileIndex < 0 || dependsOnIndex < 0 || appEnvIndex < envFileIndex || appEnvIndex > dependsOnIndex {
		t.Fatalf("app env_file entries are not kept in the env_file block:\n%s", text)
	}
	if strings.Contains(text, "APP_KEY=abc") {
		t.Fatal("Compose output leaked application environment values")
	}
}

func TestPrepareCreatesPersistentServiceSecretsAndRuntimeEnv(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeConfig(t, configDir, imageApp+"SERVICES=\"postgres redis\"\n", "")
	servicesPath := filepath.Join(configDir, "apps", "demo", "services.env")
	initial := []byte("# retain operator values\nCUSTOM_OPTION=keep\nPOSTGRES_PASSWORD=operator-secret\n")
	if err := os.WriteFile(servicesPath, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(configDir, stateDir, "demo"); err != nil {
		t.Fatal(err)
	}
	secrets, err := os.ReadFile(servicesPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"# retain operator values", "CUSTOM_OPTION=keep", "POSTGRES_PASSWORD=operator-secret", "REDIS_PASSWORD="} {
		if !strings.Contains(string(secrets), key) {
			t.Errorf("services.env missing %q", key)
		}
	}
	runtimePath := filepath.Join(stateDir, "apps", "demo", "runtime.env")
	runtimeEnv, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"DATABASE_URL=postgresql://", "DB_HOST=postgres", "REDIS_HOST=redis", "APP_URL=https://demo.example.com"} {
		if !strings.Contains(string(runtimeEnv), key) {
			t.Errorf("runtime.env missing %q: %s", key, runtimeEnv)
		}
	}
	if err := Prepare(configDir, stateDir, "demo"); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(servicesPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(secrets) {
		t.Fatal("Prepare rotated generated service credentials")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(servicesPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("services.env permissions = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestWriteCreatesAndReplacesComposeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "apps", "demo", "compose.yaml")
	first := []byte("name: demo\n")
	if err := Write(path, first); err != nil {
		t.Fatal(err)
	}
	second := []byte("name: changed\n")
	if err := Write(path, second); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(second) {
		t.Fatalf("written Compose = %q, want %q", got, second)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("Compose permissions = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestWriteRefusesSymlinkOrDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "output")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Write(output, []byte("name: demo\n")); err == nil {
		t.Fatal("Write replaced a directory")
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(dir, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := Write(link, []byte("name: demo\n")); err == nil {
			t.Fatal("Write replaced a symlink")
		}
	}
}
