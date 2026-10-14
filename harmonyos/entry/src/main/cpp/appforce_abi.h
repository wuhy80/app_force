#pragma once

#ifdef __cplusplus
extern "C" {
#endif

typedef int (*AppForceProtectFn)(int socket_fd, void *context);
typedef char *(*AppForceCallFn)(const char *request);
typedef char *(*AppForceStartFn)(const char *directory, int tun_fd, void *protect, void *context);
typedef char *(*AppForceStopFn)(void);
typedef void (*AppForceFreeFn)(void *pointer);

#ifdef __cplusplus
}
#endif
