#pragma once

// REC-03: typed envelope client for the ProxyCoreService named pipe
// (PC-110 protocol, service path only). Wire contract — core/server/
// service_envelope_windows.go and docs/recovery/REC-03.md §1:
//   request  [u32 frameLen LE][libcore::RequestEnvelope]
//   response [u32 frameLen LE][libcore::ResponseEnvelope]
// The mandatory Hello opens every connection; Health / CheckConfig / Start /
// Stop follow. The client is deliberately UI-free: outcomes return as
// API::ServiceClient::Result, and the GUI turns them into messages. Only the
// CALLER's thread blocks (mutex + condition variable); all socket I/O runs
// on a dedicated QThread, so IPC never blocks the UI thread.
//
// Error model (three independent levels, see REC-03.md §1.4):
//   - transport: pipe absent / access denied / I/O broken (no answer exists);
//   - envelope code: ResponseEnvelope.code != 0 with the service message;
//   - typed payload: code == 0 but ErrorResp.error non-empty.
// Start/Stop are sent EXACTLY once per call. A break or local timeout after
// the request reached the wire leaves the outcome UNKNOWN
// (Result::outcomeKnown() == false): the caller must establish the actual
// state (Health) instead of silently retrying.

#ifndef Q_MOC_RUN
#include <core/server/gen/libcore.pb.h>
#endif

#include <QString>
#include <QThread>
#include <atomic>
#include <condition_variable>
#include <memory>
#include <mutex>
#include <string>

class QLocalSocket;

namespace API {

    class ServiceClient {
    public:
        ServiceClient();
        ~ServiceClient();

        // Keep in sync with serviceProtocolVersion (service_windows.go) and
        // configPolicyRevision (service_config_policy.go).
        static constexpr int kProtocolVersion = 1;
        static constexpr int kConfigPolicyRevision = 1;
        // Mirror of serviceMaxEnvelopeLen: an incoming frame longer than this
        // is a protocol violation and drops the connection — checked BEFORE
        // any buffer allocation.
        static constexpr quint32 kMaxEnvelopeLen = 32u * 1024u * 1024u;
        // Fixed, documented pipe name (defaultServicePipeName); the GUI client
        // does not honor THRONE_SERVICE_PIPE overrides.
        static QString pipeName() { return QStringLiteral("\\\\.\\pipe\\ProxyCoreService"); }

        enum class Outcome {
            Ok,             // code 0; for CheckConfig/Start/Stop also ErrorResp.error empty
            PayloadError,   // code 0, ErrorResp.error non-empty (executed, business error)
            EnvelopeError,  // code != 0 (refused by the service, message carried)
            AccessDenied,   // pipe connect refused access
            ConnectFailed,  // pipe absent / service not running / connect timed out
            TransportError, // connection broke (after connect): Start/Stop outcome UNKNOWN
            Timeout,        // local budget exhausted: Start/Stop outcome UNKNOWN
            ProtocolError,  // malformed stream / response id mismatch / oversized frame
            Busy,           // HealthTry only: another call holds the client
        };

        struct Result {
            Outcome outcome = Outcome::ConnectFailed;
            bool sent = false; // the request frame reached the wire
            int envelopeCode = -1;
            int serviceProtocolVersion = 0;
            QString message;

            // The service definitively executed the operation, or the request
            // never reached it. False only for a request that was sent and
            // whose outcome did not arrive.
            bool outcomeKnown() const { return executed() || !sent; }
            bool executed() const { return outcome == Outcome::Ok || outcome == Outcome::PayloadError; }
            bool ok() const { return outcome == Outcome::Ok; }
        };

        struct HelloInfo {
            int protocolVersion = 0;
            QString serviceVersion;
        };
        struct HealthInfo {
            bool runtimeRunning = false;
        };

        Result Hello(HelloInfo *info = nullptr, int timeoutMs = 10000);
        Result Health(HealthInfo *info = nullptr, int timeoutMs = 10000);
        // Non-blocking variant for pollers: Busy instead of queueing behind
        // an in-flight call.
        Result HealthTry(HealthInfo *info = nullptr, int timeoutMs = 10000);
        Result CheckConfig(const libcore::LoadConfigReq &request, int timeoutMs = 60000);
        Result Start(const libcore::LoadConfigReq &request, int timeoutMs = 60000);
        // Stop is idempotent at the service (REC-02 teardown ownership), yet
        // a transport-UNKNOWN result is still never auto-retried here — the
        // caller establishes the state via Health first.
        Result Stop(int timeoutMs = 15000);

        ServiceClient(const ServiceClient &) = delete;
        ServiceClient &operator=(const ServiceClient &) = delete;

    private:
        // The io-thread engine: owns the socket, the read buffer and the
        // single awaited call. The serialized public API (callMutex) keeps at
        // most one request in flight.
        struct Channel;
        std::unique_ptr<Channel> channel;

        std::mutex callMutex;
        std::atomic<std::uint64_t> nextRequestId{1};

        struct CallOutcome {
            Result result;
            // Present when a response envelope arrived for this request.
            std::unique_ptr<libcore::ResponseEnvelope> resp;
        };

        CallOutcome call(const QString &operation, std::string &&payloadBytes,
                         int timeoutMs, bool tryLock);
        Result healthImpl(HealthInfo *info, int timeoutMs, bool tryLock);

        Result decodeErrorPayload(const libcore::ResponseEnvelope &resp, Result result) const;
    };

    inline ServiceClient *defaultServiceClient;
} // namespace API
