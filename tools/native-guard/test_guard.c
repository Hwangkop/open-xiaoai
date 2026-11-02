// 在音箱独立进程中验证真实 JsonCpp ABI 和真实 CharReader -> OurReader 钩子调用。
// 不开麦、不调用云端、不向运行中语音进程发信号。
#define GUARD_TEST 1
#define GUARD_DIR "/tmp/xiaoai-native-guard-unit"
#include "native_guard.c"
#undef NDEBUG // Zig 的优化构建可能定义 NDEBUG；验证程序必须执行全部断言。
#include <assert.h>

typedef union { uint64_t align; unsigned char bytes[128]; } json_storage;
static void (*value_ctor)(void *, int), (*value_dtor)(void *);
static void *char_reader;
static unsigned cases;
static int parse(const char *record) {
    json_storage json;
    value_ctor(&json, 0);
    void **vtable = *(void ***)char_reader;
    int (*fn)(void *, const char *, const char *, void *, void *) = (void *)vtable[2];
    int ok = fn(char_reader, record, record + strlen(record), &json, NULL);
    value_dtor(&json);
    return ok;
}
static void check(const char *ns, const char *name, const char *dialog, const char *payload, int expected) {
    char record[2048];
    snprintf(record, sizeof(record), "{\"header\":{\"namespace\":\"%s\",\"name\":\"%s\",\"dialog_id\":\"%s\"},\"payload\":%s}", ns, name, dialog, payload);
    int got = parse(record);
    if (got != expected) fprintf(stderr, "失败：%s.%s %s got=%d expected=%d\n", ns, name, dialog, got, expected);
    assert(got == expected);
    ++cases;
}
static void policy(int lifetime) {
    FILE *f = fopen(GUARD_DIR "/policy", "w");
    assert(f);
    fprintf(f, "1\n%d\n%ld\n开灯\n", (int)getpid(), (long)time(NULL) + lifetime);
    assert(!fclose(f));
    assert(!chmod(GUARD_DIR "/policy", 0600));
}
int main(void) {
    assert(!mkdir(GUARD_DIR, 0700));
    // Zig 的 libc 桩可能省略未直接引用的 pthread DSO；固件 SDK 在 dlopen 时需要它。
    assert(dlopen("libpthread.so.0", RTLD_NOW | RTLD_GLOBAL));
    void *sdk = dlopen("/usr/lib/libaivs_sdk.so", RTLD_NOW | RTLD_GLOBAL);
    if (!sdk) { fprintf(stderr, "%s\n", dlerror()); return 2; }
    assert(resolve_json());
    ready = 1;
    *(void **)(&value_ctor) = dlsym(RTLD_NEXT, "_ZN4Json5ValueC1ENS_9ValueTypeE");
    *(void **)(&value_dtor) = dlsym(RTLD_NEXT, "_ZN4Json5ValueD1Ev");
    void (*builder_ctor)(void *) = dlsym(RTLD_NEXT, "_ZN4Json17CharReaderBuilderC1Ev");
    void (*builder_dtor)(void *) = dlsym(RTLD_NEXT, "_ZN4Json17CharReaderBuilderD1Ev");
    void *(*new_reader)(const void *) = dlsym(RTLD_NEXT, "_ZNK4Json17CharReaderBuilder13newCharReaderEv");
    assert(value_ctor && value_dtor && builder_ctor && builder_dtor && new_reader);
    json_storage builder;
    builder_ctor(&builder);
    char_reader = new_reader(&builder);
    assert(char_reader);

    const char *final = "{\"is_final\":true,\"results\":[{\"text\":\"现在几点\"}]}";
    check("SpeechRecognizer", "RecognizeResult", "disabled", final, 1);
    check("SpeechSynthesizer", "Speak", "disabled", "{}", 1);
    policy(15);
    check("SpeechRecognizer", "RecognizeResult", "owned", final, 1);
    const char *spaces[] = {"SpeechSynthesizer", "SpeechSynthesizer", "AudioPlayer", "PlaybackController", "Speaker", "MiotController", "Execution", "Dialog"};
    const char *names[] = {"Speak", "SpeakStream", "Play", "SetProperty", "AdjustVolume", "Operate", "Group", "ExpectSpeech"};
    for (unsigned i = 0; i < 8; ++i) check(spaces[i], names[i], "owned", "{}", 0);
    check("Dialog", "Finish", "owned", "{}", 1);
    check("Nlp", "FinishAnswer", "owned", "{}", 1);
    check("SpeechRecognizer", "StopCapture", "owned", "{}", 1);
    check("SpeechRecognizer", "RecognizeResult", "empty", "{\"is_final\":true,\"results\":[{\"text\":\"\"}]}", 1);
    check("SpeechSynthesizer", "Speak", "empty", "{}", 1);
    check("SpeechRecognizer", "RecognizeResult", "blank", "{\"is_final\":true,\"results\":[{\"text\":\" \\t　\"}]}", 1);
    check("SpeechSynthesizer", "Speak", "blank", "{}", 1);
    check("SpeechRecognizer", "RecognizeResult", "partial", "{\"is_final\":false,\"results\":[{\"text\":\"测试\"}]}", 1);
    check("AudioPlayer", "Play", "partial", "{}", 1);
    check("SpeechRecognizer", "RecognizeResult", "native", "{\"is_final\":true,\"results\":[{\"text\":\"请开灯\"}]}", 1);
    check("MiotController", "Operate", "native", "{}", 1);
    check("Speaker", "AdjustVolume", "other-dialog", "{}", 1);
    policy(-1);
    check("SpeechRecognizer", "RecognizeResult", "expired", final, 1);
    check("SpeechSynthesizer", "Speak", "expired", "{}", 1);
    check("SpeechSynthesizer", "Speak", "owned", "{}", 0); // 已认领轮次仍挡迟到动作。
    check("SpeechRecognizer", "RecognizeResult", "owned", final, 1); // SDK 重复解析仍放行 final。
    check("SpeechSynthesizer", "Speak", "owned", "{}", 0);
    policy(15);
    assert(!chmod(GUARD_DIR "/policy", 0666));
    check("SpeechRecognizer", "RecognizeResult", "unsafe-policy", final, 1);
    check("AudioPlayer", "Play", "unsafe-policy", "{}", 1);
    assert(!unlink(GUARD_DIR "/policy"));
    check("SpeechSynthesizer", "Speak", "unclaimed", "{}", 1);
    void **vtable = *(void ***)char_reader;
    void (*destroy)(void *) = (void *)vtable[1];
    destroy(char_reader);
    builder_dtor(&builder);
    unlink(GUARD_DIR "/events.log");
    unlink(GUARD_DIR "/claim-owned");
    assert(!rmdir(GUARD_DIR));
    printf("通过：真实固件 ABI、同步解析钩子及 %u 项会话隔离用例\n", cases);
    return 0;
}
