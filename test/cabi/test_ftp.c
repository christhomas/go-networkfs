// A consumer's-eye test of libftp.a.
//
// This links the archive that actually ships and calls its C entry points the
// way a consumer does. It reaches three functions no Go test can:
// ftp_openfile, ftp_writefile and setOutBytes take a ByteSlice or a
// size_t, and a Go test file may not `import "C"`, so it cannot name either
// type.
//
// It also checks what a Go test structurally cannot see: that the generated
// header matches the implementation, that every symbol was exported, and that
// the archive links at all.

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "libftp.h"

static int failures = 0;
static int checks = 0;

static void check(int cond, const char *what) {
    checks++;
    if (!cond) {
        failures++;
        fprintf(stderr, "FAIL: %s\n", what);
    }
}

static void test_version(void) {
    char *v = ftp_version();
    check(v != NULL, "version returns a string");
    if (v) {
        check(strlen(v) > 0, "version is not empty");
        ftp_free(v);
    }
}

static void test_free_tolerates_null(void) {
    ftp_free(NULL);
    check(1, "free(NULL) does not crash");
}

static void test_mount_rejects_bad_input(void) {
    check(ftp_mount(1, "{not json") == -1, "malformed JSON returns -1");
    check(ftp_mount(1, "{}") != 0, "an empty config is refused");
}

// Reachable only from C: ByteSlice cannot be named in a Go test file.
static void test_openfile_unmounted(void) {
    ByteSlice out;
    out.data = NULL;
    out.len = 0;

    check(ftp_openfile(99, "/nope", &out) != 0, "openfile while unmounted fails");
    check(out.data == NULL, "a failed openfile leaves no buffer to free");
    check(out.len == 0, "a failed openfile reports zero length");
    if (out.data) ftp_free(out.data);
}

// Also reachable only from C.
static void test_writefile_unmounted(void) {
    const char *payload = "some bytes";
    ByteSlice data;
    data.data = (char *)payload;
    data.len = strlen(payload);
    check(ftp_writefile(99, "/nope", data) != 0, "writefile while unmounted fails");

    ByteSlice empty;
    empty.data = NULL;
    empty.len = 0;
    check(ftp_writefile(99, "/nope", empty) != 0,
          "writefile with an empty slice fails cleanly rather than dereferencing it");
}

static void test_operations_unmounted(void) {
    char *out = NULL;

    check(ftp_stat(99, "/p", &out) != 0, "stat while unmounted fails");
    if (out) { ftp_free(out); out = NULL; }

    check(ftp_listdir(99, "/p", &out) != 0, "listdir while unmounted fails");
    if (out) { ftp_free(out); out = NULL; }

    check(ftp_mkdir(99, "/p") != 0, "mkdir while unmounted fails");
    check(ftp_remove(99, "/p") != 0, "remove while unmounted fails");
    check(ftp_rename(99, "/a", "/b") != 0, "rename while unmounted fails");

    // Unmount is idempotent by design: tearing down what was never set up is
    // not an error.
    check(ftp_unmount(99) == 0, "unmount of a never-mounted driver succeeds");
}

// Mounted round trip.
//
// Everything above only reaches the failure paths. The success paths are where
// the interesting marshalling lives: setOutBytes hands a buffer and a length
// back across the boundary and is reached only when a read succeeds.
//
// This runs when CABI_CONFIG holds a config JSON document for this driver,
// which `make test-cabi` supplies for the drivers that have a server.
static void test_mounted_round_trip(void) {
    const char *cfg = getenv("CABI_CONFIG");
    if (!cfg || cfg[0] == '\0') {
        printf("ftp: no CABI_CONFIG; skipping the mounted tests\n");
        return;
    }

    const int id = 1;
    if (ftp_mount(id, (char *)cfg) != 0) {
        check(0, "mount against the test server");
        return;
    }
    check(1, "mount against the test server");

    const char *path = "/cabi-ftp-roundtrip.txt";
    const char *payload = "written through the C ABI";

    ByteSlice data;
    data.data = (char *)payload;
    data.len = strlen(payload);
    check(ftp_writefile(id, (char *)path, data) == 0, "writefile succeeds");

    ByteSlice out;
    out.data = NULL;
    out.len = 0;
    check(ftp_openfile(id, (char *)path, &out) == 0, "openfile succeeds");
    check(out.data != NULL, "openfile hands back a buffer");
    check(out.len == strlen(payload), "openfile reports the right length");
    if (out.data && out.len == strlen(payload)) {
        check(memcmp(out.data, payload, out.len) == 0, "the bytes survive the round trip");
    }
    if (out.data) ftp_free(out.data);

    char *info = NULL;
    check(ftp_stat(id, (char *)path, &info) == 0, "stat succeeds");
    if (info) {
        check(strstr(info, "cabi-ftp-roundtrip.txt") != NULL, "stat names the file");
        ftp_free(info);
    }

    char *listing = NULL;
    check(ftp_listdir(id, "/", &listing) == 0, "listdir succeeds");
    if (listing) ftp_free(listing);

    check(ftp_remove(id, (char *)path) == 0, "remove succeeds");
    check(ftp_unmount(id) == 0, "unmount succeeds");
}

// The contract is that the caller frees what it is given. Repeating it is what
// turns a leak or a double free into an ASan report rather than something a
// consumer meets in production.
static void test_free_contract_under_repetition(void) {
    for (int i = 0; i < 2000; i++) {
        char *v = ftp_version();
        if (!v) { check(0, "version returned NULL under repetition"); return; }
        ftp_free(v);
    }
    check(1, "2000 allocate/free cycles across the boundary");
}

// Built with -cover, the archive exposes a flush hook. A c-archive has no Go
// main, so the runtime never writes its counters on the way out.
#ifdef NETWORKFS_COVERAGE
extern int ftp_flush_coverage(char *dir);
#endif

int main(void) {
    test_version();
    test_free_tolerates_null();
    test_mount_rejects_bad_input();
    test_openfile_unmounted();
    test_writefile_unmounted();
    test_operations_unmounted();
    test_free_contract_under_repetition();
    test_mounted_round_trip();

    printf("ftp: %d checks, %d failures\n", checks, failures);

#ifdef NETWORKFS_COVERAGE
    {
        const char *dir = getenv("GOCOVERDIR");
        if (dir && ftp_flush_coverage((char *)dir) != 0) {
            fprintf(stderr, "FAIL: could not write coverage counters\n");
            failures++;
        }
    }
#endif

    return failures == 0 ? 0 : 1;
}
