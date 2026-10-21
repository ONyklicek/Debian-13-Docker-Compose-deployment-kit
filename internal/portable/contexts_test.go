package portable

import (
	"reflect"
	"testing"
)

func TestParseContexts(t *testing.T) {
	input := "{\"Name\":\"default\",\"Description\":\"\",\"DockerEndpoint\":\"unix:///var/run/docker.sock\",\"Current\":\"*\"}\n" +
		"{\"Name\":\"desktop-linux\",\"Description\":\"Docker Desktop\",\"DockerEndpoint\":\"unix:///tmp/docker.sock\",\"Current\":\"\"}\n"
	got, err := ParseContexts([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := []Context{
		{Name: "default", Endpoint: "unix:///var/run/docker.sock", Current: true},
		{Name: "desktop-linux", Description: "Docker Desktop", Endpoint: "unix:///tmp/docker.sock"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseContexts() = %#v, want %#v", got, want)
	}
}

func TestParseContextsRejectsMalformedRows(t *testing.T) {
	for _, input := range []string{`not json`, `{"Description":"missing name"}`} {
		if _, err := ParseContexts([]byte(input)); err == nil {
			t.Errorf("ParseContexts(%q) unexpectedly succeeded", input)
		}
	}
}

func TestContextFromPrecedence(t *testing.T) {
	tests := []struct {
		environment, saved, current, want string
	}{
		{"environment", "saved", "docker", "environment"},
		{"", "saved", "docker", "saved"},
		{"", "", "docker", "docker"},
		{"", "", "", ""},
	}
	for _, tt := range tests {
		if got := ContextFrom(tt.environment, tt.saved, tt.current); got != tt.want {
			t.Errorf("ContextFrom(%q, %q, %q) = %q, want %q",
				tt.environment, tt.saved, tt.current, got, tt.want)
		}
	}
}
