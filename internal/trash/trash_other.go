//go:build !darwin && !windows

package trash

func move(string) (string, error) { return "", ErrUnsupported }
