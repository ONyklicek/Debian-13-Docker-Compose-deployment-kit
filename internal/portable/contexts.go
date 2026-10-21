package portable

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Context struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Endpoint    string `json:"endpoint"`
	Current     bool   `json:"current"`
}

func ParseContexts(data []byte) ([]Context, error) {
	var contexts []Context
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var item map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, fmt.Errorf("parse Docker context: %w", err)
		}
		var name, description, endpoint, current string
		_ = json.Unmarshal(item["Name"], &name)
		_ = json.Unmarshal(item["Description"], &description)
		_ = json.Unmarshal(item["DockerEndpoint"], &endpoint)
		_ = json.Unmarshal(item["Current"], &current)
		var currentBool bool
		_ = json.Unmarshal(item["Current"], &currentBool)
		if name == "" {
			return nil, fmt.Errorf("Docker context has no name")
		}
		contexts = append(contexts, Context{
			Name: name, Description: description, Endpoint: endpoint,
			Current: current == "*" || currentBool,
		})
	}
	return contexts, nil
}

// ContextFrom resolves the selection in order of explicit environment
// override, dkit's saved preference, then the Docker CLI's active context.
func ContextFrom(environment, saved, dockerCurrent string) string {
	return first(environment, saved, dockerCurrent)
}
