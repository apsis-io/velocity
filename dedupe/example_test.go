package dedupe_test

import (
	"context"
	"fmt"

	"github.com/apsis-io/velocity/dedupe"
	"github.com/apsis-io/velocity/ownership"
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

func ExampleGroup_DoBorrowed() {
	group, _ := dedupe.New[string, int](context.Background())
	input, _ := ownership.New(21)
	handle, _ := group.DoBorrowed(context.Background(), "answer", input, func(_ context.Context, value int) (int, error) {
		return value * 2, nil
	})
	value, _ := handle.Borrow()
	result, _ := value.Project(func(number int) (int, error) { return number, nil })
	_ = value.Release()
	_ = handle.Release()
	_ = input.Release()
	fmt.Println(result)
	// Output: 42
}

func ExampleNewSingleflight() {
	group, _ := dedupe.NewSingleflight[string, int](context.Background())
	handle, _ := group.Do(context.Background(), "answer", func(context.Context) (int, error) {
		return 42, nil
	})
	value, _ := handle.Borrow()
	result, _ := value.Project(func(number int) (int, error) { return number, nil })
	_ = value.Release()
	fmt.Println(result)
	// Output: 42
}
