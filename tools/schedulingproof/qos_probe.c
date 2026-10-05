#include <errno.h>
#include <pthread.h>
#include <pthread/qos.h>
#include <sys/qos.h>
#include <stdio.h>
#include <sys/resource.h>
#include <unistd.h>

/* A disposable native fixture, never the daemon's Go-thread QoS claim. */
int main(void) {
    errno = 0;
    int priority = getpriority(PRIO_PROCESS, 0);
    printf("{\"pid\":%d,\"uid\":%d,\"nice\":%d,\"errno\":%d,"
           "\"qos_main\":%u,\"qos_self\":%u}\n",
           getpid(), getuid(), priority, errno,
           (unsigned)qos_class_main(), (unsigned)qos_class_self());
    fflush(stdout);
    for (;;) sleep(1);
}
