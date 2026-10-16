// Windows 10+ WFP TCP prototype. No packet/stream callouts are registered:
// unrelated applications' payload never enters this driver or user mode.
#include <ntddk.h>
#include <initguid.h>
#include <fwpsk.h>
#include <fwpmk.h>
#include <ws2ipdef.h>
#include <wdmsec.h>
#include "contract.h"
#include "policy_match.h"

DEFINE_GUID(AF_DEVICE, 0x943bf4d2,0x3483,0x4793,0xa3,0x21,0x6b,0x8f,0x64,0x15,0xd2,0x0c);
DEFINE_GUID(AF_SUBLAYER, 0x5460d484,0x2411,0x4349,0x97,0x5e,0xd9,0x4e,0x6d,0x73,0x40,0xae);
DEFINE_GUID(AF_TCP4, 0x38fd7375,0x950a,0x49f6,0x9a,0x1e,0x0a,0x1a,0xbb,0xa3,0x90,0x01);
DEFINE_GUID(AF_TCP6, 0x38fd7375,0x950a,0x49f6,0x9a,0x1e,0x0a,0x1a,0xbb,0xa3,0x90,0x02);
DEFINE_GUID(AF_UDP4, 0x38fd7375,0x950a,0x49f6,0x9a,0x1e,0x0a,0x1a,0xbb,0xa3,0x90,0x03);
DEFINE_GUID(AF_UDP6, 0x38fd7375,0x950a,0x49f6,0x9a,0x1e,0x0a,0x1a,0xbb,0xa3,0x90,0x04);

static const GUID* const Callouts[] = { &AF_TCP4, &AF_TCP6, &AF_UDP4, &AF_UDP6 };
static const GUID* const Layers[] = {
    &FWPM_LAYER_ALE_CONNECT_REDIRECT_V4, &FWPM_LAYER_ALE_CONNECT_REDIRECT_V6,
    &FWPM_LAYER_ALE_AUTH_CONNECT_V4, &FWPM_LAYER_ALE_AUTH_CONNECT_V6
};

typedef struct _AF_STATE { EX_RUNDOWN_REF rundown; AF_POLICY policy; } AF_STATE;
static PDEVICE_OBJECT Device;
static UINT32 CalloutIDs[4];
static HANDLE RedirectHandle, Engine;
static KSPIN_LOCK StateLock;
static KMUTEX ControlLock;
static AF_STATE* State;

DRIVER_INITIALIZE DriverEntry;
DRIVER_UNLOAD Unload;
DRIVER_DISPATCH Dispatch;

static AF_STATE* AcquireState(void) {
    KIRQL irql;
    AF_STATE* state;
    KeAcquireSpinLock(&StateLock, &irql);
    state = State;
    if (state && !ExAcquireRundownProtection(&state->rundown)) state = NULL;
    KeReleaseSpinLock(&StateLock, irql);
    return state;
}

// Called at PASSIVE_LEVEL under ControlLock. Closing the one exclusive control
// handle (also on process death) deletes all nonpersistent WFP objects.
static void StopPolicy(void) {
    AF_STATE* state;
    KIRQL irql;
    KeAcquireSpinLock(&StateLock, &irql);
    state = State;
    State = NULL;
    KeReleaseSpinLock(&StateLock, irql);
    if (Engine) { FwpmEngineClose0(Engine); Engine = NULL; }
    if (state) {
        ExWaitForRundownProtectionRelease(&state->rundown);
        ExFreePoolWithTag(state, AF_TAG);
    }
}

static void Block(FWPS_CLASSIFY_OUT0* out) {
    out->actionType = FWP_ACTION_BLOCK;
    out->rights &= ~FWPS_RIGHT_ACTION_WRITE;
}

static void NTAPI Classify(const FWPS_INCOMING_VALUES0* values,
    const FWPS_INCOMING_METADATA_VALUES0* meta, void* layerData, const void* classifyContext,
    const FWPS_FILTER1* filter, UINT64 flowContext, FWPS_CLASSIFY_OUT0* out) {
    AF_STATE* state;
    const AF_RULE* rule;
    const FWP_BYTE_BLOB* app;
    UINT32 appIndex, ipIndex, portIndex, pathBytes = 0;
    BOOLEAN v6, udp;
    UINT64 handle = 0;
    FWPS_CONNECT_REQUEST0* request = NULL;
    AF_CONTEXT* context = NULL;
    NTSTATUS status;
    UNREFERENCED_PARAMETER(layerData);
    UNREFERENCED_PARAMETER(flowContext);
    if (!(out->rights & FWPS_RIGHT_ACTION_WRITE)) return;
    out->actionType = FWP_ACTION_CONTINUE;
    state = AcquireState();
    if (!state) return;
    if (!FWPS_IS_METADATA_FIELD_PRESENT(meta, FWPS_METADATA_FIELD_PROCESS_ID) || meta->processId == state->policy.proxyPID) goto Done;
    udp = values->layerId == FWPS_LAYER_ALE_AUTH_CONNECT_V4 || values->layerId == FWPS_LAYER_ALE_AUTH_CONNECT_V6;
    v6 = values->layerId == FWPS_LAYER_ALE_CONNECT_REDIRECT_V6 || values->layerId == FWPS_LAYER_ALE_AUTH_CONNECT_V6;
    if (udp) {
        appIndex = v6 ? FWPS_FIELD_ALE_AUTH_CONNECT_V6_ALE_APP_ID : FWPS_FIELD_ALE_AUTH_CONNECT_V4_ALE_APP_ID;
        ipIndex = v6 ? FWPS_FIELD_ALE_AUTH_CONNECT_V6_IP_REMOTE_ADDRESS : FWPS_FIELD_ALE_AUTH_CONNECT_V4_IP_REMOTE_ADDRESS;
        portIndex = v6 ? FWPS_FIELD_ALE_AUTH_CONNECT_V6_IP_REMOTE_PORT : FWPS_FIELD_ALE_AUTH_CONNECT_V4_IP_REMOTE_PORT;
    } else {
        appIndex = v6 ? FWPS_FIELD_ALE_CONNECT_REDIRECT_V6_ALE_APP_ID : FWPS_FIELD_ALE_CONNECT_REDIRECT_V4_ALE_APP_ID;
        ipIndex = v6 ? FWPS_FIELD_ALE_CONNECT_REDIRECT_V6_IP_REMOTE_ADDRESS : FWPS_FIELD_ALE_CONNECT_REDIRECT_V4_IP_REMOTE_ADDRESS;
        portIndex = v6 ? FWPS_FIELD_ALE_CONNECT_REDIRECT_V6_IP_REMOTE_PORT : FWPS_FIELD_ALE_CONNECT_REDIRECT_V4_IP_REMOTE_PORT;
    }
    if (values->incomingValue[portIndex].value.uint16 == 53) goto Done;
    if (v6) {
        if (!Public6(values->incomingValue[ipIndex].value.byteArray16->byteArray16)) goto Done;
    } else if (!Public4(values->incomingValue[ipIndex].value.uint32)) goto Done;
    app = values->incomingValue[appIndex].value.byteBlob;
    rule = Match(&state->policy, app, &pathBytes);
    if (!rule) goto Done;
    if (udp) { Block(out); goto Done; }
    if (FWPS_IS_METADATA_FIELD_PRESENT(meta, FWPS_METADATA_FIELD_REDIRECT_RECORD_HANDLE)) {
        void* priorContext = NULL;
        FWPS_CONNECTION_REDIRECT_STATE redirectState = FwpsQueryConnectionRedirectState0(meta->redirectRecords, RedirectHandle, &priorContext);
        if (redirectState == FWPS_CONNECTION_REDIRECTED_BY_SELF || redirectState == FWPS_CONNECTION_PREVIOUSLY_REDIRECTED_BY_SELF) goto Done;
        // Another proxy may already own this selected connection. Fail closed
        // rather than route to its loopback endpoint or produce a proxy cycle.
        if (redirectState == FWPS_CONNECTION_REDIRECTED_BY_OTHER) { Block(out); goto Done; }
    }
    status = FwpsAcquireClassifyHandle0((void*)classifyContext, 0, &handle);
    if (!NT_SUCCESS(status)) { Block(out); goto Done; }
    status = FwpsAcquireWritableLayerDataPointer0(handle, filter->filterId, 0, (void**)&request, out);
    if (!NT_SUCCESS(status)) { Block(out); goto Release; }
    context = ExAllocatePool2(POOL_FLAG_NON_PAGED, sizeof(AF_CONTEXT), AF_TAG);
    if (!context) { Block(out); goto Apply; }
    context->magic = AF_MAGIC; context->version = AF_VERSION; context->size = sizeof(AF_CONTEXT);
    context->rule = rule->id; context->pid = meta->processId;
    context->pathBytes = pathBytes;
    RtlCopyMemory(context->path, app->data, pathBytes);
    RtlCopyMemory(context->cookie, state->policy.cookie, sizeof(context->cookie));
    if (v6) {
        SOCKADDR_IN6* address = (SOCKADDR_IN6*)&request->remoteAddressAndPort;
        context->family = 6; context->port = RtlUshortByteSwap(address->sin6_port);
        context->scope = address->sin6_scope_id;
        RtlCopyMemory(context->address, &address->sin6_addr, 16);
        RtlZeroMemory(address, sizeof(*address));
        address->sin6_family = AF_INET6; address->sin6_addr.u.Byte[15] = 1;
        address->sin6_port = RtlUshortByteSwap(state->policy.port6);
    } else {
        SOCKADDR_IN* address = (SOCKADDR_IN*)&request->remoteAddressAndPort;
        context->family = 4; context->port = RtlUshortByteSwap(address->sin_port);
        RtlCopyMemory(context->address, &address->sin_addr, 4);
        RtlZeroMemory(address, sizeof(*address));
        address->sin_family = AF_INET; address->sin_addr.S_un.S_addr = RtlUlongByteSwap(0x7f000001);
        address->sin_port = RtlUshortByteSwap(state->policy.port4);
    }
    request->localRedirectTargetPID = (UINT32)state->policy.proxyPID;
    request->localRedirectHandle = RedirectHandle;
    // Ownership transfers to WFP, which releases this nonpaged allocation when
    // the redirected flow ends. Never free it in the classify callback.
    request->localRedirectContext = context;
    request->localRedirectContextSize = sizeof(AF_CONTEXT);
    out->actionType = FWP_ACTION_PERMIT;
Apply:
    FwpsApplyModifiedLayerData0(handle, request, 0);
Release:
    FwpsReleaseClassifyHandle0(handle);
Done:
    ExReleaseRundownProtection(&state->rundown);
}

static NTSTATUS NTAPI Notify(FWPS_CALLOUT_NOTIFY_TYPE type, const GUID* key, FWPS_FILTER1* filter) {
    UNREFERENCED_PARAMETER(type); UNREFERENCED_PARAMETER(key); UNREFERENCED_PARAMETER(filter);
    return STATUS_SUCCESS;
}

static NTSTATUS InstallPolicy(const void* input, ULONG length, PIRP irp) {
    const AF_POLICY* policy = input;
    AF_STATE* state;
    FWPM_SESSION0 session = {0};
    FWPM_SUBLAYER0 sublayer = {0};
    NTSTATUS status;
    UINT32 i, j;
    KIRQL irql;
    if (State || Engine) return STATUS_DEVICE_BUSY;
    if (length < FIELD_OFFSET(AF_POLICY, rules) || policy->magic != AF_MAGIC || policy->version != AF_VERSION ||
        policy->count == 0 || policy->count > AF_MAX_RULES || policy->size != length ||
        length != FIELD_OFFSET(AF_POLICY, rules) + policy->count*sizeof(AF_RULE) ||
        !policy->port4 || !policy->port6 || policy->reserved ||
        policy->proxyPID != (UINT64)(ULONG_PTR)IoGetRequestorProcessId(irp)) return STATUS_INVALID_PARAMETER;
    for (i = 0; i < policy->count; ++i) {
        const AF_RULE* r = &policy->rules[i];
        UINT32 chars = r->pathBytes / (UINT32)sizeof(WCHAR), name;
        if (r->id != i+1 || r->helpers > 1 || r->reserved || r->pathBytes % sizeof(WCHAR) ||
            chars < 9 || chars > AF_MAX_PATH || !EqualPath(r->path, L"\\device\\", 8)) return STATUS_INVALID_PARAMETER;
        for (j = 0; j < chars; ++j) if (!r->path[j]) return STATUS_INVALID_PARAMETER;
        name = chars;
        while (name && r->path[name-1] != L'\\') --name;
        if (r->helpers && !((chars-name == 15 && EqualPath(r->path+name, L"antigravity.exe", 15)) ||
            (chars-name == 19 && EqualPath(r->path+name, L"antigravity ide.exe", 19)))) return STATUS_INVALID_PARAMETER;
    }
    state = ExAllocatePool2(POOL_FLAG_NON_PAGED, sizeof(AF_STATE), AF_TAG);
    if (!state) return STATUS_INSUFFICIENT_RESOURCES;
    ExInitializeRundownProtection(&state->rundown);
    RtlCopyMemory(&state->policy, policy, length);
    session.flags = FWPM_SESSION_FLAG_DYNAMIC;
    status = FwpmEngineOpen0(NULL, RPC_C_AUTHN_WINNT, NULL, &session, &Engine);
    if (!NT_SUCCESS(status)) goto Fail;
    status = FwpmTransactionBegin0(Engine, 0);
    if (!NT_SUCCESS(status)) goto Fail;
    sublayer.subLayerKey = AF_SUBLAYER;
    sublayer.displayData.name = L"AppForce selected applications";
    sublayer.weight = 0x8000;
    status = FwpmSubLayerAdd0(Engine, &sublayer, NULL);
    if (!NT_SUCCESS(status)) goto Abort;
    for (i = 0; i < RTL_NUMBER_OF(Callouts); ++i) {
        FWPM_CALLOUT0 callout = {0};
        FWPM_FILTER0 filter = {0};
        FWPM_FILTER_CONDITION0 protocol = {0};
        callout.calloutKey = *Callouts[i]; callout.applicableLayer = *Layers[i];
        callout.displayData.name = L"AppForce TCP prototype";
        status = FwpmCalloutAdd0(Engine, &callout, NULL, NULL);
        if (!NT_SUCCESS(status)) goto Abort;
        protocol.fieldKey = FWPM_CONDITION_IP_PROTOCOL; protocol.matchType = FWP_MATCH_EQUAL;
        protocol.conditionValue.type = FWP_UINT8;
        protocol.conditionValue.uint8 = i < 2 ? IPPROTO_TCP : IPPROTO_UDP;
        filter.layerKey = *Layers[i]; filter.subLayerKey = AF_SUBLAYER;
        filter.displayData.name = L"AppForce selected connections only";
        filter.action.type = FWP_ACTION_CALLOUT_UNKNOWN;
        filter.action.calloutKey = *Callouts[i]; filter.weight.type = FWP_EMPTY;
        filter.numFilterConditions = 1; filter.filterCondition = &protocol;
        status = FwpmFilterAdd0(Engine, &filter, NULL, NULL);
        if (!NT_SUCCESS(status)) goto Abort;
    }
    // Publish before committing so no installed filter can see an empty state.
    KeAcquireSpinLock(&StateLock, &irql); State = state; KeReleaseSpinLock(&StateLock, irql);
    status = FwpmTransactionCommit0(Engine);
    if (!NT_SUCCESS(status)) { StopPolicy(); return status; }
    return STATUS_SUCCESS;
Abort:
    FwpmTransactionAbort0(Engine);
Fail:
    if (Engine) { FwpmEngineClose0(Engine); Engine = NULL; }
    ExFreePoolWithTag(state, AF_TAG);
    return status;
}

NTSTATUS Dispatch(PDEVICE_OBJECT device, PIRP irp) {
    PIO_STACK_LOCATION stack = IoGetCurrentIrpStackLocation(irp);
    NTSTATUS status = STATUS_SUCCESS;
    UNREFERENCED_PARAMETER(device);
    KeWaitForSingleObject(&ControlLock, Executive, KernelMode, FALSE, NULL);
    switch (stack->MajorFunction) {
    case IRP_MJ_CREATE: break;
    case IRP_MJ_CLOSE: break;
    case IRP_MJ_CLEANUP: StopPolicy(); break;
    case IRP_MJ_DEVICE_CONTROL:
        if (stack->Parameters.DeviceIoControl.IoControlCode != IOCTL_AF_POLICY) status = STATUS_INVALID_DEVICE_REQUEST;
        else status = InstallPolicy(irp->AssociatedIrp.SystemBuffer, stack->Parameters.DeviceIoControl.InputBufferLength, irp);
        break;
    default: status = STATUS_INVALID_DEVICE_REQUEST;
    }
    KeReleaseMutex(&ControlLock, FALSE);
    irp->IoStatus.Status = status; irp->IoStatus.Information = 0;
    IoCompleteRequest(irp, IO_NO_INCREMENT);
    return status;
}

void Unload(PDRIVER_OBJECT driver) {
    UNICODE_STRING link = RTL_CONSTANT_STRING(L"\\DosDevices\\AppForceWfp");
    UINT32 i;
    UNREFERENCED_PARAMETER(driver);
    KeWaitForSingleObject(&ControlLock, Executive, KernelMode, FALSE, NULL); StopPolicy(); KeReleaseMutex(&ControlLock, FALSE);
    for (i = 0; i < RTL_NUMBER_OF(CalloutIDs); ++i) if (CalloutIDs[i]) { FwpsCalloutUnregisterById0(CalloutIDs[i]); CalloutIDs[i] = 0; }
    if (RedirectHandle) { FwpsRedirectHandleDestroy0(RedirectHandle); RedirectHandle = NULL; }
    IoDeleteSymbolicLink(&link);
    if (Device) { IoDeleteDevice(Device); Device = NULL; }
}

NTSTATUS DriverEntry(PDRIVER_OBJECT driver, PUNICODE_STRING registryPath) {
    UNICODE_STRING name = RTL_CONSTANT_STRING(L"\\Device\\AppForceWfp");
    UNICODE_STRING link = RTL_CONSTANT_STRING(L"\\DosDevices\\AppForceWfp");
    UNICODE_STRING sddl = RTL_CONSTANT_STRING(L"D:P(A;;GA;;;SY)(A;;GA;;;BA)");
    NTSTATUS status;
    UINT32 i;
    UNREFERENCED_PARAMETER(registryPath);
    KeInitializeSpinLock(&StateLock); KeInitializeMutex(&ControlLock, 0);
    driver->DriverUnload = Unload;
    for (i = 0; i <= IRP_MJ_MAXIMUM_FUNCTION; ++i) driver->MajorFunction[i] = Dispatch;
    status = IoCreateDeviceSecure(driver, 0, &name, FILE_DEVICE_NETWORK, FILE_DEVICE_SECURE_OPEN, TRUE, &sddl, &AF_DEVICE, &Device);
    if (!NT_SUCCESS(status)) return status;
    status = IoCreateSymbolicLink(&link, &name);
    if (!NT_SUCCESS(status)) goto Fail;
    status = FwpsRedirectHandleCreate0(&AF_DEVICE, 0, &RedirectHandle);
    if (!NT_SUCCESS(status)) goto Fail;
    for (i = 0; i < RTL_NUMBER_OF(Callouts); ++i) {
        FWPS_CALLOUT1 callout = {0};
        callout.calloutKey = *Callouts[i]; callout.classifyFn = Classify; callout.notifyFn = Notify;
        status = FwpsCalloutRegister1(Device, &callout, &CalloutIDs[i]);
        if (!NT_SUCCESS(status)) goto Fail;
    }
    Device->Flags &= ~DO_DEVICE_INITIALIZING;
    return STATUS_SUCCESS;
Fail:
    Unload(driver);
    return status;
}
