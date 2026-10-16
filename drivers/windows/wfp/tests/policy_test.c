// Run with scripts/test-wfp-policy.sh. -fshort-wchar reproduces Windows UTF-16
// literals on the development host; no WDK API behavior is simulated here.
#include <assert.h>
#include <stddef.h>
#include <stdint.h>
#include <string.h>
#include <stdio.h>

typedef uint8_t UCHAR;
typedef uint16_t UINT16;
typedef uint32_t UINT32;
typedef uint64_t UINT64;
typedef __WCHAR_TYPE__ WCHAR;
typedef int BOOLEAN;
typedef struct { UINT32 size; UCHAR* data; } FWP_BYTE_BLOB;
#define TRUE 1
#define FALSE 0
#define C_ASSERT(x) _Static_assert((x), #x)
#define FIELD_OFFSET(t, f) offsetof(t, f)
#define RTL_NUMBER_OF(x) (sizeof(x)/sizeof((x)[0]))
#include "../contract.h"
#include "../policy_match.h"

static AF_POLICY Policy;
static UINT32 chars(const WCHAR* s) { UINT32 n = 0; while (s[n]) ++n; return n; }
static void rule(UINT32 index, const WCHAR* path, UINT32 helpers) {
    AF_RULE* r = &Policy.rules[index];
    r->id = index+1; r->helpers = helpers; r->pathBytes = chars(path)*2;
    memcpy(r->path, path, r->pathBytes);
    Policy.count = index+1;
}
static UINT32 match(const WCHAR* path) {
    FWP_BYTE_BLOB app = { (chars(path)+1)*2, (UCHAR*)path };
    UINT32 pathBytes = 0;
    const AF_RULE* r = Match(&Policy, &app, &pathBytes);
    if (r) assert(pathBytes == chars(path)*2);
    return r ? r->id : 0;
}
int main(void) {
    rule(0, L"\\device\\harddiskvolume3\\apps\\antigravity\\antigravity.exe", 1);
    rule(1, L"\\device\\harddiskvolume3\\tools\\chosen.exe", 0);
    assert(match(L"\\device\\harddiskvolume3\\apps\\antigravity\\antigravity.exe") == 1);
    assert(match(L"\\device\\harddiskvolume3\\apps\\antigravity\\resources\\v2\\language_server_windows_x64.exe") == 1);
    assert(match(L"\\device\\harddiskvolume3\\apps\\antigravity\\language_server_windows_arm64.exe") == 1);
    assert(match(L"\\device\\harddiskvolume3\\apps\\antigravity\\language_server.exe") == 1);
    assert(match(L"\\device\\harddiskvolume3\\apps\\antigravity\\node.exe") == 0);
    assert(match(L"\\device\\harddiskvolume3\\apps\\antigravity\\builder.exe") == 0);
    assert(match(L"\\device\\harddiskvolume3\\apps\\antigravity-other\\language_server.exe") == 0);
    assert(match(L"\\device\\harddiskvolume4\\apps\\antigravity\\language_server.exe") == 0);
    assert(match(L"\\device\\harddiskvolume3\\tools\\chosen.exe") == 2);
    assert(match(L"\\device\\harddiskvolume3\\tools\\language_server.exe") == 0);
    assert(match(L"\\device\\harddiskvolume3\\tools\\unchosen.exe") == 0);
    {
        WCHAR path[AF_MAX_PATH+2] = {0};
        FWP_BYTE_BLOB app = { sizeof(path), (UCHAR*)path };
        UINT32 bytes = 0;
        assert(Match(&Policy, &app, &bytes) == NULL);
        app.size = 3;
        assert(Match(&Policy, &app, &bytes) == NULL);
    }
    assert(Public4(0x08080808));
    assert(!Public4(0x7f000001)); assert(!Public4(0x0a000001));
    assert(!Public4(0xac100001)); assert(!Public4(0xc0a80101));
    assert(!Public4(0xa9fe0001)); assert(!Public4(0xe0000001));
    {
        UCHAR public6[16] = {0x20, 0x01, 0x48, 0x60};
        UCHAR loopback[16] = {0};
        UCHAR local[16] = {0xfd};
        UCHAR linklocal[16] = {0xfe, 0x80};
        UCHAR multicast[16] = {0xff};
        UCHAR mapped[16] = {0};
        loopback[15] = 1; mapped[10] = mapped[11] = 0xff; mapped[12] = 8;
        assert(Public6(public6)); assert(!Public6(loopback)); assert(!Public6(local));
        assert(!Public6(linklocal)); assert(!Public6(multicast)); assert(!Public6(mapped));
    }
    puts("WFP kernel policy boundaries and C/Go wire sizes passed (not a WDK build).");
    return 0;
}
