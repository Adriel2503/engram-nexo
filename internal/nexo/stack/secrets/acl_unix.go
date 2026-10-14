//go:build !windows

package secrets

func Protect(_ string) error { return nil }
