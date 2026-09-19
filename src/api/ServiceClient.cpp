#include "include/api/ServiceClient.h"

#include <QLocalSocket>
#include <QMetaObject>
#include <QObject>
#include <QtEndian>

#include <chrono>
#include <condition_variable>
#include <cstring>
#include <mutex>
#include <vector>

namespace API {

    namespace {
        using std::chrono::milliseconds;
        using std::chrono::steady_clock;

        constexpr int kConnectBudgetMs = 5000;
        constexpr int kHandshakeBudgetMs = 10000;
        // Local wait slack over the request deadline: the service answers at
        // least the envelope-level refusals (deadline exceeded, stale
        // revision) instead of never answering.
        constexpr int kWaitSlackMs = 2000;

        qint64 epochMsNow() {
            return std::chrono::duration_cast<std::chrono::milliseconds>(
                       std::chrono::system_clock::now().time_since_epoch())
                .count();
        }

        std::vector<std::byte> stringToBytes(const std::string &s) {
            const auto *from = reinterpret_cast<const std::byte *>(s.data());
            return {from, from + s.size()};
        }

        std::vector<std::uint8_t> bytesToU8(const std::vector<std::byte> &in) {
            const auto *from = reinterpret_cast<const std::uint8_t *>(in.data());
            return {from, from + in.size()};
        }

        QByteArray frameOf(const std::string &body) {
            QByteArray frame;
            frame.resize(qint64(4 + body.size()));
            qToLittleEndian<quint32>(quint32(body.size()), frame.data());
            std::memcpy(frame.data() + 4, body.data(), body.size());
            return frame;
        }

        template <typename T>
        bool decodePayload(const std::vector<std::byte> &bytes, T &out) {
            try {
                out = spb::pb::deserialize<T>(bytesToU8(bytes));
                return true;
            } catch (const std::exception &) {
                return false;
            } catch (...) {
                return false;
            }
        }
    } // namespace

    struct ServiceClient::Channel {
        QThread *io_thread = nullptr;
        QObject *io_anchor = nullptr;
        QLocalSocket *sock = nullptr; // io_thread-only
        QByteArray read_buf;          // io_thread-only

        std::mutex mu;
        std::condition_variable cv;
        // Guarded by mu:
        std::uint64_t awaitedId = 0; // 0 = nobody waits
        bool responseDone = false;
        bool responseOk = false;     // a matching envelope arrived
        bool streamDead = false;     // the connection was given up
        bool streamViolated = false; // ...because of a protocol violation
        QString transportMessage;
        libcore::ResponseEnvelope resp;
        // End of the mu-guarded block.

        std::atomic<bool> connected{false};
        std::atomic<bool> handshaked{false};

        Channel() {
            io_thread = new QThread;
            io_anchor = new QObject;
            io_anchor->moveToThread(io_thread);
            io_thread->start();
        }

        ~Channel() {
            // Drain the io thread, then stop it. The socket is parented to
            // the anchor and dies with it on the (already stopped) io thread.
            QMetaObject::invokeMethod(
                io_anchor, [this] { resetSocket(); }, Qt::BlockingQueuedConnection);
            io_thread->quit();
            io_thread->wait();
            delete io_anchor;
            delete io_thread;
        }

        // io_thread-only.
        void resetSocket() {
            if (sock != nullptr) {
                sock->disconnect(io_anchor);
                sock->close();
                sock->deleteLater();
                sock = nullptr;
            }
            read_buf.clear();
            connected.store(false);
            handshaked.store(false);
        }

        // io_thread-only: close and wake a waiter as a protocol violation.
        void protocolViolation(const char *what) {
            resetSocket();
            std::lock_guard<std::mutex> lock(mu);
            streamDead = true;
            streamViolated = true;
            responseDone = true;
            responseOk = false;
            transportMessage = QString::fromLatin1(what);
            cv.notify_all();
        }

        // Caller-thread classification of a woken-but-empty response.
        ServiceClient::Outcome transportOutcome() {
            std::lock_guard<std::mutex> lock(mu);
            return streamViolated ? ServiceClient::Outcome::ProtocolError : ServiceClient::Outcome::TransportError;
        }

        // io_thread-only.
        void onReadyRead() {
            if (sock == nullptr) return;
            read_buf += sock->readAll();
            while (true) {
                if (read_buf.size() < 4) return;
                const quint32 frameLen = qFromLittleEndian<quint32>(read_buf.constData());
                // Size check BEFORE any allocation, mirroring the service's
                // own readServiceEnvelope bound.
                if (frameLen > ServiceClient::kMaxEnvelopeLen) {
                    protocolViolation("response frame exceeds the envelope limit");
                    return;
                }
                if (read_buf.size() < qint64(4 + frameLen)) return; // partial frame: wait for more

                libcore::ResponseEnvelope envelope;
                const bool parsed = decodePayload(
                    std::vector<std::byte>(
                        reinterpret_cast<const std::byte *>(read_buf.constData() + 4),
                        reinterpret_cast<const std::byte *>(read_buf.constData() + 4 + frameLen)),
                    envelope);
                read_buf.remove(0, qint64(4 + frameLen));
                if (!parsed) {
                    protocolViolation("malformed response envelope");
                    return;
                }

                std::unique_lock<std::mutex> lock(mu);
                const auto id = envelope.request_id.value_or(0);
                if (awaitedId == 0 || id != awaitedId) {
                    // A response nobody waits for (a late answer to an
                    // already timed-out request): attributing the NEXT answer
                    // would be unsafe — drop the connection.
                    lock.unlock();
                    protocolViolation("response with an unexpected request id");
                    return;
                }
                resp = std::move(envelope);
                responseOk = true;
                responseDone = true;
                awaitedId = 0;
                lock.unlock();
                cv.notify_all();
            }
        }

        // io_thread-only.
        void onDisconnected() {
            connected.store(false);
            handshaked.store(false);
            std::lock_guard<std::mutex> lock(mu);
            streamDead = true;
            responseDone = true;
            responseOk = false;
            if (transportMessage.isEmpty()) transportMessage = QStringLiteral("connection closed by the service");
            cv.notify_all();
        }

        // Caller-thread helpers ------------------------------------------

        void resetAsync() {
            QMetaObject::invokeMethod(
                io_anchor, [this] { resetSocket(); }, Qt::BlockingQueuedConnection);
            std::lock_guard<std::mutex> lock(mu);
            responseDone = false;
            responseOk = false;
            streamDead = false;
            streamViolated = false;
            transportMessage.clear();
            awaitedId = 0;
        }

        void armAwaiting(std::uint64_t id) {
            std::lock_guard<std::mutex> lock(mu);
            awaitedId = id;
            responseDone = false;
            responseOk = false;
            streamDead = false;
            streamViolated = false;
            transportMessage.clear();
        }

        // Returns true on success; on failure flags a wake for the caller.
        bool connectOnIoThread(int budgetMs) {
            const auto shared = std::make_shared<std::atomic<bool>>(false);
            QMetaObject::invokeMethod(
                io_anchor,
                [this, shared, budgetMs] {
                    if (sock == nullptr) {
                        sock = new QLocalSocket(io_anchor);
                        QObject::connect(sock, &QLocalSocket::readyRead, io_anchor, [this] { onReadyRead(); });
                        QObject::connect(sock, &QLocalSocket::disconnected, io_anchor, [this] { onDisconnected(); });
                    }
                    sock->close();
                    read_buf.clear();
                    sock->connectToServer(ServiceClient::pipeName());
                    if (!sock->waitForConnected(budgetMs)) {
                        connected.store(false);
                        QString what;
                        switch (sock->error()) {
                            case QLocalSocket::SocketAccessError:
                                what = QStringLiteral("access to the service pipe is denied");
                                break;
                            case QLocalSocket::ServerNotFoundError:
                            case QLocalSocket::ConnectionRefusedError:
                                what = QStringLiteral("the service pipe does not exist (is ProxyCoreService running?)");
                                break;
                            case QLocalSocket::SocketTimeoutError:
                                what = QStringLiteral("the service did not accept the connection in time");
                                break;
                            default:
                                what = sock->errorString();
                                break;
                        }
                        std::lock_guard<std::mutex> lock(mu);
                        streamDead = true;
                        responseDone = true;
                        responseOk = false;
                        transportMessage = what;
                        cv.notify_all();
                        shared->store(false);
                        return;
                    }
                    connected.store(true);
                    shared->store(true);
                },
                Qt::BlockingQueuedConnection);
            return shared->load();
        }

        // Returns true when the frame was accepted by the socket; on failure
        // flags a wake for the caller.
        bool sendOnIoThread(const QByteArray &frame) {
            const auto shared = std::make_shared<std::atomic<bool>>(true);
            QMetaObject::invokeMethod(
                io_anchor,
                [this, shared, frame] {
                    if (sock == nullptr || !connected.load()) {
                        std::lock_guard<std::mutex> lock(mu);
                        streamDead = true;
                        responseDone = true;
                        responseOk = false;
                        transportMessage = QStringLiteral("the service pipe is not connected");
                        cv.notify_all();
                        shared->store(false);
                        return;
                    }
                    const auto written = sock->write(frame);
                    sock->flush();
                    if (written != frame.size() || sock->error() != QLocalSocket::UnknownSocketError) {
                        std::lock_guard<std::mutex> lock(mu);
                        streamDead = true;
                        responseDone = true;
                        responseOk = false;
                        transportMessage = QStringLiteral("writing the request to the service pipe failed");
                        cv.notify_all();
                        shared->store(false);
                    }
                },
                Qt::BlockingQueuedConnection);
            return shared->load();
        }

        // Caller thread: waits for the armed request to be answered.
        // On return with `answered`, the mu-guarded flags are final.
        bool waitAnswer(std::unique_lock<std::mutex> &lock, steady_clock::time_point until) {
            return cv.wait_until(lock, until, [this] { return responseDone; });
        }
    };

    ServiceClient::ServiceClient()
        : channel(std::make_unique<Channel>()) {}

    ServiceClient::~ServiceClient() = default;

    ServiceClient::CallOutcome ServiceClient::call(const QString &operation, std::string &&payloadBytes,
                                                   int timeoutMs, bool tryLock) {
        CallOutcome out;

        std::unique_lock<std::mutex> callLock(callMutex, std::defer_lock);
        if (tryLock) {
            if (!callLock.try_lock()) {
                out.result.outcome = Outcome::Busy;
                return out;
            }
        } else {
            callLock.lock();
        }

        auto &ch = *channel;
        const auto deadline = steady_clock::now() + milliseconds(timeoutMs);

        // ---- ensure a connected, handshaken pipe (a broken one reconnects) --
        if (!ch.connected.load() || !ch.handshaked.load()) {
            ch.resetAsync();

            const auto remainingMs = std::chrono::duration_cast<milliseconds>(deadline - steady_clock::now()).count();
            if (remainingMs <= 0) {
                out.result.outcome = Outcome::Timeout;
                return out;
            }
            if (!ch.connectOnIoThread(int(std::min<long long>(kConnectBudgetMs, remainingMs))) || !ch.connected.load()) {
                // The io thread flagged the failure (or the connect silently
                // failed): surface the classification it recorded.
                std::unique_lock<std::mutex> lock(ch.mu);
                out.result.outcome = Outcome::ConnectFailed;
                out.result.message = ch.transportMessage;
                return out;
            }

            // Hello: the mandatory first frame of every connection.
            libcore::RequestEnvelope env;
            env.protocol_version = kProtocolVersion;
            env.request_id = nextRequestId.fetch_add(1);
            env.operation = "Hello";
            env.deadline_unix_ms = epochMsNow() + kHandshakeBudgetMs;
            env.expected_policy_revision = kConfigPolicyRevision;

            std::string envBytes;
            QByteArray frame;
            try {
                libcore::HandshakeReq helloPayload;
                env.typed_payload = stringToBytes(spb::pb::serialize<std::string>(helloPayload));
                envBytes = spb::pb::serialize<std::string>(env);
            } catch (...) {
                out.result.outcome = Outcome::ProtocolError;
                out.result.message = QStringLiteral("cannot serialize the Hello envelope");
                return out;
            }
            frame = frameOf(envBytes);

            ch.armAwaiting(env.request_id.value());
            if (!ch.sendOnIoThread(frame)) {
                std::lock_guard<std::mutex> lock(ch.mu);
                ch.awaitedId = 0;
                out.result.outcome = Outcome::TransportError;
                out.result.message = ch.transportMessage;
                return out;
            }
            bool helloDone = false;
            {
                std::unique_lock<std::mutex> lock(ch.mu);
                helloDone = ch.waitAnswer(lock, deadline + milliseconds(kWaitSlackMs));
                if (!helloDone) ch.awaitedId = 0;
                else if (ch.responseOk) out.resp = std::make_unique<libcore::ResponseEnvelope>(ch.resp);
                else out.result.message = ch.transportMessage;
            }
            if (!helloDone || !out.resp) {
                // The handshake did not complete: the connection is not
                // trusted for an operation request.
                ch.resetAsync();
                out.result.outcome = helloDone ? ch.transportOutcome() : Outcome::Timeout;
                if (out.result.message.isEmpty()) {
                    out.result.message = QStringLiteral("the service handshake did not complete");
                }
                return out;
            }
            // Validate the handshake answer.
            const auto &helloResp = *out.resp;
            out.result.envelopeCode = helloResp.code.value_or(-1);
            out.result.serviceProtocolVersion = helloResp.service_protocol_version.value_or(0);
            if (helloResp.code.value_or(-1) != 0) {
                out.result.outcome = Outcome::EnvelopeError;
                out.result.message = QString::fromStdString(helloResp.message.value_or(std::string()));
                ch.resetAsync();
                return out;
            }
            libcore::HandshakeResp handshake;
            if (!decodePayload(helloResp.typed_payload.value_or(std::vector<std::byte>{}), handshake)) {
                ch.resetAsync();
                out.result.outcome = Outcome::ProtocolError;
                out.result.message = QStringLiteral("malformed HandshakeResp payload");
                return out;
            }
            if (handshake.protocol_version.value_or(0) != kProtocolVersion ||
                helloResp.service_protocol_version.value_or(0) != kProtocolVersion) {
                ch.resetAsync();
                out.result.outcome = Outcome::EnvelopeError;
                out.result.envelopeCode = 1;
                out.result.message = QStringLiteral("service protocol version %1, client %2")
                                         .arg(helloResp.service_protocol_version.value_or(0))
                                         .arg(kProtocolVersion);
                return out;
            }
            ch.handshaked.store(true);
            out.resp.reset();
        }

        // ---- the operation itself: exactly one send -----------------------
        libcore::RequestEnvelope env;
        env.protocol_version = kProtocolVersion;
        env.request_id = nextRequestId.fetch_add(1);
        env.operation = operation.toStdString();
        env.deadline_unix_ms = epochMsNow() +
                               std::chrono::duration_cast<milliseconds>(deadline - steady_clock::now()).count();
        env.expected_policy_revision = kConfigPolicyRevision;
        if (!payloadBytes.empty()) env.typed_payload = stringToBytes(payloadBytes);

        std::string envBytes;
        try {
            envBytes = spb::pb::serialize<std::string>(env);
        } catch (...) {
            out.result.outcome = Outcome::ProtocolError;
            out.result.message = QStringLiteral("cannot serialize the request envelope");
            return out;
        }

        ch.armAwaiting(env.request_id.value());
        out.result.sent = ch.sendOnIoThread(frameOf(envBytes));
        if (!out.result.sent) {
            std::lock_guard<std::mutex> lock(ch.mu);
            ch.awaitedId = 0;
            out.result.outcome = Outcome::TransportError;
            out.result.message = ch.transportMessage;
            return out;
        }

        bool answered = false;
        {
            std::unique_lock<std::mutex> lock(ch.mu);
            answered = ch.waitAnswer(lock, deadline + milliseconds(kWaitSlackMs));
            if (!answered) {
                // The answer may still arrive later. The connection is dropped
                // so the late response can never be misattributed to another
                // request; the next call reconnects (and re-handshakes).
                ch.awaitedId = 0;
                lock.unlock();
                QMetaObject::invokeMethod(
                    ch.io_anchor, [&ch] { ch.resetSocket(); }, Qt::QueuedConnection);
                out.result.outcome = Outcome::Timeout;
                out.result.message = QStringLiteral("the service did not answer within the request deadline");
            } else if (ch.responseOk) {
                out.resp = std::make_unique<libcore::ResponseEnvelope>(ch.resp);
            } else {
                out.result.outcome = ch.transportOutcome();
                out.result.message = ch.transportMessage;
            }
        }
        if (!answered) return out;
        if (!out.resp) {
            if (out.result.outcome == Outcome::TransportError || out.result.outcome == Outcome::ProtocolError) {
                return out;
            }
            out.result.outcome = Outcome::ProtocolError;
            if (out.result.message.isEmpty()) out.result.message = QStringLiteral("no response envelope");
            return out;
        }

        // ---- classify the envelope ---------------------------------------
        const auto &resp = *out.resp;
        out.result.envelopeCode = resp.code.value_or(-1);
        out.result.serviceProtocolVersion = resp.service_protocol_version.value_or(0);
        if (resp.code.value_or(-1) != 0) {
            out.result.outcome = Outcome::EnvelopeError;
            out.result.message = QString::fromStdString(resp.message.value_or(std::string()));
            return out;
        }
        out.result.outcome = Outcome::Ok;
        return out;
    }

    ServiceClient::Result ServiceClient::decodeErrorPayload(const libcore::ResponseEnvelope &resp,
                                                            Result result) const {
        // Code 0 for CheckConfig/Start/Stop carries ErrorResp; an error there
        // is an EXECUTED operation with a business failure — distinct from an
        // envelope-level refusal.
        libcore::ErrorResp err;
        if (!decodePayload(resp.typed_payload.value_or(std::vector<std::byte>{}), err)) {
            result.outcome = Outcome::ProtocolError;
            result.message = QStringLiteral("malformed ErrorResp payload");
            return result;
        }
        const auto text = QString::fromStdString(err.error.value_or(std::string()));
        if (!text.isEmpty()) {
            result.outcome = Outcome::PayloadError;
            result.message = text;
        }
        return result;
    }

    ServiceClient::Result ServiceClient::Hello(HelloInfo *info, int timeoutMs) {
        libcore::HandshakeReq req;
        std::string payload;
        try {
            payload = spb::pb::serialize<std::string>(req);
        } catch (...) {
            Result r;
            r.outcome = Outcome::ProtocolError;
            r.message = QStringLiteral("cannot serialize the request");
            return r;
        }
        auto out = call(QStringLiteral("Hello"), std::move(payload), timeoutMs, false);
        if (out.result.ok() && out.resp != nullptr) {
            libcore::HandshakeResp handshake;
            if (!decodePayload(out.resp->typed_payload.value_or(std::vector<std::byte>{}), handshake)) {
                Result r;
                r.sent = out.result.sent;
                r.outcome = Outcome::ProtocolError;
                r.message = QStringLiteral("malformed HandshakeResp payload");
                return r;
            }
            if (handshake.protocol_version.value_or(0) != kProtocolVersion) {
                Result r;
                r.sent = out.result.sent;
                r.outcome = Outcome::EnvelopeError;
                r.envelopeCode = 1;
                r.message = QStringLiteral("service protocol version %1, client %2")
                                .arg(handshake.protocol_version.value_or(0))
                                .arg(kProtocolVersion);
                return r;
            }
            if (info != nullptr) {
                info->protocolVersion = handshake.protocol_version.value_or(0);
                info->serviceVersion = QString::fromStdString(handshake.service_version.value_or(std::string()));
            }
        }
        return out.result;
    }

    ServiceClient::Result ServiceClient::healthImpl(HealthInfo *info, int timeoutMs, bool tryLock) {
        libcore::EmptyReq req;
        std::string payload;
        try {
            payload = spb::pb::serialize<std::string>(req);
        } catch (...) {
            Result r;
            r.outcome = Outcome::ProtocolError;
            r.message = QStringLiteral("cannot serialize the request");
            return r;
        }
        auto out = call(QStringLiteral("Health"), std::move(payload), timeoutMs, tryLock);
        if (out.result.ok() && out.resp != nullptr) {
            libcore::HealthResp health;
            if (!decodePayload(out.resp->typed_payload.value_or(std::vector<std::byte>{}), health)) {
                Result r;
                r.sent = out.result.sent;
                r.outcome = Outcome::ProtocolError;
                r.message = QStringLiteral("malformed HealthResp payload");
                return r;
            }
            if (info != nullptr) info->runtimeRunning = health.runtime_running.value_or(false);
        }
        return out.result;
    }

    ServiceClient::Result ServiceClient::Health(HealthInfo *info, int timeoutMs) {
        return healthImpl(info, timeoutMs, false);
    }

    ServiceClient::Result ServiceClient::HealthTry(HealthInfo *info, int timeoutMs) {
        return healthImpl(info, timeoutMs, true);
    }

    ServiceClient::Result ServiceClient::CheckConfig(const libcore::LoadConfigReq &request, int timeoutMs) {
        std::string payload;
        try {
            payload = spb::pb::serialize<std::string>(request);
        } catch (...) {
            Result r;
            r.outcome = Outcome::ProtocolError;
            r.message = QStringLiteral("cannot serialize the request");
            return r;
        }
        auto out = call(QStringLiteral("CheckConfig"), std::move(payload), timeoutMs, false);
        if (out.result.ok() && out.resp != nullptr) out.result = decodeErrorPayload(*out.resp, out.result);
        return out.result;
    }

    ServiceClient::Result ServiceClient::Start(const libcore::LoadConfigReq &request, int timeoutMs) {
        std::string payload;
        try {
            payload = spb::pb::serialize<std::string>(request);
        } catch (...) {
            Result r;
            r.outcome = Outcome::ProtocolError;
            r.message = QStringLiteral("cannot serialize the request");
            return r;
        }
        auto out = call(QStringLiteral("Start"), std::move(payload), timeoutMs, false);
        if (out.result.ok() && out.resp != nullptr) out.result = decodeErrorPayload(*out.resp, out.result);
        return out.result;
    }

    ServiceClient::Result ServiceClient::Stop(int timeoutMs) {
        libcore::EmptyReq req;
        std::string payload;
        try {
            payload = spb::pb::serialize<std::string>(req);
        } catch (...) {
            Result r;
            r.outcome = Outcome::ProtocolError;
            r.message = QStringLiteral("cannot serialize the request");
            return r;
        }
        auto out = call(QStringLiteral("Stop"), std::move(payload), timeoutMs, false);
        if (out.result.ok() && out.resp != nullptr) out.result = decodeErrorPayload(*out.resp, out.result);
        return out.result;
    }

} // namespace API
