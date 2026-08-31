package dedupe_test

import (
	"context"
	"fmt"

	"github.com/apsis-io/velocity/dedupe"
)

func ExampleGroup_Do() {
	group, _ := dedupe.New[string, int](context.Background())
	handle, _ := group.Do(context.Background(), "answer", func(context.Context) (int, error) {
		return 42, nil
	})
	value, _ := handle.Borrow()
	result, _ := value.Project(func(number int) (int, error) { return number, nil })
	_ = value.Release()
	fmt.Println(result)
	// Output: 42
}
