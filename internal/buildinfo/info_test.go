package buildinfo

import (
	"testing"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
)

func TestFingerprintTracksBuildInputsAndIgnoresRuntimeOnlyValues(t *testing.T) {
	app := appconfig.Validate("demo", map[string]string{
		"SOURCE": "git", "GIT_REPOSITORY": "https://example.test/demo.git",
		"BUILD": "node", "BUILD_ARGS": "PUBLIC_KEY",
	})
	initial, err := Fingerprint(app, map[string]string{"PUBLIC_KEY": "public", "APP_SECRET": "one"}, map[string]string{"DB_PASSWORD": "first"})
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := Fingerprint(app, map[string]string{"PUBLIC_KEY": "public", "APP_SECRET": "two"}, map[string]string{"DB_PASSWORD": "rotated"})
	if err != nil {
		t.Fatal(err)
	}
	if initial != unchanged {
		t.Fatal("runtime-only environment changes unexpectedly invalidated the build")
	}
	changed, err := Fingerprint(app, map[string]string{"PUBLIC_KEY": "changed"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if initial == changed {
		t.Fatal("selected build environment change did not invalidate the build")
	}
	app.Values["BUILD"] = "php"
	changed, err = Fingerprint(app, map[string]string{"PUBLIC_KEY": "public"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if initial == changed {
		t.Fatal("build template change did not invalidate the build")
	}
}
