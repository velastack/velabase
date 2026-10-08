package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/tests"
)

// superuser token from the PocketBase test data (the same one upstream's apis tests use)
const superuserToken = "eyJhbGciOiJIUzI1NiJ9.eyJpZCI6InN5d2JoZWNuaDQ2cmhtMCIsInR5cGUiOiJhdXRoIiwiY29sbGVjdGlvbklkIjoicGJjXzMxNDI2MzU4MjMiLCJleHAiOjI1MjQ2MDQ0NjEsInJlZnJlc2hhYmxlIjp0cnVlfQ.UXgO3j-0BumcugrFjbd7j0M4MQvbrLggLlcu_YNGjoY"

// newEnvSettingsApp returns a test app with the env settings hooks bound.
//
// tests.NewTestApp bootstraps before the hooks can be bound, so the callers
// trigger the overlay with app.ReloadSettings() or app.Bootstrap().
func newEnvSettingsApp(t testing.TB) *tests.TestApp {
	t.Helper()

	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}

	bindEnvSettings(app)

	return app
}

func TestEnvSettingsOverlay(t *testing.T) {
	t.Setenv("APP_NAME", " Env App ")
	t.Setenv("APP_URL", "https://app.example.com")
	t.Setenv("ORIGIN", "https://origin.example.com")
	t.Setenv("PB_SENDER_NAME", "Env Sender")
	t.Setenv("PB_SENDER_ADDRESS", "env@example.com")
	t.Setenv("PB_SMTP_HOST", "smtp.env.example.com")
	t.Setenv("PB_SMTP_PORT", "2525")
	t.Setenv("PB_SMTP_USERNAME", "env_user")
	t.Setenv("PB_SMTP_PASSWORD", "env_pass")
	t.Setenv("PB_SMTP_TLS", "Yes")
	t.Setenv("PB_S3_ENDPOINT", "https://s3.env.example.com")
	t.Setenv("PB_S3_BUCKET", "env-bucket")
	t.Setenv("PB_S3_REGION", "env-region")
	t.Setenv("PB_S3_ACCESS_KEY", "env_access")
	t.Setenv("PB_S3_SECRET", "")
	t.Setenv("PB_S3_FORCE_PATH_STYLE", "1")
	t.Setenv("PB_BACKUPS_S3_ENDPOINT", "https://backups.env.example.com")
	t.Setenv("PB_BACKUPS_S3_BUCKET", "env-backups")
	t.Setenv("PB_BACKUPS_S3_REGION", "env-backups-region")
	t.Setenv("PB_BACKUPS_S3_ACCESS_KEY", "env_backups_access")
	t.Setenv("PB_BACKUPS_S3_SECRET", "env_backups_secret")
	t.Setenv("PB_BACKUPS_S3_FORCE_PATH_STYLE", "false")

	app := newEnvSettingsApp(t)
	defer app.Cleanup()

	// stored values the env must beat (incl. a non-empty secret that the empty env locks to "")
	app.Settings().Meta.AppName = "Stored App"
	app.Settings().S3.Secret = "stored_secret"
	if err := app.Save(app.Settings()); err != nil {
		t.Fatal(err)
	}

	for _, reload := range []struct {
		name string
		fn   func() error
	}{
		{"OnSettingsReload", app.ReloadSettings},
		{"OnBootstrap", app.Bootstrap},
	} {
		t.Run(reload.name, func(t *testing.T) {
			if err := reload.fn(); err != nil {
				t.Fatal(err)
			}

			s := app.Settings()

			checks := map[string][2]any{
				"meta.appName":              {s.Meta.AppName, "Env App"},
				"meta.appURL":               {s.Meta.AppURL, "https://app.example.com"},
				"meta.senderName":           {s.Meta.SenderName, "Env Sender"},
				"meta.senderAddress":        {s.Meta.SenderAddress, "env@example.com"},
				"smtp.enabled":              {s.SMTP.Enabled, true},
				"smtp.host":                 {s.SMTP.Host, "smtp.env.example.com"},
				"smtp.port":                 {s.SMTP.Port, 2525},
				"smtp.username":             {s.SMTP.Username, "env_user"},
				"smtp.password":             {s.SMTP.Password, "env_pass"},
				"smtp.tls":                  {s.SMTP.TLS, true},
				"s3.enabled":                {s.S3.Enabled, true},
				"s3.endpoint":               {s.S3.Endpoint, "https://s3.env.example.com"},
				"s3.bucket":                 {s.S3.Bucket, "env-bucket"},
				"s3.region":                 {s.S3.Region, "env-region"},
				"s3.accessKey":              {s.S3.AccessKey, "env_access"},
				"s3.secret":                 {s.S3.Secret, ""},
				"s3.forcePathStyle":         {s.S3.ForcePathStyle, true},
				"backups.s3.enabled":        {s.Backups.S3.Enabled, true},
				"backups.s3.endpoint":       {s.Backups.S3.Endpoint, "https://backups.env.example.com"},
				"backups.s3.bucket":         {s.Backups.S3.Bucket, "env-backups"},
				"backups.s3.region":         {s.Backups.S3.Region, "env-backups-region"},
				"backups.s3.accessKey":      {s.Backups.S3.AccessKey, "env_backups_access"},
				"backups.s3.secret":         {s.Backups.S3.Secret, "env_backups_secret"},
				"backups.s3.forcePathStyle": {s.Backups.S3.ForcePathStyle, false},
			}

			for field, c := range checks {
				if c[0] != c[1] {
					t.Errorf("Expected %s %#v, got %#v", field, c[1], c[0])
				}
			}
		})
	}
}

func TestEnvSettingsOriginFallback(t *testing.T) {
	t.Setenv("ORIGIN", "https://origin.example.com")

	app := newEnvSettingsApp(t)
	defer app.Cleanup()

	if err := app.ReloadSettings(); err != nil {
		t.Fatal(err)
	}

	if v := app.Settings().Meta.AppURL; v != "https://origin.example.com" {
		t.Fatalf("Expected ORIGIN to apply, got %q", v)
	}

	// the rest is untouched
	if app.Settings().SMTP.Enabled || app.Settings().S3.Enabled || app.Settings().Backups.S3.Enabled {
		t.Fatalf("Expected mail and storage to stay disabled, got %s", app.Settings().String())
	}
}

func TestEnvSettingsInvalidPort(t *testing.T) {
	t.Setenv("PB_SMTP_PORT", "abc")

	app := newEnvSettingsApp(t)
	defer app.Cleanup()

	err := app.ReloadSettings()
	if err == nil || !strings.Contains(err.Error(), "PB_SMTP_PORT") {
		t.Fatalf("Expected a PB_SMTP_PORT error, got %v", err)
	}
}

func TestEnvSettingsUpdateRequest(t *testing.T) {
	t.Setenv("APP_NAME", "Env App")
	t.Setenv("ORIGIN", "https://origin.example.com")
	t.Setenv("PB_SMTP_HOST", "smtp.env.example.com")

	factory := func(t testing.TB) *tests.TestApp {
		app := newEnvSettingsApp(t)
		if err := app.ReloadSettings(); err != nil {
			t.Fatal(err)
		}
		return app
	}

	scenarios := []tests.ApiScenario{
		{
			Name:   "changing env managed fields",
			Method: http.MethodPatch,
			URL:    "/api/settings",
			Body: strings.NewReader(`{
				"meta":{"appName":"Other", "appURL":"https://other.example.com"},
				"smtp":{"enabled":false, "host":"smtp.other.example.com", "port":2525}
			}`),
			Headers:        map[string]string{"Authorization": superuserToken},
			ExpectedStatus: 400,
			ExpectedContent: []string{
				`"appName":{"code":"validation_env_locked","message":"Set by the APP_NAME environment variable."}`,
				`"appURL":{"code":"validation_env_locked","message":"Set by the ORIGIN environment variable."}`,
				`"enabled":{"code":"validation_env_locked","message":"Set by the PB_SMTP_HOST environment variable."}`,
				`"host":{"code":"validation_env_locked","message":"Set by the PB_SMTP_HOST environment variable."}`,
			},
			NotExpectedContent: []string{`"port"`},
			ExpectedEvents: map[string]int{
				"*":                       0,
				"OnSettingsUpdateRequest": 1,
			},
			TestAppFactory: factory,
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if v := app.Settings().Meta.AppName; v != "Env App" {
					t.Fatalf("Expected the served app name to stay %q, got %q", "Env App", v)
				}
			},
		},
		{
			Name:   "resubmitting the env values with an unmanaged change",
			Method: http.MethodPatch,
			URL:    "/api/settings",
			Body: strings.NewReader(`{
				"meta":{"appName":"Env App", "appURL":"https://origin.example.com", "senderName":"New Sender"},
				"smtp":{"enabled":true, "host":"smtp.env.example.com", "port":2525}
			}`),
			Headers:        map[string]string{"Authorization": superuserToken},
			ExpectedStatus: 200,
			ExpectedContent: []string{
				`"appName":"Env App"`,
				`"senderName":"New Sender"`,
				`"port":2525`,
			},
			ExpectedEvents: map[string]int{
				"*":                         0,
				"OnSettingsUpdateRequest":   1,
				"OnModelUpdate":             1,
				"OnModelUpdateExecute":      1,
				"OnModelAfterUpdateSuccess": 1,
				"OnModelValidate":           1,
				"OnSettingsReload":          1,
			},
			TestAppFactory: factory,
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				s := app.Settings()
				if s.Meta.SenderName != "New Sender" || s.SMTP.Port != 2525 {
					t.Fatalf("Expected the unmanaged changes to be saved, got %s", s.String())
				}
				if s.Meta.AppName != "Env App" || !s.SMTP.Enabled || s.SMTP.Host != "smtp.env.example.com" {
					t.Fatalf("Expected the env values to be served, got %s", s.String())
				}
			},
		},
	}

	for _, scenario := range scenarios {
		scenario.Test(t)
	}
}
