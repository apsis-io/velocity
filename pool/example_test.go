package pool_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/apsis-io/velocity/ownership"
	"github.com/apsis-io/velocity/pool"
)

type client struct{ id int }

func ExamplePool() {
	made := 0
	clients, _ := pool.New(pool.Config[*client]{
		New:   func(context.Context) (*client, error) { made++; return &client{id: made}, nil },
		Close: func(c *client) error { fmt.Println("closed", c.id); return nil },
		Max:   2,
	})
	defer clients.Close()

	ctx := context.Background()
	first, _ := clients.Get(ctx)
	c, _ := first.Value()
	fmt.Println("got", c.id)
	_ = first.Release()

	// Returned most recently, so reused next; nothing new is made.
	again, _ := clients.Get(ctx)
	c, _ = again.Value()
	fmt.Println("got", c.id, "made", made)

	// Use after return is an error, not a stale handle.
	_ = again.Release()
	_, err := again.Value()
	fmt.Println(errors.Is(err, ownership.ErrReleased))
	// Output:
	// got 1
	// got 1 made 1
	// true
	// closed 1
}

func ExampleCheckout_Discard() {
	made := 0
	clients, _ := pool.New(pool.Config[*client]{
		New:   func(context.Context) (*client, error) { made++; return &client{id: made}, nil },
		Close: func(c *client) error { fmt.Println("closed", c.id); return nil },
		Max:   1,
	})
	defer clients.Close()

	broken, _ := clients.Get(context.Background())
	_ = broken.Discard() // closed now, capacity freed

	fresh, _ := clients.Get(context.Background())
	defer fresh.Release()
	c, _ := fresh.Value()
	fmt.Println("got", c.id)
	// Output:
	// closed 1
	// got 2
	// closed 2
}
