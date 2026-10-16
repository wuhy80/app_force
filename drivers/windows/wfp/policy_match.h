#pragma once

// Nonpageable bounded policy helpers; also compiled by the portable C tests.
static BOOLEAN EqualPath(const WCHAR* a, const WCHAR* b, UINT32 chars) {
    UINT32 i;
    // WFP ALE_APP_ID and FwpmGetAppIdFromFileName both use canonical lowercase
    // device paths. Do not call pageable Unicode conversion in classification.
    for (i = 0; i < chars; ++i) if (a[i] != b[i]) return FALSE;
    return TRUE;
}

static const AF_RULE* Match(const AF_POLICY* policy, const FWP_BYTE_BLOB* app, UINT32* pathBytes) {
    UINT32 chars, i, j, dir, name;
    const WCHAR* path;
    static const WCHAR* const Helpers[] = {
        L"language_server_windows_x64.exe", L"language_server_windows_arm64.exe", L"language_server.exe"
    };
    static const UINT32 HelperLengths[] = { 31, 33, 19 };
    if (!app || !app->data || app->size % sizeof(WCHAR)) return NULL;
    chars = app->size / (UINT32)sizeof(WCHAR);
    path = (const WCHAR*)app->data;
    if (chars && path[chars-1] == L'\0') --chars;
    if (!chars || chars > AF_MAX_PATH) return NULL;
    *pathBytes = chars * (UINT32)sizeof(WCHAR);
    for (i = 0; i < policy->count; ++i) {
        const AF_RULE* r = &policy->rules[i];
        UINT32 ruleChars = r->pathBytes / (UINT32)sizeof(WCHAR);
        if (chars == ruleChars && EqualPath(path, r->path, chars)) return r;
        if (!r->helpers) continue;
        dir = ruleChars;
        while (dir && r->path[dir-1] != L'\\') --dir;
        if (!dir || chars <= dir || !EqualPath(path, r->path, dir)) continue;
        name = chars;
        while (name && path[name-1] != L'\\') --name;
        for (j = 0; j < RTL_NUMBER_OF(Helpers); ++j) {
            if (chars-name == HelperLengths[j] && EqualPath(path+name, Helpers[j], HelperLengths[j])) return r;
        }
    }
    return NULL;
}

static BOOLEAN Public4(UINT32 ip) {
    // WFP incoming IPv4 UINT32 is in host byte order.
    UCHAR a = (UCHAR)(ip >> 24), b = (UCHAR)(ip >> 16);
    return a != 0 && a != 10 && a != 127 && a < 224 &&
        !(a == 169 && b == 254) && !(a == 172 && b >= 16 && b <= 31) && !(a == 192 && b == 168);
}

static BOOLEAN Public6(const UCHAR* a) {
    UINT32 i;
    BOOLEAN zero = TRUE, mapped = TRUE;
    for (i = 0; i < 15; ++i) if (a[i]) zero = FALSE;
    for (i = 0; i < 10; ++i) if (a[i]) mapped = FALSE;
    return !(zero && a[15] <= 1) && !(mapped && a[10] == 0xff && a[11] == 0xff) &&
        a[0] != 0xff && (a[0] & 0xfe) != 0xfc && !(a[0] == 0xfe && (a[1] & 0xc0) == 0x80);
}
