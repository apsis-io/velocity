package async_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/apsis-io/velocity/async"
	"github.com/apsis-io/velocity/ownership"
)

func ExampleRunner_Gather() {
	run, _ := async.New(async.Limited(2))
	outcomes, _ := run.Gather(context.Background(),
		async.Named("one", func(context.Context) (int, error) { return 1, nil }),
		async.Named("two", func(context.Context) (int, error) { return 2, nil }),
	)
	fmt.Println(outcomes[0].Label, outcomes[0].Value)
	fmt.Println(outcomes[1].Label, outcomes[1].Value)
	// Output:
	// one 1
	// two 2
}

func ExampleRunner_Map() {
	run, _ := async.New(async.Limited(2))
	squares, err := run.Map(context.Background(), []int{1, 2, 3},
		func(_ context.Context, n int) (int, error) { return n * n, nil })
	fmt.Println(squares, err)
	// Output: [1 4 9] <nil>
}

// Failures are reported out of band, one ItemError per failed item.
func ExampleRunner_Map_failures() {
	run, _ := async.New(async.Unlimited)
	_, err := run.Map(context.Background(), []int{1, 2, 3, 4},
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
func ExampleRunner_Map_ownedCollection() {
	run, _ := async.New(async.Unlimited)
	owner, _ := ownership.New([]string{"a", "bb", "ccc"})
	defer owner.Release()

	lengths, _ := owner.View(func(items []string) ([]int, error) {
		return run.Map(context.Background(), items,
			func(_ context.Context, s string) (int, error) { return len(s), nil })
	})
	fmt.Println(lengths)
	// Output: [1 2 3]
}

func ExampleRunner_Gather_takeRecipe() {
	run, _ := async.New(async.Unlimited)
	outcomes, _ := run.GatherFuncs(context.Background(),
		func(context.Context) (int, error) { return 1, nil },
		func(context.Context) (int, error) { return 2, nil },
		func(context.Context) (int, error) { return 3, nil },
	)
	firstTwo := outcomes[:2]
	lastTwo := outcomes[len(outcomes)-2:]
	fmt.Println(len(firstTwo), len(lastTwo))
	// Output: 2 2
}

func ExampleRunner_ErrGroup() {
	run := async.Must(async.New(async.Limited(2)))
	eg, ctx := run.ErrGroup(context.Background())
	for _, n := range []int{1, 2, 3} {
		eg.Go(func(ctx context.Context) error {
			if n == 2 {
				return errors.New("two failed")
			}
			<-ctx.Done() // siblings stop when the first error cancels the group
			return nil
		})
	}
	fmt.Println(eg.Wait(), context.Cause(ctx))
	// Output: two failed two failed
}
