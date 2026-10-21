package buildinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
)

type Info struct {
	Image         string `json:"image"`
	Revision      string `json:"revision"`
	Repository    string `json:"repository"`
	Ref           string `json:"ref"`
	DockerContext string `json:"docker_context"`
	Fingerprint   string `json:"fingerprint"`
}

// Fingerprint covers app build settings and only environment keys explicitly
// selected for the build, so unrelated runtime secrets do not invalidate an image.
func Fingerprint(app appconfig.App, env, runtimeEnv map[string]string) (string, error) {
	config := make(map[string]string)
	for _, key := range []string{
		"SOURCE", "GIT_REPOSITORY", "GIT_BRANCH", "BUILD", "DOCKERFILE",
		"TEMPLATE_ARGS", "BUILD_ARGS", "BUILD_PULL",
	} {
		config[key] = app.Values[key]
	}
	buildEnv := make(map[string]string)
	for _, key := range strings.Fields(app.Values["BUILD_ARGS"]) {
		if value, ok := env[key]; ok {
			buildEnv[key] = value
		} else if value, ok := runtimeEnv[key]; ok {
			buildEnv[key] = value
		} else {
			buildEnv[key] = ""
		}
	}
	data, err := json.Marshal(struct {
		Config   map[string]string `json:"config"`
		BuildEnv map[string]string `json:"build_env"`
	}{config, buildEnv})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
