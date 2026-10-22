//go:build linux

package bootstrap

import (
	"os"
	"testing"
)

func TestConfigureNativeGTKTheme(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		before, set := os.LookupEnv("GTK_THEME")
		_ = os.Unsetenv("GTK_THEME")
		t.Cleanup(func() {
			if set {
				_ = os.Setenv("GTK_THEME", before)
			} else {
				_ = os.Unsetenv("GTK_THEME")
			}
		})
		configureNativeGTKTheme()
		if v := os.Getenv("GTK_THEME"); v != "Adwaita:dark" {
			t.Fatalf("GTK dark fallback: %q", v)
		}
	})
	t.Run("user selected theme", func(t *testing.T) {
		t.Setenv("GTK_THEME", "Breeze:light")
		configureNativeGTKTheme()
		if v := os.Getenv("GTK_THEME"); v != "Breeze:light" {
			t.Fatalf("overwrote explicit GTK theme: %q", v)
		}
	})
	t.Run("explicit empty disables fallback", func(t *testing.T) {
		t.Setenv("GTK_THEME", "")
		configureNativeGTKTheme()
		if _, ok := os.LookupEnv("GTK_THEME"); !ok || os.Getenv("GTK_THEME") != "" {
			t.Fatal("ignored explicit empty GTK_THEME")
		}
	})
}
