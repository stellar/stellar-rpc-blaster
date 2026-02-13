package seed

// Slice returns the accumulated items from an Entry.
func (e *Entry[T]) Slice() []T {
	return e.Map[e.key].([]T)
}

// buffer for accumulating items under a JSON key
type Entry[T any] struct {
	Map map[string]any
	key string
}

// NewEntry creates a new write buffer with the given JSON key and element type
func NewEntry[T any](key string, cap int) Entry[T] {
	m := make(map[string]any)
	m[key] = make([]T, 0, cap)
	return Entry[T]{Map: m, key: key}
}

// Append adds an item to the entry's slice
func (e *Entry[T]) Append(item T) {
	e.Map[e.key] = append(e.Map[e.key].([]T), item)
}
