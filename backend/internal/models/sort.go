package models

import "sort"

// sortSlice sorts in place with a less function, keeping call sites readable.
func sortSlice[T any](items []T, less func(a, b T) bool) {
	sort.SliceStable(items, func(i, j int) bool { return less(items[i], items[j]) })
}
