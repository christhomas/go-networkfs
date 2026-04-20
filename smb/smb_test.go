package smb

import (
	"errors"
	"testing"

	"github.com/christhomas/go-networkfs/pkg/api"
)

func TestNameReturnsSMB(t *testing.T) {
	d := &SMBDriver{}
	if d.Name() != "smb" {
		t.Fatalf("Name() = %q, want %q", d.Name(), "smb")
	}
}

func TestRegisteredInGlobalRegistry(t *testing.T) {
	d, ok := api.GetDriver(DriverTypeID)
	if !ok {
		t.Fatalf("driver type %d not registered", DriverTypeID)
	}
	if d.Name() != "smb" {
		t.Fatalf("registry returned %q", d.Name())
	}
}

func TestDriverTypeID(t *testing.T) {
	if DriverTypeID != 3 {
		t.Fatalf("DriverTypeID = %d, want 3", DriverTypeID)
	}
}

func TestNotConnectedOperations(t *testing.T) {
	d := &SMBDriver{}
	if _, err := d.Stat(1, "/"); err == nil {
		t.Error("Stat should fail")
	}
	if _, err := d.ListDir(1, "/"); err == nil {
		t.Error("ListDir should fail")
	}
	if _, err := d.OpenFile(1, "/x"); err == nil {
		t.Error("OpenFile should fail")
	}
	if _, err := d.CreateFile(1, "/x"); err == nil {
		t.Error("CreateFile should fail")
	}
	if err := d.Mkdir(1, "/x"); err == nil {
		t.Error("Mkdir should fail")
	}
	if err := d.Remove(1, "/x"); err == nil {
		t.Error("Remove should fail")
	}
	if err := d.Rename(1, "/a", "/b"); err == nil {
		t.Error("Rename should fail")
	}
}

func TestMountValidation(t *testing.T) {
	d := &SMBDriver{}
	if err := d.Mount(1, map[string]string{}); err == nil {
		t.Error("missing host should fail")
	}
	if err := d.Mount(1, map[string]string{"host": "x"}); err == nil {
		t.Error("missing share should fail")
	}
	err := d.Mount(1, map[string]string{
		"host": "x", "share": "s", "user": "u", "port": "bad",
	})
	if err == nil {
		t.Error("invalid port should fail")
	}
}

// TestMountDialFailurePropagates ensures a failed TCP dial surfaces as a
// DriverError. Uses an unlikely port on localhost.
func TestMountDialFailurePropagates(t *testing.T) {
	d := &SMBDriver{}
	err := d.Mount(1, map[string]string{
		"host":  "127.0.0.1",
		"port":  "1", // reserved/unused on most systems
		"share": "x",
		"user":  "x",
		"pass":  "x",
	})
	if err == nil {
		_ = d.Unmount(1)
		t.Fatal("dial to port 1 should fail")
	}
	var drvErr *api.DriverError
	if !errors.As(err, &drvErr) {
		t.Fatalf("expected DriverError, got %T: %v", err, err)
	}
}

// TestUnmountIsIdempotent verifies Unmount is safe to call multiple
// times and before/without Mount.
func TestUnmountIsIdempotent(t *testing.T) {
	d := &SMBDriver{}
	if err := d.Unmount(1); err != nil {
		t.Fatalf("unmount on unmounted driver: %v", err)
	}
	if err := d.Unmount(1); err != nil {
		t.Fatalf("second unmount: %v", err)
	}
}
