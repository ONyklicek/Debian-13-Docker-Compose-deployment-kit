// dkit web UI: a thin front-end over the dkit CLI. Every action runs `dkit …` as a background
// job whose output is streamed to the browser; configuration files are edited in place and
// validated with `dkit app check`. Authentication is a single admin token (/etc/dkit/ui.token).
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed templates/*.html static/*
var assets embed.FS

var (
	dkitRoot  = env("DKIT_ROOT", "/opt/dkit")
	dkitEtc   = env("DKIT_ETC", "/etc/dkit")
	dkitState = env("DKIT_STATE", "/var/lib/dkit")
	listen    = env("DKIT_UI_LISTEN", ":9000")
	dkitBin   = env("DKIT_BIN", filepath.Join(dkitRoot, "bin", "dkit"))
	uiEngine  = env("DKIT_UI_ENGINE", "legacy")

	appNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	refRe     = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
	jobIDRe   = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}-[0-9a-f]{6}$`)
	editable  = map[string]os.FileMode{"app.env": 0o640, "env": 0o600, "compose.override.yaml": 0o600}
	services  = []string{"mariadb", "postgres", "redis", "s3"}
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ---------- authentication ----------

func readToken() (string, error) {
	b, err := os.ReadFile(filepath.Join(dkitEtc, "ui.token"))
	if err != nil {
		return "", err
	}
	t := strings.TrimSpace(string(b))
	if len(t) < 16 {
		return "", errors.New("token too short")
	}
	return t, nil
}

// Derived values change when the token is rotated, which logs everyone out.
func derive(purpose string) string {
	t, err := readToken()
	if err != nil {
		return ""
	}
	m := hmac.New(sha256.New, []byte(t))
	m.Write([]byte(purpose))
	return hex.EncodeToString(m.Sum(nil))
}

func equal(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func authed(r *http.Request) bool {
	c, err := r.Cookie("dkit_session")
	return err == nil && equal(c.Value, derive("session-v1"))
}

func csrfOK(r *http.Request) bool { return equal(r.FormValue("csrf"), derive("csrf-v1")) }

func secure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// ---------- jobs ----------

type Job struct {
	ID      string    `json:"id"`
	App     string    `json:"app"`
	Title   string    `json:"title"`
	Args    []string  `json:"args"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitempty"`
	Exit    int       `json:"exit"`
	Running bool      `json:"running"`
}

type jobStore struct {
	mu   sync.Mutex
	jobs map[string]*Job
}

var jobs = &jobStore{jobs: map[string]*Job{}}

func jobDir() string { return filepath.Join(dkitState, "jobs") }

func (s *jobStore) load() {
	files, _ := filepath.Glob(filepath.Join(jobDir(), "*.json"))
	for _, f := range files {
		var j Job
		if b, err := os.ReadFile(f); err == nil && json.Unmarshal(b, &j) == nil && jobIDRe.MatchString(j.ID) {
			if j.Running { // UI restarted while the job ran; its outcome is unknown.
				j.Running, j.Exit = false, -1
			}
			s.jobs[j.ID] = &j
		}
	}
}

func (s *jobStore) save(j *Job) {
	b, _ := json.Marshal(j)
	_ = os.WriteFile(filepath.Join(jobDir(), j.ID+".json"), b, 0o600)
}

func (s *jobStore) get(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

func (s *jobStore) list(app string, limit int) []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Job{}
	for _, j := range s.jobs {
		if app == "" || j.App == app {
			out = append(out, *j)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Started.After(out[b].Started) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *jobStore) start(app, title string, args ...string) (string, error) {
	return s.startSequence(app, title, [][]string{args})
}

func (s *jobStore) startSequence(app, title string, commands [][]string) (string, error) {
	if len(commands) == 0 {
		return "", errors.New("job has no commands")
	}
	if _, err := exec.LookPath(dkitBin); err != nil {
		return "", fmt.Errorf("find dkit CLI %q: %w", dkitBin, err)
	}
	if err := os.MkdirAll(jobDir(), 0o700); err != nil {
		return "", err
	}
	rb := make([]byte, 3)
	_, _ = rand.Read(rb)
	j := &Job{ID: time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(rb),
		App: app, Title: title, Args: append([]string(nil), commands[0]...), Started: time.Now(), Running: true}
	logf, err := os.OpenFile(filepath.Join(jobDir(), j.ID+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.jobs[j.ID] = j
	s.save(j)
	s.mu.Unlock()
	go func() {
		code := 0
		for _, args := range commands {
			fmt.Fprintf(logf, "$ dkit %s\n", strings.Join(args, " "))
			cmd := exec.Command(dkitBin, args...)
			cmd.Env = dkitCommandEnv("DKIT_YES=1", "TERM=dumb")
			cmd.Stdout, cmd.Stderr = logf, logf
			err := cmd.Run()
			commandCode := 0
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				commandCode = ee.ExitCode()
			} else if err != nil {
				commandCode = -1
			}
			if err != nil {
				fmt.Fprintf(logf, "[chyba: %v]\n", err)
			}
			fmt.Fprintf(logf, "[ukončeno s kódem %d]\n", commandCode)
			if commandCode != 0 {
				code = 1
			}
		}
		fmt.Fprintf(logf, "\n[úloha ukončena s kódem %d]\n", code)
		logf.Close()
		s.mu.Lock()
		j.Running, j.Exit, j.Ended = false, code, time.Now()
		s.save(j)
		s.mu.Unlock()
	}()
	s.prune()
	return j.ID, nil
}

// Keeps the newest 200 job records.
func (s *jobStore) prune() {
	all := s.list("", 1<<30)
	if len(all) <= 200 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range all[200:] {
		if j.Running {
			continue
		}
		delete(s.jobs, j.ID)
		os.Remove(filepath.Join(jobDir(), j.ID+".json"))
		os.Remove(filepath.Join(jobDir(), j.ID+".log"))
	}
}

// ---------- dkit CLI helpers ----------

type Container struct {
	Service string `json:"service"`
	State   string `json:"state"`
	Status  string `json:"status"`
}

type App struct {
	Name           string      `json:"name"`
	Error          string      `json:"error"`
	Valid          bool        `json:"valid"`
	Errors         []string    `json:"errors"`
	RuntimeChecked bool        `json:"runtime_checked"`
	ConfigValid    bool        `json:"config_valid"`
	Domains        []string    `json:"domains"`
	Source         string      `json:"source"`
	Repo           string      `json:"repo"`
	Branch         string      `json:"branch"`
	Image          string      `json:"image"`
	Build          string      `json:"build"`
	Services       []string    `json:"services"`
	Shared         []string    `json:"shared"`
	Workers        []string    `json:"workers"`
	ActiveSlot     string      `json:"active_slot"`
	Revision       string      `json:"revision"`
	Previous       string      `json:"previous_revision"`
	DeployedAt     string      `json:"deployed_at"`
	Containers     []Container `json:"containers"`
}

func (a App) Running() int {
	n := 0
	for _, c := range a.Containers {
		if c.State == "running" {
			n++
		}
	}
	return n
}

func runDkit(timeout time.Duration, args ...string) ([]byte, error) {
	return runDkitIn("", timeout, args...)
}

// runDkitIn runs the CLI with the given stdin (used for values that must not appear in argv).
func runDkitIn(stdin string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, dkitBin, args...)
	cmd.Env = dkitCommandEnv("DKIT_YES=1", "TERM=dumb")
	cmd.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func dkitCommandEnv(extra ...string) []string {
	env := append([]string(nil), os.Environ()...)
	for _, value := range append([]string{
		"DKIT_ROOT=" + dkitRoot,
		"DKIT_ETC=" + dkitEtc,
		"DKIT_STATE=" + dkitState,
		"DKIT_CONFIG_DIR=" + dkitEtc,
		"DKIT_STATE_DIR=" + dkitState,
	}, extra...) {
		key, _, _ := strings.Cut(value, "=")
		prefix := key + "="
		filtered := env[:0]
		for _, current := range env {
			if !strings.HasPrefix(current, prefix) {
				filtered = append(filtered, current)
			}
		}
		env = append(filtered, value)
	}
	return env
}

func portableUI() bool { return uiEngine == "portable" }

func listApps() ([]App, error) {
	out, err := runDkit(30*time.Second, "apps", "--json")
	if err != nil {
		return nil, err
	}
	var apps []App
	if err := json.Unmarshal(out, &apps); err != nil {
		return nil, err
	}
	for i := range apps {
		if apps[i].Error == "" && len(apps[i].Errors) != 0 {
			apps[i].Error = strings.Join(apps[i].Errors, "; ")
		}
		if portableUI() {
			details, detailErr := getApp(apps[i].Name)
			if detailErr != nil {
				apps[i].Error = detailErr.Error()
				continue
			}
			apps[i] = details
		}
	}
	return apps, nil
}

func getApp(name string) (App, error) {
	var a App
	out, err := runDkit(30*time.Second, "app", "show", name, "--json")
	if err != nil {
		return a, err
	}
	if err := json.Unmarshal(out, &a); err != nil {
		return a, err
	}
	if portableUI() {
		composePath := filepath.Join(dkitState, "apps", name, "compose.yaml")
		info, err := os.Lstat(composePath)
		if os.IsNotExist(err) {
			return a, nil
		}
		if err != nil {
			return a, fmt.Errorf("inspect Compose model for app %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return a, fmt.Errorf("Compose model for app %s is not a regular file", name)
		}
		a.RuntimeChecked = true
		ps, err := runDkit(30*time.Second, "app", "compose", "ps", name, "--json")
		if err != nil {
			a.Error = "Runtime status unavailable: " + err.Error()
			return a, nil
		}
		var statuses []struct {
			Service string `json:"Service"`
			State   string `json:"State"`
			Status  string `json:"Status"`
		}
		if err := json.Unmarshal(ps, &statuses); err != nil {
			return a, fmt.Errorf("parse Compose status for app %s: %w", name, err)
		}
		for _, status := range statuses {
			a.Containers = append(a.Containers, Container{Service: status.Service, State: status.State, Status: status.Status})
		}
	}
	return a, nil
}

func traefikState() string {
	if portableUI() {
		out, err := runDkit(10*time.Second, "docker", "inspect", "-f", "{{.State.Status}}", "dkit-traefik")
		if err != nil {
			return "nenalezen"
		}
		return strings.TrimSpace(string(out))
	}
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Status}}", "dkit-traefik").Output()
	if err != nil {
		return "nenalezen"
	}
	return strings.TrimSpace(string(out))
}

// ---------- HTTP ----------

var tpl = template.Must(template.New("").Funcs(template.FuncMap{
	"short": func(s string) string {
		if len(s) > 12 {
			return s[:12]
		}
		return s
	},
	"join":        strings.Join,
	"list":        func(s ...string) []string { return s },
	"actionTitle": func(a string) string { return actions[a] },
	"has": func(list []string, v string) bool {
		for _, x := range list {
			if x == v {
				return true
			}
		}
		return false
	},
	"fields":   strings.Fields,
	"split":    func(s, sep string) []string { return strings.Split(s, sep) },
	"replace":  strings.ReplaceAll,
	"contains": strings.Contains,
	"ago": func(t time.Time) string {
		d := time.Since(t).Round(time.Second)
		switch {
		case d < time.Minute:
			return fmt.Sprintf("před %d s", int(d.Seconds()))
		case d < time.Hour:
			return fmt.Sprintf("před %d min", int(d.Minutes()))
		case d < 48*time.Hour:
			return fmt.Sprintf("před %d h", int(d.Hours()))
		}
		return t.Format("2. 1. 2006 15:04")
	},
}).ParseFS(assets, "templates/*.html"))

type page struct {
	Title   string
	CSRF    string
	Flash   string
	Error   string
	Data    any
	Section string
}

func render(w http.ResponseWriter, r *http.Request, name string, p page) {
	p.CSRF = derive("csrf-v1")
	if p.Flash == "" {
		p.Flash = r.URL.Query().Get("ok")
	}
	if p.Error == "" {
		p.Error = r.URL.Query().Get("err")
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, name, p); err != nil {
		log.Printf("template %s: %v", name, err)
		http.Error(w, "chyba šablony", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func redirect(w http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func withAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			redirect(w, r, "/login")
			return
		}
		if r.Method == http.MethodPost && !csrfOK(r) {
			http.Error(w, "neplatný CSRF token — obnovte stránku", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		render(w, r, "login.html", page{Title: "Přihlášení"})
		return
	}
	t, err := readToken()
	if err != nil {
		render(w, r, "login.html", page{Title: "Přihlášení", Error: "Token není nastaven (dkit ui token)."})
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(r.FormValue("token"))), []byte(t)) != 1 {
		time.Sleep(1500 * time.Millisecond)
		render(w, r, "login.html", page{Title: "Přihlášení", Error: "Neplatný token."})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "dkit_session", Value: derive("session-v1"), Path: "/",
		HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteStrictMode, MaxAge: 7 * 24 * 3600})
	redirect(w, r, "/")
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "dkit_session", Value: "", Path: "/", MaxAge: -1})
	redirect(w, r, "/login")
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	apps, err := listApps()
	p := page{Title: "Aplikace", Section: "apps", Data: map[string]any{
		"Apps": apps, "Traefik": traefikState(), "Jobs": jobs.list("", 8), "UIEngine": uiEngine,
	}}
	if err != nil {
		p.Error = err.Error()
	}
	render(w, r, "dashboard.html", p)
}

func handleNew(w http.ResponseWriter, r *http.Request) {
	presets, err := listPresets()
	data := map[string]any{"Presets": presets, "Services": services}
	if err != nil {
		render(w, r, "new.html", page{Title: "Nová aplikace", Section: "new", Data: data, Error: err.Error()})
		return
	}
	if r.Method == http.MethodGet {
		render(w, r, "new.html", page{Title: "Nová aplikace", Section: "new", Data: data})
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	preset := r.FormValue("preset")
	if !appNameRe.MatchString(name) {
		render(w, r, "new.html", page{Title: "Nová aplikace", Section: "new", Data: data, Error: "Neplatné jméno (a-z, 0-9, pomlčka)."})
		return
	}
	args := []string{"app", "create", name, "--preset", preset, "--yes"}
	for _, d := range strings.FieldsFunc(r.FormValue("domains"), func(c rune) bool { return c == ',' || c == ' ' || c == '\n' }) {
		args = append(args, "--domain", strings.ToLower(strings.TrimSpace(d)))
	}
	if v := strings.TrimSpace(r.FormValue("repo")); v != "" {
		args = append(args, "--repo", v)
	}
	if v := strings.TrimSpace(r.FormValue("branch")); v != "" {
		args = append(args, "--branch", v)
	}
	if v := strings.TrimSpace(r.FormValue("image")); v != "" {
		args = append(args, "--image", v)
	}
	if v := strings.TrimSpace(r.FormValue("port")); v != "" {
		args = append(args, "--port", v)
	}
	if r.FormValue("custom_services") == "1" {
		args = append(args, "--services", strings.Join(r.Form["services"], " "))
	}
	out, err := runDkit(2*time.Minute, args...)
	if err != nil {
		render(w, r, "new.html", page{Title: "Nová aplikace", Section: "new", Data: data, Error: err.Error() + "\n" + string(out)})
		return
	}
	redirect(w, r, "/apps/"+name+"?ok=Aplikace+vytvořena.+Zkontrolujte+konfiguraci+a+spusťte+deploy.")
}

func appFile(name, file string) string { return filepath.Join(dkitEtc, "apps", name, file) }

func handleApp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !appNameRe.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	a, err := getApp(name)
	if err != nil {
		render(w, r, "error.html", page{Title: name, Error: err.Error()})
		return
	}
	files := map[string]string{}
	for f := range editable {
		b, readErr := os.ReadFile(appFile(name, f))
		if readErr != nil && !os.IsNotExist(readErr) {
			render(w, r, "error.html", page{Title: name, Error: readErr.Error()})
			return
		}
		files[f] = string(b)
	}
	cfg, err := getJSONMap("app", "get", name, "--json")
	if err != nil {
		render(w, r, "error.html", page{Title: name, Error: err.Error()})
		return
	}
	env, err := getJSONMap("env", "list", name, "--json")
	if err != nil {
		render(w, r, "error.html", page{Title: name, Error: err.Error()})
		return
	}
	options, err := buildOptions()
	if err != nil {
		render(w, r, "error.html", page{Title: name, Error: err.Error()})
		return
	}
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "overview"
	}
	render(w, r, "app.html", page{Title: name, Section: "apps", Data: map[string]any{
		"App": a, "Files": files, "Jobs": jobs.list(name, 10), "Tab": tab,
		"Groups": appGroups, "Config": cfg, "BuildOptions": options, "Workers": workersOf(cfg),
		"Env":       envVars(env),
		"Shareable": []string{"mariadb", "postgres", "s3"}, "UIEngine": uiEngine,
	}})
}

func handleSave(w http.ResponseWriter, r *http.Request) {
	name, file := r.PathValue("name"), r.PathValue("file")
	mode, ok := editable[file]
	if !appNameRe.MatchString(name) || !ok {
		http.NotFound(w, r)
		return
	}
	path := appFile(name, file)
	old, oldErr := os.ReadFile(path)
	if oldErr != nil && !os.IsNotExist(oldErr) {
		redirect(w, r, "/apps/"+name+"?tab=advanced&err="+urlq("Načtení původního souboru selhalo: "+oldErr.Error()))
		return
	}
	content := strings.ReplaceAll(r.FormValue("content"), "\r\n", "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := writeAtomic(path, []byte(content), mode); err != nil {
		redirect(w, r, "/apps/"+name+"?tab=advanced&err="+urlq("Uložení selhalo: "+err.Error()))
		return
	}
	checkArgs := []string{"app", "check", name}
	if file == "env" {
		checkArgs = []string{"env", "list", name, "--json"}
	} else if file == "compose.override.yaml" {
		if _, err := runDkit(2*time.Minute, "app", "compose", "render", name, "--write"); err == nil {
			checkArgs = []string{"app", "compose", "config", name}
		} else {
			checkArgs = nil
			out := []byte(err.Error())
			if restoreErr := restoreEditedFile(path, old, oldErr, mode); restoreErr != nil {
				redirect(w, r, "/apps/"+name+"?tab=advanced&err="+urlq("Generování Compose modelu selhalo a původní soubor se nepodařilo obnovit: "+restoreErr.Error()))
				return
			}
			redirect(w, r, "/apps/"+name+"?tab=advanced&err="+urlq("Compose model se nepodařilo vygenerovat; změna byla vrácena: "+string(out)))
			return
		}
	}
	if out, err := runDkit(2*time.Minute, checkArgs...); err != nil {
		restoreErr := restoreEditedFile(path, old, oldErr, mode)
		if restoreErr != nil {
			redirect(w, r, "/apps/"+name+"?tab=advanced&err="+urlq("Kontrola selhala a původní soubor se nepodařilo obnovit: "+restoreErr.Error()))
			return
		}
		redirect(w, r, "/apps/"+name+"?tab=advanced&err="+urlq("Konfigurace neprošla kontrolou, změna vrácena: "+err.Error()+" "+string(out)))
		return
	}
	redirect(w, r, "/apps/"+name+"?tab=advanced&ok="+urlq(file+" uložen. Projeví se po „Použít konfiguraci“ nebo dalším deployi."))
}

func restoreEditedFile(path string, old []byte, oldErr error, mode os.FileMode) error {
	if oldErr == nil {
		return writeAtomic(path, old, mode)
	}
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ui.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func urlq(s string) string {
	if len(s) > 1500 {
		s = s[:1500] + "…"
	}
	return url.QueryEscape(s)
}

var actions = map[string]string{
	"deploy": "Deploy", "rollback": "Rollback", "apply": "Použít konfiguraci", "start": "Start",
	"stop": "Stop", "restart": "Restart", "backup": "Záloha", "remove": "Odregistrovat", "purge": "Smazat včetně dat",
}

func handleAction(w http.ResponseWriter, r *http.Request) {
	name, action := r.PathValue("name"), r.PathValue("action")
	title, ok := actions[action]
	if !appNameRe.MatchString(name) || !ok {
		http.NotFound(w, r)
		return
	}
	var args []string
	if portableUI() {
		switch action {
		case "deploy", "rollback", "apply", "start", "stop", "restart":
			args = []string{"app", "compose", action, name}
			if action == "deploy" {
				if r.FormValue("migrate") == "1" {
					redirect(w, r, "/apps/"+name+"?err="+urlq("Portable blue/green deploy nepodporuje automatické migrace; spusťte je samostatně po ověření kompatibility databázové změny."))
					return
				}
				if ref := strings.TrimSpace(r.FormValue("ref")); ref != "" {
					if !refRe.MatchString(ref) {
						redirect(w, r, "/apps/"+name+"?err=Neplatná+ref")
						return
					}
					args = append(args, "--ref", ref)
				}
			}
		case "backup":
			args = []string{"backup", name}
		case "remove":
			args = []string{"app", "remove", name, "--yes"}
		case "purge":
			if r.FormValue("confirm") != name {
				redirect(w, r, "/apps/"+name+"?err="+urlq("Pro smazání opište přesně jméno aplikace."))
				return
			}
			args = []string{"app", "remove", name, "--yes", "--purge"}
		}
	} else {
		args = []string{action, name}
		switch action {
		case "deploy":
			if ref := strings.TrimSpace(r.FormValue("ref")); ref != "" {
				if !refRe.MatchString(ref) {
					redirect(w, r, "/apps/"+name+"?err=Neplatná+ref")
					return
				}
				args = append(args, "--ref", ref)
			}
			if r.FormValue("migrate") == "1" {
				args = append(args, "--migrate")
			}
		case "remove":
			args = []string{"app", "remove", name}
		case "purge":
			if r.FormValue("confirm") != name {
				redirect(w, r, "/apps/"+name+"?err="+urlq("Pro smazání opište přesně jméno aplikace."))
				return
			}
			args = []string{"app", "remove", name, "--purge"}
		}
	}
	id, err := jobs.start(name, title+" "+name, args...)
	if err != nil {
		redirect(w, r, "/apps/"+name+"?err="+urlq(err.Error()))
		return
	}
	redirect(w, r, "/jobs/"+id)
}

var bulkActions = map[string]string{"deploy": "Deploy všech aplikací", "apply": "Použít konfiguraci u všech", "backup": "Záloha všeho"}

func handleBulk(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	title, ok := bulkActions[action]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if portableUI() {
		apps, err := listApps()
		if err != nil {
			redirect(w, r, "/?err="+urlq(err.Error()))
			return
		}
		var commands [][]string
		for _, app := range apps {
			switch action {
			case "deploy":
				command := []string{"app", "compose", "deploy", app.Name}
				if app.Source == "git" {
					ref := app.Branch
					if ref == "" {
						ref = "main"
					}
					command = append(command, "--ref", ref)
				}
				commands = append(commands, command)
			case "apply":
				commands = append(commands, []string{"app", "compose", "apply", app.Name})
			case "backup":
				commands = append(commands, []string{"backup", app.Name})
			}
		}
		if len(commands) == 0 {
			redirect(w, r, "/?err="+urlq("Nejsou nastavené žádné aplikace."))
			return
		}
		id, err := jobs.startSequence("", title, commands)
		if err != nil {
			redirect(w, r, "/?err="+urlq(err.Error()))
			return
		}
		redirect(w, r, "/jobs/"+id)
		return
	}
	id, err := jobs.start("", title, action, "--all")
	if err != nil {
		redirect(w, r, "/?err="+urlq(err.Error()))
		return
	}
	redirect(w, r, "/jobs/"+id)
}

func handleLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !appNameRe.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	tail := r.URL.Query().Get("tail")
	if _, err := strconv.Atoi(tail); err != nil {
		tail = "300"
	}
	args := []string{"logs", name, "--tail", tail}
	if svc := r.URL.Query().Get("service"); regexp.MustCompile(`^[a-z0-9-]{1,40}$`).MatchString(svc) {
		args = append(args, svc)
	}
	out, err := runDkit(30*time.Second, args...)
	p := page{Title: "Logy " + name, Section: "apps", Data: map[string]any{"Name": name, "Output": string(out), "Tail": tail, "Service": r.URL.Query().Get("service")}}
	if err != nil {
		p.Error = err.Error()
	}
	render(w, r, "logs.html", p)
}

func handleJobs(w http.ResponseWriter, r *http.Request) {
	render(w, r, "jobs.html", page{Title: "Úlohy", Section: "jobs", Data: jobs.list("", 100)})
}

func handleJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, ok := jobs.get(id)
	if !jobIDRe.MatchString(id) || !ok {
		http.NotFound(w, r)
		return
	}
	render(w, r, "job.html", page{Title: j.Title, Section: "jobs", Data: j})
}

// Returns log bytes from ?offset= plus job state headers; polled by job.js.
func handleJobRaw(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, ok := jobs.get(id)
	if !jobIDRe.MatchString(id) || !ok {
		http.NotFound(w, r)
		return
	}
	off, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	f, err := os.Open(filepath.Join(jobDir(), id+".log"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer f.Close()
	if off > 0 {
		_, _ = f.Seek(off, io.SeekStart)
	}
	data, _ := io.ReadAll(bufio.NewReader(io.LimitReader(f, 4<<20)))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Offset", strconv.FormatInt(off+int64(len(data)), 10))
	w.Header().Set("X-Running", strconv.FormatBool(j.Running))
	w.Header().Set("X-Exit", strconv.Itoa(j.Exit))
	_, _ = w.Write(data)
}

func main() {
	jobs.load()
	static, _ := fs.Sub(assets, "static")
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /login", handleLogin)
	mux.HandleFunc("POST /login", handleLogin)
	mux.HandleFunc("POST /logout", handleLogout)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /", withAuth(handleDashboard))
	mux.HandleFunc("GET /apps/new", withAuth(handleNew))
	mux.HandleFunc("POST /apps/new", withAuth(handleNew))
	mux.HandleFunc("GET /apps/{name}", withAuth(handleApp))
	mux.HandleFunc("GET /apps/{name}/logs", withAuth(handleLogs))
	mux.HandleFunc("POST /apps/{name}/files/{file}", withAuth(handleSave))
	mux.HandleFunc("POST /apps/{name}/action/{action}", withAuth(handleAction))
	mux.HandleFunc("POST /apps/{name}/settings", withAuth(handleAppSettings))
	mux.HandleFunc("POST /apps/{name}/env", withAuth(handleAppEnv))
	mux.HandleFunc("POST /apps/{name}/clone", withAuth(handleClone))
	mux.HandleFunc("POST /bulk/{action}", withAuth(handleBulk))
	mux.HandleFunc("GET /settings", withAuth(handleSettings))
	mux.HandleFunc("POST /settings", withAuth(handleSettings))
	mux.HandleFunc("GET /presets/{name}", withAuth(handlePreset))
	mux.HandleFunc("POST /presets/{name}", withAuth(handlePreset))
	mux.HandleFunc("GET /templates/{name}", withAuth(handleTemplate))
	mux.HandleFunc("POST /templates/{name}", withAuth(handleTemplate))
	mux.HandleFunc("POST /templates/create", withAuth(handleTemplateCreate))
	mux.HandleFunc("GET /jobs", withAuth(handleJobs))
	mux.HandleFunc("GET /jobs/{id}", withAuth(handleJob))
	mux.HandleFunc("GET /jobs/{id}/raw", withAuth(handleJobRaw))
	srv := &http.Server{Addr: listen, Handler: securityHeaders(mux), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("dkit UI listening on %s", listen)
	log.Fatal(srv.ListenAndServe())
}
