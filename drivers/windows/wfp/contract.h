#pragma once

// Shared with internal/wfp/policy.go. All integers are little endian; IP address
// bytes are in network order. No pointers or architecture-dependent fields.
#define AF_MAGIC 0x46575041u
#define AF_VERSION 2u
#define AF_MAX_RULES 64u
#define AF_MAX_PATH 1024u
#define AF_TAG 'fWpA'
#define IOCTL_AF_POLICY CTL_CODE(FILE_DEVICE_NETWORK, 0x801, METHOD_BUFFERED, FILE_WRITE_ACCESS)
#define IOCTL_AF_HEALTH CTL_CODE(FILE_DEVICE_NETWORK, 0x802, METHOD_BUFFERED, FILE_READ_ACCESS)

typedef struct _AF_RULE {
    UINT32 id;
    UINT32 helpers;
    UINT32 pathBytes;
    UINT32 reserved;
    WCHAR path[AF_MAX_PATH];
} AF_RULE;

typedef struct _AF_POLICY {
    UINT32 magic, version, size, count;
    UINT64 proxyPID;
    UINT16 port4, port6;
    UINT32 reserved;
    UCHAR cookie[16];
    AF_RULE rules[AF_MAX_RULES];
} AF_POLICY;

typedef struct _AF_CONTEXT {
    UINT32 magic, version, size, rule;
    UINT64 pid;
    UCHAR cookie[16];
    UINT16 family, port;
    UINT32 scope;
    UCHAR address[16];
    UINT32 pathBytes, reserved;
    WCHAR path[AF_MAX_PATH];
} AF_CONTEXT;

typedef struct _AF_HEALTH {
    UINT32 magic, version, size, active;
    UCHAR cookie[16];
    UINT32 ruleCount, filterCount;
} AF_HEALTH;

C_ASSERT(FIELD_OFFSET(AF_POLICY, rules) == 48);
C_ASSERT(sizeof(AF_RULE) == 2064);
C_ASSERT(sizeof(AF_CONTEXT) == 2120);
C_ASSERT(sizeof(AF_HEALTH) == 40);
