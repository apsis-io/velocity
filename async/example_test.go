package async_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/apsis-io/velocity/async"
	"github.com/apsis-io/velocity/ownership"
)

func ExampleGather() {
	plan, _ := async.NewPlan(async.Limited(2), async.Hooks{},
		async.Task[int]{Label: "one", Run: func(context.Context) (int, error) { return 1, nil }},
		async.Task[int]{Label: "two", Run: func(context.Context) (int, error) { return 2, nil }},
	)
	outcomes, _ := async.Gather(context.Background(), plan)
	fmt.Println(outcomes[0].Label, outcomes[0].Value)
	fmt.Println(outcomes[1].Label, outcomes[1].Value)
	// Output:
	// one 1
	// two 2
}

func ExampleMap() {
	squares, err := async.Map(context.Background(), async.Limited(2), async.Hooks{}, []int{1, 2, 3},
		func(_ context.Context, n int) (int, error) { return n * n, nil })
	fmt.Println(squares, err)
	// Output: [1 4 9] <nil>
}

// Failures are reported out of band, one ItemError per failed item.
func ExampleMap_failures() {
	_, err := async.Map(context.Background(), async.Unlimited, async.Hooks{}, []int{1, 2, 3, 4},
		func(_ context.Context, n int) (int, error) {
			if n%2 == 0 {
				return 0, errors.New("even")
			}
			return n, nil
		})
	fmt.Println(err)
	var item *async.ItemError
	fmt.Println(errors.As(err, &item), item.Index)
	// Output:
	// item 1: even
	// item 3: even
	// true 1
}

// Running Map inside a read keeps the borrow for as long as any worker can
// see the slice, since they all finish before Map returns.
func ExampleMap_ownedCollection() {
	owner, _ := ownership.New([]string{"a", "bb", "ccc"})
	defer owner.Release()

	lengths, _ := owner.View(func(items []string) ([]int, error) {
		return async.Map(context.Background(), async.Unlimited, async.Hooks{}, items,
			func(_ context.Context, s string) (int, error) { return len(s), nil })
	})
	fmt.Println(lengths)
	// Output: [1 2 3]
}

func ExampleGather_takeRecipe() {
	plan, _ := async.NewPlan(async.Unlimited, async.Hooks{},
		async.Task[int]{Run: func(context.Context) (int, error) { return 1, nil }},
		async.Task[int]{Run: func(context.Context) (int, error) { return 2, nil }},
		async.Task[int]{Run: func(context.Context) (int, error) { return 3, nil }},
	)
	outcomes, _ := async.Gather(context.Background(), plan)
	firstTwo := outcomes[:2]
	lastTwo := outcomes[len(outcomes)-2:]
	fmt.Println(len(firstTwo), len(lastTwo))
	// Output: 2 2
}
