# Fix Merge

Package `interval` (file `interval.go`) exports `type Interval struct{ Lo, Hi int }` and `func Merge(ivs []Interval) []Interval`. Users report three bugs. Fix them without changing the signature or the type.

1. `Merge` reorders the caller's slice. It must leave `ivs` exactly as it was given.
2. Intervals that touch are not merged. These are closed integer ranges, so `[1,3]` and `[3,5]` overlap and `[1,2]` and `[3,4]` are contiguous: both pairs merge, to `[1,5]` and `[1,4]`. `[1,2]` and `[4,5]` stay apart.
3. An interval given with `Lo` greater than `Hi` is read as the range from `Hi` to `Lo`.

Unchanged: the result is in ascending order of `Lo`, holds no two intervals that overlap or touch, and covers exactly the integers the inputs cover. An empty or nil input gives a result of length 0.
