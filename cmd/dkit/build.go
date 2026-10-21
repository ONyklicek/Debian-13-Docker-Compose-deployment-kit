package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/applock"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/buildinfo"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/composegen"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/portable"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/templates"
)

var buildArgName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func buildApp(args []string, paths portable.Paths) (returnErr error) {
	if len(args) != 1 && len(args) != 3 || len(args) == 3 && args[1] != "--ref" {
		return fmt.Errorf("usage: dkit app build <name> [--ref <git-ref>]")
	}
	name := args[0]
	if !appconfig.ValidName(name) {
		return fmt.Errorf("invalid app name %q", name)
	}
	if paths.State == "" {
		return fmt.Errorf("state directory is not configured")
	}
	_, _, _, err := buildConfiguration(paths.Config, name, args)
	if err != nil {
		return err
	}
	lock, err := applock.Acquire(paths.State, name)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, applock.Release(name, lock))
	}()
	app, repository, ref, err := buildConfiguration(paths.Config, name, args)
	if err != nil {
		return err
	}
	if err := composegen.Prepare(paths.Config, paths.State, name); err != nil {
		return err
	}
	env, err := appconfig.ReadEnv(paths.Config, name)
	if err != nil {
		return err
	}
	stateAppDir := filepath.Join(paths.State, "apps", name)
	runtimeEnv, err := readOptionalEnv(filepath.Join(stateAppDir, "runtime.env"))
	if err != nil {
		return err
	}
	fingerprint, err := buildinfo.Fingerprint(app, env, runtimeEnv)
	if err != nil {
		return fmt.Errorf("fingerprint app %q build settings: %w", name, err)
	}
	if err := os.MkdirAll(filepath.Join(stateAppDir, "builds"), 0o700); err != nil {
		return fmt.Errorf("create app build directory: %w", err)
	}
	checkout, err := os.MkdirTemp(filepath.Join(stateAppDir, "builds"), "source-*")
	if err != nil {
		return fmt.Errorf("create Git checkout directory: %w", err)
	}
	keepCheckout := true
	defer func() {
		if !keepCheckout {
			if err := os.RemoveAll(checkout); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove successful build checkout %s: %w", checkout, err))
			}
		}
	}()
	if err := runBuildCommand("git", nil, "clone", "--quiet", "--no-checkout", "--filter=blob:none", "--", repository, checkout); err != nil {
		return fmt.Errorf("clone app %q repository: %w (checkout retained at %s)", name, err, checkout)
	}
	if err := runBuildCommand("git", nil, "-C", checkout, "fetch", "--quiet", "--depth=1", "origin", ref); err != nil {
		return fmt.Errorf("fetch app %q ref %q: %w (checkout retained at %s)", name, ref, err, checkout)
	}
	if err := runBuildCommand("git", nil, "-C", checkout, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("checkout app %q ref %q: %w (checkout retained at %s)", name, ref, err, checkout)
	}
	if err := runBuildCommand("git", nil, "-C", checkout, "submodule", "update", "--quiet", "--init", "--recursive", "--depth=1"); err != nil {
		return fmt.Errorf("update app %q submodules: %w (checkout retained at %s)", name, err, checkout)
	}
	revisionBytes, err := runBuildCommandOutput("git", nil, "-C", checkout, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("read app %q Git revision: %w (checkout retained at %s)", name, err, checkout)
	}
	revision := strings.TrimSpace(string(revisionBytes))
	if !validRevision(revision) {
		return fmt.Errorf("Git returned invalid revision %q", revision)
	}
	if _, err := os.Lstat(filepath.Join(checkout, ".env")); err == nil {
		return fmt.Errorf("repository contains a root .env file; refusing to build with potentially committed secrets (checkout retained at %s)", checkout)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect repository .env file: %w", err)
	}
	dockerfile, cleanupDockerfile, err := resolveBuildDockerfile(app, paths.Config, findRoot(), checkout, filepath.Join(stateAppDir, "builds"))
	if err != nil {
		return err
	}
	if cleanupDockerfile != nil {
		defer func() {
			if err := cleanupDockerfile(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary build template: %w", err))
			}
		}()
	}
	buildEnvPath, err := writeBuildEnv(stateAppDir, app.Values["BUILD_ARGS"], env, runtimeEnv)
	if err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(buildEnvPath); err != nil && !os.IsNotExist(err) {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary build environment %s: %w", buildEnvPath, err))
		}
	}()
	image := "dkit/" + name + ":" + revision[:12]
	contextName, err := selectedContext(paths)
	if err != nil {
		return err
	}
	dockerArgs := contextArgs(contextName)
	dockerArgs = append(dockerArgs, "build")
	if app.Values["BUILD_PULL"] != "0" {
		dockerArgs = append(dockerArgs, "--pull")
	}
	if err := appendBuildArgs(&dockerArgs, app.Values["TEMPLATE_ARGS"]); err != nil {
		return err
	}
	for _, key := range strings.Fields(app.Values["BUILD_ARGS"]) {
		dockerArgs = append(dockerArgs, "--build-arg", key+"="+envOrRuntime(env, runtimeEnv, key))
	}
	dockerArgs = append(dockerArgs,
		"--secret", "id=buildenv,src="+buildEnvPath,
		"--label", "dkit.app="+name,
		"--label", "dkit.revision="+revision,
		"--file", dockerfile,
		"--tag", image,
		checkout,
	)
	if err := runBuildCommand("docker", []string{"DOCKER_BUILDKIT=1"}, dockerArgs...); err != nil {
		return fmt.Errorf("build app %q image %s failed (checkout retained at %s): %w", name, image, checkout, err)
	}
	info := buildinfo.Info{Image: image, Revision: revision, Repository: repository, Ref: ref, DockerContext: contextName, Fingerprint: fingerprint}
	manifest, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("encode app build metadata: %w", err)
	}
	manifest = append(manifest, '\n')
	if err := composegen.Write(filepath.Join(stateAppDir, "build.json"), manifest); err != nil {
		return fmt.Errorf("save app build metadata: %w", err)
	}
	keepCheckout = false
	fmt.Printf("Built %s from %s at %s. No deployment was performed.\n", image, repository, revision)
	return nil
}

func buildConfiguration(configDir, name string, args []string) (appconfig.App, string, string, error) {
	app, err := appconfig.Read(configDir, name)
	if err != nil {
		return appconfig.App{}, "", "", err
	}
	if !app.Valid {
		return appconfig.App{}, "", "", fmt.Errorf("app %q configuration is invalid: %s", name, strings.Join(app.Errors, "; "))
	}
	if app.Values["SOURCE"] != "git" {
		return appconfig.App{}, "", "", fmt.Errorf("app %q uses SOURCE=%s; `dkit app build` is for Git apps", name, app.Values["SOURCE"])
	}
	repository := app.Values["GIT_REPOSITORY"]
	repoURL, err := url.Parse(repository)
	if err != nil || repoURL.Scheme != "https" || repoURL.Host == "" || repoURL.User != nil {
		return appconfig.App{}, "", "", fmt.Errorf("portable Git build requires an HTTPS repository URL without embedded credentials; SSH/private repository authentication is not supported yet")
	}
	ref := app.Values["GIT_BRANCH"]
	if len(args) == 3 {
		ref = args[2]
	}
	if ref == "" {
		ref = "main"
	}
	if !validGitRef(ref) {
		return appconfig.App{}, "", "", fmt.Errorf("invalid Git ref %q", ref)
	}
	return app, repository, ref, nil
}

func resolveBuildDockerfile(app appconfig.App, configDir, root, checkout, tempDir string) (string, func() error, error) {
	build := app.Values["BUILD"]
	if build == "" {
		build = "dockerfile"
	}
	if build == "dockerfile" {
		relative := app.Values["DOCKERFILE"]
		if relative == "" {
			relative = "Dockerfile"
		}
		relative = filepath.Clean(filepath.FromSlash(relative))
		if !filepath.IsLocal(relative) || strings.Contains(relative, "..") {
			return "", nil, fmt.Errorf("DOCKERFILE must be a local repository path without '..'")
		}
		path := filepath.Join(checkout, relative)
		info, err := os.Lstat(path)
		if err != nil {
			return "", nil, fmt.Errorf("inspect repository Dockerfile %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return "", nil, fmt.Errorf("repository Dockerfile %s is not a regular file", path)
		}
		return path, nil, nil
	}
	if !buildArgName.MatchString(build) {
		return "", nil, fmt.Errorf("invalid build template name %q", build)
	}
	for _, candidate := range []string{
		filepath.Join(configDir, "templates", build, "Dockerfile"),
		filepath.Join(root, "templates", build, "Dockerfile"),
	} {
		info, err := os.Lstat(candidate)
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", nil, fmt.Errorf("build template %s is not a regular file", candidate)
			}
			return candidate, nil, nil
		}
		if !os.IsNotExist(err) {
			return "", nil, fmt.Errorf("inspect build template %s: %w", candidate, err)
		}
	}
	data, err := buildtemplates.Read(build)
	if err != nil {
		return "", nil, fmt.Errorf("build template %q not found in %s or embedded templates", build, filepath.Join(configDir, "templates"))
	}
	file, err := os.CreateTemp(tempDir, ".Dockerfile-*.tmp")
	if err != nil {
		return "", nil, fmt.Errorf("create temporary embedded build template: %w", err)
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		os.Remove(path)
		return "", nil, fmt.Errorf("set temporary build template permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(path)
		return "", nil, fmt.Errorf("write temporary embedded build template: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", nil, fmt.Errorf("close temporary embedded build template: %w", err)
	}
	return path, func() error { return os.Remove(path) }, nil
}

func appendBuildArgs(args *[]string, templateArgs string) error {
	for _, argument := range strings.Fields(templateArgs) {
		key, value, ok := strings.Cut(argument, "=")
		if !ok || !buildArgName.MatchString(key) || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("invalid TEMPLATE_ARGS entry %q; expected KEY=VALUE without newlines", argument)
		}
		*args = append(*args, "--build-arg", key+"="+value)
	}
	return nil
}

func writeBuildEnv(stateDir, configuredKeys string, env, runtimeEnv map[string]string) (string, error) {
	var contents strings.Builder
	for _, key := range strings.Fields(configuredKeys) {
		if !buildArgName.MatchString(key) {
			return "", fmt.Errorf("invalid BUILD_ARGS name %q", key)
		}
		value := envOrRuntime(env, runtimeEnv, key)
		if strings.ContainsAny(value, "\r\n\x00") {
			return "", fmt.Errorf("build environment value %s contains newline or NUL", key)
		}
		fmt.Fprintf(&contents, "%s=%s\n", key, value)
	}
	file, err := os.CreateTemp(stateDir, ".buildenv-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temporary build environment: %w", err)
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		os.Remove(path)
		return "", fmt.Errorf("set build environment permissions: %w", err)
	}
	if _, err := file.WriteString(contents.String()); err != nil {
		file.Close()
		os.Remove(path)
		return "", fmt.Errorf("write temporary build environment: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("close temporary build environment: %w", err)
	}
	return path, nil
}

func readOptionalEnv(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read build environment %s: %w", path, err)
	}
	values, err := appconfig.ParseEnv(data)
	if err != nil {
		return nil, fmt.Errorf("parse build environment %s: %w", path, err)
	}
	return values, nil
}

func envOrRuntime(env, runtimeEnv map[string]string, key string) string {
	if value, ok := env[key]; ok {
		return value
	}
	return runtimeEnv[key]
}

func validGitRef(ref string) bool {
	if ref == "" || strings.HasPrefix(ref, "-") || strings.Contains(ref, "..") || strings.HasSuffix(ref, ".") || strings.Contains(ref, "@{") {
		return false
	}
	for _, r := range ref {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._/-", r) {
			continue
		}
		return false
	}
	return true
}

func validRevision(revision string) bool {
	if len(revision) != 40 && len(revision) != 64 {
		return false
	}
	for _, r := range revision {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func runBuildCommand(name string, extraEnv []string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = mergeEnvironment(os.Environ(), extraEnv)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func runBuildCommandOutput(name string, extraEnv []string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = mergeEnvironment(os.Environ(), extraEnv)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return output, nil
}
