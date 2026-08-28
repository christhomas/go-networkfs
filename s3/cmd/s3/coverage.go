//go:build coverage

// Coverage flushing for the C test harness.
//
// A c-archive has no Go main: the process starts and exits in C, so the
// runtime's end-of-run hook never fires and the counters are never written.
// Only the metadata reaches GOCOVERDIR, which on its own reports nothing.
//
// This export lets the harness flush explicitly before it returns. It is
// behind the `coverage` build tag, so it is not part of the shipped archive.

package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"os"
	"runtime/coverage"
)

//export s3_flush_coverage
func s3_flush_coverage(dir *C.char) C.int {
	if err := coverage.WriteCountersDir(stringFromC(dir)); err != nil {
		fmt.Fprintf(os.Stderr, "coverage flush: %v\n", err)
		return 1
	}
	return 0
}
