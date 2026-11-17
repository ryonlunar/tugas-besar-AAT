package api

// SplitCapacity divides admission slots into a general pool shared by every
// caller and a reserve only priority callers may use. A quarter (at least one
// slot) is reserved, so overload from Media reads cannot exhaust the capacity
// Tim Lapangan and BNPB requests need.
func SplitCapacity(total int) (general, reserved int) {
	if total < 2 {
		return 1, 1
	}
	reserved = total / 4
	if reserved < 1 {
		reserved = 1
	}
	return total - reserved, reserved
}
