package manifest

import "testing"

func TestValidateRequiresName(t *testing.T) {
	m := &Manifest{}
	m.applyDefaults()
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestValidateAcceptsBasicWebService(t *testing.T) {
	m := &Manifest{Component: Component{Name: "hello", Port: 8080}}
	m.applyDefaults()
	if err := m.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Form != "web-service" {
		t.Fatalf("default form should be web-service, got %q", m.Form)
	}
}

func TestValidateRejectsBadName(t *testing.T) {
	m := &Manifest{Component: Component{Name: "Bad Name!", Port: 8080}}
	m.applyDefaults()
	if err := m.Validate(); err == nil {
		t.Fatal("expected name validation error")
	}
}

func TestValidateAcceptsFunction(t *testing.T) {
	m := &Manifest{Component: Component{Name: "fn", Form: "function", Handler: "index.mjs"}}
	m.applyDefaults()
	if err := m.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.CPU != 100 || m.Memory != 128 {
		t.Fatalf("function defaults cpu/memory = %d/%d", m.CPU, m.Memory)
	}
}

func TestCronjobNeedsSchedule(t *testing.T) {
	m := &Manifest{Component: Component{Name: "ticker", Form: "cronjob"}}
	m.applyDefaults()
	if err := m.Validate(); err == nil {
		t.Fatal("cronjob without schedule should error")
	}
}

func TestCronjobValidWithSchedule(t *testing.T) {
	m := &Manifest{Component: Component{Name: "ticker", Form: "cronjob", Schedule: "*/5 * * * *"}}
	m.applyDefaults()
	if err := m.Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestUnknownFormRejected(t *testing.T) {
	m := &Manifest{Component: Component{Name: "x", Form: "spaceship"}}
	m.applyDefaults()
	if err := m.Validate(); err == nil {
		t.Fatal("unknown form should error")
	}
}

func TestAppRequiresComponentNames(t *testing.T) {
	m := &Manifest{
		Component:  Component{Name: "myapp"},
		Components: []Component{{Form: "web-service", Port: 8080}},
	}
	m.applyDefaults()
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for component without name")
	}
}

func TestAppRejectsDuplicateComponents(t *testing.T) {
	m := &Manifest{
		Component:  Component{Name: "myapp"},
		Components: []Component{{Name: "web", Port: 8080}, {Name: "web", Port: 8081}},
	}
	m.applyDefaults()
	if err := m.Validate(); err == nil {
		t.Fatal("duplicate components should error")
	}
}

func TestAppValidates(t *testing.T) {
	m := &Manifest{
		Component: Component{Name: "myapp"},
		Components: []Component{
			{Name: "web", Form: "web-service", Port: 8080},
			{Name: "worker", Form: "daemon"},
			{Name: "api", Form: "function", Handler: "index.js"},
			{Name: "nightly", Form: "cronjob", Schedule: "0 3 * * *", Image: "registry/foo:1"},
		},
	}
	m.applyDefaults()
	if err := m.Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !m.IsApp() {
		t.Fatal("expected IsApp")
	}
}

func TestSecretRefValidation(t *testing.T) {
	m := &Manifest{Component: Component{Name: "x", Port: 1, Secrets: []SecretRef{{Env: "FOO"}}}}
	m.applyDefaults()
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for missing secret name")
	}
}

func TestEnvironmentValidation(t *testing.T) {
	m := &Manifest{Component: Component{Name: "x", Port: 1}, Environment: "BadEnv!"}
	m.applyDefaults()
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for invalid environment")
	}
}

func TestIsolationValidation(t *testing.T) {
	m := &Manifest{Component: Component{Name: "x", Port: 1, Isolation: "kata"}}
	m.applyDefaults()
	if err := m.Validate(); err != nil {
		t.Fatalf("kata isolation should validate: %v", err)
	}

	bad := &Manifest{Component: Component{Name: "x", Port: 1, Isolation: "firecracker"}}
	bad.applyDefaults()
	if err := bad.Validate(); err == nil {
		t.Fatal("unknown isolation should error")
	}
}
