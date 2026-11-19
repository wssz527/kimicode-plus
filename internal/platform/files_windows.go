package platform

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

func RetryFileOperation(action func() error) error {
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(250 * time.Millisecond) {
		err := action()
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED) || time.Now().After(deadline) {
			return err
		}
	}
}
