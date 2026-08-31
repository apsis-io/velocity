package traits_test

import (
	"fmt"
	"strings"

	"github.com/apsis-io/velocity/traits"
)

func ExampleComposeDrops() {
	drop, err := traits.ComposeDrops(
		func(name string) error { fmt.Println("close", name); return nil },
		func(name string) error { fmt.Println("audit", name); return nil },
	)
	if err != nil {
		panic(err)
	}
	if err := drop("cache"); err != nil {
		panic(err)
	}
	// Output:
	// close cache
	// audit cache
}

func ExampleComposeClones() {
	clone, err := traits.ComposeClones(
		func(value string) (string, error) { return strings.Clone(value), nil },
		func(value string) (string, error) { return strings.ToUpper(value), nil },
	)
	if err != nil {
		panic(err)
	}
	value, err := clone("velocity")
	if err != nil {
		panic(err)
	}
	fmt.Println(value)
	// Output: VELOCITY
}

func ExampleDrop_Clone() {
	var released []string
	drop := traits.Drop[string](func(value string) error {
		released = append(released, value)
		return nil
	})
	clone, err := drop.Clone(
		func(value string) (string, error) { return strings.Clone(value), nil },
		func(value string) (string, error) { return strings.ToUpper(value), nil },
	)
	if err != nil {
		panic(err)
	}
	value, err := clone("velocity")
	if err != nil {
		panic(err)
	}
	fmt.Println(value, released)
	// Output: VELOCITY [velocity]
}
