package dedupe_test

import (
	"context"
	"fmt"

	"github.com/apsis-io/velocity/dedupe"
	"github.com/apsis-io/velocity/ownership"
)

func ExampleGroup_Do() {
	group, _ := dedupe.New[string, int]()
	value, _ := group.Do(context.Background(), "answer", func(context.Context) (int, error) {
		return 42, nil
	})
	fmt.Println(value)
	// Output: 42
}

// An owned group keeps one cell per round and hands every caller a counted
// handle, so the result's Drop runs once, after the last of them releases.
func ExampleGroup_DoShared() {
	group, _ := dedupe.New[string, []byte](
		dedupe.WithResultDrop[string](func(buf []byte) error {
			fmt.Println("dropped", len(buf), "bytes")
			return nil
		}),
	)
	handle, _ := group.DoShared(context.Background(), "page", func(context.Context) ([]byte, error) {
		return make([]byte, 1024), nil
	})
	n, _ := handle.View(func(buf []byte) (int, error) { return len(buf), nil })
	fmt.Println("read", n)
	_ = handle.Release()
	// Output:
	// read 1024
	// dropped 1024 bytes
}

func ExampleGroup_DoBorrowed() {
	group, _ := dedupe.New[string, int]()
	input := ownership.Own(21)
	defer input.Release()
	value, _ := group.DoBorrowed(context.Background(), "answer", input, func(_ context.Context, value int) (int, error) {
		return value * 2, nil
	})
	fmt.Println(value)
	// Output: 42
}

func ExampleNewSingleflight() {
	group, _ := dedupe.NewSingleflight[string, int]()
	value, _ := group.Do(context.Background(), "answer", func(context.Context) (int, error) {
		return 42, nil
	})
	fmt.Println(value)
	// Output: 42
}
