// 固件原生指令隔离：在 JsonCpp 解析入口同步认领非空 final 对应的 dialog。
// 不操作声卡、播放器、音量或进程信号；仅阻止已认领 dialog 的下行动作进入派发。
// JsonCpp ABI 访问方式参考 open-speaker-llm/xiaomi_ai_llm（MIT，见 LICENSE.reference）。
#define _GNU_SOURCE
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

#ifndef GUARD_DIR
#define GUARD_DIR "/tmp/xiaoai-native-guard"
#endif
#define READER_SYMBOL "_ZN4Json9OurReader5parseEPKcS2_RNS_5ValueEb"
#define POLICY_LIMIT 8192

static const void *(*json_member)(const void *, const char *);
static const void *(*json_index)(const void *, unsigned);
static const char *(*json_string)(const void *);
static int (*json_type)(const void *), (*json_bool)(const void *);
static unsigned (*json_size)(const void *);
static int (*original_parse)(void *, const char *, const char *, void *, int);
static int ready;
static pthread_mutex_t guard_lock = PTHREAD_MUTEX_INITIALIZER;
static char blocked[128][96];
static unsigned cursor;

static const void *member(const void *v, const char *key) {
    return v && json_type(v) == 7 ? json_member(v, key) : NULL;
}
static const char *string_value(const void *v) {
    return v && json_type(v) == 4 ? json_string(v) : "";
}
static const char *string_member(const void *v, const char *key) {
    return string_value(member(v, key));
}
static int valid_id(const char *id) {
    size_t n = strlen(id);
    return n > 0 && n < sizeof(blocked[0]) &&
        strspn(id, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") == n;
}
static int nonblank(const char *text) {
    // 与 Go strings.TrimSpace 对齐 Unicode White_Space，避免空白 final 被认领。
    const unsigned char *s = (const unsigned char *)text;
    while (*s) {
        if ((*s >= 9 && *s <= 13) || *s == 32) { ++s; continue; }
        if (s[0] == 0xc2 && (s[1] == 0x85 || s[1] == 0xa0)) { s += 2; continue; }
        if (s[0] == 0xe1 && s[1] == 0x9a && s[2] == 0x80) { s += 3; continue; }
        if (s[0] == 0xe2 && ((s[1] == 0x80 && ((s[2] >= 0x80 && s[2] <= 0x8a) ||
            s[2] == 0xa8 || s[2] == 0xa9 || s[2] == 0xaf)) ||
            (s[1] == 0x81 && s[2] == 0x9f))) { s += 3; continue; }
        if (s[0] == 0xe3 && s[1] == 0x80 && s[2] == 0x80) { s += 3; continue; }
        return 1;
    }
    return 0;
}
static int remember(const char *id) {
    char path[256];
    unsigned slot = cursor++ % 128;
    if (*blocked[slot]) {
        snprintf(path, sizeof(path), GUARD_DIR "/claim-%s", blocked[slot]);
        unlink(path);
    }
    snprintf(path, sizeof(path), GUARD_DIR "/claim-%s", id);
    int fd = open(path, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC | O_NOFOLLOW, 0600);
    if (fd < 0) return 0;
    char pid[32];
    int n = snprintf(pid, sizeof(pid), "%d\n", (int)getpid());
    int ok = write(fd, pid, (size_t)n) == n;
    close(fd);
    if (!ok) { unlink(path); return 0; }
    snprintf(blocked[slot], sizeof(blocked[0]), "%s", id);
    return 1;
}
static void note(const char *action, const char *ns, const char *name, const char *id) {
    char line[384];
    int n = snprintf(line, sizeof(line), "🛡️ %s %s.%s dialog=%s\n", action, ns, name, id);
    int fd = open(GUARD_DIR "/events.log", O_WRONLY | O_CREAT | O_APPEND | O_CLOEXEC | O_NOFOLLOW, 0600);
    if (fd >= 0) {
        if (n > 0) (void)write(fd, line, (size_t)n < sizeof(line) ? (size_t)n : sizeof(line) - 1);
        close(fd);
    }
}

// policy 格式：版本、存活 client PID、截止 Unix 秒、逐行透传关键词。
// 只有短租约生效时才认领新 dialog；已认领 dialog 保留墓碑，挡住迟到/重试动作。
static int should_claim(const char *text) {
    char data[POLICY_LIMIT];
    struct stat st;
    int fd = open(GUARD_DIR "/policy", O_RDONLY | O_CLOEXEC | O_NOFOLLOW | O_NONBLOCK);
    if (fd < 0) return 0;
    int valid = !fstat(fd, &st) && S_ISREG(st.st_mode) && st.st_uid == geteuid() &&
        !(st.st_mode & 022) && st.st_size > 0 && st.st_size < POLICY_LIMIT;
    ssize_t n = valid ? read(fd, data, (size_t)st.st_size) : -1;
    close(fd);
    if (!valid || n != st.st_size) return 0;
    data[n] = 0;
    char *save = NULL, *version = strtok_r(data, "\n", &save);
    char *owner = strtok_r(NULL, "\n", &save), *expiry = strtok_r(NULL, "\n", &save), *end;
    if (!version || strcmp(version, "1") || !owner || !expiry) return 0;
    errno = 0;
    long pid = strtol(owner, &end, 10);
    if (errno || *end || pid <= 1 || pid > INT32_MAX || kill((pid_t)pid, 0)) return 0;
    errno = 0;
    long until = strtol(expiry, &end, 10);
    time_t now = time(NULL);
    if (errno || *end || until <= now || until - now > 20) return 0;
    for (char *keyword = strtok_r(NULL, "\n", &save); keyword; keyword = strtok_r(NULL, "\n", &save)) {
        if (*keyword && strstr(text, keyword)) return 0;
    }
    return 1;
}

static int receive_json(int ok, void *json) {
    if (!ok || !ready || !json) return ok;
    const void *header = member(json, "header"), *payload = member(json, "payload");
    const char *ns = string_member(header, "namespace"), *name = string_member(header, "name");
    const char *id = string_member(header, "dialog_id");
    if (!valid_id(id)) return ok;
    pthread_mutex_lock(&guard_lock);
    int owned = 0;
    for (unsigned i = 0; i < 128; ++i) if (!strcmp(id, blocked[i])) { owned = 1; break; }
    int result = !strcmp(ns, "SpeechRecognizer") && !strcmp(name, "RecognizeResult");
    if (!owned && result) {
        const void *final = member(payload, "is_final"), *results = member(payload, "results");
        if (final && json_type(final) == 5 && json_bool(final) && results &&
            json_type(results) == 6 && json_size(results) > 0) {
            note("观察终态", ns, name, id);
            const char *text = string_member(json_index(results, 0), "text");
            if (nonblank(text) && strlen(text) < 8192 && should_claim(text) && remember(id)) {
                owned = 1;
                note("认领", ns, name, id);
            }
        }
    }
    pthread_mutex_unlock(&guard_lock);
    if (!owned) return ok;
    // 保留 ASR 与结束/异常的生命周期，不允许续听、播放、音量或家电动作。
    int permit = result || (!strcmp(ns, "SpeechRecognizer") &&
        (!strcmp(name, "StopCapture") || !strcmp(name, "RecognizeStreamFinished"))) ||
        (!strcmp(ns, "Dialog") && !strcmp(name, "Finish")) ||
        (!strcmp(ns, "Nlp") && (!strcmp(name, "StartAnswer") || !strcmp(name, "FinishAnswer"))) ||
        (!strcmp(ns, "System") && (!strcmp(name, "Exception") || !strcmp(name, "Abort") ||
            !strcmp(name, "TruncationNotification")));
    if (!permit) note("拦截", ns, name, id);
    return permit ? ok : 0;
}

int reader_parse(void *reader, const char *start, const char *end, void *json, int comments)
    __asm__(READER_SYMBOL);
int reader_parse(void *reader, const char *start, const char *end, void *json, int comments) {
    if (!original_parse) original_parse = dlsym(RTLD_NEXT, READER_SYMBOL);
    return receive_json(original_parse ? original_parse(reader, start, end, json, comments) : 0, json);
}

static int resolve_json(void) {
#define LOAD(var, symbol) do { *(void **)(&var) = dlsym(RTLD_NEXT, symbol); if (!var) return 0; } while (0)
    LOAD(json_member, "_ZNK4Json5ValueixEPKc");
    LOAD(json_index, "_ZNK4Json5ValueixEj");
    LOAD(json_string, "_ZNK4Json5Value9asCStringEv");
    LOAD(json_type, "_ZNK4Json5Value4typeEv");
    LOAD(json_bool, "_ZNK4Json5Value6asBoolEv");
    LOAD(json_size, "_ZNK4Json5Value4sizeEv");
    LOAD(original_parse, READER_SYMBOL);
#undef LOAD
    return 1;
}

#ifndef GUARD_TEST
__attribute__((constructor)) static void initialize(void) {
    char exe[256];
    ssize_t n = readlink("/proc/self/exe", exe, sizeof(exe) - 1);
    if (n < 0) return;
    exe[n] = 0;
    if (strcmp(exe, "/usr/bin/mico_aivs_lab")) return;
    unsetenv("LD_PRELOAD"); // 不把钩子传给原生助手启动的其他程序。
    if (!resolve_json()) return;
    ready = 1;
    int fd = open(GUARD_DIR "/ready", O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC | O_NOFOLLOW, 0600);
    if (fd >= 0) {
        char pid[32];
        int len = snprintf(pid, sizeof(pid), "%d\n", (int)getpid());
        (void)write(fd, pid, (size_t)len);
        close(fd);
    }
    note("已加载", "guard", "ready", "-");
}
#endif
