package traefikroute

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
)

var (
	domainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	pathPattern   = regexp.MustCompile(`^/[A-Za-z0-9._~/?=&-]*$`)
)

func Render(name string, domains []string, port, healthPath string) ([]byte, error) {
	return render(name, domains, port, healthPath, "dkit-"+name+"-app")
}

func RenderForSlot(name string, domains []string, port, healthPath, slot string) ([]byte, error) {
	if slot != "blue" && slot != "green" {
		return nil, fmt.Errorf("app %q has invalid deployment slot %q", name, slot)
	}
	return render(name, domains, port, healthPath, "dkit-"+name+"-web-"+slot)
}

func render(name string, domains []string, port, healthPath, backend string) ([]byte, error) {
	if !appconfig.ValidName(name) {
		return nil, fmt.Errorf("invalid app name %q", name)
	}
	if len(domains) == 0 {
		return nil, fmt.Errorf("app %q has no public domains", name)
	}
	for _, domain := range domains {
		if !domainPattern.MatchString(domain) {
			return nil, fmt.Errorf("app %q has invalid domain %q", name, domain)
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, fmt.Errorf("app %q has invalid web port %q", name, port)
	}
	if healthPath == "" {
		healthPath = "/"
	}
	if !pathPattern.MatchString(healthPath) {
		return nil, fmt.Errorf("app %q has invalid HTTP health path %q", name, healthPath)
	}

	rule := "Host(`" + strings.Join(domains, "`) || Host(`") + "`)"
	router := "dkit-" + name + "-web"
	service := router
	middleware := router + "-headers"
	var b strings.Builder
	fmt.Fprintln(&b, "http:")
	fmt.Fprintln(&b, "  routers:")
	fmt.Fprintf(&b, "    %s:\n", router)
	fmt.Fprintf(&b, "      rule: %s\n", yamlString(rule))
	fmt.Fprintln(&b, "      entryPoints: [websecure]")
	fmt.Fprintf(&b, "      service: %s\n", service)
	fmt.Fprintf(&b, "      middlewares: [%s]\n", middleware)
	fmt.Fprintln(&b, "      tls:")
	fmt.Fprintln(&b, "        certResolver: le")
	fmt.Fprintln(&b, "  middlewares:")
	fmt.Fprintf(&b, "    %s:\n", middleware)
	fmt.Fprintln(&b, "      headers:")
	fmt.Fprintln(&b, "        stsSeconds: 31536000")
	fmt.Fprintln(&b, "        contentTypeNosniff: true")
	fmt.Fprintln(&b, "        referrerPolicy: strict-origin-when-cross-origin")
	fmt.Fprintln(&b, "  services:")
	fmt.Fprintf(&b, "    %s:\n", service)
	fmt.Fprintln(&b, "      loadBalancer:")
	fmt.Fprintln(&b, "        servers:")
	fmt.Fprintf(&b, "          - url: %s\n", yamlString(fmt.Sprintf("http://%s:%d", backend, portNumber)))
	fmt.Fprintln(&b, "        healthCheck:")
	fmt.Fprintf(&b, "          path: %s\n", yamlString(healthPath))
	fmt.Fprintln(&b, "          interval: 2s")
	fmt.Fprintln(&b, "          timeout: 3s")
	return []byte(b.String()), nil
}

func Write(stateDir, name string, content []byte) error {
	if stateDir == "" || !appconfig.ValidName(name) {
		return fmt.Errorf("invalid Traefik route location")
	}
	dir := filepath.Join(stateDir, "traefik", "dynamic")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create Traefik app route directory: %w", err)
	}
	path := filepath.Join(dir, name+".yml")
	tmp, err := os.CreateTemp(dir, "."+name+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary Traefik route: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary Traefik route: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temporary Traefik route: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary Traefik route: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("set Traefik route permissions: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace Traefik route: %w", err)
	}
	return nil
}

func Read(stateDir, name string) ([]byte, bool, error) {
	if stateDir == "" || !appconfig.ValidName(name) {
		return nil, false, fmt.Errorf("invalid Traefik route location")
	}
	path := filepath.Join(stateDir, "traefik", "dynamic", name+".yml")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect Traefik route %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("Traefik route %s is not a regular file", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read Traefik route %s: %w", path, err)
	}
	return content, true, nil
}

func Remove(stateDir, name string) error {
	if stateDir == "" || !appconfig.ValidName(name) {
		return fmt.Errorf("invalid Traefik route location")
	}
	path := filepath.Join(stateDir, "traefik", "dynamic", name+".yml")
	if _, exists, err := Read(stateDir, name); err != nil {
		return err
	} else if !exists {
		return nil
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove Traefik route %s: %w", path, err)
	}
	return nil
}

func yamlString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
