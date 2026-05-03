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
	m := &Manifest{Name: "hello", Port: 8080}
	m.applyDefaults()
	if err := m.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Form != "web-service" {
		t.Fatalf("default form should be web-service, got %q", m.Form)
	}
}

func TestValidateRejectsBadName(t *testing.T) {
	m := &Manifest{Name: "Bad Name!", Port: 8080}
	m.applyDefaults()
	if err := m.Validate(); err == nil {
		t.Fatal("expected name validation error")
	}
}

func TestCronjobNeedsSchedule(t *testing.T) {
	m := &Manifest{Name: "ticker", Form: "cronjob"}
	m.applyDefaults()
	if err := m.Validate(); err == nil {
		t.Fatal("cronjob without schedule should error")
	}
}

func TestUnknownFormRejected(t *testing.T) {
	m := &Manifest{Name: "x", Form: "spaceship"}
	m.applyDefaults()
	if err := m.Validate(); err == nil {
		t.Fatal("unknown form should error")
	}
}
