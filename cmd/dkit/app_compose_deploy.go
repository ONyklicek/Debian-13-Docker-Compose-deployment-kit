package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/buildinfo"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/composegen"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/deploymentstate"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/portable"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/traefikroute"
)

var blueGreenImagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+-]*$`)

func appComposeBlueGreenDeploy(name string, paths portable.Paths, output io.Writer) error {
	override, err := composeOverridePath(paths.Config, name)
	if err != nil {
		return err
	}
	if override != "" {
		return fmt.Errorf("app %q has compose.override.yaml; blue/green deploy does not support Compose overrides", name)
	}
	app, err := appconfig.Read(paths.Config, name)
	if err != nil {
		return err
	}
	if !app.Valid {
		return fmt.Errorf("app %q configuration is invalid: %s", name, strings.Join(app.Errors, "; "))
	}
	if app.Values["SOURCE"] != "image" && app.Values["SOURCE"] != "git" {
		return fmt.Errorf("app %q has unsupported SOURCE=%s", name, app.Values["SOURCE"])
	}
	if err := composegen.Prepare(paths.Config, paths.State, name); err != nil {
		return err
	}
	inputsSHA256, err := blueGreenInputsSHA256(name, paths)
	if err != nil {
		return err
	}
	contextName, err := selectedContext(paths)
	if err != nil {
		return err
	}
	current, err := deploymentstate.Load(paths.State, name)
	if err != nil {
		return err
	}
	if err := current.Validate(); err != nil {
		return err
	}
	if current.ActiveSlot != "" && current.DockerContext != contextName {
		return fmt.Errorf("app %q blue/green deployment belongs to Docker context %q, not selected context %q", name, current.DockerContext, contextName)
	}

	image, revision, err := blueGreenCandidate(app, paths, contextName)
	if err != nil {
		return err
	}
	candidateReference := image
	target := "blue"
	blueImage, greenImage := image, image
	if current.ActiveSlot == "blue" {
		target = "green"
		blueImage = current.ActiveImage
	} else if current.ActiveSlot == "green" {
		target = "blue"
		greenImage = current.ActiveImage
	}
	if _, err := composegen.RenderBlueGreen(paths.Config, paths.State, name, blueImage, greenImage); err != nil {
		return err
	}
	if app.Values["SOURCE"] == "image" {
		pullArgs := append(contextArgs(contextName), "pull", image)
		if err := runCommand("docker", pullArgs...); err != nil {
			return fmt.Errorf("pull candidate image %q for app %q: %w", image, name, err)
		}
	}
	imageID, err := dockerOutput(contextArgs(contextName), "image", "inspect", "--format", "{{.Id}}", image)
	if err != nil {
		return fmt.Errorf("resolve immutable image ID for app %q candidate %q: %w", name, image, err)
	}
	image = strings.TrimSpace(string(imageID))
	if !validImageID(image) {
		return fmt.Errorf("Docker returned invalid image ID %q for app %q candidate", image, name)
	}
	blueImage, greenImage = image, image
	if current.ActiveSlot == "blue" {
		blueImage = current.ActiveImage
	} else if current.ActiveSlot == "green" {
		greenImage = current.ActiveImage
	}
	model, err := composegen.RenderBlueGreen(paths.Config, paths.State, name, blueImage, greenImage)
	if err != nil {
		return err
	}
	composePath := filepath.Join(paths.State, "apps", name, "blue-green-compose.yaml")
	if err := composegen.Write(composePath, model); err != nil {
		return err
	}
	composeArgs, err := blueGreenProjectArgs(name, target, composePath, paths, contextName)
	if err != nil {
		return err
	}
	if err := runCommand("docker", append(append([]string(nil), composeArgs...), "config", "--quiet")...); err != nil {
		return fmt.Errorf("blue/green Compose model for app %q is invalid: %w", name, err)
	}
	upArgs := append(append([]string(nil), composeArgs...), "up", "--detach", "--no-build", "--wait", "--wait-timeout", "300", "web-"+target, "health-probe-"+target)
	if err := runCommand("docker", upArgs...); err != nil {
		return cleanupBlueGreenTarget(name, target, composeArgs, fmt.Errorf("start candidate slot %s: %w", target, err))
	}
	statuses, err := dockerOutput(composeArgs, "ps", "--all", "--format", "json")
	if err != nil {
		return cleanupBlueGreenTarget(name, target, composeArgs, fmt.Errorf("inspect candidate slot %s health: %w", target, err))
	}
	if err := inspectAppSlotHealth(name, target, statuses); err != nil {
		return cleanupBlueGreenTarget(name, target, composeArgs, err)
	}
	if err := verifyBlueGreenInputs(paths, name, inputsSHA256, target); err != nil {
		return cleanupBlueGreenTarget(name, target, composeArgs, err)
	}
	latestApp, err := appconfig.Read(paths.Config, name)
	if err != nil {
		return cleanupBlueGreenTarget(name, target, composeArgs, err)
	}
	if !latestApp.Valid {
		return cleanupBlueGreenTarget(name, target, composeArgs,
			fmt.Errorf("app %q configuration changed during deployment and is now invalid: %s", name, strings.Join(latestApp.Errors, "; ")))
	}
	latestImage, latestRevision, err := blueGreenCandidate(latestApp, paths, contextName)
	if err != nil {
		return cleanupBlueGreenTarget(name, target, composeArgs, err)
	}
	if latestApp.Values["SOURCE"] != app.Values["SOURCE"] || latestImage != candidateReference || latestRevision != revision {
		return cleanupBlueGreenTarget(name, target, composeArgs,
			fmt.Errorf("app %q image selection changed during deployment; candidate slot %s was not promoted", name, target))
	}
	if err := verifyBlueGreenModelFile(paths.State, name, blueGreenModelSHA256(model)); err != nil {
		return cleanupBlueGreenTarget(name, target, composeArgs, err)
	}

	previousRoute, routeExisted, err := traefikroute.Read(paths.State, name)
	if err != nil {
		return cleanupBlueGreenTarget(name, target, composeArgs, err)
	}
	if app.Values["PUBLIC"] == "0" {
		err = traefikroute.Remove(paths.State, name)
	} else {
		var route []byte
		route, err = renderBlueGreenRoute(app, target)
		if err == nil {
			err = traefikroute.Write(paths.State, name, route)
		}
	}
	if err != nil {
		return cleanupBlueGreenTarget(name, target, composeArgs, fmt.Errorf("switch app %q route to slot %s: %w", name, target, err))
	}

	next := deploymentstate.State{
		ActiveSlot:         target,
		ActiveImage:        image,
		ActiveRevision:     revision,
		DockerContext:      contextName,
		DeployedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		ComposeSHA256:      blueGreenModelSHA256(model),
		ActiveInputsSHA256: inputsSHA256,
	}
	if current.ActiveSlot != "" {
		next.PreviousSlot = current.ActiveSlot
		next.PreviousImage = current.ActiveImage
		next.PrevRevision = current.ActiveRevision
		next.PreviousInputsSHA256 = current.ActiveInputsSHA256
	}
	if err := deploymentstate.Save(paths.State, name, next); err != nil {
		persisted, stateErr := deploymentstate.Load(paths.State, name)
		if stateErr != nil {
			return errors.Join(
				fmt.Errorf("save blue/green deployment state for app %q: %w", name, err),
				fmt.Errorf("cannot verify persisted deployment state; candidate slot %s and its route were left running: %w", target, stateErr),
			)
		}
		if persisted == next {
			return fmt.Errorf("deployment to slot %s is active and its state file contains the new state, but state durability could not be confirmed: %w", target, err)
		}
		if persisted != current {
			return fmt.Errorf("save blue/green deployment state for app %q: %w; unexpected state was found, so candidate slot %s and its route were left running", name, err, target)
		}
		restoreErr := restoreBlueGreenRoute(paths.State, name, previousRoute, routeExisted)
		cleanupErr := error(nil)
		if restoreErr == nil {
			cleanupErr = stopBlueGreenTarget(name, target, composeArgs)
		}
		return errors.Join(fmt.Errorf("save blue/green deployment state for app %q: %w", name, err), restoreErr, cleanupErr)
	}
	_, err = fmt.Fprintf(output, "Deployed app %q to healthy slot %s at %s. Previous slot %s remains available for rollback; database migrations and workers were not changed.\n",
		name, target, revision, defaultText(current.ActiveSlot, "(none)"))
	return err
}

func appComposeBlueGreenRollback(name string, paths portable.Paths, output io.Writer) error {
	if override, err := composeOverridePath(paths.Config, name); err != nil {
		return err
	} else if override != "" {
		return fmt.Errorf("app %q has compose.override.yaml; blue/green rollback does not support Compose overrides", name)
	}
	state, err := deploymentstate.Load(paths.State, name)
	if err != nil {
		return err
	}
	if err := state.Validate(); err != nil {
		return err
	}
	if state.ActiveSlot == "" || state.PreviousSlot == "" {
		return fmt.Errorf("app %q has no previous blue/green slot to roll back to", name)
	}
	if err := verifyBlueGreenInputs(paths, name, state.PreviousInputsSHA256, state.PreviousSlot); err != nil {
		return err
	}
	contextName, err := selectedContext(paths)
	if err != nil {
		return err
	}
	if state.DockerContext != contextName {
		return fmt.Errorf("app %q blue/green deployment belongs to Docker context %q, not selected context %q", name, state.DockerContext, contextName)
	}
	app, err := appconfig.Read(paths.Config, name)
	if err != nil {
		return err
	}
	if !app.Valid {
		return fmt.Errorf("app %q configuration is invalid: %s", name, strings.Join(app.Errors, "; "))
	}
	composePath := filepath.Join(paths.State, "apps", name, "blue-green-compose.yaml")
	if _, err := readVerifiedBlueGreenModel(paths.State, name, state); err != nil {
		return err
	}
	args, err := blueGreenProjectArgs(name, state.PreviousSlot, composePath, paths, contextName)
	if err != nil {
		return err
	}
	if err := runCommand("docker", append(append([]string(nil), args...), "config", "--quiet")...); err != nil {
		return fmt.Errorf("blue/green Compose model for app %q is invalid: %w", name, err)
	}
	upArgs := append(append([]string(nil), args...), "up", "--detach", "--no-build", "--wait", "--wait-timeout", "300", "web-"+state.PreviousSlot, "health-probe-"+state.PreviousSlot)
	if err := runCommand("docker", upArgs...); err != nil {
		return fmt.Errorf("start rollback slot %s for app %q: %w", state.PreviousSlot, name, err)
	}
	statuses, err := dockerOutput(args, "ps", "--all", "--format", "json")
	if err != nil {
		return fmt.Errorf("inspect rollback slot %s health: %w", state.PreviousSlot, err)
	}
	if err := inspectAppSlotHealth(name, state.PreviousSlot, statuses); err != nil {
		return err
	}
	if err := verifyBlueGreenInputs(paths, name, state.PreviousInputsSHA256, state.PreviousSlot); err != nil {
		return err
	}
	if err := verifyBlueGreenModelFile(paths.State, name, state.ComposeSHA256); err != nil {
		return err
	}
	previousRoute, routeExisted, err := traefikroute.Read(paths.State, name)
	if err != nil {
		return err
	}
	if app.Values["PUBLIC"] == "0" {
		err = traefikroute.Remove(paths.State, name)
	} else {
		var route []byte
		route, err = renderBlueGreenRoute(app, state.PreviousSlot)
		if err == nil {
			err = traefikroute.Write(paths.State, name, route)
		}
	}
	if err != nil {
		return fmt.Errorf("switch app %q route to rollback slot %s: %w", name, state.PreviousSlot, err)
	}
	next := deploymentstate.State{
		ActiveSlot:           state.PreviousSlot,
		PreviousSlot:         state.ActiveSlot,
		ActiveImage:          state.PreviousImage,
		PreviousImage:        state.ActiveImage,
		ActiveRevision:       state.PrevRevision,
		PrevRevision:         state.ActiveRevision,
		DockerContext:        contextName,
		DeployedAt:           time.Now().UTC().Format(time.RFC3339Nano),
		ComposeSHA256:        state.ComposeSHA256,
		ActiveInputsSHA256:   state.PreviousInputsSHA256,
		PreviousInputsSHA256: state.ActiveInputsSHA256,
	}
	if err := deploymentstate.Save(paths.State, name, next); err != nil {
		persisted, stateErr := deploymentstate.Load(paths.State, name)
		if stateErr != nil {
			return errors.Join(
				fmt.Errorf("save rollback state for app %q: %w", name, err),
				fmt.Errorf("cannot verify persisted rollback state; route remains on slot %s: %w", next.ActiveSlot, stateErr),
			)
		}
		if persisted == next {
			return fmt.Errorf("rollback route is on slot %s and its state file contains the new state, but state durability could not be confirmed: %w", next.ActiveSlot, err)
		}
		if persisted != state {
			return fmt.Errorf("save rollback state for app %q: %w; unexpected state was found and route remains on slot %s", name, err, next.ActiveSlot)
		}
		return errors.Join(fmt.Errorf("save rollback state for app %q: %w", name, err),
			restoreBlueGreenRoute(paths.State, name, previousRoute, routeExisted))
	}
	_, err = fmt.Fprintf(output, "Rolled app %q back to healthy slot %s at %s. The former active slot remains available; database data and migrations were not reverted.\n",
		name, next.ActiveSlot, next.ActiveRevision)
	return err
}

func blueGreenCandidate(app appconfig.App, paths portable.Paths, contextName string) (string, string, error) {
	if app.Values["SOURCE"] == "image" {
		image := app.Values["IMAGE"]
		if strings.TrimSpace(image) != image || !blueGreenImagePattern.MatchString(image) {
			return "", "", fmt.Errorf("app %q has an invalid IMAGE value for blue/green deployment", app.Name)
		}
		return image, image, nil
	}
	path := filepath.Join(paths.State, "apps", app.Name, "build.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("app %q has no portable Git build; run `dkit app build %s` first: %w", app.Name, app.Name, err)
	}
	var info buildinfo.Info
	if err := json.Unmarshal(data, &info); err != nil {
		return "", "", fmt.Errorf("parse Git build metadata for app %q: %w", app.Name, err)
	}
	if info.Image == "" || info.Revision == "" {
		return "", "", fmt.Errorf("Git build metadata for app %q is incomplete; run `dkit app build %s` again", app.Name, app.Name)
	}
	if !blueGreenImagePattern.MatchString(info.Image) {
		return "", "", fmt.Errorf("Git build metadata for app %q has an invalid image reference", app.Name)
	}
	if info.DockerContext != contextName {
		return "", "", fmt.Errorf("app %q was built for Docker context %q, not selected context %q; run `dkit app build %s` again", app.Name, info.DockerContext, contextName, app.Name)
	}
	env, err := appconfig.ReadEnv(paths.Config, app.Name)
	if err != nil {
		return "", "", err
	}
	runtimeEnv, err := readOptionalEnv(filepath.Join(paths.State, "apps", app.Name, "runtime.env"))
	if err != nil {
		return "", "", err
	}
	fingerprint, err := buildinfo.Fingerprint(app, env, runtimeEnv)
	if err != nil {
		return "", "", fmt.Errorf("fingerprint app %q build settings: %w", app.Name, err)
	}
	if info.Fingerprint != fingerprint {
		return "", "", fmt.Errorf("app %q build is stale for current source/build settings; run `dkit app build %s` again", app.Name, app.Name)
	}
	return info.Image, info.Revision, nil
}

func validImageID(value string) bool {
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	digest := strings.TrimPrefix(value, "sha256:")
	if len(digest) != 64 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func blueGreenModelSHA256(model []byte) string {
	sum := sha256.Sum256(model)
	return hex.EncodeToString(sum[:])
}

func blueGreenInputsSHA256(name string, paths portable.Paths) (string, error) {
	app, err := appconfig.Read(paths.Config, name)
	if err != nil {
		return "", err
	}
	if !app.Valid {
		return "", fmt.Errorf("app %q configuration is invalid: %s", name, strings.Join(app.Errors, "; "))
	}
	appPath, err := appconfig.AppFilePath(paths.Config, name)
	if err != nil {
		return "", err
	}
	inputs := []struct {
		name string
		path string
	}{
		{name: "app-env", path: filepath.Join(filepath.Dir(appPath), "env")},
		{name: "runtime-env", path: filepath.Join(paths.State, "apps", name, "runtime.env")},
		{name: "services-env", path: filepath.Join(filepath.Dir(appPath), "services.env")},
	}
	type input struct {
		Name    string
		Present bool
		Content []byte
		Config  map[string]string
	}
	config := make(map[string]string, len(app.Values))
	for key, value := range app.Values {
		switch key {
		case "SOURCE", "IMAGE", "GIT_REPOSITORY", "GIT_BRANCH", "BUILD", "DOCKERFILE",
			"TEMPLATE_ARGS", "BUILD_ARGS", "BUILD_PULL":
			continue
		}
		config[key] = value
	}
	recorded := make([]input, 0, len(inputs)+1)
	recorded = append(recorded, input{Name: "app-config", Config: config})
	for _, item := range inputs {
		info, err := os.Lstat(item.path)
		if os.IsNotExist(err) {
			recorded = append(recorded, input{Name: item.name})
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspect blue/green %s for app %q: %w", item.name, name, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("blue/green %s for app %q is not a regular file", item.name, name)
		}
		content, err := os.ReadFile(item.path)
		if err != nil {
			return "", fmt.Errorf("read blue/green %s for app %q: %w", item.name, name, err)
		}
		recorded = append(recorded, input{Name: item.name, Present: true, Content: content})
	}
	data, err := json.Marshal(recorded)
	if err != nil {
		return "", fmt.Errorf("encode blue/green environment inputs for app %q: %w", name, err)
	}
	return blueGreenModelSHA256(data), nil
}

func verifyBlueGreenInputs(paths portable.Paths, name, expected, slot string) error {
	actual, err := blueGreenInputsSHA256(name, paths)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("app %q runtime configuration or environment files have changed since slot %s was deployed; refusing blue/green operation", name, slot)
	}
	return nil
}

func readVerifiedBlueGreenModel(stateDir, name string, state deploymentstate.State) ([]byte, error) {
	model, err := readBlueGreenModel(stateDir, name)
	if err != nil {
		return nil, err
	}
	if blueGreenModelSHA256(model) != state.ComposeSHA256 {
		return nil, fmt.Errorf("blue/green Compose model for app %q was modified since deployment; refusing to operate on it", name)
	}
	return model, nil
}

func verifyBlueGreenModelFile(stateDir, name, expectedSHA256 string) error {
	model, err := readBlueGreenModel(stateDir, name)
	if err != nil {
		return err
	}
	if blueGreenModelSHA256(model) != expectedSHA256 {
		return fmt.Errorf("blue/green Compose model for app %q changed during operation; refusing to promote a slot", name)
	}
	return nil
}

func readBlueGreenModel(stateDir, name string) ([]byte, error) {
	path := filepath.Join(stateDir, "apps", name, "blue-green-compose.yaml")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect blue/green Compose model for app %q: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("blue/green Compose model for app %q is not a regular file", name)
	}
	model, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read blue/green Compose model for app %q: %w", name, err)
	}
	return model, nil
}

func blueGreenProjectArgs(name, slot, composePath string, paths portable.Paths, contextName string) ([]string, error) {
	args := contextArgs(contextName)
	args = append(args, "compose", "--profile", slot)
	appPath, err := appconfig.AppFilePath(paths.Config, name)
	if err != nil {
		return nil, err
	}
	servicesEnv := filepath.Join(filepath.Dir(appPath), "services.env")
	if info, err := os.Lstat(servicesEnv); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("app %q service credentials are not a regular file: %s", name, servicesEnv)
		}
		args = append(args, "--env-file", servicesEnv)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect app %q service credentials: %w", name, err)
	}
	args = append(args, "--project-name", "dkit-"+name, "--file", composePath)
	return args, nil
}

func appComposeArgsForDeployment(name string, paths portable.Paths, state deploymentstate.State) ([]string, error) {
	if state.ActiveSlot == "" {
		return appComposeProjectArgs(name, paths)
	}
	contextName, err := selectedContext(paths)
	if err != nil {
		return nil, err
	}
	if contextName != state.DockerContext {
		return nil, fmt.Errorf("app %q blue/green deployment belongs to Docker context %q, not selected context %q", name, state.DockerContext, contextName)
	}
	if err := verifyBlueGreenInputs(paths, name, state.ActiveInputsSHA256, state.ActiveSlot); err != nil {
		return nil, err
	}
	if _, err := readVerifiedBlueGreenModel(paths.State, name, state); err != nil {
		return nil, err
	}
	composePath := filepath.Join(paths.State, "apps", name, "blue-green-compose.yaml")
	return blueGreenProjectArgs(name, state.ActiveSlot, composePath, paths, contextName)
}

func inspectAppSlotHealth(app, slot string, data []byte) error {
	statuses, err := parseComposeStatuses(data)
	if err != nil {
		return err
	}
	appService, probeService := "web-"+slot, "health-probe-"+slot
	appFound, appReady, probeReady := false, false, false
	for _, status := range statuses {
		switch status.Service {
		case appService:
			appFound = true
			appReady = strings.EqualFold(strings.TrimSpace(status.State), "running") &&
				(strings.TrimSpace(status.Health) == "" || strings.EqualFold(strings.TrimSpace(status.Health), "healthy"))
		case probeService:
			probeReady = strings.EqualFold(strings.TrimSpace(status.State), "running") &&
				strings.EqualFold(strings.TrimSpace(status.Health), "healthy")
		}
	}
	if !appFound || !appReady || !probeReady {
		return fmt.Errorf("app %q candidate slot %s did not pass readiness: %s and %s must be running and the HTTP probe healthy", app, slot, appService, probeService)
	}
	return nil
}

func cleanupBlueGreenTarget(name, slot string, composeArgs []string, cause error) error {
	if err := stopBlueGreenTarget(name, slot, composeArgs); err != nil {
		return errors.Join(cause, fmt.Errorf("stop failed candidate slot %s: %w", slot, err))
	}
	return cause
}

func stopBlueGreenTarget(name, slot string, composeArgs []string) error {
	args := append(append([]string(nil), composeArgs...), "stop", "--timeout", "10", "web-"+slot, "health-probe-"+slot)
	if err := runCommand("docker", args...); err != nil {
		return fmt.Errorf("stop candidate slot %s for app %q: %w", slot, name, err)
	}
	return nil
}

func restoreBlueGreenRoute(stateDir, name string, previous []byte, existed bool) error {
	if existed {
		return traefikroute.Write(stateDir, name, previous)
	}
	return traefikroute.Remove(stateDir, name)
}

func renderBlueGreenRoute(app appconfig.App, slot string) ([]byte, error) {
	port := app.Values["WEB_PORT"]
	if port == "" {
		port = "8080"
	}
	healthPath := app.Values["HEALTH_PATH"]
	if healthPath == "" {
		healthPath = "/"
	}
	return traefikroute.RenderForSlot(app.Name, app.Domains, port, healthPath, slot)
}
