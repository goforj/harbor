package helper

import "time"

// Clock supplies the trusted admission time after ticket redemption completes.
type Clock interface {
	// Now returns the trusted wall-clock instant used for ticket admission.
	Now() time.Time
}

// SystemClock reads the host wall clock in UTC.
type SystemClock struct{}

// Now returns the current UTC wall-clock time.
func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}
