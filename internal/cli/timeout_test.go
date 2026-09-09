package cli

import "time"

func timeoutAfter() <-chan time.Time { return time.After(10 * time.Second) }
