package api

import (
	"errors"
	"io"
	"testing"
)

// fakeDriver is a minimal Driver used to exercise the registry and
// MountManager without touching any real network code.
type fakeDriver struct {
	mounted    bool
	unmounted  bool
	lastMount  int
	lastConfig map[string]string
	mountErr   error
	unmountErr error
}

func (f *fakeDriver) Name() string { return "fake" }
func (f *fakeDriver) Mount(id int, c map[string]string) error {
	f.mounted = true
	f.lastMount = id
	f.lastConfig = c
	return f.mountErr
}
func (f *fakeDriver) Unmount(id int) error {
	f.unmounted = true
	return f.unmountErr
}
func (f *fakeDriver) Stat(int, string) (FileInfo, error)      { return FileInfo{}, nil }
func (f *fakeDriver) ListDir(int, string) ([]FileInfo, error) { return nil, nil }
func (f *fakeDriver) OpenFile(int, string) (io.ReadCloser, error) {
	return nil, nil
}
func (f *fakeDriver) CreateFile(int, string) (io.WriteCloser, error) {
	return nil, nil
}
func (f *fakeDriver) Mkdir(int, string) error          { return nil }
func (f *fakeDriver) Remove(int, string) error         { return nil }
func (f *fakeDriver) Rename(int, string, string) error { return nil }

// resetRegistry restores the registry after a test mutates it, so we
// don't pollute global state for other tests in the package.
func resetRegistry(t *testing.T) {
	t.Helper()
	saved := make(map[int]DriverFactory, len(registry))
	for k, v := range registry {
		saved[k] = v
	}
	t.Cleanup(func() { registry = saved })
}

func TestRegisterAndGetDriver(t *testing.T) {
	resetRegistry(t)
	registry = make(map[int]DriverFactory)

	calls := 0
	RegisterDriver(42, func() Driver {
		calls++
		return &fakeDriver{}
	})

	d, ok := GetDriver(42)
	if !ok {
		t.Fatal("expected driver 42 to be registered")
	}
	if d == nil {
		t.Fatal("expected non-nil driver")
	}
	if calls != 1 {
		t.Fatalf("factory called %d times, want 1", calls)
	}

	// Factory should run fresh on each GetDriver call so mounts don't
	// share state via accident.
	_, _ = GetDriver(42)
	if calls != 2 {
		t.Fatalf("factory called %d times, want 2 after second GetDriver", calls)
	}
}

func TestGetDriverUnknown(t *testing.T) {
	resetRegistry(t)
	registry = make(map[int]DriverFactory)

	if d, ok := GetDriver(999); ok || d != nil {
		t.Fatalf("expected (nil,false) for unknown driver, got (%v,%v)", d, ok)
	}
}

func TestListDriverTypes(t *testing.T) {
	resetRegistry(t)
	registry = make(map[int]DriverFactory)

	RegisterDriver(1, func() Driver { return &fakeDriver{} })
	RegisterDriver(2, func() Driver { return &fakeDriver{} })
	RegisterDriver(3, func() Driver { return &fakeDriver{} })

	got := ListDriverTypes()
	if len(got) != 3 {
		t.Fatalf("ListDriverTypes len=%d, want 3", len(got))
	}
	seen := map[int]bool{}
	for _, v := range got {
		seen[v] = true
	}
	for _, want := range []int{1, 2, 3} {
		if !seen[want] {
			t.Errorf("missing driver type %d", want)
		}
	}
}

func TestMountManagerMountUnmount(t *testing.T) {
	resetRegistry(t)
	registry = make(map[int]DriverFactory)

	var last *fakeDriver
	RegisterDriver(1, func() Driver {
		last = &fakeDriver{}
		return last
	})

	mm := NewMountManager()
	cfg := map[string]string{"host": "example"}
	if err := mm.Mount(100, 1, cfg); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if !last.mounted || last.lastMount != 100 || last.lastConfig["host"] != "example" {
		t.Fatalf("driver not called correctly: %+v", last)
	}

	d, ok := mm.Get(100)
	if !ok || d != last {
		t.Fatal("Get(100) did not return mounted driver")
	}

	if err := mm.Unmount(100); err != nil {
		t.Fatalf("Unmount: %v", err)
	}
	if !last.unmounted {
		t.Fatal("Unmount did not call driver.Unmount")
	}

	if _, ok := mm.Get(100); ok {
		t.Fatal("Get should return false after Unmount")
	}
}

func TestMountManagerUnknownDriver(t *testing.T) {
	resetRegistry(t)
	registry = make(map[int]DriverFactory)

	mm := NewMountManager()
	err := mm.Mount(1, 999, nil)
	if !errors.Is(err, ErrUnknownDriverType) && err != ErrUnknownDriverType {
		t.Fatalf("expected ErrUnknownDriverType, got %v", err)
	}
}

func TestMountManagerMountError(t *testing.T) {
	resetRegistry(t)
	registry = make(map[int]DriverFactory)

	want := errors.New("boom")
	RegisterDriver(1, func() Driver { return &fakeDriver{mountErr: want} })

	mm := NewMountManager()
	if err := mm.Mount(1, 1, nil); err != want {
		t.Fatalf("expected %v, got %v", want, err)
	}
	if _, ok := mm.Get(1); ok {
		t.Fatal("failed mount should not populate MountManager")
	}
}

func TestMountManagerUnmountNotFound(t *testing.T) {
	mm := NewMountManager()
	if err := mm.Unmount(42); err != ErrMountNotFound {
		t.Fatalf("expected ErrMountNotFound, got %v", err)
	}
}

func TestDriverErrorError(t *testing.T) {
	e := &DriverError{Code: 7, Message: "nope"}
	if e.Error() != "nope" {
		t.Fatalf("Error() = %q, want %q", e.Error(), "nope")
	}
}

func TestSentinelErrors(t *testing.T) {
	// Sanity-check that the sentinel errors all have non-empty messages
	// and distinct codes.
	errs := []*DriverError{
		ErrUnknownDriverType, ErrMountNotFound, ErrNotConnected,
		ErrPermissionDenied, ErrNotFound, ErrExists,
	}
	codes := map[int]bool{}
	for _, e := range errs {
		if e.Message == "" {
			t.Errorf("sentinel with code %d has empty message", e.Code)
		}
		if codes[e.Code] {
			t.Errorf("duplicate sentinel code %d", e.Code)
		}
		codes[e.Code] = true
	}
}
