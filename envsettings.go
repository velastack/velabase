package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	validation "github.com/pocketbase/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
)

// envSettings maps env variables to the core settings fields they override.
//
// The first set variable of envs wins. The env value is applied on every
// settings load so it always beats whatever is stored in the database, and
// the superuser UI update request is rejected when it changes the field.
//
// Only APP_NAME/APP_URL/ORIGIN are unprefixed. The SvelteKit app and
// PocketBase load the same env files, so an app's own SMTP_HOST or S3_BUCKET
// must not switch PocketBase mail or storage on.
var envSettings = []struct {
	envs  []string
	field string
	set   func(s *core.Settings, v string) error
	get   func(s *core.Settings) any
}{
	{[]string{"APP_NAME"}, "meta.appName",
		func(s *core.Settings, v string) error { s.Meta.AppName = v; return nil },
		func(s *core.Settings) any { return s.Meta.AppName }},
	// ORIGIN is adapter-node's variable and what the vela deploy writes
	{[]string{"APP_URL", "ORIGIN"}, "meta.appURL",
		func(s *core.Settings, v string) error { s.Meta.AppURL = v; return nil },
		func(s *core.Settings) any { return s.Meta.AppURL }},
	{[]string{"PB_SENDER_NAME"}, "meta.senderName",
		func(s *core.Settings, v string) error { s.Meta.SenderName = v; return nil },
		func(s *core.Settings) any { return s.Meta.SenderName }},
	{[]string{"PB_SENDER_ADDRESS"}, "meta.senderAddress",
		func(s *core.Settings, v string) error { s.Meta.SenderAddress = v; return nil },
		func(s *core.Settings) any { return s.Meta.SenderAddress }},

	{[]string{"PB_SMTP_HOST"}, "smtp.enabled",
		func(s *core.Settings, v string) error { s.SMTP.Enabled = true; return nil },
		func(s *core.Settings) any { return s.SMTP.Enabled }},
	{[]string{"PB_SMTP_HOST"}, "smtp.host",
		func(s *core.Settings, v string) error { s.SMTP.Host = v; return nil },
		func(s *core.Settings) any { return s.SMTP.Host }},
	{[]string{"PB_SMTP_PORT"}, "smtp.port",
		func(s *core.Settings, v string) (err error) { s.SMTP.Port, err = envInt(v); return err },
		func(s *core.Settings) any { return s.SMTP.Port }},
	{[]string{"PB_SMTP_USERNAME"}, "smtp.username",
		func(s *core.Settings, v string) error { s.SMTP.Username = v; return nil },
		func(s *core.Settings) any { return s.SMTP.Username }},
	{[]string{"PB_SMTP_PASSWORD"}, "smtp.password",
		func(s *core.Settings, v string) error { s.SMTP.Password = v; return nil },
		func(s *core.Settings) any { return s.SMTP.Password }},
	{[]string{"PB_SMTP_TLS"}, "smtp.tls",
		func(s *core.Settings, v string) error { s.SMTP.TLS = envBool(v); return nil },
		func(s *core.Settings) any { return s.SMTP.TLS }},

	{[]string{"PB_S3_ENDPOINT"}, "s3.enabled",
		func(s *core.Settings, v string) error { s.S3.Enabled = true; return nil },
		func(s *core.Settings) any { return s.S3.Enabled }},
	{[]string{"PB_S3_ENDPOINT"}, "s3.endpoint",
		func(s *core.Settings, v string) error { s.S3.Endpoint = v; return nil },
		func(s *core.Settings) any { return s.S3.Endpoint }},
	{[]string{"PB_S3_BUCKET"}, "s3.bucket",
		func(s *core.Settings, v string) error { s.S3.Bucket = v; return nil },
		func(s *core.Settings) any { return s.S3.Bucket }},
	{[]string{"PB_S3_REGION"}, "s3.region",
		func(s *core.Settings, v string) error { s.S3.Region = v; return nil },
		func(s *core.Settings) any { return s.S3.Region }},
	{[]string{"PB_S3_ACCESS_KEY"}, "s3.accessKey",
		func(s *core.Settings, v string) error { s.S3.AccessKey = v; return nil },
		func(s *core.Settings) any { return s.S3.AccessKey }},
	{[]string{"PB_S3_SECRET"}, "s3.secret",
		func(s *core.Settings, v string) error { s.S3.Secret = v; return nil },
		func(s *core.Settings) any { return s.S3.Secret }},
	{[]string{"PB_S3_FORCE_PATH_STYLE"}, "s3.forcePathStyle",
		func(s *core.Settings, v string) error { s.S3.ForcePathStyle = envBool(v); return nil },
		func(s *core.Settings) any { return s.S3.ForcePathStyle }},

	{[]string{"PB_BACKUPS_S3_ENDPOINT"}, "backups.s3.enabled",
		func(s *core.Settings, v string) error { s.Backups.S3.Enabled = true; return nil },
		func(s *core.Settings) any { return s.Backups.S3.Enabled }},
	{[]string{"PB_BACKUPS_S3_ENDPOINT"}, "backups.s3.endpoint",
		func(s *core.Settings, v string) error { s.Backups.S3.Endpoint = v; return nil },
		func(s *core.Settings) any { return s.Backups.S3.Endpoint }},
	{[]string{"PB_BACKUPS_S3_BUCKET"}, "backups.s3.bucket",
		func(s *core.Settings, v string) error { s.Backups.S3.Bucket = v; return nil },
		func(s *core.Settings) any { return s.Backups.S3.Bucket }},
	{[]string{"PB_BACKUPS_S3_REGION"}, "backups.s3.region",
		func(s *core.Settings, v string) error { s.Backups.S3.Region = v; return nil },
		func(s *core.Settings) any { return s.Backups.S3.Region }},
	{[]string{"PB_BACKUPS_S3_ACCESS_KEY"}, "backups.s3.accessKey",
		func(s *core.Settings, v string) error { s.Backups.S3.AccessKey = v; return nil },
		func(s *core.Settings) any { return s.Backups.S3.AccessKey }},
	{[]string{"PB_BACKUPS_S3_SECRET"}, "backups.s3.secret",
		func(s *core.Settings, v string) error { s.Backups.S3.Secret = v; return nil },
		func(s *core.Settings) any { return s.Backups.S3.Secret }},
	{[]string{"PB_BACKUPS_S3_FORCE_PATH_STYLE"}, "backups.s3.forcePathStyle",
		func(s *core.Settings, v string) error { s.Backups.S3.ForcePathStyle = envBool(v); return nil },
		func(s *core.Settings) any { return s.Backups.S3.ForcePathStyle }},
}

// bindEnvSettings applies the env overrides to the app settings after every
// load and rejects superuser UI updates of the overridden fields.
func bindEnvSettings(app core.App) {
	app.OnBootstrap().Bind(&hook.Handler[*core.BootstrapEvent]{
		Func: func(e *core.BootstrapEvent) error {
			if err := e.Next(); err != nil {
				return err
			}

			return applyEnvSettings(e.App.Settings())
		},
		// the lowest priority handler is the outermost one, i.e. its code
		// after e.Next() runs last and the overlay has the last word
		Priority: -999,
	})

	app.OnSettingsReload().Bind(&hook.Handler[*core.SettingsReloadEvent]{
		Func: func(e *core.SettingsReloadEvent) error {
			if err := e.Next(); err != nil {
				return err
			}

			return applyEnvSettings(e.App.Settings())
		},
		Priority: -999,
	})

	app.OnSettingsUpdateRequest().Bind(&hook.Handler[*core.SettingsUpdateRequestEvent]{
		Func: func(e *core.SettingsUpdateRequestEvent) error {
			if errs := checkEnvSettings(e.NewSettings); len(errs) > 0 {
				return e.BadRequestError("An error occurred while saving the new settings.", errs)
			}

			if err := e.Next(); err != nil {
				return err
			}

			// the save already triggered a reload (and with it the overlay);
			// re-apply anyway so the served settings never drift
			return applyEnvSettings(e.App.Settings())
		},
		Priority: -999, // reject before the other handlers see the request
	})
}

// applyEnvSettings writes the set env variables into s.
//
// The exported settings fields are assigned directly, the same way the
// PocketBase system hooks do it (Settings.Merge would drop an empty S3 secret
// because of its omitempty tag).
func applyEnvSettings(s *core.Settings) error {
	for _, o := range envSettings {
		if env, v, ok := lookupEnv(o.envs); ok {
			if err := o.set(s, v); err != nil {
				return fmt.Errorf("%s: %w", env, err)
			}
		}
	}

	return nil
}

// checkEnvSettings returns a validation error for every env managed field
// that s doesn't carry the env value of (keyed the way the UI expects it,
// e.g. {"meta": {"appName": {...}}} so that it renders inline).
func checkEnvSettings(s *core.Settings) validation.Errors {
	expected, err := s.Clone()
	if err != nil {
		return nil
	}
	if err := applyEnvSettings(expected); err != nil {
		return nil // already reported on load
	}

	errs := validation.Errors{}

	for _, o := range envSettings {
		env, _, ok := lookupEnv(o.envs)
		if !ok {
			continue
		}

		if o.get(s) != o.get(expected) {
			setNestedError(errs, o.field, validation.NewError(
				"validation_env_locked",
				fmt.Sprintf("Set by the %s environment variable.", env),
			))
		}
	}

	if len(errs) == 0 {
		return nil
	}

	return errs
}

// lookupEnv returns the name and trimmed value of the first set variable.
//
// A variable that is set but empty still counts as set.
func lookupEnv(envs []string) (string, string, bool) {
	for _, env := range envs {
		if v, ok := os.LookupEnv(env); ok {
			return env, strings.TrimSpace(v), true
		}
	}

	return "", "", false
}

func envBool(v string) bool {
	return strings.EqualFold(v, "true") || v == "1" || strings.EqualFold(v, "yes")
}

func envInt(v string) (int, error) {
	if v == "" {
		return 0, nil
	}

	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid integer env value %q", v)
	}

	return n, nil
}

// setNestedError stores err under the dot separated path (e.g. "smtp.host").
func setNestedError(errs validation.Errors, path string, err error) {
	parts := strings.Split(path, ".")

	for _, part := range parts[:len(parts)-1] {
		nested, ok := errs[part].(validation.Errors)
		if !ok {
			nested = validation.Errors{}
			errs[part] = nested
		}
		errs = nested
	}

	errs[parts[len(parts)-1]] = err
}
