package ownership_test

import (
	"errors"
	"testing"

	"github.com/apsis-io/velocity/ownership"
)

func FuzzOwnershipModel(f *testing.F) {
	f.Add([]byte{0, 0, 1, 2, 3, 4, 5, 6})
	f.Add([]byte{3, 4, 6, 7, 2, 5, 1})
	f.Fuzz(func(t *testing.T, ops []byte) {
		owner := mustOwner(t, 0)
		var shared []*ownership.Shared[int]
		var frozen []*ownership.Frozen[int]
		var reads []*ownership.ReadBorrow[int]
		var writes []*ownership.WriteBorrow[int]

		for _, op := range ops {
			switch op % 16 {
			case 15:
				// Sealing is irreversible, so afterwards every borrow attempt
				// must report ErrSealed rather than succeeding.
				if owner != nil {
					if err := owner.Seal(); err != nil && !knownLifecycleError(err) {
						t.Fatalf("Seal: %v", err)
					}
					if _, err := owner.Borrow(); err == nil {
						t.Fatal("Borrow succeeded on a sealed owner")
					} else if !knownLifecycleError(err) && !errors.Is(err, ownership.ErrSealed) {
						t.Fatalf("Borrow after Seal: %v", err)
					}
				}
			case 12:
				if owner != nil {
					if next, err := owner.Freeze(); err == nil {
						frozen = append(frozen, next)
						owner = nil
					} else if !knownLifecycleError(err) {
						t.Fatalf("Freeze: %v", err)
					}
				}
			case 13:
				if len(frozen) > 0 {
					if next, err := frozen[0].Clone(); err == nil {
						frozen = append(frozen, next)
					} else if !knownLifecycleError(err) {
						t.Fatalf("Frozen.Clone: %v", err)
					}
				}
			case 14:
				if len(frozen) == 1 {
					if next, err := frozen[0].IntoOwner(); err == nil {
						owner = next
						frozen = nil
					} else if !knownLifecycleError(err) {
						t.Fatalf("Frozen.IntoOwner: %v", err)
					}
				} else if len(frozen) > 1 {
					_ = frozen[len(frozen)-1].Release()
					frozen = frozen[:len(frozen)-1]
				}
			case 10:
				if owner != nil {
					if borrow, err := owner.BorrowUntracked(); err == nil {
						reads = append(reads, borrow)
					} else if !knownLifecycleError(err) {
						t.Fatalf("BorrowUntracked: %v", err)
					}
				}
			case 11:
				if owner != nil {
					if borrow, err := owner.BorrowMutUntracked(); err == nil {
						writes = append(writes, borrow)
					} else if !knownLifecycleError(err) {
						t.Fatalf("BorrowMutUntracked: %v", err)
					}
				}
			case 0:
				if owner != nil {
					if borrow, err := owner.Borrow(); err == nil {
						reads = append(reads, borrow)
					} else if !knownLifecycleError(err) {
						t.Fatalf("Borrow: %v", err)
					}
				}
			case 1:
				if owner != nil {
					if borrow, err := owner.BorrowMut(); err == nil {
						writes = append(writes, borrow)
					} else if !knownLifecycleError(err) {
						t.Fatalf("BorrowMut: %v", err)
					}
				}
			case 2:
				if len(reads) > 0 {
					_ = reads[0].Release()
					reads = reads[1:]
				}
			case 3:
				if len(writes) > 0 {
					_ = writes[0].Release()
					writes = writes[1:]
				}
			case 4:
				if owner != nil {
					if next, err := owner.Move(); err == nil {
						owner = next
					} else if !knownLifecycleError(err) {
						t.Fatalf("Move: %v", err)
					}
				}
			case 5:
				if owner != nil {
					if next, err := owner.IntoShared(); err == nil {
						shared = append(shared, next)
						owner = nil
					} else if !knownLifecycleError(err) {
						t.Fatalf("IntoShared: %v", err)
					}
				}
			case 6:
				if len(shared) > 0 {
					if next, err := shared[0].Clone(); err == nil {
						shared = append(shared, next)
					} else if !knownLifecycleError(err) {
						t.Fatalf("Clone: %v", err)
					}
				}
			case 7:
				if len(shared) > 0 {
					_ = shared[len(shared)-1].Release()
					shared = shared[:len(shared)-1]
				}
			case 8:
				if len(shared) == 1 {
					if next, err := shared[0].IntoOwner(); err == nil {
						owner = next
						shared = nil
					} else if !knownLifecycleError(err) {
						t.Fatalf("IntoOwner: %v", err)
					}
				}
			case 9:
				if owner != nil {
					state := owner.State()
					if state.Readers < 0 || (state.Writer && state.Readers != 0) {
						t.Fatalf("invalid state: %+v", state)
					}
				}
			}
		}

		for _, borrow := range reads {
			_ = borrow.Release()
		}
		for _, borrow := range writes {
			_ = borrow.Release()
		}
		for _, handle := range shared {
			_ = handle.Release()
		}
		for _, handle := range frozen {
			_ = handle.Release()
		}
		if owner != nil {
			_ = owner.Release()
		}
	})
}

func knownLifecycleError(err error) bool {
	return errors.Is(err, ownership.ErrConflict) ||
		errors.Is(err, ownership.ErrMoved) ||
		errors.Is(err, ownership.ErrReleased) ||
		errors.Is(err, ownership.ErrSealed)
}
