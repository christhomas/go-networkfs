// A consumer's-eye test of libnetworkfs.a.
//
// Every test elsewhere in this repository calls the Go code from Go. Nothing
// exercised the artifact that actually ships: the archive, its generated
// header, and the C entry points diskjockey links against. This does, by being
// an ordinary C program that includes the header and links the archive.
//
// It reaches three functions no Go test can. networkfs_openfile,
// networkfs_writefile and setOutBytes take a ByteSlice or a size_t, and a Go
// test file may not `import "C"`, so it cannot name either type. In C they are
// ordinary arguments.
//
// It also checks things a Go test structurally cannot see: that the header
// matches the implementation, that every symbol was exported, and that the
// archive links at all.

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "libnetworkfs.h"

static int failures = 0;
static int checks = 0;

static void check(int cond, const char *what) {
    checks++;
    if (!cond) {
        failures++;
        fprintf(stderr, "FAIL: %s\n", what);
    }
}

// ---------------------------------------------------------------------------

static void test_version(void) {
    char *v = networkfs_version();
    check(v != NULL, "version returns a string");
    if (v) {
        check(strlen(v) > 0, "version is not empty");
        networkfs_free(v);
    }
}

static void test_free_tolerates_null(void) {
    networkfs_free(NULL);
    check(1, "free(NULL) does not crash");
}

// The archive links every driver, so the registry must report them all. This
// is the failure the unified archive exists to prevent and one no per-driver
// test can see.
static void test_drivers_are_all_linked(void) {
    char *out = NULL;
    int rc = networkfs_drivers(&out);

    check(rc == 0, "drivers returns 0");
    check(out != NULL, "drivers writes a result");
    if (!out) return;

    // The payload is a JSON array of ints; count the commas rather than
    // parsing, since a JSON parser is not the thing under test.
    int commas = 0;
    for (const char *p = out; *p; p++) {
        if (*p == ',') commas++;
    }
    check(commas + 1 >= 8, "all eight drivers are linked in");
    networkfs_free(out);
}

// The three mount failures are told apart by return code, and a consumer
// branches on them.
static void test_mount_failure_codes(void) {
    char *err = NULL;

    check(networkfs_mount(1, 3, "{not json", &err) == -1, "malformed JSON returns -1");
    check(err != NULL, "malformed JSON writes an error message");
    if (err) { networkfs_free(err); err = NULL; }

    check(networkfs_mount(1, 999, "{}", &err) == 1, "unknown driver type returns 1");
    check(err != NULL, "unknown driver type writes an error message");
    if (err) {
        check(strstr(err, "999") != NULL, "the message names the driver type");
        networkfs_free(err);
        err = NULL;
    }

    check(networkfs_mount(1, 3, "{\"host\":\"\"}", &err) == 2, "a refused config returns 2");
    check(err != NULL, "a refused config writes an error message");
    if (err) { networkfs_free(err); err = NULL; }
}

// Reachable only from C: ByteSlice cannot be named in a Go test file.
static void test_openfile_on_unknown_mount(void) {
    ByteSlice out;
    out.data = NULL;
    out.len = 0;

    int rc = networkfs_openfile(4242, "/nope", &out);

    check(rc != 0, "openfile on an unknown mount fails");
    check(out.data == NULL, "a failed openfile leaves no buffer to free");
    check(out.len == 0, "a failed openfile reports zero length");
    if (out.data) networkfs_free(out.data);
}

// Also reachable only from C.
static void test_writefile_on_unknown_mount(void) {
    const char *payload = "some bytes";
    ByteSlice data;
    data.data = (char *)payload;
    data.len = strlen(payload);

    check(networkfs_writefile(4242, "/nope", data) != 0,
          "writefile on an unknown mount fails");

    // A zero-length slice must be handled rather than dereferenced.
    ByteSlice empty;
    empty.data = NULL;
    empty.len = 0;
    check(networkfs_writefile(4242, "/nope", empty) != 0,
          "writefile with an empty slice fails cleanly");
}

static void test_operations_on_unknown_mount(void) {
    char *out = NULL;

    check(networkfs_stat(4242, "/p", &out) != 0, "stat on an unknown mount fails");
    if (out) {
        check(strstr(out, "mount") != NULL, "stat says the mount is missing");
        networkfs_free(out);
        out = NULL;
    }

    check(networkfs_listdir(4242, "/p", &out) != 0, "listdir on an unknown mount fails");
    if (out) { networkfs_free(out); out = NULL; }

    check(networkfs_mkdir(4242, "/p") != 0, "mkdir on an unknown mount fails");
    check(networkfs_remove(4242, "/p") != 0, "remove on an unknown mount fails");
    check(networkfs_rename(4242, "/a", "/b") != 0, "rename on an unknown mount fails");
    check(networkfs_unmount(4242) != 0, "unmount of an unknown mount fails");
}

// ---------------------------------------------------------------------------
// Mounted tests.
//
// Everything above runs without a server and therefore only ever reaches the
// failure paths. The success paths are where the interesting marshalling lives
// — setOutBytes hands a buffer and a length back across the boundary, and it
// is only reached when a read actually succeeds.
//
// These run when SMB_HOST is set, which `make test-cabi` does after starting
// the Samba container.

static int have_server(void) { return getenv("SMB_HOST") != NULL; }

static int mount_smb(int mount_id) {
    const char *host = getenv("SMB_HOST");
    const char *port = getenv("SMB_PORT");
    const char *share = getenv("SMB_SHARE");
    const char *user = getenv("SMB_USER");
    const char *pass = getenv("SMB_PASS");

    char config[1024];
    snprintf(config, sizeof config,
             "{\"host\":\"%s\",\"port\":\"%s\",\"share\":\"%s\","
             "\"user\":\"%s\",\"pass\":\"%s\"}",
             host ? host : "", port ? port : "445", share ? share : "",
             user ? user : "", pass ? pass : "");

    char *err = NULL;
    int rc = networkfs_mount(mount_id, 3 /* SMB */, config, &err);
    if (rc != 0) {
        fprintf(stderr, "mount failed (%d): %s\n", rc, err ? err : "no message");
    }
    if (err) networkfs_free(err);
    return rc;
}

// A write followed by a read back, entirely through the C ABI. This is the
// path a consumer takes and the only one that reaches setOutBytes.
static void test_write_then_read_back(void) {
    const int id = 1;
    if (mount_smb(id) != 0) {
        check(0, "mount against the test server");
        return;
    }
    check(1, "mount against the test server");

    const char *path = "/cabi-roundtrip.txt";
    const char *payload = "written through the C ABI";

    ByteSlice data;
    data.data = (char *)payload;
    data.len = strlen(payload);
    check(networkfs_writefile(id, (char *)path, data) == 0, "writefile succeeds");

    ByteSlice out;
    out.data = NULL;
    out.len = 0;
    int rc = networkfs_openfile(id, (char *)path, &out);

    check(rc == 0, "openfile succeeds");
    check(out.data != NULL, "openfile hands back a buffer");
    check(out.len == strlen(payload), "openfile reports the right length");
    if (out.data && out.len == strlen(payload)) {
        check(memcmp(out.data, payload, out.len) == 0, "the bytes survive the round trip");
    }
    if (out.data) networkfs_free(out.data);

    char *info = NULL;
    check(networkfs_stat(id, (char *)path, &info) == 0, "stat succeeds");
    if (info) {
        check(strstr(info, "cabi-roundtrip.txt") != NULL, "stat names the file");
        networkfs_free(info);
    }

    char *listing = NULL;
    check(networkfs_listdir(id, "/", &listing) == 0, "listdir succeeds");
    if (listing) {
        check(strstr(listing, "cabi-roundtrip.txt") != NULL, "the file appears in the listing");
        networkfs_free(listing);
    }

    check(networkfs_remove(id, (char *)path) == 0, "remove succeeds");
    check(networkfs_unmount(id) == 0, "unmount succeeds");
}

// An empty file is the edge of the ByteSlice contract: no bytes, and nothing
// for the caller to free.
static void test_empty_file_round_trip(void) {
    const int id = 2;
    if (mount_smb(id) != 0) return;

    const char *path = "/cabi-empty.txt";
    ByteSlice data;
    data.data = NULL;
    data.len = 0;
    check(networkfs_writefile(id, (char *)path, data) == 0, "writefile of an empty file succeeds");

    ByteSlice out;
    out.data = NULL;
    out.len = 1;
    check(networkfs_openfile(id, (char *)path, &out) == 0, "openfile of an empty file succeeds");
    check(out.len == 0, "an empty file reports zero length");
    if (out.data) networkfs_free(out.data);

    networkfs_remove(id, (char *)path);
    networkfs_unmount(id);
}

static void test_mkdir_and_rename(void) {
    const int id = 3;
    if (mount_smb(id) != 0) return;

    check(networkfs_mkdir(id, "/cabi-dir") == 0, "mkdir succeeds");
    check(networkfs_rename(id, "/cabi-dir", "/cabi-dir-moved") == 0, "rename succeeds");
    check(networkfs_remove(id, "/cabi-dir-moved") == 0, "remove of the renamed directory succeeds");
    networkfs_unmount(id);
}

// ---------------------------------------------------------------------------

// The ABI's contract is that the caller frees what it is given. Doing that a
// few thousand times is what turns a leak or a double free into a crash or an
// ASan report rather than something diskjockey meets in production.
static void test_free_contract_under_repetition(void) {
    for (int i = 0; i < 2000; i++) {
        char *v = networkfs_version();
        if (!v) { check(0, "version returned NULL under repetition"); return; }
        networkfs_free(v);

        char *err = NULL;
        networkfs_mount(1, 999, "{}", &err);
        if (err) networkfs_free(err);
    }
    check(1, "2000 allocate/free cycles across the boundary");
}

// Built with -cover, the archive exposes a flush hook. A c-archive has no Go
// main, so the runtime never writes its counters on the way out and the
// profile would otherwise be empty.
#ifdef NETWORKFS_COVERAGE
extern int networkfs_flush_coverage(char *dir);
#endif

int main(void) {
    test_version();
    test_free_tolerates_null();
    test_drivers_are_all_linked();
    test_mount_failure_codes();
    test_openfile_on_unknown_mount();
    test_writefile_on_unknown_mount();
    test_operations_on_unknown_mount();
    test_free_contract_under_repetition();

    if (have_server()) {
        test_write_then_read_back();
        test_empty_file_round_trip();
        test_mkdir_and_rename();
    } else {
        printf("no SMB_HOST set; skipping the mounted tests\n");
    }

    printf("%d checks, %d failures\n", checks, failures);

#ifdef NETWORKFS_COVERAGE
    {
        const char *dir = getenv("GOCOVERDIR");
        if (dir && networkfs_flush_coverage((char *)dir) != 0) {
            fprintf(stderr, "FAIL: could not write coverage counters\n");
            failures++;
        }
    }
#endif

    return failures == 0 ? 0 : 1;
}
