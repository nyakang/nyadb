//go:build !linux && !darwin && !windows

package dpi

import "errors"

func openLibrary(string) (uintptr, error)     { return 0, errors.New("DM8 native runtime unavailable") }
func symbol(uintptr, string) (uintptr, error) { return 0, errors.New("DM8 native runtime unavailable") }
func closeLibrary(uintptr)                    {}
