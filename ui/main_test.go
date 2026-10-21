package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("DKIT_TEST_DKIT_HELPER") == "1" {
		args := strings.Join(os.Args[1:], " ")
		_, _ = fmt.Fprintln(os.Stdout, args)
		if strings.Contains(args, "fail") {
			os.Exit(3)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestDkitCommandEnvPassesUIConfigAndStatePathsOnce(t *testing.T) {
	oldRoot, oldEtc, oldState := dkitRoot, dkitEtc, dkitState
	dkitRoot, dkitEtc, dkitState = "/kit", "/config", "/state"
	defer func() {
		dkitRoot, dkitEtc, dkitState = oldRoot, oldEtc, oldState
	}()

	values := map[string][]string{}
	for _, item := range dkitCommandEnv("DKIT_YES=1") {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = append(values[key], value)
		}
	}

	for key, want := range map[string]string{
		"DKIT_ROOT": "/kit", "DKIT_ETC": "/config", "DKIT_STATE": "/state",
		"DKIT_CONFIG_DIR": "/config", "DKIT_STATE_DIR": "/state", "DKIT_YES": "1",
	} {
		got := values[key]
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s environment values = %#v, want exactly %q", key, got, want)
		}
	}
}

func TestJobStoreRunsEveryCommandInBatchAndReportsFailures(t *testing.T) {
	oldState, oldBin := dkitState, dkitBin
	dkitState = t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dkitBin = executable
	t.Setenv("DKIT_TEST_DKIT_HELPER", "1")
	defer func() {
		dkitState, dkitBin = oldState, oldBin
	}()

	store := &jobStore{jobs: map[string]*Job{}}
	id, err := store.startSequence("", "batch", [][]string{{"first"}, {"fail"}, {"last"}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var job Job
	for time.Now().Before(deadline) {
		job, _ = store.get(id)
		if !job.Running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job.Running || job.Exit != 1 {
		t.Fatalf("batch job = %#v, want completed with failure exit code", job)
	}
	log, err := os.ReadFile(filepath.Join(jobDir(), id+".log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"$ dkit first", "$ dkit fail", "$ dkit last", "first\n", "fail\n", "last\n"} {
		if !strings.Contains(string(log), expected) {
			t.Errorf("batch log %q does not contain %q", log, expected)
		}
	}
}
