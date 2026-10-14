#include "appforce_abi.h"
#include "napi/native_api.h"

#include <chrono>
#include <condition_variable>
#include <dlfcn.h>
#include <memory>
#include <mutex>
#include <string>
#include <unistd.h>

namespace {
constexpr size_t MAX_REQUEST = 16 * 1024 * 1024;
std::mutex libraryMutex;
std::mutex operationMutex;
void *library = nullptr;
AppForceCallFn coreCall = nullptr;
AppForceStartFn coreStart = nullptr;
AppForceStopFn coreStop = nullptr;
AppForceFreeFn coreFree = nullptr;

bool LoadCore()
{
    std::lock_guard<std::mutex> guard(libraryMutex);
    if (library) return true;
    void *handle = dlopen("libappforce_core.so", RTLD_NOW | RTLD_LOCAL);
    if (!handle) return false;
    auto call = reinterpret_cast<AppForceCallFn>(dlsym(handle, "AppForceCall"));
    auto start = reinterpret_cast<AppForceStartFn>(dlsym(handle, "AppForceStart"));
    auto stop = reinterpret_cast<AppForceStopFn>(dlsym(handle, "AppForceStop"));
    auto free = reinterpret_cast<AppForceFreeFn>(dlsym(handle, "AppForceFree"));
    if (!call || !start || !stop || !free) {
        dlclose(handle);
        return false;
    }
    coreCall = call; coreStart = start; coreStop = stop; coreFree = free;
    library = handle;
    // A Go shared runtime stays loaded for the process lifetime.
    return true;
}

std::string TakeReply(char *raw)
{
    if (!raw) return R"({"ok":false,"error":"内核返回为空"})";
    std::string result(raw);
    coreFree(raw);
    return result;
}

struct ProtectRequest {
    explicit ProtectRequest(int descriptor) : fd(descriptor) {}
    ~ProtectRequest() { if (fd >= 0) close(fd); }
    std::mutex mutex;
    std::condition_variable changed;
    int fd;
    bool done = false;
    bool succeeded = false;

    void Finish(bool ok)
    {
        std::lock_guard<std::mutex> guard(mutex);
        if (done) return;
        done = true; succeeded = ok;
        if (fd >= 0) { close(fd); fd = -1; }
        changed.notify_all();
    }
};

using RequestHolder = std::shared_ptr<ProtectRequest>;
struct Protector { napi_threadsafe_function callback = nullptr; };
std::unique_ptr<Protector> sessionProtector;

void FinalizeHolder(napi_env, void *data, void *) { delete static_cast<RequestHolder *>(data); }

napi_value ResolveProtection(napi_env env, napi_callback_info info)
{
    void *data = nullptr;
    napi_get_cb_info(env, info, nullptr, nullptr, nullptr, &data);
    static_cast<RequestHolder *>(data)->get()->Finish(true);
    napi_value result; napi_get_undefined(env, &result); return result;
}

napi_value RejectProtection(napi_env env, napi_callback_info info)
{
    void *data = nullptr;
    napi_get_cb_info(env, info, nullptr, nullptr, nullptr, &data);
    static_cast<RequestHolder *>(data)->get()->Finish(false);
    napi_value result; napi_get_undefined(env, &result); return result;
}

bool MakeHandler(napi_env env, napi_callback callback, const RequestHolder &request, napi_value *out)
{
    auto *holder = new RequestHolder(request);
    if (napi_create_function(env, nullptr, 0, callback, holder, out) != napi_ok) {
        delete holder; return false;
    }
    if (napi_add_finalizer(env, *out, holder, FinalizeHolder, nullptr, nullptr) != napi_ok) {
        // Creation failed before the handler is exposed to JavaScript.
        delete holder; return false;
    }
    return true;
}

void DispatchProtection(napi_env env, napi_value callback, void *, void *data)
{
    std::unique_ptr<RequestHolder> holder(static_cast<RequestHolder *>(data));
    const auto request = *holder;
    if (!env || !callback) { request->Finish(false); return; }
    napi_value fd, receiver, promise;
    napi_create_int32(env, request->fd, &fd);
    napi_get_undefined(env, &receiver);
    if (napi_call_function(env, receiver, callback, 1, &fd, &promise) != napi_ok) {
        bool pending = false; napi_is_exception_pending(env, &pending);
        if (pending) { napi_value exception; napi_get_and_clear_last_exception(env, &exception); }
        request->Finish(false); return;
    }
    bool isPromise = false;
    napi_is_promise(env, promise, &isPromise);
    if (!isPromise) { request->Finish(false); return; }
    napi_value then, handlers[2], ignored;
    if (napi_get_named_property(env, promise, "then", &then) != napi_ok ||
        !MakeHandler(env, ResolveProtection, request, &handlers[0]) ||
        !MakeHandler(env, RejectProtection, request, &handlers[1]) ||
        napi_call_function(env, promise, then, 2, handlers, &ignored) != napi_ok) {
        bool pending = false; napi_is_exception_pending(env, &pending);
        if (pending) { napi_value exception; napi_get_and_clear_last_exception(env, &exception); }
        request->Finish(false);
    }
}

int ProtectSocket(int fd, void *context)
{
    auto *protector = static_cast<Protector *>(context);
    // Protect a duplicate of the same socket. If the asynchronous call times out,
    // it must never affect a newly reused descriptor after Go closes the original.
    const int duplicate = dup(fd);
    if (duplicate < 0) return -1;
    auto request = std::make_shared<ProtectRequest>(duplicate);
    auto *payload = new RequestHolder(request);
    if (napi_call_threadsafe_function(protector->callback, payload, napi_tsfn_nonblocking) != napi_ok) {
        delete payload; return -1;
    }
    std::unique_lock<std::mutex> guard(request->mutex);
    if (!request->changed.wait_for(guard, std::chrono::seconds(10), [&] { return request->done; })) return -1;
    return request->succeeded ? 0 : -1;
}

enum class Operation { Call, Start, Stop };
struct Work {
    napi_env env;
    napi_async_work work = nullptr;
    napi_deferred deferred = nullptr;
    Operation operation;
    std::string input;
    int fd = -1;
    std::unique_ptr<Protector> protector;
    std::string result;
};

void ReleaseProtector(std::unique_ptr<Protector> &protector)
{
    if (protector && protector->callback) napi_release_threadsafe_function(protector->callback, napi_tsfn_release);
    protector.reset();
}

void Execute(napi_env, void *data)
{
    auto &job = *static_cast<Work *>(data);
    if (!LoadCore()) {
        job.result = R"({"ok":false,"error":"代理内核未安装或加载失败，请检查应用构建"})";
        ReleaseProtector(job.protector); return;
    }
    if (job.operation == Operation::Call) { job.result = TakeReply(coreCall(job.input.c_str())); return; }
    std::lock_guard<std::mutex> guard(operationMutex);
    if (job.operation == Operation::Stop) {
        job.result = TakeReply(coreStop());
        ReleaseProtector(sessionProtector);
    } else {
        if (sessionProtector) {
            job.result = R"({"ok":false,"error":"代理尚未停止"})";
            ReleaseProtector(job.protector); return;
        }
        job.result = TakeReply(coreStart(job.input.c_str(), job.fd,
            reinterpret_cast<void *>(ProtectSocket), job.protector.get()));
        // Replies are serialized by Go with `ok` as the first field.
        if (job.result.rfind("{\"ok\":true", 0) == 0) sessionProtector = std::move(job.protector);
        else ReleaseProtector(job.protector);
    }
}

void Complete(napi_env env, napi_status status, void *data)
{
    std::unique_ptr<Work> job(static_cast<Work *>(data));
    if (status != napi_ok) job->result = R"({"ok":false,"error":"原生操作已取消"})";
    napi_value value;
    napi_create_string_utf8(env, job->result.c_str(), job->result.size(), &value);
    napi_resolve_deferred(env, job->deferred, value);
    napi_delete_async_work(env, job->work);
}

bool ReadString(napi_env env, napi_value value, std::string &out)
{
    size_t length = 0;
    if (napi_get_value_string_utf8(env, value, nullptr, 0, &length) != napi_ok || length > MAX_REQUEST) return false;
    out.resize(length + 1);
    if (napi_get_value_string_utf8(env, value, out.data(), out.size(), &length) != napi_ok) return false;
    out.resize(length);
    return out.find('\0') == std::string::npos;
}

napi_value Queue(napi_env env, std::unique_ptr<Work> job)
{
    napi_value promise, label;
    napi_create_promise(env, &job->deferred, &promise);
    napi_create_string_utf8(env, "AppForce", NAPI_AUTO_LENGTH, &label);
    if (napi_create_async_work(env, nullptr, label, Execute, Complete, job.get(), &job->work) != napi_ok ||
        napi_queue_async_work(env, job->work) != napi_ok) {
        ReleaseProtector(job->protector);
        if (job->work) napi_delete_async_work(env, job->work);
        napi_throw_error(env, nullptr, "无法调度内核操作"); return nullptr;
    }
    job.release(); return promise;
}

napi_value Available(napi_env env, napi_callback_info)
{
    napi_value value; napi_get_boolean(env, LoadCore(), &value); return value;
}

napi_value Call(napi_env env, napi_callback_info info)
{
    size_t argc = 1; napi_value argv[1];
    napi_get_cb_info(env, info, &argc, argv, nullptr, nullptr);
    auto job = std::make_unique<Work>(); job->env = env; job->operation = Operation::Call;
    if (argc != 1 || !ReadString(env, argv[0], job->input)) { napi_throw_type_error(env, nullptr, "请求格式无效"); return nullptr; }
    return Queue(env, std::move(job));
}

napi_value Start(napi_env env, napi_callback_info info)
{
    size_t argc = 3; napi_value argv[3], label;
    napi_get_cb_info(env, info, &argc, argv, nullptr, nullptr);
    auto job = std::make_unique<Work>(); job->env = env; job->operation = Operation::Start;
    napi_valuetype callbackType;
    if (argc != 3 || !ReadString(env, argv[0], job->input) ||
        napi_get_value_int32(env, argv[1], &job->fd) != napi_ok || job->fd <= 0 ||
        napi_typeof(env, argv[2], &callbackType) != napi_ok || callbackType != napi_function) {
        napi_throw_type_error(env, nullptr, "VPN 启动参数无效"); return nullptr;
    }
    job->protector = std::make_unique<Protector>();
    napi_create_string_utf8(env, "AppForceSocketProtect", NAPI_AUTO_LENGTH, &label);
    if (napi_create_threadsafe_function(env, argv[2], nullptr, label, 64, 1, nullptr, nullptr,
        nullptr, DispatchProtection, &job->protector->callback) != napi_ok) {
        napi_throw_error(env, nullptr, "无法创建 socket 保护回调"); return nullptr;
    }
    return Queue(env, std::move(job));
}

napi_value Stop(napi_env env, napi_callback_info)
{
    auto job = std::make_unique<Work>(); job->env = env; job->operation = Operation::Stop;
    return Queue(env, std::move(job));
}

napi_value Init(napi_env env, napi_value exports)
{
    napi_property_descriptor properties[] = {
        {"available", nullptr, Available, nullptr, nullptr, nullptr, napi_default, nullptr},
        {"call", nullptr, Call, nullptr, nullptr, nullptr, napi_default, nullptr},
        {"start", nullptr, Start, nullptr, nullptr, nullptr, napi_default, nullptr},
        {"stop", nullptr, Stop, nullptr, nullptr, nullptr, napi_default, nullptr},
    };
    napi_define_properties(env, exports, sizeof(properties) / sizeof(properties[0]), properties);
    return exports;
}

napi_module module = {1, 0, nullptr, Init, "appforce", nullptr, {0}};
} // namespace

extern "C" __attribute__((constructor)) void RegisterAppForce()
{
    napi_module_register(&module);
}
