package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/portable"
)

func TestMain(m *testing.M) {
	executable := strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe")
	if executable == "docker" && os.Getenv("DKIT_TEST_DOCKER_HELPER") == "1" {
		os.Exit(runDockerHelper(os.Args[1:]))
	}
	if executable == "restic" && os.Getenv("DKIT_TEST_RESTIC_HELPER") == "1" {
		os.Exit(runResticHelper(os.Args[1:]))
	}
	if executable == "git" && os.Getenv("DKIT_TEST_GIT_HELPER") == "1" {
		os.Exit(runGitHelper(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func runDockerHelper(args []string) int {
	if capture := os.Getenv("DKIT_TEST_DOCKER_BUILD_CAPTURE"); capture != "" {
		result := struct {
			Args         []string `json:"args"`
			BuildKit     string   `json:"buildkit"`
			BuildEnv     string   `json:"build_env"`
			BuildEnvMode uint32   `json:"build_env_mode"`
		}{Args: args, BuildKit: os.Getenv("DOCKER_BUILDKIT")}
		for _, arg := range args {
			if strings.HasPrefix(arg, "id=buildenv,src=") {
				path := strings.TrimPrefix(arg, "id=buildenv,src=")
				data, err := os.ReadFile(path)
				if err != nil {
					return 1
				}
				result.BuildEnv = string(data)
				if info, err := os.Stat(path); err == nil {
					result.BuildEnvMode = uint32(info.Mode().Perm())
				}
			}
		}
		data, err := json.Marshal(result)
		if err != nil || os.WriteFile(capture, data, 0o600) != nil {
			return 1
		}
	}
	if capture := os.Getenv("DKIT_TEST_DOCKER_CAPTURE"); capture != "" {
		data, err := json.Marshal(args)
		if err != nil {
			return 1
		}

		if err := os.WriteFile(capture, data, 0o600); err != nil {
			return 1
		}
	}
	if capture := os.Getenv("DKIT_TEST_DOCKER_SEQUENCE"); capture != "" {
		data, err := json.Marshal(args)
		if err != nil {
			return 1
		}
		file, err := os.OpenFile(capture, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return 1
		}
		_, writeErr := file.Write(append(data, '\n'))
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			return 1
		}
	}
	if containsSequence(args, []string{"context", "ls"}) {
		_, _ = os.Stdout.WriteString(
			"{\"Name\":\"default\",\"Description\":\"\",\"DockerEndpoint\":\"unix:///var/run/docker.sock\",\"Current\":\"*\"}\n" +
				"{\"Name\":\"desktop-linux\",\"Description\":\"Docker Desktop\",\"DockerEndpoint\":\"unix:///tmp/docker.sock\",\"Current\":\"\"}\n",
		)
	} else if containsSequence(args, []string{"context", "show"}) {
		_, _ = os.Stdout.WriteString("default\n")
	} else if containsSequence(args, []string{"ps", "--all", "--format", "json"}) {
		_, _ = os.Stdout.WriteString(os.Getenv("DKIT_TEST_DOCKER_COMPOSE_PS"))
	} else if containsSequence(args, []string{"volume", "ls"}) {
		_, _ = os.Stdout.WriteString(os.Getenv("DKIT_TEST_DOCKER_VOLUME_LIST"))
	} else if containsSequence(args, []string{"run"}) {
		if capture := os.Getenv("DKIT_TEST_DOCKER_VOLUME_CAPTURE"); capture != "" {
			data, err := json.Marshal(args)
			if err != nil {
				return 1
			}
			file, err := os.OpenFile(capture, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return 1
			}
			_, writeErr := file.Write(append(data, '\n'))
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				return 1
			}
		}
		_, _ = os.Stdout.WriteString("mock-volume-archive")
	}
	if os.Getenv("DKIT_TEST_DOCKER_FAIL") == "1" {
		return 2
	}
	if match := os.Getenv("DKIT_TEST_DOCKER_FAIL_MATCH"); match != "" && strings.Contains(strings.Join(args, " "), match) {
		return 2
	}
	return 0
}

func runGitHelper(args []string) int {
	if len(args) > 0 && args[0] == "clone" {
		checkout := args[len(args)-1]
		if err := os.MkdirAll(checkout, 0o700); err != nil {
			return 1
		}
		if err := os.WriteFile(filepath.Join(checkout, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
			return 1
		}
		return 0
	}
	if len(args) > 0 && args[len(args)-1] == "HEAD" {
		_, _ = os.Stdout.WriteString("0123456789abcdef0123456789abcdef01234567\n")
	}
	return 0
}

func installDockerHelper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	name := "docker"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DKIT_TEST_DOCKER_HELPER", "1")
	return filepath.Join(dir, "docker-args.json")
}

func runResticHelper(args []string) int {
	capture := os.Getenv("DKIT_TEST_RESTIC_CAPTURE")
	if capture == "" {
		return 1
	}
	result := struct {
		Args       []string `json:"args"`
		Repository string   `json:"repository"`
		Password   string   `json:"password"`
	}{args, os.Getenv("RESTIC_REPOSITORY"), os.Getenv("RESTIC_PASSWORD")}
	data, err := json.Marshal(result)
	if err != nil {
		return 1
	}
	if err := os.WriteFile(capture, data, 0o600); err != nil {
		return 1
	}
	if os.Getenv("DKIT_TEST_RESTIC_FAIL") == "1" {
		return 2
	}
	return 0
}

func installResticHelper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	name := "restic"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DKIT_TEST_RESTIC_HELPER", "1")
	capture := filepath.Join(dir, "restic-capture.json")
	t.Setenv("DKIT_TEST_RESTIC_CAPTURE", capture)
	return capture
}

func installBuildHelpers(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"git", "docker"} {
		filename := name
		if runtime.GOOS == "windows" {
			filename += ".exe"
		}
		if err := os.WriteFile(filepath.Join(dir, filename), data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DKIT_TEST_GIT_HELPER", "1")
	t.Setenv("DKIT_TEST_DOCKER_HELPER", "1")
	capture := filepath.Join(dir, "docker-build.json")
	t.Setenv("DKIT_TEST_DOCKER_BUILD_CAPTURE", capture)
	return capture
}

func TestContextSelectionReachesDockerCLI(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	configDir := filepath.Join(t.TempDir(), "config")
	t.Setenv("DOCKER_CONTEXT", "")
	paths := portable.Paths{Config: configDir}

	if err := contextCommand([]string{"use", "desktop-linux"}, paths); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadSettings(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DockerContext != "desktop-linux" {
		t.Fatalf("saved Docker context = %q, want desktop-linux", cfg.DockerContext)
	}

	if err := dockerCommand([]string{"info"}, paths); err != nil {
		t.Fatal(err)
	}
	gotBytes, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(gotBytes, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"--context", "desktop-linux", "info"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Docker CLI arguments = %#v, want %#v", got, want)
	}

	t.Setenv("DOCKER_CONTEXT", "environment-context")
	if err := dockerCommand([]string{"info"}, paths); err != nil {
		t.Fatal(err)
	}
	gotBytes, err = os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gotBytes, &got); err != nil {
		t.Fatal(err)
	}
	want = []string{"--context", "environment-context", "info"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Docker CLI arguments with DOCKER_CONTEXT = %#v, want %#v", got, want)
	}
}

func TestBackupListUsesPortableBackupConfiguration(t *testing.T) {
	capture := installResticHelper(t)
	t.Setenv("RESTIC_PASSWORD", "outside-password")
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "backup.env"), []byte("RESTIC_REPOSITORY=C:\\backups\nRESTIC_PASSWORD=config-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := backupCommand([]string{"list", "demo"}, portable.Paths{Config: configDir}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Args       []string `json:"args"`
		Repository string   `json:"repository"`
		Password   string   `json:"password"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Args, []string{"snapshots", "--tag", "dkit:demo"}) {
		t.Fatalf("restic args = %#v", got.Args)
	}
	if got.Repository != `C:\backups` || got.Password != "config-password" {
		t.Fatalf("restic environment = repository %q, password %q", got.Repository, got.Password)
	}
}

func TestBackupRestoreStagesSelectedSnapshotWithoutTouchingDocker(t *testing.T) {
	resticCapture := installResticHelper(t)
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "backup.env"), []byte("RESTIC_REPOSITORY=/backups\nRESTIC_PASSWORD=secret\nBACKUP_HOST=portable-host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restore-stage")
	if err := backupCommand([]string{"restore", "demo", "abcd1234", "--target", target}, portable.Paths{Config: configDir}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(resticCapture)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"restore", "abcd1234", "--tag", "dkit:demo", "--host", "portable-host", "--target", target}
	if !reflect.DeepEqual(got.Args, want) {
		t.Fatalf("restic args = %#v, want %#v", got.Args, want)
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		t.Fatalf("restore staging directory not created: info=%v err=%v", info, err)
	}
}

func TestBackupRestoreRefusesExistingOrInvalidTarget(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "backup.env"), []byte("RESTIC_REPOSITORY=/backups\nRESTIC_PASSWORD=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := portable.Paths{Config: configDir}
	existing := t.TempDir()
	if err := backupCommand([]string{"restore", "demo", "--target", existing}, paths); err == nil {
		t.Fatal("restore accepted an existing target directory")
	}
	target := filepath.Join(t.TempDir(), "new-target")
	if err := backupCommand([]string{"restore", "demo", "-o", "--target", target}, paths); err == nil {
		t.Fatal("restore accepted a flag-like snapshot selector")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("invalid restore created target: stat error = %v", err)
	}
}

func TestBackupRestoreRetainsPartialStagingDirectoryOnResticFailure(t *testing.T) {
	installResticHelper(t)
	t.Setenv("DKIT_TEST_RESTIC_FAIL", "1")
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "backup.env"), []byte("RESTIC_REPOSITORY=/backups\nRESTIC_PASSWORD=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restore-stage")
	err := backupCommand([]string{"restore", "demo", "--target", target}, portable.Paths{Config: configDir})
	if err == nil || !strings.Contains(err.Error(), "partial restored files") {
		t.Fatalf("restore error = %v, want partial restore warning", err)
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("staging directory should remain after failed restic restore: info=%v err=%v", info, err)
	}
}

func TestBackupImageAppDumpsDatabaseAndUsesRestic(t *testing.T) {
	dockerCapture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", dockerCapture)
	t.Setenv("DOCKER_CONTEXT", "desktop-linux")
	resticCapture := installResticHelper(t)
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:latest\nSERVICES=postgres\n")
	appDir := filepath.Join(configDir, "apps", "demo")
	if err := os.WriteFile(filepath.Join(appDir, "services.env"), []byte("POSTGRES_PASSWORD=db-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "backup.env"), []byte("RESTIC_REPOSITORY=/backups\nRESTIC_PASSWORD=backup-secret\nBACKUP_HOST=portable-host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := portable.Paths{Config: configDir, State: stateDir}
	if err := appCommand([]string{"compose", "render", "demo", "--write"}, paths, io.Discard); err != nil {
		t.Fatal(err)
	}

	if err := backupCommand([]string{"demo"}, paths); err != nil {
		t.Fatal(err)
	}
	dockerData, err := os.ReadFile(dockerCapture)
	if err != nil {
		t.Fatal(err)
	}
	var dockerArgs []string
	if err := json.Unmarshal(dockerData, &dockerArgs); err != nil {
		t.Fatal(err)
	}
	composeFile := filepath.Join(stateDir, "apps", "demo", "compose.yaml")
	wantDockerArgs := []string{
		"--context", "desktop-linux", "compose",
		"--env-file", filepath.Join(appDir, "services.env"),
		"--project-name", "dkit-demo", "--file", composeFile,
		"exec", "-T", "postgres", "sh", "-c", dbDumpCommand("postgres"),
	}
	if !reflect.DeepEqual(dockerArgs, wantDockerArgs) {
		t.Fatalf("Docker args = %#v, want %#v", dockerArgs, wantDockerArgs)
	}
	resticData, err := os.ReadFile(resticCapture)
	if err != nil {
		t.Fatal(err)
	}
	var restic struct {
		Args       []string `json:"args"`
		Repository string   `json:"repository"`
		Password   string   `json:"password"`
	}
	if err := json.Unmarshal(resticData, &restic); err != nil {
		t.Fatal(err)
	}
	if len(restic.Args) != 7 || restic.Args[0] != "backup" || restic.Args[1] != "--tag" || restic.Args[2] != "dkit:demo" ||
		restic.Args[3] != "--host" || restic.Args[4] != "portable-host" || restic.Args[5] != appDir ||
		restic.Args[6] != filepath.Join(stateDir, "apps", "demo") {
		t.Fatalf("restic args = %#v", restic.Args)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "apps", "demo", "backup-dumps")); !os.IsNotExist(err) {
		t.Fatalf("temporary dump directory was not removed: stat error = %v", err)
	}
	if restic.Repository != "/backups" || restic.Password != "backup-secret" {
		t.Fatalf("restic environment = repository %q, password %q", restic.Repository, restic.Password)
	}
}

func TestBackupSnapshotsExistingNamedVolumes(t *testing.T) {
	dockerCapture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", dockerCapture)
	volumeCapture := filepath.Join(t.TempDir(), "volume-args.jsonl")
	t.Setenv("DKIT_TEST_DOCKER_VOLUME_CAPTURE", volumeCapture)
	t.Setenv("DKIT_TEST_DOCKER_VOLUME_LIST", "dkit-demo_postgres_data\ndkit-demo_redis_data\ndkit-demo_persist_uploads\nunrelated_volume\n")
	t.Setenv("DOCKER_CONTEXT", "desktop-linux")
	resticCapture := installResticHelper(t)
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:latest\nSERVICES=\"postgres redis\"\nPERSIST=uploads:/srv/uploads\n")
	appDir := filepath.Join(configDir, "apps", "demo")
	if err := os.WriteFile(filepath.Join(appDir, "services.env"), []byte("POSTGRES_PASSWORD=db-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "backup.env"), []byte("RESTIC_REPOSITORY=/backups\nRESTIC_PASSWORD=backup-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := portable.Paths{Config: configDir, State: stateDir}
	if err := appCommand([]string{"compose", "render", "demo", "--write"}, paths, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := backupCommand([]string{"demo"}, paths); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(volumeCapture)
	if err != nil {
		t.Fatal(err)
	}
	var got [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		got = append(got, args)
	}
	if len(got) != 3 {
		t.Fatalf("captured volume commands = %d, want 3: %#v", len(got), got)
	}
	for index, volume := range []string{"dkit-demo_postgres_data", "dkit-demo_redis_data", "dkit-demo_persist_uploads"} {
		if !containsSequence(got[index], []string{"--mount", "type=volume,src=" + volume + ",dst=/data,readonly"}) ||
			!containsSequence(got[index], []string{"busybox:1.36.1", "tar", "-C", "/data", "-czf", "-", "."}) ||
			!containsSequence(got[index], []string{"--context", "desktop-linux"}) {
			t.Errorf("volume %s was not snapshotted read-only via selected context: %#v", volume, got[index])
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "apps", "demo", "volume-snapshots")); !os.IsNotExist(err) {
		t.Fatalf("temporary volume snapshots were not removed: %v", err)
	}
	if _, err := os.Stat(resticCapture); err != nil {
		t.Fatalf("Restic was not called after successful volume snapshots: %v", err)
	}
}

func TestBackupDoesNotRunResticWhenVolumeSnapshotFails(t *testing.T) {
	installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_VOLUME_LIST", "dkit-demo_postgres_data\n")
	t.Setenv("DKIT_TEST_DOCKER_FAIL_MATCH", "run --rm")
	resticCapture := installResticHelper(t)
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:latest\nSERVICES=postgres\n")
	appDir := filepath.Join(configDir, "apps", "demo")
	if err := os.WriteFile(filepath.Join(appDir, "services.env"), []byte("POSTGRES_PASSWORD=db-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "backup.env"), []byte("RESTIC_REPOSITORY=/backups\nRESTIC_PASSWORD=backup-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := portable.Paths{Config: configDir, State: stateDir}
	if err := appCommand([]string{"compose", "render", "demo", "--write"}, paths, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := backupCommand([]string{"demo"}, paths); err == nil || !strings.Contains(err.Error(), "archive Docker volume") {
		t.Fatalf("backup error = %v, want volume archive failure", err)
	}
	if _, err := os.Stat(resticCapture); !os.IsNotExist(err) {
		t.Fatalf("Restic ran after volume archive failure: %v", err)
	}
	for _, path := range []string{
		filepath.Join(stateDir, "apps", "demo", "backup-dumps"),
		filepath.Join(stateDir, "apps", "demo", "volume-snapshots"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("temporary backup data %s remains after failure: %v", path, err)
		}
	}
}

func TestBackupRejectsMissingConfigAndUnsupportedApps(t *testing.T) {
	paths := portable.Paths{Config: t.TempDir(), State: t.TempDir()}
	if err := backupCommand([]string{"list"}, paths); err == nil {
		t.Fatal("backup list accepted missing backup.env")
	}
	writeAppConfig(t, paths.Config, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:latest\n")
	if err := os.WriteFile(filepath.Join(paths.Config, "backup.env"), []byte("RESTIC_REPOSITORY=/backups\nRESTIC_PASSWORD=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := backupCommand([]string{"demo"}, paths); err == nil {
		t.Fatal("backup accepted app without a dedicated database")
	}
}

func TestBackupDoesNotRunResticWhenDatabaseDumpFails(t *testing.T) {
	dockerCapture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", dockerCapture)
	t.Setenv("DKIT_TEST_DOCKER_FAIL_MATCH", "exec -T postgres")
	resticCapture := installResticHelper(t)
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:latest\nSERVICES=postgres\n")
	appDir := filepath.Join(configDir, "apps", "demo")
	if err := os.WriteFile(filepath.Join(appDir, "services.env"), []byte("POSTGRES_PASSWORD=db-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "backup.env"), []byte("RESTIC_REPOSITORY=/backups\nRESTIC_PASSWORD=backup-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := portable.Paths{Config: configDir, State: stateDir}
	if err := appCommand([]string{"compose", "render", "demo", "--write"}, paths, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := backupCommand([]string{"demo"}, paths); err == nil {
		t.Fatal("backup succeeded after database dump failed")
	}
	if _, err := os.Stat(resticCapture); !os.IsNotExist(err) {
		t.Fatalf("Restic helper ran after database dump failure: stat error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "apps", "demo", "backup-dumps")); !os.IsNotExist(err) {
		t.Fatalf("temporary dump directory was not removed after failure: stat error = %v", err)
	}
}

func TestGitAppBuildProducesComposeReadyImageWithoutDeployment(t *testing.T) {
	capture := installBuildHelpers(t)
	t.Setenv("DOCKER_CONTEXT", "")
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=git\nGIT_REPOSITORY=https://example.com/team/demo.git\nBUILD=dockerfile\nBUILD_ARGS=PUBLIC_KEY\n")
	if err := os.WriteFile(filepath.Join(configDir, "apps", "demo", "env"), []byte("PUBLIC_KEY=public-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveSettings(configDir, settings{DockerContext: "desktop-linux"}); err != nil {
		t.Fatal(err)
	}
	paths := portable.Paths{Config: configDir, State: stateDir}
	if err := buildApp([]string{"demo", "--ref", "feature/release-1"}, paths); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Args         []string `json:"args"`
		BuildKit     string   `json:"buildkit"`
		BuildEnv     string   `json:"build_env"`
		BuildEnvMode uint32   `json:"build_env_mode"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.BuildKit != "1" || got.BuildEnv != "PUBLIC_KEY=public-value\n" {
		t.Fatalf("BuildKit/env = %q / %q", got.BuildKit, got.BuildEnv)
	}
	if runtime.GOOS != "windows" && got.BuildEnvMode != 0o600 {
		t.Fatalf("build env file mode = %o, want 600", got.BuildEnvMode)
	}
	checks := [][]string{
		{"--context", "desktop-linux"},
		{"build", "--pull"},
		{"--build-arg", "PUBLIC_KEY=public-value"},
		{"--label", "dkit.app=demo"},
		{"--label", "dkit.revision=0123456789abcdef0123456789abcdef01234567"},
		{"--tag", "dkit/demo:0123456789ab"},
	}
	for _, check := range checks {
		if !containsSequence(got.Args, check) {
			t.Errorf("Docker build args %#v do not contain %#v", got.Args, check)
		}
	}
	var info struct {
		Image         string `json:"image"`
		Revision      string `json:"revision"`
		Ref           string `json:"ref"`
		DockerContext string `json:"docker_context"`
		Fingerprint   string `json:"fingerprint"`
	}
	buildData, err := os.ReadFile(filepath.Join(stateDir, "apps", "demo", "build.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(buildData, &info); err != nil {
		t.Fatal(err)
	}
	if info.Image != "dkit/demo:0123456789ab" || info.Revision != "0123456789abcdef0123456789abcdef01234567" ||
		info.Ref != "feature/release-1" || info.DockerContext != "desktop-linux" {
		t.Fatalf("build metadata = %+v", info)
	}
	compose, err := appCommandRenderForTest(paths, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), `image: "dkit/demo:0123456789ab"`) {
		t.Fatalf("Compose did not use built image:\n%s", compose)
	}
	if err := appCommand([]string{"compose", "render", "demo", "--write"}, paths, io.Discard); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONTEXT", "other-context")
	if _, err := appComposeProjectArgs("demo", paths); err == nil || !strings.Contains(err.Error(), `built for Docker context "desktop-linux"`) {
		t.Fatalf("Compose operation on a different context error = %v, want context mismatch", err)
	}
	t.Setenv("DOCKER_CONTEXT", "")
	if _, err := appComposeProjectArgs("demo", paths); err != nil {
		t.Fatalf("Compose operation on build context failed: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(stateDir, "apps", "demo", "builds")); err != nil || len(entries) != 0 {
		t.Fatalf("successful checkout not cleaned: entries=%v err=%v", entries, err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "apps", "demo", "env"), []byte("PUBLIC_KEY=changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := appCommandRenderForTest(paths, "demo"); err == nil || !strings.Contains(err.Error(), "build is stale") {
		t.Fatalf("render after changing build env error = %v, want stale build refusal", err)
	}
}

func containsSequence(haystack, needle []string) bool {
	for start := 0; start+len(needle) <= len(haystack); start++ {
		if reflect.DeepEqual(haystack[start:start+len(needle)], needle) {
			return true
		}
	}
	return false
}

func appCommandRenderForTest(paths portable.Paths, name string) ([]byte, error) {
	var output bytes.Buffer
	if err := appCommand([]string{"compose", "render", name}, paths, &output); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func TestComposeUsesSelectedContext(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DOCKER_CONTEXT", "")
	configDir := filepath.Join(t.TempDir(), "config")
	if err := saveSettings(configDir, settings{DockerContext: "desktop-linux"}); err != nil {
		t.Fatal(err)
	}

	args := []string{"-f", "compose.yaml", "ps", "--all"}
	if err := composeCommand(args, portable.Paths{Config: configDir}); err != nil {
		t.Fatal(err)
	}
	gotBytes, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(gotBytes, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"--context", "desktop-linux", "compose", "-f", "compose.yaml", "ps", "--all"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Compose CLI arguments = %#v, want %#v", got, want)
	}

	t.Setenv("DOCKER_CONTEXT", "remote-prod")
	if err := composeCommand([]string{"version"}, portable.Paths{Config: configDir}); err != nil {
		t.Fatal(err)
	}
	gotBytes, err = os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gotBytes, &got); err != nil {
		t.Fatal(err)
	}
	want = []string{"--context", "remote-prod", "compose", "version"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Compose CLI arguments with DOCKER_CONTEXT = %#v, want %#v", got, want)
	}
}
