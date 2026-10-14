/* Host test double for the C ABI, never linked or packaged into the app. */
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

typedef int (*ProtectFn)(int, void *);
static char *reply(int ok) { return strdup(ok ? "{\"ok\":true}" : "{\"ok\":false,\"error\":\"protection denied\"}"); }
char *AppForceCall(const char *request) { (void)request; return reply(1); }
char *AppForceStart(const char *directory, int tun_fd, void *callback, void *context) {
    (void)directory; (void)tun_fd;
    int fd = socket(AF_INET, SOCK_STREAM, 0);
    int result = fd < 0 ? -1 : ((ProtectFn)callback)(fd, context);
    if (fd >= 0) close(fd);
    return reply(result == 0);
}
char *AppForceStop(void) { return reply(1); }
void AppForceFree(void *data) { free(data); }
