package ownership_test

import (
	"fmt"
	"slices"

	"github.com/apsis-io/velocity/ownership"
)

func ExampleOwner_Read() {
	owner, _ := ownership.New([]int{1, 2, 3})
	defer owner.Release()

	sum, _ := owner.Read(func(access ownership.ReadAccess[[]int]) (int, error) {
		return access.Project(func(values []int) (int, error) {
			total := 0
			for _, value := range values {
				total += value
			}
			return total, nil
		})
	})
	fmt.Println(sum)
	// Output: 6
}

func ExampleOwner_Write() {
	owner, _ := ownership.New(3)
	defer owner.Release()

	_, _ = owner.Write(func(access ownership.WriteAccess[int]) (struct{}, error) {
		return access.Update(func(value *int) (struct{}, error) {
			*value *= 2
			return struct{}{}, nil
		})
	})
	value, _ := owner.Read(func(access ownership.ReadAccess[int]) (int, error) {
		return access.Project(func(value int) (int, error) { return value, nil })
	})
	fmt.Println(value)
	// Output: 6
}

func ExampleOwner_IntoShared() {
	owner, _ := ownership.New("velocity")
	shared, _ := owner.IntoShared()
	defer shared.Release()
	clone, _ := shared.Clone()
	defer clone.Release()

	length, _ := clone.Read(func(access ownership.ReadAccess[string]) (int, error) {
		return access.Project(func(value string) (int, error) { return len(value), nil })
	})
	fmt.Println(length)
	// Output: 8
}

func ExampleOwner_Snapshot() {
	owner, _ := ownership.New(
		[]int{1, 2},
		ownership.WithClone(func(value []int) ([]int, error) { return slices.Clone(value), nil }),
	)
	defer owner.Release()

	snapshot, _ := owner.Snapshot()
	snapshot[0] = 9
	fmt.Println(snapshot)
	// Output: [9 2]
}
