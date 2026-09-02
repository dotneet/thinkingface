// Paging. Every listing in this package takes the same (limit, offset) pair
// from its endpoint and clamps it the same way, so the clamp lives here
// rather than beside whichever listing happened to need it first -- it was
// buried in the middle of users.go, and ListFailedSyncJobs went on to write
// the same three ifs out by hand without noticing.

package store

// pageLimit resolves a requested page size. Nothing (or nonsense) asked for
// means the endpoint's default; more than the maximum is served *at* the
// maximum. It is deliberately a clamp and not a fallback: "max 100" reads as
// a ceiling, so answering ?limit=200 with 30 rows -- fewer than a caller who
// asked for nothing would get -- is the opposite of what was requested.
func pageLimit(limit, defaultSize, maxSize int) int {
	switch {
	case limit <= 0:
		return defaultSize
	case limit > maxSize:
		return maxSize
	default:
		return limit
	}
}

// pageWindow is pageLimit plus the offset half. A negative offset is the
// first page: Postgres rejects a negative OFFSET outright, so a hand-edited
// query string would otherwise be a 500 rather than a first page.
func pageWindow(limit, offset, defaultSize, maxSize int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	return pageLimit(limit, defaultSize, maxSize), offset
}
