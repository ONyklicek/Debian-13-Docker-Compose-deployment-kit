// Form-based editing of app settings (app.env), app environment (env), global settings
// (dkit.env), presets and Dockerfile templates. All changes go through the dkit CLI, which
// validates them and rolls back invalid ones; presets and templates are plain files.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Field struct {
	Key     string
	Label   string
	Help    string
	Kind    string // text, textarea, select, check, services
	Options []string
	Unset   bool // empty value removes the key so the default applies
}

type FieldGroup struct {
	Title  string
	Fields []Field
}

// Settings shown in the app form; the rest of app.env stays editable in the raw editor.
var appGroups = []FieldGroup{
	{"Domény a web", []Field{
		{Key: "DOMAINS", Label: "Domény", Help: "Jedna na řádek; první je hlavní (APP_URL).", Kind: "textarea"},
		{Key: "WEB_PORT", Label: "Port aplikace v kontejneru", Kind: "text"},
		{Key: "HEALTH_PATH", Label: "Kontrola zdraví (cesta)", Help: "Musí vracet 2xx/3xx, jinak se nepřepne provoz.", Kind: "text"},
		{Key: "WEB_CMD", Label: "Příkaz webu", Help: "Prázdné = výchozí příkaz image.", Kind: "text", Unset: true},
		{Key: "PUBLIC", Label: "Veřejně dostupné přes Traefik", Kind: "check"},
	}},
	{"Zdroj a build", []Field{
		{Key: "SOURCE", Label: "Zdroj", Kind: "select", Options: []string{"git", "image"}},
		{Key: "GIT_REPOSITORY", Label: "Git repozitář", Kind: "text", Unset: true},
		{Key: "GIT_BRANCH", Label: "Větev", Kind: "text", Unset: true},
		{Key: "IMAGE", Label: "Docker image", Help: "Pro zdroj „image“.", Kind: "text", Unset: true},
		{Key: "BUILD", Label: "Build", Help: "dockerfile = Dockerfile z repozitáře, jinak šablona kitu.", Kind: "select"},
		{Key: "DOCKERFILE", Label: "Cesta k Dockerfile", Kind: "text", Unset: true},
		{Key: "TEMPLATE_ARGS", Label: "Parametry šablony", Help: "Např. PHP_VERSION=8.3 PHP_EXTENSIONS=imagick", Kind: "text", Unset: true},
		{Key: "BUILD_ARGS", Label: "Proměnné pro build", Help: "Jména proměnných z prostředí (VITE_*, NEXT_PUBLIC_*).", Kind: "text", Unset: true},
	}},
	{"Služby", []Field{
		{Key: "SERVICES", Label: "Kontejnery služeb", Kind: "services", Options: []string{"mariadb", "postgres", "redis", "s3"}},
		{Key: "S3_DOMAIN", Label: "Veřejná doména pro S3", Kind: "text", Unset: true},
	}},
	{"Data, migrace a provoz", []Field{
		{Key: "PERSIST", Label: "Trvalé adresáře", Help: "jméno:/cesta/v/kontejneru, jeden na řádek.", Kind: "textarea", Unset: true},
		{Key: "PERSIST_OWNER", Label: "Vlastník nových adresářů (uid:gid)", Kind: "text", Unset: true},
		{Key: "MIGRATE_CMD", Label: "Příkaz migrací", Kind: "text", Unset: true},
		{Key: "MIGRATE_USER", Label: "Uživatel migrací", Kind: "text", Unset: true},
		{Key: "AUTO_MIGRATE", Label: "Spouštět migrace při každém deployi", Kind: "check"},
		{Key: "MEMORY_LIMIT", Label: "Limit paměti", Help: "Např. 512m, 2g; prázdné = bez limitu.", Kind: "text", Unset: true},
		{Key: "KEEP_PREVIOUS", Label: "Předchozí verze po deployi", Kind: "select", Options: []string{"", "1", "0"}, Unset: true},
	}},
}

// Global settings (dkit.env) shown in the settings page.
var globalFields = []Field{
	{Key: "ACME_EMAIL", Label: "E-mail pro Let's Encrypt", Kind: "text"},
	{Key: "ACME_CA_SERVER", Label: "ACME server", Help: "Prázdné = produkční Let's Encrypt.", Kind: "text"},
	{Key: "TZ", Label: "Časové pásmo", Kind: "text"},
	{Key: "SHARED_SERVICES", Label: "Sdílené služby pro nové aplikace", Help: "Šetří RAM při mnoha webech; každá aplikace má vlastní DB, uživatele a bucket. Existující aplikace se nemění.", Kind: "services", Options: []string{"mariadb", "postgres", "s3"}},
	{Key: "KEEP_PREVIOUS", Label: "Nechat předchozí verzi běžet (rychlý rollback)", Help: "0 = šetří RAM při mnoha webech.", Kind: "select", Options: []string{"1", "0"}},
	{Key: "KEEP_IMAGES", Label: "Počet uchovaných image na aplikaci", Kind: "text"},
	{Key: "BUILD_PULL", Label: "Stahovat aktuální základní image při buildu", Kind: "select", Options: []string{"1", "0"}},
	{Key: "APP_SUBNET_POOL", Label: "Rozsah privátních sítí aplikací", Help: "Nesmí kolidovat s vaší LAN. Měňte jen před první aplikací.", Kind: "text"},
	{Key: "APP_SUBNET_PREFIX", Label: "Velikost sítě aplikace (prefix)", Kind: "text"},
	{Key: "UI_DOMAIN", Label: "Veřejná doména web UI", Help: "Prázdné = jen přes SSH tunel (doporučeno).", Kind: "text"},
	{Key: "DKIT_UI_ENGINE", Label: "Engine web UI", Help: "legacy = plná Debian/Bash správa; portable = Go CLI pro aplikace s per-app hromadnými úlohami, bez správy platformy.", Kind: "select", Options: []string{"legacy", "portable"}},
	{Key: "DKIT_BIN", Label: "Cesta k CLI pro web UI", Help: "Portable režim: /usr/local/bin/dkit-go. Výchozí legacy cesta zůstává /opt/dkit/bin/dkit.", Kind: "text"},
	{Key: "HSTS_SECONDS", Label: "HSTS (s)", Kind: "text"},
	{Key: "TRAEFIK_IMAGE", Label: "Traefik image", Kind: "text"},
	{Key: "MARIADB_IMAGE", Label: "MariaDB image", Kind: "text"},
	{Key: "POSTGRES_IMAGE", Label: "PostgreSQL image", Kind: "text"},
	{Key: "REDIS_IMAGE", Label: "Redis image", Kind: "text"},
	{Key: "S3_IMAGE", Label: "S3 (SeaweedFS) image", Kind: "text"},
	{Key: "S3_ARGS", Label: "Další parametry S3", Kind: "text"},
	{Key: "ADMINER_IMAGE", Label: "Adminer image", Kind: "text"},
}

var (
	envKeyRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	secretKeyRe = regexp.MustCompile(`(KEY|SECRET|PASSWORD|TOKEN|PASS)$`)
	workerRe    = regexp.MustCompile(`^WORKER_([a-z][a-z0-9]*)_(CMD|USER|PORT|PATH)$`)
	presetRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
)

type Worker struct{ Name, Cmd, User, Port, Path string }

type EnvVar struct {
	Key, Value string
	Secret     bool
}

func getJSONMap(args ...string) (map[string]string, error) {
	out, err := runDkit(30*time.Second, args...)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	return m, json.Unmarshal(out, &m)
}

type Preset struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Local       bool   `json:"local"`
}

func listPresets() ([]Preset, error) {
	var p []Preset
	out, err := runDkit(30*time.Second, "preset", "list", "--json")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, fmt.Errorf("parse preset list from dkit: %w", err)
	}
	return p, nil
}

type Template struct {
	Name  string `json:"name"`
	Local bool   `json:"local"`
}

func listTemplates() ([]Template, error) {
	var t []Template
	out, err := runDkit(30*time.Second, "template", "list", "--json")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(out, &t); err != nil {
		return nil, fmt.Errorf("parse template list from dkit: %w", err)
	}
	return t, nil
}

func buildOptions() ([]string, error) {
	opts := []string{"dockerfile"}
	templates, err := listTemplates()
	if err != nil {
		return nil, err
	}
	for _, t := range templates {
		opts = append(opts, t.Name)
	}
	return opts, nil
}

func workersOf(cfg map[string]string) []Worker {
	var ws []Worker
	for _, n := range strings.Fields(strings.ReplaceAll(cfg["WORKERS"], ",", " ")) {
		ws = append(ws, Worker{n, cfg["WORKER_"+n+"_CMD"], cfg["WORKER_"+n+"_USER"], cfg["WORKER_"+n+"_PORT"], cfg["WORKER_"+n+"_PATH"]})
	}
	return ws
}

func envVars(env map[string]string) []EnvVar {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	vars := make([]EnvVar, 0, len(keys))
	for _, k := range keys {
		vars = append(vars, EnvVar{k, env[k], secretKeyRe.MatchString(k)})
	}
	return vars
}

// formValue converts a field's form input to the stored representation.
func formValue(r *http.Request, f Field) string {
	switch f.Kind {
	case "check":
		if r.FormValue(f.Key) == "1" {
			return "1"
		}
		return "0"
	case "services":
		return strings.Join(r.Form[f.Key], " ")
	case "textarea":
		parts := strings.FieldsFunc(r.FormValue(f.Key), func(c rune) bool { return c == '\n' || c == '\r' || c == ',' || c == ' ' })
		if f.Key == "DOMAINS" {
			for i := range parts {
				parts[i] = strings.ToLower(parts[i])
			}
			return strings.Join(parts, ",")
		}
		return strings.Join(parts, " ")
	}
	return strings.TrimSpace(strings.ReplaceAll(r.FormValue(f.Key), "\n", " "))
}

func handleAppSettings(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !appNameRe.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	cur, err := getJSONMap("app", "get", name, "--json")
	if err != nil {
		redirect(w, r, "/apps/"+name+"?err="+urlq(err.Error()))
		return
	}
	var lines []string
	for _, g := range appGroups {
		for _, f := range g.Fields {
			v := formValue(r, f)
			if v == "" && f.Unset {
				lines = append(lines, "-"+f.Key)
			} else {
				lines = append(lines, f.Key+"="+v)
			}
		}
	}
	lines = append(lines, "SHARED_SERVICES="+strings.Join(r.Form["SHARED_SERVICES"], " "))
	// Workers: rows with an empty name are dropped; keys of removed workers are unset.
	keep := map[string]bool{}
	var names []string
	wn, wc, wu, wp, wpa := r.Form["worker_name"], r.Form["worker_cmd"], r.Form["worker_user"], r.Form["worker_port"], r.Form["worker_path"]
	for i, n := range wn {
		n = strings.TrimSpace(n)
		if n == "" || i >= len(wc) || i >= len(wu) || i >= len(wp) || i >= len(wpa) {
			continue
		}
		names = append(names, n)
		for k, v := range map[string]string{"CMD": wc[i], "USER": wu[i], "PORT": wp[i], "PATH": wpa[i]} {
			key := "WORKER_" + n + "_" + k
			if v = strings.TrimSpace(v); v != "" {
				lines = append(lines, key+"="+v)
				keep[key] = true
			}
		}
	}
	lines = append(lines, "WORKERS="+strings.Join(names, " "))
	for k := range cur {
		if workerRe.MatchString(k) && !keep[k] {
			lines = append(lines, "-"+k)
		}
	}
	for _, l := range lines {
		if strings.ContainsAny(l, "\n\r") {
			redirect(w, r, "/apps/"+name+"?err=Hodnoty+nesmí+obsahovat+nový+řádek")
			return
		}
	}
	if out, err := runDkitIn(strings.Join(lines, "\n")+"\n", 2*time.Minute, "app", "set", name, "-"); err != nil {
		redirect(w, r, "/apps/"+name+"?tab=settings&err="+urlq("Nastavení neuloženo: "+err.Error()+" "+string(out)))
		return
	}
	if r.FormValue("then") == "apply" {
		if id, err := jobs.start(name, "Použít konfiguraci "+name, "apply", name); err == nil {
			redirect(w, r, "/jobs/"+id)
			return
		}
	}
	redirect(w, r, "/apps/"+name+"?tab=settings&ok="+urlq("Nastavení uloženo. Běžící aplikace je převezme po „Použít konfiguraci“."))
}

func handleAppEnv(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !appNameRe.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	keys, vals := r.Form["env_key"], r.Form["env_value"]
	var b strings.Builder
	seen := map[string]bool{}
	for i, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" || i >= len(vals) {
			continue
		}
		if !envKeyRe.MatchString(k) || seen[k] || strings.ContainsAny(vals[i], "\n\r") {
			redirect(w, r, "/apps/"+name+"?tab=env&err="+urlq("Neplatná nebo duplicitní proměnná: "+k))
			return
		}
		seen[k] = true
		b.WriteString(k + "=" + vals[i] + "\n")
	}
	if out, err := runDkitIn(b.String(), 2*time.Minute, "env", "replace", name); err != nil {
		redirect(w, r, "/apps/"+name+"?tab=env&err="+urlq("Neuloženo: "+err.Error()+" "+string(out)))
		return
	}
	if r.FormValue("then") == "apply" {
		if id, err := jobs.start(name, "Použít konfiguraci "+name, "apply", name); err == nil {
			redirect(w, r, "/jobs/"+id)
			return
		}
	}
	redirect(w, r, "/apps/"+name+"?tab=env&ok="+urlq("Proměnné uloženy. Projeví se po „Použít konfiguraci“."))
}

func handleClone(w http.ResponseWriter, r *http.Request) {
	name, dst := r.PathValue("name"), strings.TrimSpace(r.FormValue("new_name"))
	if !appNameRe.MatchString(name) || !appNameRe.MatchString(dst) {
		redirect(w, r, "/apps/"+name+"?err=Neplatné+jméno+nové+aplikace")
		return
	}
	args := []string{"app", "clone", name, dst}
	for _, d := range strings.FieldsFunc(r.FormValue("domains"), func(c rune) bool { return c == ',' || c == ' ' || c == '\n' }) {
		args = append(args, "--domain", strings.ToLower(d))
	}
	if r.FormValue("with_env") != "1" {
		args = append(args, "--no-env")
	}
	if out, err := runDkit(2*time.Minute, args...); err != nil {
		redirect(w, r, "/apps/"+name+"?err="+urlq(err.Error()+" "+string(out)))
		return
	}
	redirect(w, r, "/apps/"+dst+"?ok="+urlq("Aplikace vytvořena podle "+name+". Zkontrolujte nastavení a spusťte deploy."))
}

// ---------- global settings ----------

func handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if r.FormValue("op") == "create" {
			name, from := strings.TrimSpace(r.FormValue("name")), strings.TrimSpace(r.FormValue("from"))
			if !presetRe.MatchString(name) || !presetRe.MatchString(from) {
				redirect(w, r, "/settings?err="+urlq("Neplatné jméno presetu nebo zdroje."))
				return
			}
			if out, err := runDkit(time.Minute, "preset", "create", name, "--from", from); err != nil {
				redirect(w, r, "/settings?err="+urlq(err.Error()+" "+string(out)))
				return
			}
			redirect(w, r, "/presets/"+name+"?ok="+urlq("Vlastní preset vytvořen."))
			return
		}
		var lines []string
		for _, f := range globalFields {
			v := formValue(r, f)
			if strings.ContainsAny(v, "\n\r") {
				redirect(w, r, "/settings?err=Neplatná+hodnota")
				return
			}
			lines = append(lines, f.Key+"="+v)
		}
		if out, err := runDkitIn(strings.Join(lines, "\n")+"\n", time.Minute, "config", "set", "-"); err != nil {
			redirect(w, r, "/settings?err="+urlq("Neuloženo: "+err.Error()+" "+string(out)))
			return
		}
		if r.FormValue("then") == "platform" {
			if portableUI() {
				redirect(w, r, "/settings?err="+urlq("Změna byla uložena, ale restart platformy není dostupný v portable UI."))
				return
			}
			if id, err := jobs.start("", "Restart platformy", "platform", "up"); err == nil {
				redirect(w, r, "/jobs/"+id)
				return
			}
		}
		redirect(w, r, "/settings?ok="+urlq("Uloženo. Traefik a sdílené služby převezmou změny po „Uložit a restartovat platformu“."))
		return
	}
	cfg, err := getJSONMap("config", "get", "--json")
	presets, presetErr := listPresets()
	templates, templateErr := listTemplates()
	p := page{Title: "Nastavení", Section: "settings", Data: map[string]any{
		"Fields": globalFields, "Config": cfg, "Presets": presets, "Templates": templates, "UIEngine": uiEngine,
	}}
	if err != nil {
		p.Error = err.Error()
	} else if presetErr != nil {
		p.Error = presetErr.Error()
	} else if templateErr != nil {
		p.Error = templateErr.Error()
	}
	render(w, r, "settings.html", p)
}

// ---------- presets and templates (plain files in /etc/dkit) ----------

func presetPath(name, file string) string { return filepath.Join(dkitEtc, "presets", name, file) }

type presetDocument struct {
	Name   string `json:"name"`
	Local  bool   `json:"local"`
	AppEnv string `json:"app_env"`
	Env    string `json:"env"`
}

type templateDocument struct {
	Name    string `json:"name"`
	Local   bool   `json:"local"`
	Content string `json:"content"`
}

func handlePreset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !presetRe.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		switch r.FormValue("op") {
		case "create":
			from := r.FormValue("from")
			if !presetRe.MatchString(from) {
				from = "dockerfile"
			}
			if out, err := runDkit(time.Minute, "preset", "create", name, "--from", from); err != nil {
				redirect(w, r, "/settings?err="+urlq(err.Error()+" "+string(out)))
				return
			}
		case "remove":
			if out, err := runDkit(time.Minute, "preset", "remove", name); err != nil {
				redirect(w, r, "/settings?err="+urlq(err.Error()+" "+string(out)))
				return
			}
			redirect(w, r, "/settings?ok=Preset+odstraněn")
			return
		default:
			if _, err := os.Stat(presetPath(name, "app.env")); err != nil {
				redirect(w, r, "/settings?err=Lze+upravit+jen+vlastní+preset")
				return
			}
			for _, f := range []string{"app.env", "env"} {
				c := strings.ReplaceAll(r.FormValue(f), "\r\n", "\n")
				if f == "env" && strings.TrimSpace(c) == "" {
					os.Remove(presetPath(name, f))
					continue
				}
				if err := writeAtomic(presetPath(name, f), []byte(c), 0o600); err != nil {
					redirect(w, r, "/presets/"+name+"?err="+urlq(err.Error()))
					return
				}
			}
			redirect(w, r, "/presets/"+name+"?ok=Preset+uložen")
			return
		}
		redirect(w, r, "/presets/"+name)
		return
	}
	if portableUI() {
		out, err := runDkit(30*time.Second, "preset", "show", name, "--json")
		if err != nil {
			render(w, r, "error.html", page{Title: "Preset " + name, Error: err.Error()})
			return
		}
		var document presetDocument
		if err := json.Unmarshal(out, &document); err != nil {
			render(w, r, "error.html", page{Title: "Preset " + name, Error: "Neplatná odpověď CLI: " + err.Error()})
			return
		}
		render(w, r, "preset.html", page{Title: "Preset " + name, Section: "settings", Data: map[string]any{
			"Name": name, "Local": document.Local, "AppEnv": document.AppEnv, "Env": document.Env,
		}})
		return
	}
	appEnv, err := os.ReadFile(presetPath(name, "app.env"))
	local := err == nil
	if !local {
		appEnv, err = os.ReadFile(filepath.Join(dkitRoot, "presets", name, "app.env"))
		if err != nil {
			render(w, r, "error.html", page{Title: "Preset " + name, Error: err.Error()})
			return
		}
	}
	envB, _ := os.ReadFile(presetPath(name, "env"))
	if !local {
		envB, _ = os.ReadFile(filepath.Join(dkitRoot, "presets", name, "env"))
	}
	render(w, r, "preset.html", page{Title: "Preset " + name, Section: "settings", Data: map[string]any{
		"Name": name, "Local": local, "AppEnv": string(appEnv), "Env": string(envB),
	}})
}

func handleTemplate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !presetRe.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	localDir := filepath.Join(dkitEtc, "templates", name)
	localFile := filepath.Join(localDir, "Dockerfile")
	if r.Method == http.MethodPost {
		if r.FormValue("op") == "reset" {
			if out, err := runDkit(time.Minute, "template", "reset", name); err != nil {
				redirect(w, r, "/templates/"+name+"?err="+urlq(err.Error()+" "+string(out)))
				return
			}
			redirect(w, r, "/templates/"+name+"?ok=Platí+výchozí+šablona")
			return
		}
		if err := os.MkdirAll(localDir, 0o700); err != nil {
			redirect(w, r, "/templates/"+name+"?err="+urlq(err.Error()))
			return
		}
		if ign, err := os.ReadFile(filepath.Join(dkitRoot, "templates", name, "Dockerfile.dockerignore")); err == nil {
			if _, err := os.Stat(localFile + ".dockerignore"); err != nil {
				_ = writeAtomic(localFile+".dockerignore", ign, 0o600)
			}
		}
		if err := writeAtomic(localFile, []byte(strings.ReplaceAll(r.FormValue("content"), "\r\n", "\n")), 0o600); err != nil {
			redirect(w, r, "/templates/"+name+"?err="+urlq(err.Error()))
			return
		}
		redirect(w, r, "/templates/"+name+"?ok="+urlq("Uloženo jako vlastní šablona; použije se při dalším deployi."))
		return
	}
	if portableUI() {
		out, err := runDkit(30*time.Second, "template", "show", name, "--json")
		if err != nil {
			render(w, r, "error.html", page{Title: "Šablona " + name, Error: err.Error()})
			return
		}
		var document templateDocument
		if err := json.Unmarshal(out, &document); err != nil {
			render(w, r, "error.html", page{Title: "Šablona " + name, Error: "Neplatná odpověď CLI: " + err.Error()})
			return
		}
		render(w, r, "template.html", page{Title: "Šablona " + name, Section: "settings", Data: map[string]any{
			"Name": name, "Local": document.Local, "Content": document.Content,
		}})
		return
	}
	content, err := os.ReadFile(localFile)
	local := err == nil
	if !local {
		content, err = os.ReadFile(filepath.Join(dkitRoot, "templates", name, "Dockerfile"))
		if err != nil {
			render(w, r, "error.html", page{Title: "Šablona " + name, Error: err.Error()})
			return
		}
	}
	render(w, r, "template.html", page{Title: "Šablona " + name, Section: "settings", Data: map[string]any{
		"Name": name, "Local": local, "Content": string(content),
	}})
}

func handleTemplateCreate(w http.ResponseWriter, r *http.Request) {
	name, from := strings.TrimSpace(r.FormValue("name")), strings.TrimSpace(r.FormValue("from"))
	if !presetRe.MatchString(name) || !presetRe.MatchString(from) {
		redirect(w, r, "/settings?err="+urlq("Neplatné jméno šablony nebo zdroje."))
		return
	}
	if out, err := runDkit(time.Minute, "template", "create", name, "--from", from); err != nil {
		redirect(w, r, "/settings?err="+urlq(err.Error()+" "+string(out)))
		return
	}
	redirect(w, r, "/templates/"+name+"?ok="+urlq("Vlastní šablona vytvořena."))
}
