#include <pthread.h>
#include <pthread/qos.h>
#include <sys/qos.h>
#include <stdio.h>
#include <unistd.h>

int main(void) {
    unsigned before = (unsigned)qos_class_self();
    int result = pthread_set_qos_class_self_np(QOS_CLASS_USER_INITIATED, 0);
    printf("{\"pid\":%d,\"uid\":%d,\"before\":%u,\"requested\":%u,\"setter_result\":%d,\"after\":%u}\n",
           getpid(), getuid(), before, (unsigned)QOS_CLASS_USER_INITIATED,
           result, (unsigned)qos_class_self());
    fflush(stdout);
    for (;;) sleep(1);
}
