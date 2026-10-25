//go:build !darwin

package photos

import "errors"

var errNoClone = errors.New("clonefile not supported on this platform")

func cloneFile(src, dst string) error { return errNoClone }
