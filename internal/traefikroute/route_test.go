package traefikroute

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderWebRoute(t *testing.T) {
	got, err := Render("demo", []string{"demo.example.com", "www.demo.example.com"}, "3000", "/ready?full=1")
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, expected := range []string{
		"rule: \"Host(`demo.example.com`) || Host(`www.demo.example.com`)\"",
		"entryPoints: [websecure]",
		"middlewares: [dkit-demo-web-headers]",
		"dkit-demo-web-headers:",
		"stsSeconds: 31536000",
		"contentTypeNosniff: true",
		"referrerPolicy: strict-origin-when-cross-origin",
		`certResolver: le`,
		`url: "http://dkit-demo-app:3000"`,
		`path: "/ready?full=1"`,
		"interval: 2s",
		"timeout: 3s",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("route missing %q:\n%s", expected, text)
		}
	}
}

func TestRenderRouteForSlot(t *testing.T) {
	got, err := RenderForSlot("demo", []string{"demo.example.com"}, "3000", "/ready", "green")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `url: "http://dkit-demo-web-green:3000"`) {
		t.Fatalf("slot route does not target the green backend:\n%s", got)
	}
}

func TestRenderRouteForSlotRejectsInvalidSlot(t *testing.T) {
	if _, err := RenderForSlot("demo", []string{"demo.example.com"}, "3000", "/", "red"); err == nil {
		t.Fatal("RenderForSlot() accepted invalid slot")
	}
}

func TestRenderRejectsInvalidRouteInputs(t *testing.T) {
	for _, tc := range []struct {
		name, app, port, path string
		domains               []string
	}{
		{name: "bad name", app: "Demo", port: "8080", path: "/", domains: []string{"demo.example.com"}},
		{name: "bad domain", app: "demo", port: "8080", path: "/", domains: []string{"demo.example.com` || PathPrefix(`/"}},
		{name: "bad port", app: "demo", port: "70000", path: "/", domains: []string{"demo.example.com"}},
		{name: "bad path", app: "demo", port: "8080", path: "/ready\nrule: Host(`bad.example.com`)", domains: []string{"demo.example.com"}},
		{name: "missing domain", app: "demo", port: "8080", path: "/", domains: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Render(tc.app, tc.domains, tc.port, tc.path); err == nil {
				t.Fatal("Render unexpectedly accepted invalid input")
			}
		})
	}
}

func TestWriteReplacesRouteAndRemoveDeletesIt(t *testing.T) {
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, "traefik", "dynamic", "demo.yml")
	if err := Write(stateDir, "demo", []byte("first")); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil || string(first) != "first" {
		t.Fatalf("first route = %q, err=%v", first, err)
	}
	if err := Write(stateDir, "demo", []byte("second")); err != nil {
		t.Fatal(err)
	}
	configFiles, err := filepath.Glob(filepath.Join(filepath.Dir(path), "*.yml"))
	if err != nil || len(configFiles) != 1 || configFiles[0] != path {
		t.Fatalf("Traefik configuration files during replacement = %v, err=%v", configFiles, err)
	}
	second, err := os.ReadFile(path)
	if err != nil || string(second) != "second" {
		t.Fatalf("replaced route = %q, err=%v", second, err)
	}
	if err := Remove(stateDir, "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("route still exists after Remove: %v", err)
	}
	if err := Remove(stateDir, "demo"); err != nil {
		t.Fatalf("removing absent route should be idempotent: %v", err)
	}
}
