package resilience_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/apsis-io/velocity/resilience"
)

func ExampleRetry() {
	attempts := 0
	backoff, _ := resilience.ExponentialBackoff(0, 0, 0)
	value, err := resilience.Retry(context.Background(), resilience.Policy{
		MaxAttempts: 3,
		Backoff:     backoff,
	}, func(context.Context) (string, error) {
		attempts++
		if attempts < 2 {
			return "", errors.New("try again")
		}
		return "ok", nil
	})
	fmt.Println(value, err)
	// Output: ok <nil>
}
