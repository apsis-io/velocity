package async_test

import (
	"context"
	"fmt"

	"github.com/apsis-io/velocity/async"
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
